package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPct(t *testing.T) {
	if got := pct(0, 0); got != nil {
		t.Fatalf("без замеров процент неопределён, ожидали nil, получили %v", *got)
	}
	cases := []struct {
		up, total int64
		want      float64
	}{
		{10, 10, 100},
		{0, 10, 0},
		{1, 3, 33.33}, // округление до сотых
		{2, 3, 66.67},
	}
	for _, c := range cases {
		got := pct(c.up, c.total)
		if got == nil || *got != c.want {
			t.Errorf("pct(%d,%d) = %v, ожидали %v", c.up, c.total, got, c.want)
		}
	}
}

func TestToInt(t *testing.T) {
	// Из JSON числа приходят как float64, изнутри — как int64.
	// Обе формы должны читаться одинаково, иначе агрегаты «обнуляются»
	// после первой же перезагрузки истории с диска.
	if toInt(float64(7)) != 7 {
		t.Error("float64 из JSON не разобран")
	}
	if toInt(int64(7)) != 7 {
		t.Error("int64 не разобран")
	}
	if toInt("мусор") != 0 {
		t.Error("неожиданный тип должен давать 0, а не панику")
	}
}

func TestBumpBucketAggregatesSameKey(t *testing.T) {
	var b []bucket
	b = bumpBucket(b, "2026-08-02", true, 100, 10)
	b = bumpBucket(b, "2026-08-02", true, 200, 10)
	b = bumpBucket(b, "2026-08-02", false, 300, 10)

	if len(b) != 1 {
		t.Fatalf("замеры одних суток должны лечь в одну корзину, получили %d", len(b))
	}
	if up := toInt(b[0][1]); up != 2 {
		t.Errorf("успешных ожидали 2, получили %d", up)
	}
	if total := toInt(b[0][2]); total != 3 {
		t.Errorf("всего ожидали 3, получили %d", total)
	}
	if avg := toInt(b[0][3]); avg != 200 {
		t.Errorf("среднее (100+200+300)/3 = 200, получили %d", avg)
	}
}

func TestBumpBucketStartsNewKeyAndTrims(t *testing.T) {
	var b []bucket
	for i := 0; i < 5; i++ {
		b = bumpBucket(b, time.Date(2026, 8, i+1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), true, 10, 3)
	}
	if len(b) != 3 {
		t.Fatalf("хранить должны только 3 последние корзины, получили %d", len(b))
	}
	if b[len(b)-1][0] != "2026-08-05" {
		t.Errorf("последней должна остаться свежая корзина, получили %v", b[len(b)-1][0])
	}
}

func TestBumpBucketMergesAcrossJSONRoundTrip(t *testing.T) {
	// Между запусками агента история едет через диск, и часовой ключ
	// возвращается из JSON как float64. Пока сравнение было по fmt.Sprint,
	// "1.785618e+09" не совпадал с "1785618000" и каждый запуск дописывал
	// новую корзину: часовые корзины держали минуты вместо часов.
	key := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).Unix()
	var b []bucket
	for i := 0; i < 4; i++ {
		b = bumpBucket(b, key, true, 100, hourlyKeep)

		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("маршалинг истории: %v", err)
		}
		b = nil
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("разбор истории: %v", err)
		}
	}
	if len(b) != 1 {
		t.Fatalf("замеры одного часа должны лечь в одну корзину, получили %d: %v", len(b), b)
	}
	if total := toInt(b[0][2]); total != 4 {
		t.Errorf("всего ожидали 4, получили %d", total)
	}
	// Ключ, прочитанный с диска, должен находиться тем же окном, что и свежий.
	if _, ok := indexBuckets(b)[fmt.Sprint(key)]; !ok {
		t.Errorf("корзина с ключом из JSON не нашлась по ключу %d: %v", key, b)
	}
}

func TestIndexBucketsFoldsDuplicateKeys(t *testing.T) {
	// В уже записанных файлах дубли есть — их наплодил сломанный bumpBucket.
	// Брать из них последнюю значило бы выкинуть почти все замеры часа.
	key := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).Unix()
	b := []bucket{
		{float64(key), int64(1), int64(1), int64(100)},
		{float64(key), int64(0), int64(1), int64(300)},
	}
	got := indexBuckets(b)[fmt.Sprint(key)]
	if toInt(got[1]) != 1 || toInt(got[2]) != 2 {
		t.Errorf("дубли должны складываться, получили up=%d total=%d", toInt(got[1]), toInt(got[2]))
	}
	if avg := toInt(got[3]); avg != 200 {
		t.Errorf("среднее (100+300)/2 = 200, получили %d", avg)
	}
}

func TestValidateCheckIDsЛовитДубли(t *testing.T) {
	cfg := Config{Projects: []Project{
		{ID: "a", Checks: []Check{{ID: "site"}, {ID: "api"}}},
		{ID: "b", Checks: []Check{{ID: "site"}}},
	}}
	err := validateCheckIDs(cfg)
	if err == nil {
		t.Fatal("повторяющийся id проверки должен быть ошибкой конфига")
	}
	for _, want := range []string{"site", "a", "b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	cfg.Projects[1].Checks[0].ID = "site-b"
	if err := validateCheckIDs(cfg); err != nil {
		t.Errorf("уникальные id — не ошибка: %v", err)
	}
}

func TestUptimeFromRawIgnoresOldSamples(t *testing.T) {
	now := time.Now()
	raw := []sample{
		{now.Add(-48 * time.Hour).Unix(), 0, 10, 500}, // за пределами окна
		{now.Add(-1 * time.Hour).Unix(), 1, 10, 200},
		{now.Add(-2 * time.Hour).Unix(), 1, 10, 200},
	}
	got := uptimeFromRaw(raw, 24)
	if got == nil || *got != 100 {
		t.Fatalf("старый провал не должен влиять на сутки, получили %v", got)
	}
}

func TestCheckHTTPStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := srv.Client()

	r := checkOnce(Check{URL: srv.URL + "/ok", Expect: 200}, client)
	if r.status != statusUp || r.code != 200 {
		t.Errorf("ожидали успех, получили status=%q code=%d err=%q", r.status, r.code, r.errText)
	}

	r = checkOnce(Check{URL: srv.URL + "/teapot", Expect: 200}, client)
	if r.ok() {
		t.Error("код 418 при ожидаемом 200 — это провал проверки")
	}
	if r.errText != "HTTP 418 вместо 200" {
		t.Errorf("в ошибке должен быть код ответа, получили %q", r.errText)
	}

	// Ожидаемым может быть не только 200: например, редирект-заглушка.
	r = checkOnce(Check{URL: srv.URL + "/teapot", Expect: 418}, client)
	if !r.ok() {
		t.Error("если 418 и есть ожидаемый код, проверка успешна")
	}
}

func TestCheckHTTPUnreachable(t *testing.T) {
	// Порт, на котором заведомо никто не слушает.
	r := checkOnce(Check{URL: "http://127.0.0.1:1/", Expect: 200}, &http.Client{Timeout: 2 * time.Second})
	if r.ok() {
		t.Error("недоступный адрес не может быть успешной проверкой")
	}
	if r.errText == "" {
		t.Error("причина недоступности должна попадать в отчёт")
	}
}

func TestBuildInfoFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "index.html")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildInfo(Build{Title: "Сайт", Type: "file", Path: f}, &http.Client{Timeout: httpTimeout})
	if got.At == "" {
		t.Error("для файла должна определяться дата выкатки")
	}
	if got.Version != "" {
		t.Error("у обычного файла версии нет — только дата")
	}
}

func TestBuildInfoRelease(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "releases", "20260801-225039-5486b2d")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "current")
	if err := os.Symlink(release, link); err != nil {
		t.Skipf("символические ссылки недоступны в этой среде: %v", err)
	}
	got := buildInfo(Build{Title: "Сервер", Type: "release", Path: link}, &http.Client{Timeout: httpTimeout})
	if got.Version != "20260801-225039-5486b2d" {
		t.Errorf("версия берётся из имени каталога релиза, получили %q", got.Version)
	}
	if got.At == "" {
		t.Error("дата переключения симлинка должна попадать в отчёт")
	}
}

func TestBuildInfoFromVersionURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"20260802-010203-abc1234","commit":"abc1234","builtAt":"2026-08-02T01:02:03Z"}`))
	}))
	defer srv.Close()

	got := buildInfo(Build{Title: "Сайт", Type: "url", Path: srv.URL}, srv.Client())
	if got.Version != "20260802-010203-abc1234" {
		t.Errorf("версию берём из ответа сервиса, получили %q", got.Version)
	}
	if got.At != "2026-08-02T01:02:03Z" {
		t.Errorf("время сборки берём оттуда же, получили %q", got.At)
	}
}

func TestBuildInfoVersionURLUnavailable(t *testing.T) {
	// Сервис лежит или ещё не отдаёт version.json — агент обязан пережить
	// это молча и показать остальные проекты, а не свалиться целиком.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	got := buildInfo(Build{Title: "Сайт", Type: "url", Path: srv.URL}, srv.Client())
	if got.Version != "" || got.At != "" {
		t.Errorf("при недоступности ожидались пустые поля, получили %+v", got)
	}
	if got.Title != "Сайт" {
		t.Error("название должно оставаться — строка на странице не исчезает")
	}
}

func TestBuildInfoMissingPathIsNotFatal(t *testing.T) {
	// Сервис может быть ещё не выкачен — агент обязан пережить это молча,
	// а не падать и лишать страницу всех остальных данных.
	got := buildInfo(Build{Title: "Нет такого", Type: "file", Path: "/no/such/path"}, &http.Client{Timeout: httpTimeout})
	if got.Version != "" || got.At != "" {
		t.Error("для отсутствующего пути ожидались пустые поля")
	}
}

func TestWriteJSONIsAtomicAndReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "summary.json")

	in := Summary{Updated: "2026-08-02T00:00:00Z", Overall: "operational"}
	if err := writeJSON(path, in); err != nil {
		t.Fatalf("запись провалилась: %v", err)
	}

	// Временный файл не должен оставаться рядом: страница читает каталог
	// в любой момент и не должна видеть полуфабрикаты.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("временный файл остался после записи")
	}

	var out Summary
	if !readJSON(path, &out) {
		t.Fatal("записанный файл не читается обратно")
	}
	if out.Overall != in.Overall || out.Updated != in.Updated {
		t.Errorf("данные исказились: %+v", out)
	}
}

func TestReadJSONMissingFile(t *testing.T) {
	var v map[string]any
	if readJSON(filepath.Join(t.TempDir(), "нет.json"), &v) {
		t.Error("отсутствующий файл должен давать false, а не панику")
	}
}

func TestReadJSONBrokenFile(t *testing.T) {
	// Битый файл истории не должен ронять обход: агент начнёт копить заново.
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("{это не json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if readJSON(path, &v) {
		t.Error("битый JSON должен давать false")
	}
}

func TestSummaryMarshalsExpectedShape(t *testing.T) {
	// Страница читает эти поля напрямую; переименование поля здесь ломает её
	// молча, поэтому форма зафиксирована тестом.
	b, err := json.Marshal(Summary{
		Updated: "t", Overall: "operational",
		Projects: []OutProject{{ID: "p", Title: "P", Status: "up", Up: 1, Total: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"updated"`, `"overall"`, `"projects"`, `"status"`, `"up"`, `"total"`} {
		if !contains(string(b), key) {
			t.Errorf("в выдаче нет обязательного поля %s", key)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// ------------------------------------------------------- история из событий

// testEvent — событие выкатки для тестов. id получается из номера, чтобы в
// тексте теста было видно, где повтор доставки, а где новая выкатка.
func testEvent(n int, kind, app, version string) deployEvent {
	return deployEvent{
		File:    fmt.Sprintf("178592410%04d-%s-%s.json", n, app, kind),
		ID:      strings.Repeat(fmt.Sprintf("%x", n%16), 64),
		Kind:    kind,
		App:     app,
		At:      "2026-08-02T10:00:00Z",
		Version: version,
	}
}

func TestТриВыкаткиЗаМинутуДаютТриЗаписи(t *testing.T) {
	// То, ради чего история переехала на события: разница двух снимков
	// /version.json оставляла от трёх выкаток одну запись, а две промежуточные
	// версии не существовали ни для чата, ни для истории.
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	var h []Release
	for i, v := range []string{"v1", "v2", "v3"} {
		h = applyEvent(h, testEvent(i+1, evSuccess, "snakes", v), now.Add(time.Duration(i)*time.Second))
	}

	if len(h) != 3 {
		t.Fatalf("трём выкаткам положено три записи, получили %d: %+v", len(h), h)
	}
	if h[0].Version != "v3" || h[2].Version != "v1" {
		t.Errorf("порядок истории нарушен: %+v", h)
	}
	if h[0].Seen == "" || h[0].At != "2026-08-02T10:00:00Z" {
		t.Errorf("запись без времени выкатки или без момента записи: %+v", h[0])
	}
}

func TestПовторДоставкиНеДаётВторойЗаписи(t *testing.T) {
	// Доставка повторяется по построению: три попытки транспорта, повтор
	// прогона одной кнопкой. Отличить повтор можно ТОЛЬКО по id — имена файлов
	// у него разные, и курсор здесь не помогает никак.
	now := time.Now().UTC()
	ev := testEvent(1, evSuccess, "snakes", "v1")
	h := applyEvent(nil, ev, now)

	again := ev
	again.File = "1785924109999-snakes-success.json"
	h = applyEvent(h, again, now.Add(time.Minute))

	if len(h) != 1 {
		t.Fatalf("повтор доставки завёл вторую запись: %+v", h)
	}
}

func TestОднаВерсияДваждыОстаётсяДвумяВыкатками(t *testing.T) {
	// Перевыкатка того же коммита и повтор прогона после починки
	// инфраструктуры несут одну версию, но это ДВЕ выкатки. Схлопнув их по
	// версии, мы потеряли бы настоящую — то самое, ради чего события заводились.
	now := time.Now().UTC()
	h := applyEvent(nil, testEvent(1, evSuccess, "snakes", "v1"), now)
	h = applyEvent(h, testEvent(2, evSuccess, "snakes", "v1"), now.Add(time.Hour))

	if len(h) != 2 {
		t.Fatalf("две выкатки одной версии схлопнулись в одну запись: %+v", h)
	}
}

func TestРучнойОткатПопадаетВИсториюАвтооткатНет(t *testing.T) {
	now := time.Now().UTC()
	h := applyEvent(nil, testEvent(1, evSuccess, "snakes", "v2"), now)

	// Автооткат несёт версию, которую выкатывали и СНЯЛИ: на проде её не было
	// ни минуты, и записывать её как релиз значило бы врать в истории.
	h = applyEvent(h, testEvent(2, evRolledBack, "snakes", "v3"), now.Add(time.Minute))
	if len(h) != 1 {
		t.Fatalf("автооткат записан релизом: %+v", h)
	}

	// Ручной откат — наоборот: version в нём это релиз, НА который вернулись,
	// и он на проде работает.
	h = applyEvent(h, testEvent(3, evRollback, "snakes", "v1"), now.Add(2*time.Minute))
	if len(h) != 2 || h[0].Version != "v1" {
		t.Fatalf("возврат на прежний релиз не попал в историю: %+v", h)
	}

	// Провал и начало выкатки истории не касаются: на проде не появилось
	// ничего нового, рассказывать о них — работа бота, а не журнала релизов.
	h = applyEvent(h, testEvent(4, "failure", "snakes", "v4"), now.Add(3*time.Minute))
	h = applyEvent(h, testEvent(5, "started", "snakes", "v4"), now.Add(4*time.Minute))
	if len(h) != 2 {
		t.Fatalf("провал или начало выкатки попали в историю: %+v", h)
	}
}

func TestИсторияИзСобытийОбрезаетсяДоПредела(t *testing.T) {
	now := time.Now().UTC()
	var h []Release
	for i := 0; i < releasesKeep+5; i++ {
		ev := testEvent(i, evSuccess, "snakes", fmt.Sprintf("v%d", i))
		// id обязан быть разным: иначе сработает дедупликация, а проверяем
		// здесь другое.
		ev.ID = fmt.Sprintf("%064x", i)
		h = applyEvent(h, ev, now.Add(time.Duration(i)*time.Minute))
	}
	if len(h) != releasesKeep {
		t.Fatalf("история обрезается до %d записей, получили %d", releasesKeep, len(h))
	}
	if h[0].Version != fmt.Sprintf("v%d", releasesKeep+4) {
		t.Errorf("свежая версия должна остаться первой, получили %q", h[0].Version)
	}
}

// -------------------------------------------------------- список изменений

func TestBuildInfoЧитаетСписокИзменений(t *testing.T) {
	// Выкатке проще всего положить в version.json ровно то, что напечатал
	// deploy-kit/bin/changelog: одной строкой, с заголовком, маркерами «•» и
	// уже экранированными для Telegram символами. Разбираем именно это —
	// иначе на стороне выкатки завелось бы второе место, знающее формат.
	body := `{"version":"v2","builtAt":"2026-08-02T01:02:03Z",` +
		`"changelog":"<b>Изменения</b>\n• поднять go до 1.22 &lt;-- важно\n• обновить nginx\n…и ещё 12 коммитов"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got := buildInfo(Build{Title: "Сайт", Type: "url", Path: srv.URL}, srv.Client())
	want := []string{"поднять go до 1.22 <-- важно", "обновить nginx", "…и ещё 12 коммитов"}
	if len(got.Changelog) != len(want) {
		t.Fatalf("разобрано %d пунктов, ожидали %d: %q", len(got.Changelog), len(want), got.Changelog)
	}
	for i, w := range want {
		if got.Changelog[i] != w {
			t.Errorf("пункт %d = %q, ожидали %q", i, got.Changelog[i], w)
		}
	}
	if got.Version != "v2" {
		t.Errorf("версия потерялась из-за списка изменений: %q", got.Version)
	}
}

func TestBuildInfoПринимаетСписокМассивом(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"v2","changelog":["первое","второе"]}`))
	}))
	defer srv.Close()

	got := buildInfo(Build{Title: "Сайт", Type: "url", Path: srv.URL}, srv.Client())
	if len(got.Changelog) != 2 || got.Changelog[0] != "первое" {
		t.Errorf("массив строк не разобран: %q", got.Changelog)
	}
}

func TestBuildInfoПереживаетЧужойФорматСписка(t *testing.T) {
	// version.json нужен прежде всего ради версии. Мусор в необязательном
	// поле не повод потерять версию — а вместе с ней и всю выкатку, о которой
	// тогда никто не узнает.
	for _, body := range []string{
		`{"version":"v2","changelog":42}`,
		`{"version":"v2","changelog":null}`,
		`{"version":"v2","changelog":{"было":"стало"}}`,
		`{"version":"v2"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		got := buildInfo(Build{Title: "Сайт", Type: "url", Path: srv.URL}, srv.Client())
		srv.Close()
		if got.Version != "v2" {
			t.Errorf("%s: версия потеряна, получили %q", body, got.Version)
		}
		if len(got.Changelog) != 0 {
			t.Errorf("%s: из мусора собрался список %q", body, got.Changelog)
		}
	}
}

func TestNormalizeChangelogОграничиваетЧужойФайл(t *testing.T) {
	// Файл чужой и приезжает по сети, а summary.json переписывается раз в
	// минуту и целиком лежит в памяти у каждого читателя.
	var in []string
	for i := 0; i < 100; i++ {
		in = append(in, strings.Repeat("тема ", 200))
	}
	got := normalizeChangelog(in)
	if len(got) > changelogKeep {
		t.Errorf("пунктов %d, ожидали не больше %d", len(got), changelogKeep)
	}
	for _, s := range got {
		if n := utf8.RuneCountInString(s); n > changelogLineChars {
			t.Errorf("пункт длиной %d символов, ожидали не больше %d", n, changelogLineChars)
		}
		if !utf8.ValidString(s) {
			t.Error("обрезка разрезала символ UTF-8")
		}
	}
}

func TestНормализацияНеТрогаетТемуВ120Символов(t *testing.T) {
	// Потолок темы задан владельцем в СИМВОЛАХ (CLAUDE.md, --width 120 у
	// генератора). Агент — труба между генератором и ботом, и он не имеет
	// права укорачивать то, что генератор уже посчитал влезающим.
	//
	// Кириллица здесь не для экзотики, а потому, что именно на ней всё и
	// ломалось: 120 символов — это 240 байт, и прежний байтовый предел резал
	// такую тему ровно посередине.
	subject := strings.Repeat("тема ", 23) + "конец" // 120 символов, 240 байт
	if n := utf8.RuneCountInString(subject); n != 120 {
		t.Fatalf("подготовка теста: тема на %d символов, нужна ровно 120", n)
	}

	got := normalizeChangelog([]string{"<b>Изменения</b>", "• " + subject})
	if len(got) != 1 {
		t.Fatalf("пункт не разобран: %q", got)
	}
	if got[0] != subject {
		t.Errorf("тема в 120 символов изменилась по дороге:\nбыло  %q\nстало %q", subject, got[0])
	}
	if strings.HasSuffix(got[0], "…") {
		t.Error("агент дорисовал многоточие: обрезкой по месту занят бот")
	}
}

func TestНормализацияЖмётВраньёВПоле(t *testing.T) {
	// Обратная сторона: генератор мог не запуститься вовсе, а version.json
	// собрать чья-то рука или чужой скрипт. Предел агента остаётся пределом.
	hostile := strings.Repeat("я", 500)

	got := normalizeChangelog([]string{hostile})
	if len(got) != 1 {
		t.Fatalf("пункт потерялся: %q", got)
	}
	if n := utf8.RuneCountInString(got[0]); n > changelogLineChars {
		t.Errorf("пункт на %d символов, предел %d", n, changelogLineChars)
	}
	if !utf8.ValidString(got[0]) {
		t.Error("обрезка разрезала символ UTF-8")
	}
	if !strings.HasSuffix(got[0], "…") {
		t.Errorf("обрезка не отмечена многоточием: %q", got[0])
	}
}

func TestCutRunesСчитаетСимволыИНеРежетИх(t *testing.T) {
	// Байтовый счёт на кириллице вдвое строже написанного — из-за него темы и
	// обрывались на полуслове. И отдельно: битая строка UTF-8 — это отказ
	// Telegram, то есть молча не пришедшее сообщение.
	s := strings.Repeat("тема ", 60)
	for n := 1; n < 120; n++ {
		got := cutRunes(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("cutRunes(%d) разрезал символ: %q", n, got)
		}
		if c := utf8.RuneCountInString(got); c > n {
			t.Fatalf("cutRunes(%d) вернул %d символов: %q", n, c, got)
		}
	}
	if got := cutRunes("коротко", 100); got != "коротко" {
		t.Errorf("cutRunes обрезал то, что помещалось: %q", got)
	}
	// Ровно по пределу — тоже «помещается»: иначе тема максимальной
	// разрешённой длины получала бы многоточие ни за что.
	edge := strings.Repeat("я", 120)
	if got := cutRunes(edge, 120); got != edge {
		t.Errorf("строка ровно по пределу обрезана: %q", got)
	}
}

// --------------------------------------- список изменений в истории выкаток

func TestИсторияХранитСписокИзмененийСобытия(t *testing.T) {
	// В summary.json changelog есть только у текущей версии. Вопрос «что было
	// в релизе X» задают про прошлые — отвечать на него нечем, если список не
	// осел в истории вместе с самой записью о выкатке.
	now := time.Now().UTC()
	first := testEvent(1, evSuccess, "snakes", "v1")
	first.Changelog = []string{"поднять go до 1.22", "обновить nginx"}
	h := applyEvent(nil, first, now)

	if len(h) != 1 || len(h[0].Changelog) != 2 {
		t.Fatalf("список изменений не осел в истории: %+v", h)
	}
	second := testEvent(2, evSuccess, "snakes", "v2")
	second.Changelog = []string{"починить логин"}
	h = applyEvent(h, second, now.Add(time.Hour))
	if len(h) != 2 {
		t.Fatalf("новая версия не попала в историю: %+v", h)
	}
	if len(h[0].Changelog) != 1 || h[0].Changelog[0] != "починить логин" {
		t.Errorf("свежая запись потеряла свой список: %+v", h[0])
	}
	// Главное: прошлый релиз сохранил СВОЙ список, а не перенял новый.
	if len(h[1].Changelog) != 2 || h[1].Changelog[0] != "поднять go до 1.22" {
		t.Errorf("прошлый релиз потерял свой список: %+v", h[1])
	}
}

func TestTrimReleaseChangelogsДержитФайлВБерегах(t *testing.T) {
	// Файл переписывается раз в минуту и лежит в вебруте. Растёт он прежде
	// всего вширь по истории, поэтому предел проверяем на длинной истории.
	var h []Release
	now := time.Now().UTC()
	for i := 0; i < releasesKeep+5; i++ {
		ev := testEvent(i, evSuccess, "snakes", fmt.Sprintf("v%d", i))
		ev.ID = fmt.Sprintf("%064x", i)
		for j := 0; j < 40; j++ {
			ev.Changelog = append(ev.Changelog, strings.Repeat("тема ", 200))
		}
		h = applyEvent(h, ev, now.Add(time.Duration(i)*time.Minute))
	}

	if len(h) != releasesKeep {
		t.Fatalf("история обрезается до %d записей, получили %d", releasesKeep, len(h))
	}
	for i, r := range h {
		if i >= releaseChangelogKeep {
			if len(r.Changelog) != 0 {
				t.Errorf("запись %d старше %d, список должен быть вычищен: %d пунктов",
					i, releaseChangelogKeep, len(r.Changelog))
			}
			continue
		}
		if len(r.Changelog) > releaseChangelogLines {
			t.Errorf("запись %d: пунктов %d, ожидали не больше %d", i, len(r.Changelog), releaseChangelogLines)
		}
		total := 0
		for _, s := range r.Changelog {
			total += utf8.RuneCountInString(s)
			if !utf8.ValidString(s) {
				t.Errorf("запись %d: обрезка разрезала символ UTF-8", i)
			}
		}
		if total > releaseChangelogChars {
			t.Errorf("запись %d: список весит %d символов, ожидали не больше %d", i, total, releaseChangelogChars)
		}
		// Дата и версия остаются на всю глубину истории — дорог только текст.
		if r.Version == "" || r.Seen == "" {
			t.Errorf("запись %d осталась без версии или даты: %+v", i, r)
		}
	}
}

func TestTrimReleaseChangelogsЧиститЧужойФайл(t *testing.T) {
	// Пределы применяются к прочитанному, а не только к дописанному: файл мог
	// записать агент другой версии. Предел, который держится только на
	// записи, — не предел.
	var cl changelogField
	for i := 0; i < 100; i++ {
		cl = append(cl, strings.Repeat("x", 500))
	}
	h := make([]Release, releasesKeep)
	for i := range h {
		h[i] = Release{Version: fmt.Sprintf("v%d", i), Seen: "2026-08-01T00:00:00Z", Changelog: cl}
	}
	h = trimReleaseChangelogs(h)

	for i, r := range h {
		if i >= releaseChangelogKeep && len(r.Changelog) != 0 {
			t.Errorf("запись %d: список не вычищен при чтении", i)
		}
		total := 0
		for _, s := range r.Changelog {
			total += utf8.RuneCountInString(s)
		}
		if total > releaseChangelogChars {
			t.Errorf("запись %d: %d символов после чтения, ожидали не больше %d", i, total, releaseChangelogChars)
		}
	}
}

func TestИсторияХранитПолныйСписокГенератора(t *testing.T) {
	// Самая обидная из старых обрезок: в чат уходило восемь изменений, а в
	// истории того же релиза оставалось четыре с половиной — килобайт БАЙТ на
	// список кончался на пятом кириллическом пункте. Экран «/changelog имя»
	// после этого рассказывал про другую выкатку, чем сообщение о ней.
	//
	// Пределы releases.json подобраны так, чтобы ПОЛНЫЙ вывод генератора
	// (восемь тем предельной длины плюс хвост) влезал целиком.
	subject := strings.Repeat("я", 120)
	var cl changelogField
	for i := 0; i < 8; i++ {
		cl = append(cl, subject)
	}
	cl = append(cl, "…и ещё 12 коммитов")

	got := cutChangelog(cl)
	if len(got) != len(cl) {
		t.Fatalf("список генератора не влез в историю: %d пунктов из %d", len(got), len(cl))
	}
	for i, s := range got {
		if utf8.RuneCountInString(s) != utf8.RuneCountInString(cl[i]) {
			t.Errorf("пункт %d обрезан: %q", i, s)
		}
	}
}

func TestHistoryForPageНеТащитСпискиВSummary(t *testing.T) {
	// summary.json страница тянет целиком раз в минуту, а из истории читает
	// только версию и дату. Пять списков на каждую цель — это трафик каждой
	// загрузки за то, чего страница не показывает.
	h := []Release{
		{Version: "v2", Seen: "2026-08-02T00:00:00Z", Changelog: changelogField{"починить логин"}},
		{Version: "v1", Seen: "2026-08-01T00:00:00Z", Changelog: changelogField{"обновить nginx"}},
	}
	got := historyForPage(h)

	if len(got) != len(h) {
		t.Fatalf("история для страницы укоротилась: %d записей вместо %d", len(got), len(h))
	}
	for i, r := range got {
		if len(r.Changelog) != 0 {
			t.Errorf("запись %d утащила список в summary.json: %+v", i, r)
		}
		if r.Version != h[i].Version || r.Seen != h[i].Seen {
			t.Errorf("запись %d потеряла версию или дату: %+v", i, r)
		}
	}
	// Исходник не должен пострадать: это та же история, что уедет в releases.json.
	if len(h[0].Changelog) != 1 {
		t.Errorf("вычистили список прямо в хранилище: %+v", h[0])
	}
	if historyForPage(nil) != nil {
		t.Error("пустая история должна остаться пустой, а не стать []")
	}
}

func TestReleasesJSONСтарыйФорматЧитается(t *testing.T) {
	// На диске лежат файлы без поля changelog. Появление поля не должно
	// стоить накопленной истории выкаток.
	const old = `{"samoy::Сайт":[{"version":"v2","at":"2026-08-02T01:00:00Z","seen":"2026-08-02T01:05:00Z"},` +
		`{"version":"v1","seen":"2026-08-01T00:00:00Z"}]}`

	var releases map[string][]Release
	if err := json.Unmarshal([]byte(old), &releases); err != nil {
		t.Fatalf("файл старого формата не читается: %v", err)
	}
	h := releases["samoy::Сайт"]
	if len(h) != 2 || h[0].Version != "v2" || h[1].Version != "v1" {
		t.Fatalf("история старого формата разобрана неверно: %+v", h)
	}
	if h[0].At != "2026-08-02T01:00:00Z" || h[0].Seen != "2026-08-02T01:05:00Z" {
		t.Errorf("даты потерялись: %+v", h[0])
	}
	if len(h[0].Changelog) != 0 {
		t.Errorf("из ниоткуда собрался список: %+v", h[0].Changelog)
	}

	// И обратно: запись без списка не должна обрастать пустым полем — иначе
	// файл раздувается на пустом месте.
	b, err := json.Marshal(releases)
	if err != nil {
		t.Fatalf("не сериализуется: %v", err)
	}
	if strings.Contains(string(b), "changelog") {
		t.Errorf("пустое поле попало в файл: %s", b)
	}
}

func TestReleasesJSONНовыйФорматПереживаетКругЧерезДиск(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	releases := map[string][]Release{}
	ev := testEvent(1, evSuccess, "samoylove", "v2")
	ev.At = "2026-08-02T01:00:00Z"
	ev.Changelog = []string{"поднять go до 1.22 <-- важно", "обновить nginx"}
	releases["samoy::Сайт"] = applyEvent(releases["samoy::Сайт"], ev, now)

	path := filepath.Join(t.TempDir(), "releases.json")
	if err := writeJSON(path, releases); err != nil {
		t.Fatalf("не записалось: %v", err)
	}
	var back map[string][]Release
	if !readJSON(path, &back) {
		t.Fatal("файл нового формата не прочитался")
	}
	h := back["samoy::Сайт"]
	if len(h) != 1 {
		t.Fatalf("история не доехала: %+v", back)
	}
	if len(h[0].Changelog) != 2 || h[0].Changelog[0] != "поднять go до 1.22 <-- важно" {
		t.Errorf("список изменений не пережил круг через диск: %+v", h[0].Changelog)
	}
	if h[0].Version != "v2" || h[0].At != "2026-08-02T01:00:00Z" || h[0].Seen != now.Format(time.RFC3339) {
		t.Errorf("запись изменилась после круга через диск: %+v", h[0])
	}
	// Формат на диске — обычный массив строк: его читает не только этот агент.
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"changelog":["`) {
		t.Errorf("список записан не массивом строк: %s", raw)
	}
}

func TestReleasesJSONПереживаетЧужойФорматСписка(t *testing.T) {
	// То же терпение, что и у version.json, и по той же причине: releases.json
	// читается целиком одним Unmarshal, и мусор в необязательном поле одной
	// записи стоил бы истории выкаток ВСЕХ сервисов сразу.
	cases := map[string]int{
		`{"s":[{"version":"v1","seen":"t","changelog":"строкой\nв две"}]}`: 2,
		`{"s":[{"version":"v1","seen":"t","changelog":42}]}`:               0,
		`{"s":[{"version":"v1","seen":"t","changelog":null}]}`:             0,
		`{"s":[{"version":"v1","seen":"t","changelog":{"было":"стало"}}]}`: 0,
		`{"s":[{"version":"v1","seen":"t","changelog":["массивом"]}]}`:     1,
	}
	for body, want := range cases {
		var releases map[string][]Release
		if err := json.Unmarshal([]byte(body), &releases); err != nil {
			t.Errorf("%s: чтение упало (%v) — потеряна вся история", body, err)
			continue
		}
		h := releases["s"]
		if len(h) != 1 || h[0].Version != "v1" {
			t.Errorf("%s: версия потеряна из-за списка: %+v", body, h)
			continue
		}
		if len(h[0].Changelog) != want {
			t.Errorf("%s: разобрано %d пунктов, ожидали %d: %q", body, len(h[0].Changelog), want, h[0].Changelog)
		}
	}
}

// -------------------------------------------------- чтение журнала выкаток

// writeEventFile кладёт в каталог файл журнала. Тело — как его пишет выкатка,
// то есть по контракту deploy-kit/docs/events.md.
func writeEventFile(t *testing.T, dir string, ms int64, app, kind string, body map[string]any) string {
	t.Helper()
	name := fmt.Sprintf("%013d-%s-%s.json", ms, app, kind)
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o640); err != nil {
		t.Fatal(err)
	}
	return name
}

// eventBody — минимальное правильное событие по контракту.
func eventBody(app, kind, version string) map[string]any {
	return map[string]any{
		"v":        1,
		"id":       strings.Repeat("a", 64),
		"kind":     kind,
		"app":      app,
		"at":       "2026-08-05T10:01:42Z",
		"source":   "ci",
		"group":    strings.Repeat("b", 64),
		"groupSeq": 1,
		"version":  version,
	}
}

func TestЖурналСобытийЧитаетсяПоКурсору(t *testing.T) {
	dir := t.TempDir()
	writeEventFile(t, dir, 1785924102000, "snakes", "success", eventBody("snakes", "success", "v1"))
	second := eventBody("snakes", "success", "v2")
	second["id"] = strings.Repeat("c", 64)
	writeEventFile(t, dir, 1785924103000, "snakes", "success", second)

	// Курсора нет: журнал считается прочитанным, иначе в историю уехали бы две
	// недели выкаток, о которых записи уже есть.
	got, cursor := readEvents(dir, 0, eventsMaxPerRun)
	if len(got) != 0 {
		t.Fatalf("первый запуск разобрал журнал целиком: %+v", got)
	}
	if cursor != 1785924103000 {
		t.Fatalf("курсор после первого запуска %d", cursor)
	}

	// А дальше — всё, что не старше курсора: события той же миллисекунды у
	// другой цели иначе потерялись бы навсегда.
	got, cursor = readEvents(dir, 1785924102000, eventsMaxPerRun)
	if len(got) != 2 {
		t.Fatalf("прочитано %d событий: %+v", len(got), got)
	}
	if got[0].Version != "v1" || got[1].Version != "v2" {
		t.Errorf("порядок событий нарушен: %+v", got)
	}
	if cursor != 1785924103000 {
		t.Errorf("курсор не доехал до последнего файла: %d", cursor)
	}

	got, _ = readEvents(dir, 1785924103001, eventsMaxPerRun)
	if len(got) != 0 {
		t.Errorf("прочитано лишнее после курсора: %+v", got)
	}
}

func TestЖурналСобытийНеПадаетНаМусоре(t *testing.T) {
	dir := t.TempDir()

	// Имя не по шаблону — файла для читателя не существует.
	if err := os.WriteFile(filepath.Join(dir, "1785924102000-snakes-success.json.tmp"),
		[]byte(`{"v":1}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "события.json"), []byte(`{"v":1}`), 0o640); err != nil {
		t.Fatal(err)
	}
	// Обрезанный файл: если писателю однажды заменят rename на cp, читатель
	// обязан это пережить.
	if err := os.WriteFile(filepath.Join(dir, "1785924102100-snakes-success.json"),
		[]byte(`{"v":1,"id":"aaa`), 0o640); err != nil {
		t.Fatal(err)
	}
	// Не та версия схемы.
	future := eventBody("snakes", "success", "v9")
	future["v"] = 2
	writeEventFile(t, dir, 1785924102200, "snakes", "success", future)
	// Имя файла не совпадает с содержимым: по имени считается порядок, и
	// разъехавшись, они сломали бы курсор.
	writeEventFile(t, dir, 1785924102300, "snakes", "success", eventBody("metro", "success", "v9"))
	// id не sha256.
	badID := eventBody("snakes", "success", "v9")
	badID["id"] = "нет"
	writeEventFile(t, dir, 1785924102400, "snakes", "success", badID)
	// Версия не из разрешённого алфавита: она уезжает на публичную страницу.
	writeEventFile(t, dir, 1785924102500, "snakes", "success", eventBody("snakes", "success", "../../etc/passwd"))
	// Файл сверх предела: размер смотрится stat'ом до чтения.
	huge := eventBody("snakes", "success", "v9")
	huge["changelog"] = []string{strings.Repeat("я", 8<<10)}
	writeEventFile(t, dir, 1785924102600, "snakes", "success", huge)
	// И одно годное событие последним.
	good := eventBody("snakes", "success", "v1")
	good["id"] = strings.Repeat("d", 64)
	writeEventFile(t, dir, 1785924102700, "snakes", "success", good)

	got, cursor := readEvents(dir, 1785924102000, eventsMaxPerRun)
	if len(got) != 1 || got[0].Version != "v1" {
		t.Fatalf("мусор проехал в историю или утащил с собой годное событие: %+v", got)
	}
	// Курсор двигается и на пропущенных файлах: одна опечатка писателя не имеет
	// права остановить историю навсегда.
	if cursor != 1785924102700 {
		t.Errorf("курсор застрял на %d", cursor)
	}
}

func TestСимлинкВЖурналеНеЧитается(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.json")
	b, _ := json.Marshal(eventBody("snakes", "success", "v9"))
	if err := os.WriteFile(secret, b, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "1785924102000-snakes-success.json")
	if err := os.Symlink(secret, link); err != nil {
		// На Windows создание ссылки требует прав, которых у обычного
		// пользователя нет. Молча признавать тест пройденным нельзя.
		t.Skipf("симлинк не создан: %v", err)
	}
	good := eventBody("metro", "success", "v1")
	good["id"] = strings.Repeat("e", 64)
	writeEventFile(t, dir, 1785924102500, "metro", "success", good)

	got, _ := readEvents(dir, 1785924102000, eventsMaxPerRun)
	if len(got) != 1 || got[0].App != "metro" {
		t.Fatalf("симлинк прочитан: %+v", got)
	}
}

func TestЖурналСобытийБерётНеБольшеПределаЗаЗапуск(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		body := eventBody("snakes", "success", fmt.Sprintf("v%d", i))
		body["id"] = fmt.Sprintf("%064x", i)
		writeEventFile(t, dir, 1785924102000+int64(i), "snakes", "success", body)
	}
	got, cursor := readEvents(dir, 1785924102000, 2)
	if len(got) != 2 {
		t.Fatalf("предел на запуск не соблюдён: %d событий", len(got))
	}
	// Остаток обязан доехать следующим запуском — за этим курсор и нужен.
	if cursor != 1785924102001 {
		t.Fatalf("курсор перескочил неразобранное: %d", cursor)
	}
	got, _ = readEvents(dir, cursor, 10)
	if len(got) != 4 {
		t.Errorf("остаток журнала потерян: %+v", got)
	}
}

func TestОтсутствующийЖурналНеАвария(t *testing.T) {
	// Агент обязан работать на машине, где deploy-kit ещё не раскладывали.
	got, cursor := readEvents(filepath.Join(t.TempDir(), "нет"), 42, eventsMaxPerRun)
	if got != nil || cursor != 42 {
		t.Errorf("отсутствие каталога сдвинуло курсор или выдумало события: %+v, %d", got, cursor)
	}
}

func TestСписокИзмененийСобытияОстаётсяТекстом(t *testing.T) {
	// releases.json читают бот и страница, и бот признаёт ссылкой пункт,
	// который выглядит как ссылка на PR. Чужой якорь из события обязан остаться
	// текстом, а перевод строки — не разрывать пункт пополам.
	got := cleanEventChangelog([]string{
		`тема <a href="https://чужой/х">#1</a>`,
		"первая\nстрока",
		"‮обратный порядок",
		"",
	})
	if len(got) != 3 {
		t.Fatalf("пункты потеряны или размножились: %q", got)
	}
	if strings.Contains(got[0], "<a") || !strings.Contains(got[0], "#1") {
		t.Errorf("якорь не снят или номер потерян: %q", got[0])
	}
	if got[1] != "первая строка" {
		t.Errorf("перевод строки не склеен: %q", got[1])
	}
	if strings.ContainsRune(got[2], 0x202e) {
		t.Errorf("переворот направления текста проехал: %q", got[2])
	}
}

// ------------------------------------------------------------- карта целей

func TestКартаЦелейВыводитсяИзПутиРелиза(t *testing.T) {
	cfg := Config{Projects: []Project{{
		ID: "status",
		Builds: []Build{
			{Title: "Агент", Type: "release", Path: "/opt/status-agent/current"},
			{Title: "Страница", Type: "url", Path: "https://status.samoy.love/version.json", App: "status-site"},
			// У цели типа url без app связать событие не с чем: адрес ничего
			// не говорит о том, как называется цель выкатки.
			{Title: "Безымянная", Type: "url", Path: "https://example.com/version.json"},
		},
	}}}

	keys := eventKeys(cfg)
	if keys["status-agent"] != "status::Агент" {
		t.Errorf("id цели не выведен из пути релиза: %+v", keys)
	}
	if keys["status-site"] != "status::Страница" {
		t.Errorf("явный app из конфига не сработал: %+v", keys)
	}
	if len(keys) != 2 {
		t.Errorf("в карту попало лишнее: %+v", keys)
	}
}

func TestОдинИдНаДвеЦелиСнимаетсяСКарты(t *testing.T) {
	// Две цели с одним APP затирали бы историю друг друга, и заметить это можно
	// было бы только по расходящимся спискам изменений.
	cfg := Config{Projects: []Project{{
		ID: "status",
		Builds: []Build{
			{Title: "Первая", Type: "release", Path: "/opt/status-agent/current"},
			{Title: "Вторая", Type: "release", Path: "/opt/status-agent/current"},
		},
	}}}
	if keys := eventKeys(cfg); len(keys) != 0 {
		t.Errorf("неоднозначный id остался в карте: %+v", keys)
	}
}

// ------------------------------------------------------- опрос как проверка

func TestВерсияБезСобытияСтановитсяАномалией(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	history := []Release{{Version: "v1", Seen: "2026-08-05T11:00:00Z", EventID: strings.Repeat("a", 64)}}

	// Версия, о которой событие было, — обычное состояние.
	a := newAnomalyTracker(nil)
	if kind, _ := a.check("status::Сайт", "v1", history, now); kind != "" {
		t.Errorf("объявленная версия названа аномалией: %q", kind)
	}
	if len(a.next) != 0 {
		t.Errorf("в состоянии осталась запись о нормальной версии: %+v", a.next)
	}

	// Незнакомая версия сразу тревогой не становится: version.json на проде
	// меняется раньше, чем выкатка пишет событие об успехе.
	a = newAnomalyTracker(nil)
	kind, since := a.check("status::Сайт", "v2", history, now)
	if kind != "" || since != "" {
		t.Errorf("тревога поднята в окне выкатки: %q %q", kind, since)
	}
	rec, ok := a.next["status::Сайт"]
	if !ok || rec.Version != "v2" {
		t.Fatalf("момент первого наблюдения не запомнен: %+v", a.next)
	}

	// А вот когда версия остаётся необъявленной дольше отведённого, это
	// аномалия: выкатили мимо пайплайна или событие потерялось.
	later := now.Add(anomalyGrace + time.Minute)
	b := newAnomalyTracker(a.next)
	kind, since = b.check("status::Сайт", "v2", history, later)
	if kind != anomalyNoEvent {
		t.Fatalf("аномалия не объявлена: %q", kind)
	}
	if since != rec.Since {
		t.Errorf("отсчёт начат заново: %q вместо %q", since, rec.Since)
	}

	// Пришло событие об этой версии — аномалия снимается, а запись о ней
	// уходит из состояния.
	c := newAnomalyTracker(b.next)
	history = append([]Release{{
		Version: "v2", Seen: later.Format(time.RFC3339), EventID: strings.Repeat("f", 64),
	}}, history...)
	if kind, _ = c.check("status::Сайт", "v2", history, later); kind != "" {
		t.Errorf("аномалия осталась после события: %q", kind)
	}
	if len(c.next) != 0 {
		t.Errorf("состояние копит снятые аномалии: %+v", c.next)
	}
}

func TestВерсияИзСтарогоФайлаНеАномалия(t *testing.T) {
	// В день перехода на события история состоит из записей прежней механики —
	// без id события. Объявив их необъявленными, агент поднял бы тревогу на всём
	// хозяйстве сразу.
	old := []Release{{Version: "v1", Seen: "2026-07-01T00:00:00Z"}}
	a := newAnomalyTracker(nil)
	if kind, _ := a.check("status::Сайт", "v1", old, time.Now().UTC()); kind != "" {
		t.Errorf("запись прежней механики названа аномалией: %q", kind)
	}
}

func TestБезВерсииАномалииНет(t *testing.T) {
	// Сервис не отдал version.json: сказать о выкатке нечего, и аномалии тут
	// нет — есть недоступный version.json, о котором говорят проверки.
	a := newAnomalyTracker(map[string]Unexplained{"status::Сайт": {Version: "v2", Since: "2026-08-01T00:00:00Z"}})
	if kind, _ := a.check("status::Сайт", "", nil, time.Now().UTC()); kind != "" {
		t.Errorf("пустая версия названа аномалией: %q", kind)
	}
	if len(a.next) != 0 {
		t.Errorf("запись без версии осталась в состоянии: %+v", a.next)
	}
}
