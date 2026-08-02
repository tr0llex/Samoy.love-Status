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
	case "degraded":
		return degraded
	default:
		return unknown
	}
}

func formatHelp() string {
	return strings.Join([]string{
		"<b>Статус samoy.love</b>",
		"",
		"/status — что живо, что лежит, аптайм",
		"/versions — версии сервисов и страниц со временем сборки",
		"/incidents — последние падения",
		"/help — эта справка",
		"",
		"Сам сообщу, когда что-то упадёт, поднимется или обновится.",
	}, "\n")
}

// formatStatus — ответ на /status.
//
// Порядок проектов не сортируется: он задан конфигом и совпадает с порядком
// на самой странице, чтобы взгляд искал сервис в одном и том же месте.
func formatStatus(s *Summary, now time.Time) string {
	var b strings.Builder

	switch s.Overall {
	case "operational":
		b.WriteString(up + " <b>Всё работает</b>\n")
	case "down":
		b.WriteString(down + " <b>Всё лежит</b>\n")
	default:
		b.WriteString(degraded + " <b>Частичный сбой</b>\n")
	}

	for _, p := range s.Projects {
		fmt.Fprintf(&b, "\n%s <b>%s</b> — %d/%d\n",
			statusIcon(p.Status), esc(p.Title), p.Up, p.Total)

		for _, c := range p.Checks {
			fmt.Fprintf(&b, "  %s %s", statusIcon(c.Status), esc(c.Name))
			if c.Status == "up" {
				fmt.Fprintf(&b, " — %d мс", c.Ms)
			} else if c.Error != "" {
				fmt.Fprintf(&b, " — %s", esc(c.Error))
			}
			if v := c.Uptime["d1"]; v != nil {
				fmt.Fprintf(&b, ", сутки %.2f%%", *v)
			}
			if t, ok := parseTime(c.Since); ok {
				fmt.Fprintf(&b, ", в этом состоянии %s", humanDur(now.Sub(t)))
			}
			b.WriteString("\n")
		}

		for _, u := range p.Units {
			icon := down
			if u.Active {
				icon = up
			}
			fmt.Fprintf(&b, "  %s %s — %s", icon, esc(u.Title), esc(u.State))
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
		fmt.Fprintf(&b, "\n<b>%s</b>\n", esc(p.Title))
		if len(p.Builds) == 0 {
			b.WriteString("  источник версии не настроен\n")
			continue
		}
		for _, bl := range p.Builds {
			version := bl.Version
			if version == "" {
				version = "неизвестна"
			}
			fmt.Fprintf(&b, "  %s: <code>%s</code>", esc(bl.Title), esc(version))
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
		s := fmt.Sprintf("%s <b>%s</b> недоступен", down, esc(e.Title))
		if e.Reason != "" {
			s += "\n" + esc(e.Reason)
		}
		return s + "\n" + fmtTime(e.At)
	case KindStillDown:
		s := fmt.Sprintf("%s <b>%s</b> всё ещё недоступен — %s", down, esc(e.Title), humanDur(e.Duration))
		if e.Reason != "" {
			s += "\n" + esc(e.Reason)
		}
		return s
	case KindUp:
		return fmt.Sprintf("%s <b>%s</b> снова работает\nпростой: %s\n%s",
			up, esc(e.Title), humanDur(e.Duration), fmtTime(e.At))
	case KindRelease:
		s := fmt.Sprintf("🚀 <b>%s</b> обновлён\nверсия: <code>%s</code>", esc(e.Title), esc(e.Version))
		if e.Previous != "" {
			s += fmt.Sprintf(" (была <code>%s</code>)", esc(e.Previous))
		}
		return s + "\nсобрано: " + fmtTime(e.At)
	default:
		return esc(e.Title)
	}
}
