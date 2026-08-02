package main

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Время во всех сообщениях — московское.
//
// Зона задана смещением, а не именем: с 2014 года в Москве нет переходов на
// летнее время, поэтому UTC+3 постоянен, а бот не зависит от наличия tzdata
// в системе. Агент пишет всё в UTC, показывать это владельцу неудобно.
var msk = time.FixedZone("MSK", 3*60*60)

const (
	up       = "🟢"
	down     = "🔴"
	degraded = "🟠"
	slowIcon = "🟡"
	unknown  = "⚪️"
)

func esc(s string) string { return html.EscapeString(s) }

func fmtTime(t time.Time) string {
	return t.In(msk).Format("02.01 15:04 MSK")
}

// humanDur — длительность по-русски.
//
// Единицы сокращённые («12 ч 30 мин»), чтобы не тащить в бот склонение
// числительных ради строки, которую всё равно читает один человек.
func humanDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "меньше минуты"
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%d ч", h)
		}
		return fmt.Sprintf("%d ч %d мин", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) - days*24
		if h == 0 {
			return fmt.Sprintf("%d д", days)
		}
		return fmt.Sprintf("%d д %d ч", days, h)
	}
}

func statusIcon(status string) string {
	switch status {
	case "up", "operational":
		return up
	case "down":
		return down
	case "slow":
		// «Медленно» — не падение и не норма: сервис отвечает, но дольше
		// порога. Своя иконка, чтобы не путать ни с тем, ни с другим.
		return slowIcon
	case "degraded", "major":
		return degraded
	default:
		return unknown
	}
}

func formatHelp() string {
	return strings.Join([]string{
		"<b>Статус samoy.love</b>",
		"",
		"Кнопки под сообщением переключают экраны прямо здесь — новые",
		"сообщения при этом не плодятся, переписывается текущее.",
		"«📊 Открыть» показывает страницу целиком, не выходя из Telegram.",
		"",
		"Если удобнее командами:",
		"/status — что живо, что лежит, аптайм",
		"/versions — версии сервисов со временем сборки",
		"/incidents — последние падения",
		"/help — эта справка",
		"",
		"Сам сообщу, когда что-то упадёт, поднимется или обновится.",
		"Про «медленно» будить не буду — это видно на экране статуса.",
		"",
		"<b>Полоска доступности</b> под проектом — две недели по суткам:",
		"🟩 без сбоев · 🟨 меньше 1% · 🟧 до 10% · 🟥 больше · ⬜ нет данных",
	}, "\n")
}

// formatStatus — ответ на /status.
//
// Порядок проектов не сортируется: он задан конфигом и совпадает с порядком
// на самой странице, чтобы взгляд искал сервис в одном и том же месте.
// stripDays — сколько суток показываем полоской. Четырнадцать умещаются в
// одну строку на телефоне; девяносто, как на странице, переносились бы и
// превращались в кашу.
const stripDays = 14

// Квадраты полоски. Порог тот же, что у ступеней на странице: сутки с одной
// сбойной минутой из 1440 не должны выглядеть как сутки, лежавшие наполовину.
func dayCell(d *Day) string {
	if d == nil || d.Total == 0 {
		return "⬜"
	}
	switch ratio := float64(d.Up) / float64(d.Total); {
	case ratio == 1:
		return "🟩"
	case ratio >= 0.99:
		return "🟨"
	case ratio >= 0.9:
		return "🟧"
	default:
		return "🟥"
	}
}

// projectStrip — доступность проекта за две недели одной строкой.
//
// Считаем по ключевым проверкам и по худшей из них за сутки: если в этот день
// лежал игровой сервер, день плохой, даже когда сайт открывался.
func projectStrip(p Project) string {
	worst := make([]*Day, stripDays)
	for _, c := range p.Checks {
		if !c.Critical || len(c.Days) == 0 {
			continue
		}
		days := c.Days
		if len(days) > stripDays {
			days = days[len(days)-stripDays:]
		}
		for i, d := range days {
			slot := stripDays - len(days) + i
			if slot < 0 || d == nil || d.Total == 0 {
				continue
			}
			cur := worst[slot]
			if cur == nil || float64(d.Up)/float64(d.Total) < float64(cur.Up)/float64(cur.Total) {
				worst[slot] = d
			}
		}
	}
	var b strings.Builder
	for _, d := range worst {
		b.WriteString(dayCell(d))
	}
	return b.String()
}

func formatStatus(s *Summary, now time.Time) string {
	var b strings.Builder

	// Тот же принцип, что на странице: здоровое сворачивается, сломанное
	// поднимается наверх. Раньше бот печатал все проекты со всеми проверками,
	// службами и полосками — два десятка зелёных строк, среди которых
	// единственную красную приходилось искать глазами.
	switch s.Overall {
	case "operational":
		b.WriteString(up + " <b>Всё работает</b>")
	case "down":
		b.WriteString(down + " <b>Всё лежит</b>")
	case "major":
		b.WriteString(down + " <b>Крупный сбой</b>")
	default:
		b.WriteString(degraded + " <b>Частичный сбой</b>")
	}

	var okCrit, totalCrit, auxBad int
	for _, p := range s.Projects {
		okCrit += p.Up
		totalCrit += p.Total
		auxBad += p.AuxDown + p.AuxSlow
	}
	fmt.Fprintf(&b, "\n<code>%d/%d</code> ключевых проверок в норме", okCrit, totalCrit)
	if auxBad > 0 {
		fmt.Fprintf(&b, " · <code>%d</code> второстеп. не в порядке", auxBad)
	}
	b.WriteString("\n")

	// Сломанное — единственное, что раскрывается подробно.
	for _, p := range s.Projects {
		for _, c := range p.Checks {
			if c.Status == "up" {
				continue
			}
			icon, tail := statusIcon(c.Status), ""
			if !c.Critical {
				icon, tail = degraded, " <i>(второстеп.)</i>"
			}
			fmt.Fprintf(&b, "\n%s <b>%s · %s</b>%s", icon, link(p.Title, p.URL), link(c.Name, c.URL), tail)
			if c.Impact != "" && c.Status == "down" {
				b.WriteString("\n   " + esc(c.Impact))
			}
			if c.Error != "" {
				b.WriteString("\n   <code>" + esc(c.Error) + "</code>")
			}
			if t, ok := parseTime(c.Since); ok {
				fmt.Fprintf(&b, "\n   %s", humanDur(now.Sub(t)))
			}
			b.WriteString("\n")
		}
	}

	// Остальные проекты — по строке. Полоска за две недели говорит о них
	// больше, чем перечисление процентов у каждой проверки.
	b.WriteString("\n")
	for _, p := range s.Projects {
		aux := ""
		if n := p.AuxDown + p.AuxSlow; n > 0 {
			aux = fmt.Sprintf(" <i>+%d</i>", n)
		}
		fmt.Fprintf(&b, "%s %s <code>%d/%d</code>%s", statusIcon(p.Status), link(p.Title, p.URL), p.Up, p.Total, aux)
		if strip := projectStrip(p); strings.ContainsAny(strip, "🟩🟨🟧🟥") {
			b.WriteString("\n" + strip)
		}
		b.WriteString("\n")
	}

	// Службы показываем только когда с ними что-то не так: перечислять
	// работающие значит утопить в них неработающую.
	var dead []string
	for _, p := range s.Projects {
		for _, u := range p.Units {
			if !u.Active {
				dead = append(dead, fmt.Sprintf("%s %s · %s — %s", down, esc(p.Title), esc(u.Title), esc(u.State)))
			}
		}
	}
	if len(dead) > 0 {
		b.WriteString("\n" + strings.Join(dead, "\n") + "\n")
	}

	b.WriteString("\n" + freshness(s, now))
	return b.String()
}

// freshness — строка о свежести данных.
//
// Нужна отдельной строкой: если агент упал, все проверки в файле выглядят
// зелёными и сколь угодно старыми. Без отметки времени такой ответ вводит
// в заблуждение сильнее, чем отсутствие ответа.
func freshness(s *Summary, now time.Time) string {
	t, ok := parseTime(s.Updated)
	if !ok {
		return "<i>Время обновления данных неизвестно</i>"
	}
	age := now.Sub(t)
	line := fmt.Sprintf("<i>Данные агента: %s (%s назад)</i>", fmtTime(t), humanDur(age))
	if age >= staleAfter {
		line += "\n" + degraded + " <b>Данные устарели</b> — похоже, агент не обходит сервисы"
	}
	return line
}

func formatVersions(s *Summary, now time.Time) string {
	var b strings.Builder
	b.WriteString("<b>Версии</b>\n")
	for _, p := range s.Projects {
		fmt.Fprintf(&b, "\n<b>%s</b>\n", link(p.Title, p.URL))
		if len(p.Builds) == 0 {
			b.WriteString("  источник версии не настроен\n")
			continue
		}
		for _, bl := range p.Builds {
			version := bl.Version
			if version == "" {
				version = "неизвестна"
			}
			fmt.Fprintf(&b, "  %s: <code>%s</code>", link(bl.Title, bl.URL), esc(version))
			if t, ok := parseTime(bl.At); ok {
				fmt.Fprintf(&b, "\n    собрано %s (%s назад)", fmtTime(t), humanDur(now.Sub(t)))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n" + freshness(s, now))
	return b.String()
}

func formatIncidents(s *Summary, now time.Time) string {
	if len(s.Incidents) == 0 {
		return up + " Инцидентов не было"
	}
	var b strings.Builder
	b.WriteString("<b>Последние инциденты</b>\n")
	for _, in := range s.Incidents {
		start, hasStart := parseTime(in.Start)
		fmt.Fprintf(&b, "\n%s <b>%s</b>\n", statusIcon(incidentStatus(in)), esc(in.Name))
		if hasStart {
			fmt.Fprintf(&b, "  начало: %s\n", fmtTime(start))
		}
		if in.End == "" {
			if hasStart {
				fmt.Fprintf(&b, "  идёт уже %s\n", humanDur(now.Sub(start)))
			} else {
				b.WriteString("  идёт\n")
			}
		} else {
			fmt.Fprintf(&b, "  длился %s\n", humanDur(time.Duration(in.DurationMs)*time.Millisecond))
		}
		if in.Reason != "" {
			fmt.Fprintf(&b, "  причина: %s\n", esc(in.Reason))
		}
	}
	return b.String()
}

func incidentStatus(in Incident) string {
	if in.End == "" {
		return "down"
	}
	return "up"
}

// formatEvent — текст уведомления о событии.
//
// Порядок строк выбран под сценарий «пришло ночью, смотрю с телефона»:
// сначала ЧТО и для кого это плохо, потом техническая причина, потом время.
// Причина первой строкой заставляла бы разбирать «connect: connection
// refused» до того, как понятно, надо ли вообще вставать.
func formatEvent(e Event) string {
	switch e.Kind {
	case KindDown:
		// Дальше идёт последствие для пользователя, если оно описано в
		// конфиге, иначе техпричина.
		s := fmt.Sprintf("%s <b>%s</b> недоступен", down, link(e.Title, e.URL))
		if e.Reason != "" {
			s += "\n" + esc(e.Reason)
		}
		return s + "\n<i>" + fmtTime(e.At) + "</i>"

	case KindStillDown:
		// Напоминание короче первого сообщения: подробности уже приходили,
		// здесь важно только, сколько это тянется.
		return fmt.Sprintf("%s <b>%s</b> лежит уже %s",
			down, link(e.Title, e.URL), humanDur(e.Duration))

	case KindUp:
		return fmt.Sprintf("%s <b>%s</b> снова работает\nпростой: %s\n<i>%s</i>",
			up, link(e.Title, e.URL), humanDur(e.Duration), fmtTime(e.At))

	case KindRelease:
		s := fmt.Sprintf("🚀 <b>%s</b> обновлён\n<code>%s</code>",
			link(e.Title, e.URL), esc(e.Version))
		if e.Previous != "" {
			s += fmt.Sprintf("\nбыла <code>%s</code>", esc(e.Previous))
		}
		return s + "\n<i>собрано " + fmtTime(e.At) + "</i>"

	default:
		return esc(e.Title)
	}
}

// link — подпись со ссылкой на сам сервис.
//
// Уведомление без ссылки заставляет владельца искать адрес руками ровно в тот
// момент, когда некогда: увидел «недоступен» — хочешь открыть и посмотреть.
// Адрес пускаем только http(s): в конфиге он свой, но подставлять в разметку
// что попало всё равно не стоит.
func link(text, url string) string {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return esc(text)
	}
	return fmt.Sprintf(`<a href="%s">%s</a>`, esc(url), esc(text))
}
