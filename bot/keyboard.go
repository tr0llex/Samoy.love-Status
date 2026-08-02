package main

import "strings"

// Кнопки под сообщениями.
//
// Инлайн-клавиатура здесь не украшение: без неё каждый повторный взгляд на
// статус — это набрать команду с телефона, а каждый ответ — новое сообщение
// в ленте. С кнопками владелец жмёт «Обновить», и карточка переписывается
// на месте.
//
// callback_data ограничен 64 байтами, поэтому в него кладём короткий ключ
// экрана, а не состояние: всё, что нужно для отрисовки, и так лежит в
// summary.json.
// Адреса приходят из конфига (config.go) — единственного места настроек.
// Пакетные переменные, а не параметр каждой функции: клавиатура рисуется в
// десятке мест, и таскать через все них две строки незачем.
var (
	statusPageURL = "https://status.samoy.love/"
	miniApp       = "https://status.samoy.love/tg/"
)

// applyConfig задаёт адреса один раз при старте.
func applyConfig(c Config) {
	statusPageURL = c.StatusURL
	miniApp = c.MiniApp
}

// Действия под уведомлением. Ночью нужна не навигация по экранам, а способ
// прекратить поток: сервис уже чинится, а напоминания продолжают приходить.
const (
	ActMute2h = "a:mute:2h"
	ActMute8h = "a:mute:8h"
	ActUnmute = "a:unmute"
)

const (
	ViewStatus    = "v:status"
	ViewVersions  = "v:versions"
	ViewIncidents = "v:incidents"
	ViewHelp      = "v:help"
	// ViewProject — экран одного проекта, ключ вида "v:p:metro".
	// Подробности переехали сюда с общего экрана: раньше /status печатал все
	// проекты со всеми проверками, службами и полосками, и единственную
	// красную строку приходилось искать глазами среди двух десятков зелёных.
	ViewProject = "v:p:"
)

// projectOfView возвращает id проекта, если это экран проекта.
func projectOfView(view string) (string, bool) {
	if !strings.HasPrefix(view, ViewProject) {
		return "", false
	}
	id := strings.TrimPrefix(view, ViewProject)
	return id, id != ""
}

// Кнопка мини-приложения работает только в личной переписке и только по
// https. Если адрес почему-то не https, отдаём обычную ссылку — пусть
// откроется в браузере, но кнопка не пропадёт.
func openButton() Button {
	if strings.HasPrefix(miniApp, "https://") {
		return Button{Text: "Открыть", WebApp: &WebApp{URL: miniApp}}
	}
	return Button{Text: "Открыть", URL: miniApp}
}

// Пометка текущего экрана. Точка перед подписью вместо обрамления с двух
// сторон: Telegram и так центрирует текст кнопки, а симметричные точки
// читались как часть названия.
func mark(view, current, label string) string {
	if view == current {
		return "· " + label
	}
	return label
}

// navKeyboard — клавиатура под экраном.
//
// Эмодзи убраны из навигации намеренно: значок несёт смысл там, где сообщает
// состояние (кнопка проекта, строка проверки). В кнопке «Версии» он ничего не
// сообщает и превращает панель в рябь.
func navKeyboard(current string) *Keyboard {
	return &Keyboard{InlineKeyboard: navRows(current)}
}

func navRows(current string) [][]Button {
	return [][]Button{
		{
			{Text: mark(ViewStatus, current, "Статус"), CallbackData: ViewStatus},
			{Text: mark(ViewVersions, current, "Версии"), CallbackData: ViewVersions},
			{Text: mark(ViewIncidents, current, "Инциденты"), CallbackData: ViewIncidents},
		},
		{
			{Text: "Обновить", CallbackData: current},
			openButton(),
		},
	}
}

// statusKeyboard — навигация плюс ряд проектов.
//
// Кнопка проекта несёт его состояние значком, поэтому на общем экране больше
// не нужно печатать строку про каждый живой сервис: и так видно, что все
// зелёные, а подробности — по нажатию.
func statusKeyboard(s *Summary) *Keyboard {
	rows := projectRows(s, "")
	return &Keyboard{InlineKeyboard: append(rows, navRows(ViewStatus)...)}
}

// projectKeyboard — экран одного проекта: соседние проекты остаются под рукой,
// чтобы обойти их не возвращаясь каждый раз назад.
func projectKeyboard(s *Summary, current string) *Keyboard {
	rows := projectRows(s, current)
	return &Keyboard{InlineKeyboard: append(rows, navRows(current)...)}
}

// По три в ряд: названия проектов короткие, и на телефоне три кнопки со
// значком помещаются без переноса.
const projectsPerRow = 3

func projectRows(s *Summary, current string) [][]Button {
	if s == nil {
		return nil
	}
	var rows [][]Button
	var row []Button
	for _, p := range s.Projects {
		label := statusIcon(p.Status) + " " + p.Title
		row = append(row, Button{
			Text:         mark(ViewProject+p.ID, current, label),
			CallbackData: ViewProject + p.ID,
		})
		if len(row) == projectsPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return rows
}

// alertKeyboard — под уведомлением о падении.
//
// Здесь не до навигации: нужно понять масштаб, открыть сервис — или сказать
// боту помолчать, пока чинишь. Без последнего владелец либо терпит
// напоминания, либо глушит чат целиком и пропускает следующую аварию.
//
// projectID — если известно, какой проект упал, первая кнопка ведёт сразу в
// него: иначе владелец попадает на общий экран и ищет там то, о чём ему
// только что написали.
func alertKeyboard(projectID string) *Keyboard {
	what := Button{Text: "Что сейчас", CallbackData: ViewStatus}
	if projectID != "" {
		what.CallbackData = ViewProject + projectID
	}
	return &Keyboard{InlineKeyboard: [][]Button{
		{what, openButton()},
		{
			{Text: "Тихо 2 ч", CallbackData: ActMute2h},
			{Text: "До утра", CallbackData: ActMute8h},
		},
	}}
}

// mutedKeyboard — под подтверждением тишины: единственное осмысленное
// действие здесь — передумать.
func mutedKeyboard() *Keyboard {
	return &Keyboard{InlineKeyboard: [][]Button{
		{
			{Text: "Снова говорить", CallbackData: ActUnmute},
			{Text: "Что сейчас", CallbackData: ViewStatus},
		},
	}}
}
