package main

import (
	"strings"
	"testing"
	"time"
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
