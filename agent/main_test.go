package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestUptimeFromBuckets(t *testing.T) {
	b := []bucket{
		{"d1", int64(10), int64(10), int64(50)},
		{"d2", int64(5), int64(10), int64(50)},
	}
	got := uptimeFromBuckets(b, 2)
	if got == nil || *got != 75 {
		t.Fatalf("15 из 20 = 75%%, получили %v", got)
	}
	// Окно короче истории: берём только хвост.
	got = uptimeFromBuckets(b, 1)
	if got == nil || *got != 50 {
		t.Fatalf("последняя корзина 5 из 10 = 50%%, получили %v", got)
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
