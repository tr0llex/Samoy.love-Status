package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHumanDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "меньше минуты"},
		{time.Minute, "1 мин"},
		{59 * time.Minute, "59 мин"},
		{time.Hour, "1 ч"},
		{90 * time.Minute, "1 ч 30 мин"},
		{25 * time.Hour, "1 д 1 ч"},
		{48 * time.Hour, "2 д"},
		// Часы сервера иногда уходят назад (ntp), и разница становится
		// отрицательной. Показывать «-5 мин назад» нельзя.
		{-5 * time.Minute, "5 мин"},
	}
	for _, c := range cases {
		if got := humanDur(c.d); got != c.want {
			t.Errorf("humanDur(%s) = %q, ожидали %q", c.d, got, c.want)
		}
	}
}

func TestFmtTimeMoscow(t *testing.T) {
	// Агент пишет UTC, человеку нужно московское время.
	got := fmtTime(time.Date(2026, 8, 2, 9, 5, 0, 0, time.UTC))
	if got != "02.08 12:05 MSK" {
		t.Errorf("fmtTime = %q, ожидали 02.08 12:05 MSK", got)
	}
}

func TestFormatHelpListsCommands(t *testing.T) {
	help := formatHelp()
	for _, cmd := range []string{"/status", "/versions", "/incidents", "/help"} {
		if !strings.Contains(help, cmd) {
			t.Errorf("в справке нет %s", cmd)
		}
	}
}

func TestFormatStatus(t *testing.T) {
	uptime := 99.98
	now := base
	s := &Summary{
		Updated: now.Add(-time.Minute).Format(time.RFC3339),
		Overall: "degraded",
		Projects: []Project{{
			Title: "Змейки", Status: "degraded", Up: 1, Total: 2,
			Checks: []Check{
				{
					Name: "Клиент", Status: "up", Critical: true, Ms: 120,
					Since:  now.Add(-2 * time.Hour).Format(time.RFC3339),
					Uptime: map[string]*float64{"d1": &uptime},
				},
				{
					Name: "Игровой сервер", Status: "down", Critical: true, Error: "HTTP 502",
					Impact: "Матчи не идут",
					Since:  now.Add(-10 * time.Minute).Format(time.RFC3339),
				},
			},
			Units: []Unit{{
				Title: "Игровой сервер", Active: false, State: "failed",
			}},
		}},
	}

	got := formatStatus(s, now)

	// Сломанное разворачивается целиком: что это, для кого плохо, почему и
	// сколько уже длится.
	for _, want := range []string{
		"Частичный сбой", "1/2",
		"Игровой сервер", "Матчи не идут", "HTTP 502", "10 мин",
		"failed", "Данные агента",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в ответе нет %q:\n%s", want, got)
		}
	}

	// А исправное — сворачивается. Раньше бот печатал время ответа и проценты
	// у каждой зелёной проверки, и ответ на /status превращался в простыню,
	// в которой единственную красную строку приходилось искать глазами.
	for _, unwanted := range []string{"120 мс", "99.98%"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("подробности исправной проверки не нужны, но %q есть:\n%s", unwanted, got)
		}
	}
}

func TestFormatStatusWarnsAboutStaleData(t *testing.T) {
	now := base
	s := &Summary{
		// Агент молчит полчаса, но все проверки в файле зелёные: без
		// предупреждения ответ выглядел бы как «всё хорошо».
		Updated: now.Add(-30 * time.Minute).Format(time.RFC3339),
		Overall: "operational",
	}
	got := formatStatus(s, now)
	if !strings.Contains(got, "Данные устарели") {
		t.Errorf("нет предупреждения о несвежих данных:\n%s", got)
	}
}

func TestFormatEscapesHTML(t *testing.T) {
	now := base
	s := &Summary{
		Updated: now.Format(time.RFC3339),
		Overall: "down",
		Projects: []Project{{
			Title: "<b>взлом</b>", Status: "down", Up: 0, Total: 1,
			Checks: []Check{{Name: "A & B", Status: "down", Error: "<script>"}},
		}},
	}
	got := formatStatus(s, now)
	if strings.Contains(got, "<script>") || strings.Contains(got, "<b>взлом</b>") {
		t.Errorf("разметка из данных попала в сообщение как есть:\n%s", got)
	}
	if !strings.Contains(got, "A &amp; B") {
		t.Errorf("амперсанд не экранирован:\n%s", got)
	}
}

func TestFormatVersions(t *testing.T) {
	now := base
	s := &Summary{
		Updated: now.Format(time.RFC3339),
		Projects: []Project{
			{
				Title: "Snakes",
				Builds: []Build{{
					Title: "Сервер и клиент", Version: "20260802-1200-abc1234",
					At: now.Add(-3 * time.Hour).Format(time.RFC3339),
				}},
			},
			// Источник версии не настроен — это должно быть видно, а не
			// выглядеть как пустой проект.
			{Title: "Метро"},
		},
	}
	got := formatVersions(s, now)
	for _, want := range []string{"20260802-1200-abc1234", "собрано", "3 ч назад", "не настроен"} {
		if !strings.Contains(got, want) {
			t.Errorf("в ответе нет %q:\n%s", want, got)
		}
	}
}

func TestFormatIncidents(t *testing.T) {
	now := base
	empty := formatIncidents(&Summary{}, now)
	if !strings.Contains(empty, "Инцидентов не было") {
		t.Errorf("пустая история описана неверно: %s", empty)
	}

	s := &Summary{Incidents: []Incident{
		{
			Name: "Snakes · Клиент", Start: now.Add(-20 * time.Minute).Format(time.RFC3339),
			Reason: "HTTP 502",
		},
		{
			Name: "samoy.love · Сайт", Start: now.Add(-25 * time.Hour).Format(time.RFC3339),
			End: now.Add(-24 * time.Hour).Format(time.RFC3339), DurationMs: 3600_000,
			Reason: "таймаут 12s",
		},
	}}
	got := formatIncidents(s, now)
	for _, want := range []string{"Snakes · Клиент", "идёт уже 20 мин", "HTTP 502", "длился 1 ч"} {
		if !strings.Contains(got, want) {
			t.Errorf("в ответе нет %q:\n%s", want, got)
		}
	}
}

func TestFormatEvent(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  []string
	}{
		{
			"падение",
			Event{Kind: KindDown, Title: "Snakes · Клиент", Reason: "HTTP 502", At: base},
			[]string{"Snakes · Клиент", "недоступен", "HTTP 502"},
		},
		{
			"напоминание",
			Event{Kind: KindStillDown, Title: "Snakes · Клиент", Duration: 45 * time.Minute},
			[]string{"лежит уже", "45 мин"},
		},
		{
			"восстановление",
			Event{Kind: KindUp, Title: "Snakes · Клиент", Duration: time.Hour, At: base},
			[]string{"снова работает", "простой: 1 ч"},
		},
		{
			"релиз",
			Event{
				Kind: KindRelease, Title: "Snakes · Сервер", Version: "v2",
				Previous: "v1", At: base,
			},
			[]string{"обновлён", "v2", "была", "v1", "собрано"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatEvent(c.event)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("нет %q в сообщении:\n%s", want, got)
				}
			}
		})
	}
}

func TestПолоскаБерётХудшуюИзКритичныхПроверок(t *testing.T) {
	// Сайт открывался весь день, игровой сервер лежал. День плохой: брать
	// первую попавшуюся проверку значит показать благополучие, которого не было.
	full := func(up, total int64) []*Day {
		d := make([]*Day, stripDays)
		for i := range d {
			d[i] = &Day{D: "x", Up: up, Total: total}
		}
		return d
	}
	p := Project{Checks: []Check{
		{Name: "Клиент", Critical: true, Days: full(1440, 1440)},
		{Name: "Игровой сервер", Critical: true, Days: full(0, 1440)},
	}}
	if got := projectStrip(p); strings.Contains(got, "🟩") {
		t.Errorf("день с лежащим сервером не может быть зелёным: %s", got)
	}

	// Второстепенная проверка полоску портить не должна: вердикт она не роняет.
	p2 := Project{Checks: []Check{
		{Name: "Сайт", Critical: true, Days: full(1440, 1440)},
		{Name: "Админка", Critical: false, Days: full(0, 1440)},
	}}
	if got := projectStrip(p2); strings.Contains(got, "🟥") {
		t.Errorf("второстепенная проверка не должна красить полоску: %s", got)
	}
}

func TestСсылкиВедутНаСервисы(t *testing.T) {
	// Уведомление без ссылки заставляет искать адрес руками ровно тогда,
	// когда некогда: увидел «недоступен» — хочешь открыть и посмотреть.
	got := formatEvent(Event{
		Kind: KindDown, Title: "Snakes · Игровой сервер",
		URL: "https://snakes.samoy.love/healthz", Reason: "HTTP 502",
	})
	if !strings.Contains(got, `<a href="https://snakes.samoy.love/healthz">`) {
		t.Errorf("в уведомлении о падении нет ссылки: %s", got)
	}

	rel := formatEvent(Event{
		Kind: KindRelease, Title: "ChillHub · Публичный API",
		URL: "https://launcher.samoy.love/", Version: "1.2.3",
	})
	if !strings.Contains(rel, `<a href="https://launcher.samoy.love/">`) {
		t.Errorf("в сообщении о релизе нет ссылки на компонент: %s", rel)
	}

	// Мусор вместо адреса не должен уезжать в разметку.
	bad := formatEvent(Event{Kind: KindDown, Title: "X", URL: "javascript:alert(1)"})
	if strings.Contains(bad, "<a href") {
		t.Errorf("недопустимая схема попала в ссылку: %s", bad)
	}
}

func TestАптаймНеУмножаетсяДважды(t *testing.T) {
	// Агент отдаёт проценты (agent/main.go, pct). Лишнее умножение на сто
	// давало «9991.00% за 90 дней» — число, которое читается как поломка
	// бота, а не как доступность.
	v := 99.91
	full := 100.0
	cases := []struct {
		name   string
		uptime map[string]*float64
		want   string
	}{
		{"90 дней", map[string]*float64{"d90": &v}, "99.91% за 90 дней"},
		{"ровно сто", map[string]*float64{"d90": &full}, "100% за 90 дней"},
		{"только неделя", map[string]*float64{"d7": &v}, "99.91% за неделю"},
		{"нет данных", map[string]*float64{"d90": nil}, ""},
		{"пусто", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := uptimeText(Check{Uptime: c.uptime}); got != c.want {
				t.Errorf("получили %q, ожидали %q", got, c.want)
			}
		})
	}
}

// --------------------------------------------------------- список изменений

func TestРелизБезИзмененийОстаётсяПрежним(t *testing.T) {
	// Главное требование: цель, чья выкатка ничего не публикует, обязана
	// давать ровно то же сообщение, что и до появления блока изменений.
	// Иначе украшение превращается в изменение всех уведомлений сразу.
	e := Event{
		Kind: KindRelease, Title: "Snakes · Сервер", URL: "https://snakes.samoy.love/",
		Version: "v2", Previous: "v1", At: base,
	}
	want := formatEvent(e)

	e.Changelog = []string{}
	if got := formatEvent(e); got != want {
		t.Errorf("пустой список изменил сообщение:\n%s", got)
	}
	e.Changelog = []string{"", "   ", "\n"}
	if got := formatEvent(e); got != want {
		t.Errorf("список из пустых строк изменил сообщение:\n%s", got)
	}
	if strings.Contains(want, "Изменения") {
		t.Errorf("блок изменений появился без данных:\n%s", want)
	}
}

func TestСписокИзмененийИдётПоследним(t *testing.T) {
	got := formatEvent(Event{
		Kind: KindRelease, Title: "Snakes · Сервер", URL: "https://snakes.samoy.love/",
		Version: "v2", Previous: "v1", At: base,
		Changelog: []string{
			"исправить падение на пустом конфиге",
			"обновить nginx до 1.24",
		},
	})

	// Всё, ради чего уведомление читают, осталось на месте.
	for _, want := range []string{"обновлён", "<code>v2</code>", "была <code>v1</code>", "собрано"} {
		if !strings.Contains(got, want) {
			t.Errorf("из сообщения о релизе пропало %q:\n%s", want, got)
		}
	}
	// Формат блока — тот же, что у deploy-kit/bin/changelog.
	if !strings.Contains(got, "<b>Изменения</b>\n• исправить падение на пустом конфиге\n• обновить nginx до 1.24") {
		t.Errorf("блок изменений собран не по общему формату:\n%s", got)
	}
	// Именно последним: он самый длинный и единственный необязательный.
	if !strings.HasSuffix(got, "• обновить nginx до 1.24") {
		t.Errorf("блок изменений не в конце сообщения:\n%s", got)
	}
	if strings.Index(got, "Изменения") < strings.Index(got, "собрано") {
		t.Errorf("блок изменений оказался выше даты сборки:\n%s", got)
	}
}

func TestИзмененияЭкранируются(t *testing.T) {
	// Тема коммита — чужой текст, а сообщение уходит с parse_mode=HTML: один
	// «<» делает разметку невалидной, Telegram отвечает ошибкой, и владелец
	// не получает уведомления о релизе вообще.
	got := formatChangelog([]string{
		"поднять go до 1.22 <-- важно",
		"<b>жирный</b> & <i>курсив</i>",
		`закрыть <a href="javascript:alert(1)">дыру</a>`,
	})
	if strings.Contains(got, "<-- важно") || strings.Contains(got, "<b>жирный") ||
		strings.Contains(got, "<a href") || strings.Contains(got, "<i>") {
		t.Errorf("чужая разметка уехала в сообщение как есть:\n%s", got)
	}
	for _, want := range []string{"&lt;-- важно", "&lt;b&gt;жирный&lt;/b&gt; &amp; ", "&lt;a href="} {
		if !strings.Contains(got, want) {
			t.Errorf("нет экранированного %q:\n%s", want, got)
		}
	}
	// Заголовок блока рисуем мы сами, и он остаётся разметкой.
	if !strings.HasPrefix(got, "<b>Изменения</b>\n") {
		t.Errorf("заголовок блока потерялся:\n%s", got)
	}
}

func TestВраньёВСпискеУпираетсяВПотолок(t *testing.T) {
	// Лимит Telegram снят не сокращением списка, а разбиением на сообщения, но
	// потолок против чужого файла остался: тысяча строк по 540 символов — это
	// уже не релиз, а враньё в version.json. Потолок обязан быть виден.
	var huge []string
	for i := 0; i < 1000; i++ {
		huge = append(huge, strings.Repeat("очень длинная тема коммита ", 20))
	}
	msg := formatEvent(Event{
		Kind: KindRelease, Title: "Snakes · Сервер", URL: "https://snakes.samoy.love/",
		Version: "v2", Previous: "v1", At: base, Changelog: huge,
	})
	if !utf8.ValidString(msg) {
		t.Error("обрезка разрезала символ UTF-8: такое сообщение Telegram отвергнет целиком")
	}
	if n := utf8.RuneCountInString(msg); n > changelogBudget+changelogReserve+400 {
		t.Errorf("сообщение на %d символов — потолок %d не сработал", n, changelogBudget)
	}
	// Молча оборванный список читается как «больше ничего и не было».
	if !strings.Contains(msg, "список не поместился") {
		t.Errorf("потолок сработал молча:\n%s", msg[:200])
	}
	// И всё это обязано разложиться по сообщениям, каждое из которых Telegram
	// примет: превышение он не режет, а отвергает целиком.
	for i, p := range splitMessage(msg, telegramTextLimit) {
		if n := utf16Len(p); n > telegramTextLimit {
			t.Errorf("часть %d: %d единиц UTF-16 — Telegram её не примет", i+1, n)
		}
	}
}

func TestХвостГенератораНеСтановитсяПунктом(t *testing.T) {
	// deploy-kit/bin/changelog заканчивает блок строкой «…и ещё 12 коммитов».
	// Она приезжает сюда такой же строкой, как и темы коммитов, но пунктом
	// списка не является.
	got := formatChangelog([]string{"обновить nginx до 1.24", "…и ещё 12 коммитов"})
	if strings.Contains(got, "• …и ещё") {
		t.Errorf("хвост получил маркер пункта:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n…и ещё 12 коммитов") {
		t.Errorf("хвост не сохранён:\n%s", got)
	}
	// Одного хвоста без пунктов мало: блок «Изменения» ни о чём не сообщает.
	if got := formatChangelog([]string{"…и ещё 12 коммитов"}); got != "" {
		t.Errorf("блок собрался из одного хвоста: %q", got)
	}
}

func TestБотРазбираетВыводГенератораСам(t *testing.T) {
	// Путь «summary.json собран мимо агента» описан в summary.go как рабочий.
	// На нём сюда приезжает не разобранный агентом список, а ровно то, что
	// напечатал deploy-kit/bin/changelog: с заголовком «Изменения», с маркерами
	// и с уже экранированными & < >.
	//
	// Пока разбор был односторонним (заголовок и маркеры снимал только агент),
	// на этом пути заголовок доезжал до владельца ПУНКТОМ СПИСКА — под
	// настоящим заголовком, который бот рисует сам, и экранированным во второй
	// раз.
	got := formatChangelog([]string{
		"<b>Изменения</b>",
		"• ускорить расписание",
		"• поднять go до 1.22 &lt;-- важно",
		"- перевести карту на новый тайлсет",
		"…и ещё 3 коммита",
	})

	if strings.Count(got, "Изменения") != 1 {
		t.Errorf("заголовок задвоился:\n%s", got)
	}
	if strings.Contains(got, "&lt;b&gt;Изменения") {
		t.Errorf("заголовок генератора стал пунктом списка:\n%s", got)
	}
	// Маркер ставит бот, и чужой маркер дал бы «• • ускорить расписание».
	for _, bad := range []string{"• •", "• -", "• *"} {
		if strings.Contains(got, bad) {
			t.Errorf("маркер задвоился (%q):\n%s", bad, got)
		}
	}
	// Экранирование одно, а не два: иначе «go 1.22 <-- важно» доезжает до
	// владельца как «go 1.22 &amp;lt;-- важно».
	if !strings.Contains(got, "go до 1.22 &lt;-- важно") || strings.Contains(got, "&amp;lt;") {
		t.Errorf("экранирование наложилось дважды:\n%s", got)
	}
	// Всё остальное осталось на своих местах.
	if !strings.HasPrefix(got, "<b>Изменения</b>\n• ускорить расписание") {
		t.Errorf("список собран не по общему формату:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n…и ещё 3 коммита") {
		t.Errorf("хвост генератора потерялся:\n%s", got)
	}
	if strings.Count(got, "\n• ") != 3 {
		t.Errorf("пунктов должно быть три:\n%s", got)
	}

	// Один заголовок без пунктов — не блок: сообщение о релизе обязано
	// остаться прежним, а не получить пустую шапку.
	if g := formatChangelog([]string{"<b>Изменения</b>", "Изменения", "•", "-"}); g != "" {
		t.Errorf("блок собрался из одной разметки: %q", g)
	}
}

func TestМногострочнаяТемаНеРвётСписок(t *testing.T) {
	// Тема приходит многострочной, если в сообщении коммита нет пустой
	// строки после первой. В пункт списка это не годится.
	got := formatChangelog([]string{"исправить падение\nна пустом конфиге\tи не только"})
	if strings.Count(got, "\n") != 1 {
		t.Errorf("пункт разорвал список:\n%s", got)
	}
	if !strings.HasSuffix(got, "• исправить падение на пустом конфиге и не только") {
		t.Errorf("строки не склеены:\n%s", got)
	}
}

func TestОбрезкаНеРежетСимволы(t *testing.T) {
	// Битую строку UTF-8 Telegram отвергает целиком, то есть сообщение
	// молча не приходит. Проверяем обе обрезки: и ту, что считает символы для
	// читателя, и ту, что считает байты для callback_data.
	const s = "обновить nginx до версии 1.24 и перечитать конфиг"
	for n := 1; n < 80; n++ {
		got := cutRunes(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("cutRunes(%d) разрезал символ: %q", n, got)
		}
		if c := utf8.RuneCountInString(got); c > n {
			t.Fatalf("cutRunes(%d) вернул %d символов: %q", n, c, got)
		}

		got = cutBytes(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("cutBytes(%d) разрезал символ: %q", n, got)
		}
		if len(got) > n {
			t.Fatalf("cutBytes(%d) вернул %d байт: %q", n, len(got), got)
		}
	}
	// Короткая строка не трогается вовсе.
	if got := cutRunes("коротко", 100); got != "коротко" {
		t.Errorf("cutRunes обрезал то, что помещалось: %q", got)
	}
	if got := cutBytes("коротко", 100); got != "коротко" {
		t.Errorf("cutBytes обрезал то, что помещалось: %q", got)
	}
	// Ровно по пределу — тоже «помещается»: иначе тема максимальной
	// разрешённой длины получала бы многоточие ни за что.
	edge := strings.Repeat("я", changelogWidth)
	if got := cutRunes(edge, changelogWidth); got != edge {
		t.Errorf("строка ровно по пределу обрезана: %q", got)
	}
}

// changelogSubject120 — тема ровно по владельческому потолку: 120 СИМВОЛОВ,
// 240 байт. Кириллица здесь не для экзотики, а потому, что именно на ней всё и
// ломалось: прежний предел в 100 БАЙТ обрывал такую тему на сорок девятом
// символе.
func changelogSubject120(t *testing.T) string {
	t.Helper()
	s := strings.Repeat("тема ", 23) + "конец"
	if n := utf8.RuneCountInString(s); n != changelogWidth {
		t.Fatalf("подготовка теста: тема на %d символов, нужна ровно %d", n, changelogWidth)
	}
	return s
}

func TestТемаВ120СимволовДоезжаетЦеликом(t *testing.T) {
	// Потолок темы задан владельцем в СИМВОЛАХ (CLAUDE.md), и ровно столько же
	// режет генератор. Бот — последний в цепочке: стоит ему оказаться строже
	// генератора, и одну тему режут дважды на разной длине, причём второй рез
	// приходится уже на многоточие первого.
	subject := changelogSubject120(t)

	// Вход — ровно то, что печатает deploy-kit/bin/changelog: заголовок,
	// маркер, экранированный текст. Этот путь описан в summary.go как рабочий.
	got := formatChangelog([]string{"<b>Изменения</b>", "• " + subject})
	if !strings.Contains(got, "• "+subject) {
		t.Errorf("тема в 120 символов не доехала целиком:\n%s", got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("тема ровно по потолку получила многоточие:\n%s", got)
	}

	// Тот же список на экране «/changelog имя» — слово в слово. Экран и
	// сообщение рассказывают про одну и ту же выкатку, и расходиться им нельзя.
	screen := itemLines(release("v1", base, subject), "")
	if !strings.Contains(screen, "• "+subject) {
		t.Errorf("экран разошёлся с сообщением на теме предельной длины:\n%s", screen)
	}
}

// changelogOf40 — релиз из сорока тем предельной длины.
//
// Число не выдумано: сорок один коммит — самый крупный релиз хозяйства за год,
// и именно на нём становится видно, что 4096 единиц Telegram недостаточно.
// Темы разные, но длина у каждой ровно предельная: одинаковые пункты позволили
// бы проверке пройти, ничего не проверив.
func changelogOf40(t *testing.T) []string {
	t.Helper()
	subject := []rune(changelogSubject120(t))
	lines := []string{"<b>Изменения</b>"}
	for i := 0; i < 40; i++ {
		item := fmt.Sprintf("%02d ", i) + string(subject[3:])
		if n := utf8.RuneCountInString(item); n != changelogWidth {
			t.Fatalf("подготовка теста: пункт на %d символов, нужно %d", n, changelogWidth)
		}
		lines = append(lines, "• "+item)
	}
	return lines
}

func TestРелизИзСорокаКоммитовНеТеряетНиОдного(t *testing.T) {
	// ГЛАВНАЯ ПРОВЕРКА ЗАДАЧИ. Владелец сказал: не обрезай список выкаченных
	// коммитов. Сорок тем по 120 символов — это около 4800 единиц UTF-16, то
	// есть больше одного сообщения Telegram. Ответ на это — не показать
	// меньше, а разложить по нескольким сообщениям подряд.
	lines := changelogOf40(t)
	msg := formatEvent(Event{
		Kind: KindRelease, Title: "Snakes · Сервер", URL: "https://snakes.samoy.love/",
		Version: "v2", Previous: "v1", At: base, Changelog: lines,
	})
	if n := strings.Count(msg, "\n• "); n != 40 {
		t.Fatalf("в сообщение попало %d пунктов из 40:\n%s", n, msg)
	}
	if strings.Contains(msg, "…и ещё") {
		t.Errorf("список свёрнут в «…и ещё N» — ровно то, от чего уходили:\n%s", msg)
	}

	parts := splitMessage(msg, telegramTextLimit)
	if len(parts) < 2 {
		t.Fatalf("сорок тем предельной длины (%d единиц) обязаны занять больше одного сообщения",
			utf16Len(msg))
	}
	for i, p := range parts {
		if n := utf16Len(p); n > telegramTextLimit {
			t.Errorf("часть %d: %d единиц UTF-16 — Telegram её не примет", i+1, n)
		}
		if !utf8.ValidString(p) {
			t.Errorf("часть %d разрезала символ UTF-8", i+1)
		}
		// Разметка не должна оказаться разрезанной пополам: негодный HTML —
		// это отказ Telegram, то есть молчание вместо уведомления.
		if strings.Count(p, "<b>") != strings.Count(p, "</b>") {
			t.Errorf("часть %d разрезана посреди разметки:\n%s", i+1, p)
		}
		// Продолжение обязано узнаваться: иначе вторая половина списка
		// читается как отдельное сообщение о другой выкатке.
		if i > 0 && !strings.HasPrefix(p, "<i>продолжение (") {
			t.Errorf("часть %d не помечена продолжением:\n%s", i+1, p)
		}
	}

	// НИЧЕГО НЕ ПОТЕРЯНО И ПОРЯДОК СОХРАНЁН — ради этого всё и затевалось.
	joined := strings.Join(parts, "\n")
	prev := -1
	for i := 0; i < 40; i++ {
		want := fmt.Sprintf("\n• %02d ", i)
		at := strings.Index(joined, want)
		if at < 0 {
			t.Fatalf("пункт %02d не доехал ни до одной части", i)
		}
		if at < prev {
			t.Fatalf("пункт %02d уехал вперёд предыдущего: порядок нарушен", i)
		}
		prev = at
	}
}

func TestПолныйСписокГенератораНеОбрезаетсяБюджетом(t *testing.T) {
	// Восемь тем по 120 символов кириллицы — это около 2100 БАЙТ, и прежний
	// бюджет в 1400 байт обрывал такой блок на третьем пункте. Генератор
	// прислал восемь — читатель обязан увидеть восемь.
	subject := []rune(changelogSubject120(t))
	lines := []string{"<b>Изменения</b>"}
	for i := 0; i < 8; i++ {
		// Темы разные, но длина у каждой ровно предельная: одинаковые бот
		// пропустил бы, а проверка вышла бы ни о чём.
		item := fmt.Sprintf("%d ", i) + string(subject[2:])
		if n := utf8.RuneCountInString(item); n != changelogWidth {
			t.Fatalf("подготовка теста: пункт на %d символов, нужно %d", n, changelogWidth)
		}
		lines = append(lines, "• "+item)
	}

	got := formatChangelog(lines)
	if n := strings.Count(got, "\n• "); n != 8 {
		t.Fatalf("пунктов %d, ожидали 8:\n%s", n, got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("что-то обрезано или объявлено непоместившимся:\n%s", got)
	}

	// Восьмикоммитный релиз — обычная выкатка, и она обязана по-прежнему
	// приходить ОДНИМ сообщением: разбиение заводится ради длинных, а не ради
	// каждого.
	msg := formatEvent(Event{
		Kind: KindRelease, Title: "Snakes · Сервер", URL: "https://snakes.samoy.love/",
		Version: "v2", Previous: "v1", At: base, Changelog: lines,
	})
	if n := utf16Len(msg); n > 4096 {
		t.Errorf("сообщение на %d единиц UTF-16 — Telegram его не примет", n)
	}
	if n := len(splitMessage(msg, telegramTextLimit)); n != 1 {
		t.Errorf("обычная выкатка разъехалась на %d сообщений", n)
	}
}

func TestВраньёВПолеЗажимаетсяПоСимволам(t *testing.T) {
	// Генератор мог не запускаться вовсе: version.json собирает выкатка, а её
	// пишут руками. Пределы бота — защита от чужого файла, и на 500 символах
	// в одном пункте они обязаны сработать.
	hostile := strings.Repeat("я", 500)

	got := formatChangelog([]string{hostile})
	line, ok := strings.CutPrefix(got, "<b>Изменения</b>\n• ")
	if !ok {
		t.Fatalf("блок собран не по формату:\n%s", got)
	}
	if n := utf8.RuneCountInString(line); n > changelogWidth {
		t.Errorf("пункт на %d символов, предел %d", n, changelogWidth)
	}
	if !strings.HasSuffix(line, "…") {
		t.Errorf("обрезка не отмечена многоточием: %q", line)
	}
	if !utf8.ValidString(got) {
		t.Error("обрезка разрезала символ UTF-8: такое сообщение Telegram отвергнет целиком")
	}

	// Та же защита на экране цели: он читает тот же чужой файл.
	screen := itemLines(release("v1", base, hostile), "")
	for _, l := range strings.Split(strings.TrimSuffix(screen, "\n"), "\n") {
		if n := utf8.RuneCountInString(strings.TrimPrefix(l, "• ")); n > changelogWidth {
			t.Errorf("на экране пункт на %d символов, предел %d", n, changelogWidth)
		}
	}
}

func TestСклонениеКоммитов(t *testing.T) {
	// «…и ещё 2 коммитов» читается как поломка формата, а не как список.
	cases := map[int]string{
		1: "коммит", 2: "коммита", 4: "коммита", 5: "коммитов",
		11: "коммитов", 12: "коммитов", 14: "коммитов", 21: "коммит",
		22: "коммита", 25: "коммитов", 111: "коммитов", 101: "коммит",
	}
	for n, want := range cases {
		if got := pluralCommits(n); got != want {
			t.Errorf("pluralCommits(%d) = %q, ожидали %q", n, got, want)
		}
	}
}
