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

func TestRecordReleaseAppendsOnlyOnChange(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	var h []Release

	h = recordRelease(h, OutBuild{Version: "v1", At: "2026-08-01T00:00:00Z"}, now)
	if len(h) != 1 {
		t.Fatalf("первая версия должна попасть в историю, получили %d записей", len(h))
	}
	if h[0].Seen != now.Format(time.RFC3339) {
		t.Errorf("момент обнаружения версии не записан: %+v", h[0])
	}

	// Тот же самый релиз проверяется раз в минуту — плодить записи нельзя.
	h = recordRelease(h, OutBuild{Version: "v1", At: "2026-08-01T00:00:00Z"}, now.Add(time.Minute))
	if len(h) != 1 {
		t.Fatalf("неизменная версия не должна дублироваться, получили %d записей", len(h))
	}

	h = recordRelease(h, OutBuild{Version: "v2"}, now.Add(time.Hour))
	if len(h) != 2 || h[0].Version != "v2" {
		t.Fatalf("новая версия должна встать первой, получили %+v", h)
	}
}

func TestRecordReleaseKeepsRollback(t *testing.T) {
	// Откат на предыдущую версию — это тоже выкатка. Если сравнивать со всем
	// списком, а не только с последней записью, откат исчезнет из истории.
	now := time.Now().UTC()
	var h []Release
	h = recordRelease(h, OutBuild{Version: "v1"}, now)
	h = recordRelease(h, OutBuild{Version: "v2"}, now.Add(time.Minute))
	h = recordRelease(h, OutBuild{Version: "v1"}, now.Add(2*time.Minute))

	if len(h) != 3 {
		t.Fatalf("откат должен попасть в историю отдельной записью, получили %d", len(h))
	}
	if h[0].Version != "v1" || h[1].Version != "v2" {
		t.Errorf("порядок истории нарушен: %+v", h)
	}
}

func TestRecordReleaseIgnoresEmptyVersionAndTrims(t *testing.T) {
	now := time.Now().UTC()
	// Сервис не отдал version.json — писать в историю нечего.
	if got := recordRelease(nil, OutBuild{Title: "Сайт"}, now); got != nil {
		t.Errorf("пустая версия не должна попадать в историю, получили %+v", got)
	}

	var h []Release
	for i := 0; i < releasesKeep+5; i++ {
		h = recordRelease(h, OutBuild{Version: fmt.Sprintf("v%d", i)}, now.Add(time.Duration(i)*time.Minute))
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

func TestRecordReleaseХранитСписокИзменений(t *testing.T) {
	// В summary.json changelog есть только у текущей версии. Вопрос «что было
	// в релизе X» задают про прошлые — отвечать на него нечем, если список не
	// осел в истории вместе с самой записью о выкатке.
	now := time.Now().UTC()
	h := recordRelease(nil, OutBuild{
		Version:   "v1",
		Changelog: []string{"поднять go до 1.22", "обновить nginx"},
	}, now)

	if len(h) != 1 || len(h[0].Changelog) != 2 {
		t.Fatalf("список изменений не осел в истории: %+v", h)
	}
	h = recordRelease(h, OutBuild{Version: "v2", Changelog: []string{"починить логин"}}, now.Add(time.Hour))
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

func TestRecordReleaseДописываетСписокВГотовуюЗапись(t *testing.T) {
	// releases.json старого формата уже содержит запись о текущей версии, но
	// без списка. Ждать следующей выкатки, чтобы история начала заполняться,
	// незачем — дописываем в пустое место.
	now := time.Now().UTC()
	h := []Release{{Version: "v1", Seen: "2026-08-01T00:00:00Z"}}
	h = recordRelease(h, OutBuild{Version: "v1", Changelog: []string{"обновить nginx"}}, now)

	if len(h) != 1 {
		t.Fatalf("та же версия не должна плодить записи: %+v", h)
	}
	if len(h[0].Changelog) != 1 || h[0].Changelog[0] != "обновить nginx" {
		t.Errorf("список не дописан в готовую запись: %+v", h[0])
	}
	if h[0].Seen != "2026-08-01T00:00:00Z" {
		t.Errorf("момент обнаружения версии переписан: %+v", h[0])
	}

	// А непустой список затирать нечем: version.json могли перезаписать без
	// поля, но история — это то, что было, а не то, что видно сейчас.
	h = recordRelease(h, OutBuild{Version: "v1"}, now.Add(time.Minute))
	if len(h[0].Changelog) != 1 {
		t.Errorf("сохранённый список затёрт пустым: %+v", h[0])
	}
}

func TestTrimReleaseChangelogsДержитФайлВБерегах(t *testing.T) {
	// Файл переписывается раз в минуту и лежит в вебруте. Растёт он прежде
	// всего вширь по истории, поэтому предел проверяем на длинной истории.
	var h []Release
	now := time.Now().UTC()
	for i := 0; i < releasesKeep+5; i++ {
		var cl []string
		for j := 0; j < 40; j++ {
			cl = append(cl, strings.Repeat("тема ", 200))
		}
		h = recordRelease(h, OutBuild{Version: fmt.Sprintf("v%d", i), Changelog: cl}, now.Add(time.Duration(i)*time.Minute))
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
	releases["samoy::Сайт"] = recordRelease(releases["samoy::Сайт"], OutBuild{
		Version:   "v2",
		At:        "2026-08-02T01:00:00Z",
		Changelog: []string{"поднять go до 1.22 <-- важно", "обновить nginx"},
	}, now)

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
