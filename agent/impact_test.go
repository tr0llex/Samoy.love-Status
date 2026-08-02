package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Тесты про то, ЧЕМ меряется состояние: по каким признакам проверка считается
// сломанной и как из проверок складывается вердикт для пользователя.

// ------------------------------------------------------- признаки сбоя

func TestКод200СНеправильнымТеломЭтоСбой(t *testing.T) {
	// Сервис отвечает 200 и страницей об ошибке. По одному коду он здоров, для
	// пользователя — нет.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<h1>503 Service Unavailable</h1>`))
	}))
	defer srv.Close()

	r := checkOnce(Check{URL: srv.URL, Expect: 200, BodyIncludes: "</html>"}, srv.Client())
	if r.status != statusDown {
		t.Fatalf("страница об ошибке при коде 200 должна быть сбоем, получили %q", r.status)
	}
	if r.errText == "" {
		t.Error("причина должна называть, чего именно не хватило в теле")
	}
}

func TestНеправильныйContentTypeЭтоСбой(t *testing.T) {
	// Главный тихий сбой service worker'а: SPA-фолбэк отдаёт HTML вместо
	// скрипта, код при этом 200, а офлайн-режим у пользователей сломан.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	}))
	defer srv.Close()

	r := checkOnce(Check{URL: srv.URL, Expect: 200, ExpectType: "javascript"}, srv.Client())
	if r.status != statusDown {
		t.Fatalf("HTML вместо скрипта должен быть сбоем, получили %q", r.status)
	}
}

func TestМедленныйОтветЭтоОтдельноеСостояние(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := checkOnce(Check{URL: srv.URL, Expect: 200, SlowMs: 50}, srv.Client())
	if r.status != statusSlow {
		t.Fatalf("ответ дольше порога — это «медленно», получили %q", r.status)
	}
	// «Медленно» остаётся доступностью: аптайм должен считаться так же, как
	// раньше, иначе его нельзя сравнить с собственной историей.
	if !r.ok() {
		t.Error("медленный ответ — это всё ещё доступность, а не простой")
	}

	// Без порога тот же ответ обязан считаться нормальным.
	r = checkOnce(Check{URL: srv.URL, Expect: 200}, srv.Client())
	if r.status != statusUp {
		t.Errorf("без порога медленный ответ — обычный успех, получили %q", r.status)
	}
}

func TestУпавшаяПроверкаНеПометитсяМедленной(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := checkOnce(Check{URL: srv.URL, Expect: 200, SlowMs: 10}, srv.Client())
	if r.status != statusDown {
		t.Fatalf("«медленно» не должно перебивать «не работает», получили %q", r.status)
	}
}

func TestРедиректНаЧужойХостЭтоСбой(t *testing.T) {
	// Угнанный домен или кривой конфиг nginx иначе выглядят здоровьем:
	// посторонний сервер ответил 200, и проверка довольна.
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer srv.Close()

	// Оба сервера httptest живут на 127.0.0.1, поэтому хост совпадает и такой
	// редирект законен.
	r := checkOnce(Check{URL: srv.URL, Expect: 200}, srv.Client())
	if r.status != statusUp {
		t.Fatalf("редирект внутри того же хоста законен, получили %q (%s)", r.status, r.errText)
	}

	// Настоящий кросс-хостовый случай: тот же сервер, но исходный адрес назван
	// localhost, а редирект уводит на 127.0.0.1 — для проверки это разные хосты.
	viaLocalhost := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if viaLocalhost == srv.URL {
		t.Skip("httptest выдал неожиданный адрес, подменить хост нечем")
	}
	r = checkOnce(Check{URL: viaLocalhost, Expect: 200}, srv.Client())
	if r.status != statusDown {
		t.Fatalf("уход на другой хост обязан быть сбоем, получили %q (%s)", r.status, r.errText)
	}
	if !contains(r.errText, "посторонний хост") {
		t.Errorf("причина должна называть уход на чужой хост, получили %q", r.errText)
	}

	// А если чужой хост объявлен разрешённым — это уже осознанное решение.
	r = checkOnce(Check{URL: viaLocalhost, Expect: 200, AllowHosts: []string{"127.0.0.1"}}, srv.Client())
	if r.status != statusUp {
		t.Fatalf("разрешённый редирект не должен быть сбоем, получили %q (%s)", r.status, r.errText)
	}
}

func TestСписокРазрешённыхХостов(t *testing.T) {
	if allowedHost("evil.example", nil) {
		t.Error("посторонний хост не может быть разрешён пустым списком")
	}
	if !allowedHost("Evil.Example", []string{"evil.example"}) {
		t.Error("разрешённый хост сравнивается без учёта регистра")
	}
}

// ------------------------------------------------------- подтверждение сбоя

func TestСбойПодтверждаетсяВторымЗапросом(t *testing.T) {
	// Первый запрос падает, второй проходит: моргнула сеть, а не сервис.
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := checkHTTP(Check{URL: srv.URL, Expect: 200}, srv.Client())
	if r.status != statusUp {
		t.Fatalf("разовый сбой не должен становиться инцидентом, получили %q", r.status)
	}
	if r.attempts != 2 {
		t.Errorf("ожидали ровно два захода, получили %d", r.attempts)
	}
}

func TestНастоящийСбойПереживаетПодтверждение(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	r := checkHTTP(Check{URL: srv.URL, Expect: 200}, srv.Client())
	if r.status != statusDown {
		t.Fatalf("устойчивый сбой обязан остаться сбоем, получили %q", r.status)
	}
}

func TestУспехНеТребуетВторогоЗапроса(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	checkHTTP(Check{URL: srv.URL, Expect: 200}, srv.Client())
	if n != 1 {
		t.Errorf("подтверждать нечего — ожидали один запрос, получили %d", n)
	}
}

// ------------------------------------------------------- сценарии

func TestСценарийПроходитПутьПользователя(t *testing.T) {
	// Манифест объявляет версию, вторым шагом забираем манифест этой версии.
	// Одиночный GET по latest.json такую поломку не увидит.
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.2.2"}`))
	})
	mux.HandleFunc("/1.2.2.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1.2.2","files":[{"path":"a.dll"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := Check{Steps: []Step{
		{Name: "последняя версия", URL: srv.URL + "/latest.json", Capture: map[string]string{"version": "version"}},
		{Name: "манифест сборки", URL: srv.URL + "/{version}.json", BodyIncludes: `"files"`},
	}}
	if r := checkOnce(c, srv.Client()); r.status != statusUp {
		t.Fatalf("исправный путь обязан проходить, получили %q (%s)", r.status, r.errText)
	}
}

func TestСценарийЛовитОборваннуюЦепочку(t *testing.T) {
	// Версия объявлена, а манифеста для неё нет: публикация доехала наполовину.
	// Это ровно тот случай, когда latest.json отвечает 200 и всё «хорошо».
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"9.9.9"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := Check{Steps: []Step{
		{Name: "последняя версия", URL: srv.URL + "/latest.json", Capture: map[string]string{"version": "version"}},
		{Name: "манифест сборки", URL: srv.URL + "/{version}.json"},
	}}
	r := checkOnce(c, srv.Client())
	if r.status != statusDown {
		t.Fatalf("оборванная цепочка — это сбой, получили %q", r.status)
	}
	if !contains(r.errText, "манифест сборки") {
		t.Errorf("в ошибке должен называться упавший шаг, получили %q", r.errText)
	}
}

func TestСценарийСообщаетОПропавшемПолеЗахвата(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"нетверсии":true}`))
	}))
	defer srv.Close()

	c := Check{Steps: []Step{
		{Name: "версия", URL: srv.URL, Capture: map[string]string{"version": "version"}},
	}}
	r := checkOnce(c, srv.Client())
	if r.status != statusDown {
		t.Fatalf("без нужного поля продолжать нечем, получили %q", r.status)
	}
}

// ------------------------------------------------------- вердикт проекта

func критичная() *bool      { b := true; return &b }
func второстепенная() *bool { b := false; return &b }

func TestКритичностьЗадаётсяЯвноИПоУмолчаниюВключена(t *testing.T) {
	if !(Check{}).IsCritical() {
		t.Error("незаполненное поле должно означать критичную проверку, а не второстепенную")
	}
	if !(Check{Critical: критичная()}).IsCritical() {
		t.Error("critical:true — критичная")
	}
	if (Check{Critical: второстепенная()}).IsCritical() {
		t.Error("critical:false — второстепенная")
	}
}

func TestВторостепеннаяПроверкаНеРоняетВердиктПроекта(t *testing.T) {
	// Ровно случай ChillHub: админка недоступна, пользователей это не касается.
	op := OutProject{Total: 2, Up: 2, AuxDown: 1}
	if got := projectStatus(op); got != statusUp {
		t.Fatalf("падение второстепенной проверки не меняет вердикт, получили %q", got)
	}
	if op.AuxDown != 1 {
		t.Error("но умолчать о ней тоже нельзя — счётчик должен доехать до страницы")
	}
}

func TestКритичнаяПроверкаРоняетВердиктПроекта(t *testing.T) {
	// Случай Snakes: игровой сервер лежит, клиент открывается. Матчей нет.
	if got := projectStatus(OutProject{Total: 2, Up: 1}); got != "degraded" {
		t.Fatalf("ожидали degraded, получили %q", got)
	}
	if got := projectStatus(OutProject{Total: 2, Up: 0}); got != statusDown {
		t.Fatalf("все критичные лежат — проект не работает, получили %q", got)
	}
}

func TestМедленнаяКритичнаяПроверкаДаётДеградацию(t *testing.T) {
	if got := projectStatus(OutProject{Total: 2, Up: 2, Slow: 1}); got != "degraded" {
		t.Fatalf("медленный критичный сервис — это деградация, получили %q", got)
	}
}

// ------------------------------------------------------- общий вердикт

func TestОбщийСтатусРазличаетМасштабСбоя(t *testing.T) {
	// Раньше всё в этой таблице, кроме последней строки, называлось одинаково.
	cases := []struct {
		name     string
		projects []OutProject
		want     string
	}{
		{
			"всё работает",
			[]OutProject{{Total: 3, Up: 3}, {Total: 2, Up: 2}},
			"operational",
		},
		{
			"лежит одна второстепенная проверка",
			[]OutProject{{Total: 3, Up: 3, AuxDown: 1}, {Total: 2, Up: 2}},
			"degraded",
		},
		{
			"лежит одна критичная из пяти",
			[]OutProject{{Total: 3, Up: 2}, {Total: 2, Up: 2}},
			"degraded",
		},
		{
			"лежит больше половины критичных",
			[]OutProject{{Total: 3, Up: 0}, {Total: 2, Up: 2}},
			"major",
		},
		{
			"лежит всё",
			[]OutProject{{Total: 3, Up: 0}, {Total: 2, Up: 0}},
			"down",
		},
	}
	for _, c := range cases {
		if got := overallStatus(c.projects); got != c.want {
			t.Errorf("%s: ожидали %q, получили %q", c.name, c.want, got)
		}
	}
}

// ------------------------------------------------------- честный аптайм

func TestАптаймСчитаетсяПоКалендарюАНеПоЧислуКорзин(t *testing.T) {
	// Три дня замеров, растянутые на три месяца. Раньше это давало «100% за 90
	// дней»: простой самого агента не просто не учитывался, а улучшал цифру.
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	daily := []bucket{
		{"2026-05-01", int64(1440), int64(1440), int64(100)},
		{"2026-07-01", int64(1440), int64(1440), int64(100)},
		{now.Format("2006-01-02"), int64(1440), int64(1440), int64(100)},
	}

	// В окно 90 суток от 2026-08-02 попадает только 2026-07-01 и сегодня.
	got := uptimeOverDays(daily, now, 90)
	if got == nil {
		t.Fatal("замеры в окне есть, аптайм не может быть неизвестен")
	}
	if *got != 100 {
		t.Errorf("по попавшим в окно дням всё исправно, ожидали 100, получили %v", *got)
	}

	days := daysForPage(daily, now, 90)
	if len(days) != 90 {
		t.Fatalf("шкала всегда 90 ячеек по календарю, получили %d", len(days))
	}
	known := 0
	for _, d := range days {
		if d != nil {
			known++
		}
	}
	if known != 2 {
		t.Errorf("данные есть за 2 суток из 90, остальные — дырки; насчитали %d", known)
	}
	if days[89] == nil || days[89].D != now.Format("2006-01-02") {
		t.Error("последняя ячейка — сегодняшний день")
	}
}

func TestДниБезЗамеровОстаютсяДырками(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	daily := []bucket{{"2026-08-02", int64(10), int64(10), int64(50)}}
	days := daysForPage(daily, now, 5)
	for i := 0; i < 4; i++ {
		if days[i] != nil {
			t.Errorf("ячейка %d должна быть дыркой, получили %+v", i, days[i])
		}
	}
	if days[4] == nil {
		t.Fatal("сегодняшний день известен")
	}
}

func TestПустоеОкноДаётНеизвестность(t *testing.T) {
	// Ни одного замера в окне — это «неизвестно», а не «ноль процентов»
	// и тем более не «сто».
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	daily := []bucket{{"2020-01-01", int64(10), int64(10), int64(50)}}
	if got := uptimeOverDays(daily, now, 90); got != nil {
		t.Errorf("без замеров в окне аптайм неизвестен, получили %v", *got)
	}
}

func TestЧасовоеОкноТожеПоКалендарю(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 30, 0, 0, time.UTC)
	hourly := []bucket{
		{fmt.Sprint(now.Truncate(time.Hour).Unix()), int64(30), int64(60), int64(100)},
		{fmt.Sprint(now.Add(-1000 * time.Hour).Truncate(time.Hour).Unix()), int64(60), int64(60), int64(100)},
	}
	got := uptimeOverHours(hourly, now, 24*7)
	if got == nil {
		t.Fatal("текущий час в окне, аптайм известен")
	}
	if *got != 50 {
		t.Errorf("старая корзина вне окна не должна вытягивать цифру, ожидали 50, получили %v", *got)
	}
}

func TestМёртваяСлужбаРоняетВердикт(t *testing.T) {
	// Юниты в вердикт не входили вовсе: бот будил владельца сообщением
	// «служба упала», а страница в ту же минуту показывала «Все системы
	// работают». Проверяем, что расхождения больше нет.
	op := OutProject{Total: 2, Up: 2, UnitsDown: 1}
	if got := projectStatus(op); got != "degraded" {
		t.Errorf("проект с мёртвой службой: %q, ожидали degraded", got)
	}
	// Не «лежит»: запросы в этот момент часто ещё обслуживаются.
	if got := projectStatus(OutProject{Total: 2, Up: 2, UnitsDown: 2}); got == statusDown {
		t.Error("мёртвая служба при живых проверках не должна означать «лежит»")
	}
	// Проект вообще без критичных проверок — судим по службам.
	if got := projectStatus(OutProject{UnitsDown: 1}); got != "degraded" {
		t.Errorf("проект без проверок с мёртвой службой: %q", got)
	}
	if got := overallStatus([]OutProject{{Total: 2, Up: 2, UnitsDown: 1}}); got != "degraded" {
		t.Errorf("общий вердикт с мёртвой службой: %q, ожидали degraded", got)
	}
	if got := overallStatus([]OutProject{{UnitsDown: 1}}); got != "degraded" {
		t.Errorf("общий вердикт без проверок, но с мёртвой службой: %q", got)
	}
}

func TestЖизненныйЦиклИнцидента(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	at := now.Format(time.RFC3339)

	// Падение при первом же наблюдении обязано попасть в историю. Раньше оно
	// молчало: инцидент открывался только при переходе из известного
	// состояния, и падение, заставшее агента без истории, исчезало.
	inc := applyIncident(nil, incidentChange{
		id: "metro", name: "Метро · Расписание", prev: nil,
		status: statusDown, errText: "connection refused", at: at,
	}, now)
	if len(inc) != 1 {
		t.Fatalf("падение при первом наблюдении не записано: %+v", inc)
	}
	if inc[0].Reason != "connection refused" || inc[0].End != "" {
		t.Errorf("инцидент записан неверно: %+v", inc[0])
	}

	// Повторное наблюдение того же падения второго инцидента не создаёт.
	same := applyIncident(inc, incidentChange{
		id: "metro", name: "Метро · Расписание",
		prev: &CheckState{Status: statusDown}, status: statusDown, at: at,
	}, now)
	if len(same) != 1 {
		t.Fatalf("повтор падения удвоил историю: %+v", same)
	}

	// Восстановление закрывает инцидент и считает длительность.
	later := now.Add(30 * time.Minute)
	closed := applyIncident(same, incidentChange{
		id: "metro", name: "Метро · Расписание",
		prev: &CheckState{Status: statusDown}, status: statusUp,
		at: later.Format(time.RFC3339),
	}, later)
	if closed[0].End == "" {
		t.Fatal("инцидент не закрыт")
	}
	if got := closed[0].DurationMs; got != int64(30*time.Minute/time.Millisecond) {
		t.Errorf("длительность %d мс, ожидали 30 минут", got)
	}

	// «Медленно» — не инцидент: иначе история засорится деградациями и в ней
	// утонут настоящие падения.
	slow := applyIncident(nil, incidentChange{
		id: "metro", name: "Метро · Расписание",
		prev: &CheckState{Status: statusUp}, status: statusSlow, at: at,
	}, now)
	if len(slow) != 0 {
		t.Errorf("«медленно» записано инцидентом: %+v", slow)
	}

	// Возврат из «медленно» в «работает» тоже ничего не закрывает.
	if got := applyIncident(closed, incidentChange{
		id: "metro", prev: &CheckState{Status: statusSlow}, status: statusUp, at: at,
	}, now); len(got) != 1 || got[0].End != closed[0].End {
		t.Errorf("выход из «медленно» тронул историю: %+v", got)
	}
}

func TestИмяИнцидентаБерётсяИзАктуальногоКонфига(t *testing.T) {
	incidents := []Incident{
		{Service: "metro-sw", Name: "Hello Kitty Метро · Service worker"},
		{Service: "ушедшая-проверка", Name: "Что-то · Проверка"},
	}
	renameIncidents(incidents, map[string]string{
		"metro-sw": "Метро · Service worker",
	})

	if got := incidents[0].Name; got != "Метро · Service worker" {
		t.Errorf("старое название пережило переименование: %q", got)
	}
	// Проверки в конфиге нет — судить не по чему, оставляем записанное.
	if got := incidents[1].Name; got != "Что-то · Проверка" {
		t.Errorf("инцидент исчезнувшей проверки остался без имени: %q", got)
	}
}

func TestИнцидентЗакрываетсяУСвоейПроверки(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	at := now.Format(time.RFC3339)
	incidents := []Incident{
		{Service: "metro", Name: "Метро", Start: at},
		{Service: "snakes", Name: "Змейки", Start: at},
	}
	got := applyIncident(incidents, incidentChange{
		id: "snakes", prev: &CheckState{Status: statusDown}, status: statusUp,
		at: now.Add(time.Hour).Format(time.RFC3339),
	}, now.Add(time.Hour))

	if got[0].End != "" {
		t.Error("закрыт чужой инцидент")
	}
	if got[1].End == "" {
		t.Error("свой инцидент не закрыт")
	}
}
