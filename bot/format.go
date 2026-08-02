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

	// Заголовок — одно утверждение крупно, как на странице: с него читают.
	switch s.Overall {
	case "operational":
		b.WriteString(up + " <b>Всё работает</b>")
	case "down":
		b.WriteString(down + " <b>Всё лежит</b>")
	case "major":
		// Ступень между частичным и массовым: больше половины ключевых
		// проверок лежит. Раньше это описывалось словом «частичный».
		b.WriteString(down + " <b>Крупный сбой</b>")
	default:
		b.WriteString(degraded + " <b>Частичный сбой</b>")
	}

	// Сводка одной строкой под заголовком.
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

	// Сломанное — отдельным блоком наверху, ровно как на странице: искать
	// красную строку среди зелёных владелец не должен.
	var broken []string
	for _, p := range s.Projects {
		for _, c := range p.Checks {
			if c.Status != "down" {
				continue
			}
			icon := statusIcon(c.Status)
			tail := ""
			if !c.Critical {
				icon, tail = degraded, " <i>(второстеп.)</i>"
			}
			line := fmt.Sprintf("%s <b>%s · %s</b>%s",
				icon, link(p.Title, p.URL), link(c.Name, c.URL), tail)
			if c.Impact != "" {
				line += "\n   " + esc(c.Impact)
			}
			if c.Error != "" {
				line += "\n   <code>" + esc(c.Error) + "</code>"
			}
			if t, ok := parseTime(c.Since); ok {
				line += "\n   недоступен " + humanDur(now.Sub(t))
			}
			broken = append(broken, line)
		}
	}
	if len(broken) > 0 {
		b.WriteString("\n" + strings.Join(broken, "\n") + "\n")
	}

	for _, p := range s.Projects {
		aux := ""
		if n := p.AuxDown + p.AuxSlow; n > 0 {
			aux = fmt.Sprintf(" <i>+%d второстеп.</i>", n)
		}
		fmt.Fprintf(&b, "\n%s <b>%s</b> <code>%d/%d</code>%s\n",
			statusIcon(p.Status), link(p.Title, p.URL), p.Up, p.Total, aux)

		if strip := projectStrip(p); strings.Contains(strip, "🟩") ||
			strings.Contains(strip, "🟨") || strings.Contains(strip, "🟧") ||
			strings.Contains(strip, "🟥") {
			b.WriteString(strip + " <i>14 дн.</i>\n")
		}

		for _, c := range p.Checks {
			mark := " "
			if !c.Critical {
				mark = "·"
			}
			fmt.Fprintf(&b, "%s%s %s", mark, statusIcon(c.Status), link(c.Name, c.URL))
			switch c.Status {
			case "up":
				fmt.Fprintf(&b, " <code>%d мс</code>", c.Ms)
			case "slow":
				fmt.Fprintf(&b, " <code>%d мс</code> — медленно", c.Ms)
			default:
				// Причину не повторяем: она уже развёрнута в блоке сбоев
				// наверху, а здесь строка должна остаться в одну.
				b.WriteString(" — недоступен")
			}
			if v := c.Uptime["d1"]; v != nil {
				fmt.Fprintf(&b, " · сутки <code>%.2f%%</code>", *v)
			}
			// Сколько держится текущее состояние. У упавших это уже сказано
			// в блоке сбоев выше, здесь — про то, как давно всё хорошо.
			if t, ok := parseTime(c.Since); ok && c.Status != "down" {
				fmt.Fprintf(&b, " · %s", humanDur(now.Sub(t)))
			}
			b.WriteString("\n")
		}

		for _, u := range p.Units {
			icon := down
			if u.Active {
				icon = up
			}
			fmt.Fprintf(&b, " %s %s — %s", icon, esc(u.Title), esc(u.State))
			if t, ok := parseTime(u.Since); ok && u.Active {
				fmt.Fprintf(&b, ", аптайм %s", humanDur(now.Sub(t)))
			}
			b.WriteString("\n")
		}
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
func formatEvent(e Event) string {
	switch e.Kind {
	case KindDown:
		s := fmt.Sprintf("%s <b>%s</b> недоступен", down, link(e.Title, e.URL))
		if e.Reason != "" {
			s += "\n" + esc(e.Reason)
		}
		return s + "\n" + fmtTime(e.At)
	case KindStillDown:
		s := fmt.Sprintf("%s <b>%s</b> всё ещё недоступен — %s", down, link(e.Title, e.URL), humanDur(e.Duration))
		if e.Reason != "" {
			s += "\n" + esc(e.Reason)
		}
		return s
	case KindUp:
		return fmt.Sprintf("%s <b>%s</b> снова работает\nпростой: %s\n%s",
			up, esc(e.Title), humanDur(e.Duration), fmtTime(e.At))
	case KindRelease:
		s := fmt.Sprintf("🚀 <b>%s</b> обновлён\nверсия: <code>%s</code>", link(e.Title, e.URL), esc(e.Version))
		if e.Previous != "" {
			s += fmt.Sprintf(" (была <code>%s</code>)", esc(e.Previous))
		}
		return s + "\nсобрано: " + fmtTime(e.At)
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
