package main

import (
	"fmt"
	"html"
	"sort"
	"strconv"
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
		"Верхний ряд — проекты: значок показывает состояние, нажатие",
		"раскрывает проверки, службы, версии и историю по дням.",
		"«Открыть» показывает страницу целиком, не выходя из Telegram.",
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

// worstDays — худшая ключевая проверка за каждые сутки.
//
// Считаем по ключевым проверкам и по худшей из них за сутки: если в этот день
// лежал игровой сервер, день плохой, даже когда сайт открывался. Та же логика
// живёт в src/lib/status.ts для страницы и мини-аппа — расхождение здесь
// означало бы, что бот и страница рисуют разную историю одного дня.
func worstDays(checks []Check, count int) []*Day {
	worst := make([]*Day, count)
	for _, c := range checks {
		if !c.Critical || len(c.Days) == 0 {
			continue
		}
		days := c.Days
		if len(days) > count {
			days = days[len(days)-count:]
		}
		for i, d := range days {
			slot := count - len(days) + i
			if slot < 0 || d == nil || d.Total == 0 {
				continue
			}
			cur := worst[slot]
			if cur == nil || float64(d.Up)/float64(d.Total) < float64(cur.Up)/float64(cur.Total) {
				worst[slot] = d
			}
		}
	}
	return worst
}

func strip(days []*Day) string {
	var b strings.Builder
	for _, d := range days {
		b.WriteString(dayCell(d))
	}
	return b.String()
}

// projectStrip — доступность проекта за две недели одной строкой.
func projectStrip(p Project) string { return strip(worstDays(p.Checks, stripDays)) }

// overallStrip — то же по всей экосистеме: одна строка вместо пяти
// одинаково зелёных полосок под каждым проектом.
func overallStrip(s *Summary) string {
	var all []Check
	for _, p := range s.Projects {
		all = append(all, p.Checks...)
	}
	return strip(worstDays(all, stripDays))
}

// hasHistory — есть ли в полоске хоть один день с данными. Полоска из
// четырнадцати белых квадратов не сообщает ничего и только занимает строку.
func hasHistory(s string) bool { return strings.ContainsAny(s, "🟩🟨🟧🟥") }

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
			fmt.Fprintf(&b, "\n%s <b>%s · %s</b>%s",
				checkIcon(c), link(p.Title, p.URL), link(c.Name, c.URL), auxTail(c))
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

	// Одна полоска на всю экосистему вместо пяти одинаковых под каждым
	// проектом. Разбивка по проектам — на кнопках и на экране проекта.
	if st := overallStrip(s); hasHistory(st) {
		fmt.Fprintf(&b, "\n%s\n<i>%d дней</i>\n", st, stripDays)
	}

	b.WriteString("\n" + freshness(s, now))
	return b.String()
}

// formatProject — экран одного проекта.
//
// Сюда переехали подробности, которые раньше печатались для всех проектов
// сразу: проверки с аптаймом, службы, версии, полоска. Общий экран от этого
// стал коротким, а разбор конкретного сервиса — полным.
func formatProject(p Project, s *Summary, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b>", statusIcon(p.Status), link(p.Title, p.URL))
	fmt.Fprintf(&b, "\n<code>%d/%d</code> ключевых проверок в норме\n", p.Up, p.Total)

	// Сломанные проверки идут первыми: на экране проекта ищут именно их.
	sorted := append([]Check(nil), p.Checks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return checkRank(sorted[i]) < checkRank(sorted[j])
	})

	for _, c := range sorted {
		fmt.Fprintf(&b, "\n%s <b>%s</b>%s", checkIcon(c), link(c.Name, c.URL), auxTail(c))
		if c.Status == "up" {
			if u := uptimeText(c); u != "" {
				b.WriteString(" — " + u)
			}
			continue
		}
		if c.Impact != "" && c.Status == "down" {
			b.WriteString("\n   " + esc(c.Impact))
		}
		if c.Error != "" {
			b.WriteString("\n   <code>" + esc(c.Error) + "</code>")
		}
		if t, ok := parseTime(c.Since); ok {
			fmt.Fprintf(&b, "\n   %s", humanDur(now.Sub(t)))
		}
	}
	b.WriteString("\n")

	if len(p.Units) > 0 {
		b.WriteString("\n<b>Службы</b>")
		for _, u := range p.Units {
			icon := up
			if !u.Active {
				icon = down
			}
			fmt.Fprintf(&b, "\n%s %s — %s", icon, esc(u.Title), esc(u.State))
		}
		b.WriteString("\n")
	}

	if len(p.Builds) > 0 {
		b.WriteString("\n<b>Версии</b>")
		for _, bl := range p.Builds {
			version := bl.Version
			if version == "" {
				version = "неизвестна"
			}
			fmt.Fprintf(&b, "\n%s: <code>%s</code>", link(bl.Title, bl.URL), esc(version))
			if t, ok := parseTime(bl.At); ok {
				fmt.Fprintf(&b, " · %s назад", humanDur(now.Sub(t)))
			}
		}
		b.WriteString("\n")
	}

	if st := projectStrip(p); hasHistory(st) {
		fmt.Fprintf(&b, "\n%s\n<i>%d дней</i>\n", st, stripDays)
	}

	b.WriteString("\n" + freshness(s, now))
	return b.String()
}

// checkIcon — значок проверки.
//
// У второстепенной упавшей проверки значок оранжевый, а не красный: она не
// роняет вердикт проекта, и красный кружок рядом с работающим сервисом
// заставляет искать аварию там, где её нет. Функция одна на общий экран и на
// экран проекта — иначе одна и та же проверка выглядела бы на них по-разному.
func checkIcon(c Check) string {
	if !c.Critical && c.Status == "down" {
		return degraded
	}
	return statusIcon(c.Status)
}

func auxTail(c Check) string {
	if c.Critical {
		return ""
	}
	return " <i>(второстеп.)</i>"
}

// checkRank — порядок проверок на экране проекта: сначала лежащие, потом
// медленные, потом живые. Внутри группы порядок конфига сохраняется.
func checkRank(c Check) int {
	switch {
	case c.Status == "down" && c.Critical:
		return 0
	case c.Status == "down":
		return 1
	case c.Status == "slow":
		return 2
	default:
		return 3
	}
}

// uptimeText — доступность живой проверки короткой строкой.
//
// Агент отдаёт аптайм УЖЕ в процентах (agent/main.go, pct). Умножать здесь
// ещё на сто — как было в первой версии — значит показывать «9991.00%»:
// число выглядит настолько неправдоподобно, что читается как поломка бота,
// а не как доступность.
func uptimeText(c Check) string {
	v, ok := c.Uptime["d90"]
	if !ok || v == nil {
		if v, ok = c.Uptime["d7"]; !ok || v == nil {
			return ""
		}
		return fmt.Sprintf("%s за неделю", fmtPct(*v))
	}
	return fmt.Sprintf("%s за 90 дней", fmtPct(*v))
}

// fmtPct — процент без хвостовых нулей: «100%» вместо «100.00%», но
// «99.87%» целиком. То же правило, что на странице (src/lib/status.ts).
func fmtPct(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s + "%"
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
