package main

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
)

// Кнопка мини-приложения работает только в личной переписке и только по
// https. Если адрес почему-то не https, отдаём обычную ссылку — пусть
// откроется в браузере, но кнопка не пропадёт.
func openButton() Button {
	url := miniApp
	if len(url) > 8 && url[:8] == "https://" {
		return Button{Text: "📊 Открыть", WebApp: &WebApp{URL: url}}
	}
	return Button{Text: "📊 Открыть", URL: url}
}

// navKeyboard — клавиатура под экраном. Текущий экран помечен точкой, чтобы
// было видно, где находишься: одинаковые кнопки на всех экранах иначе
// сбивают с толку.
func navKeyboard(current string) *Keyboard {
	mark := func(view, label string) string {
		if view == current {
			return "· " + label + " ·"
		}
		return label
	}
	return &Keyboard{InlineKeyboard: [][]Button{
		{
			{Text: mark(ViewStatus, "🩺 Статус"), CallbackData: ViewStatus},
			{Text: mark(ViewVersions, "📦 Версии"), CallbackData: ViewVersions},
		},
		{
			{Text: mark(ViewIncidents, "📉 Инциденты"), CallbackData: ViewIncidents},
			{Text: "🔄 Обновить", CallbackData: current},
		},
		{openButton()},
	}}
}

// alertKeyboard — под уведомлением о падении.
//
// Здесь не до навигации: нужно понять масштаб, открыть сервис — или сказать
// боту помолчать, пока чинишь. Без последнего владелец либо терпит
// напоминания, либо глушит чат целиком и пропускает следующую аварию.
func alertKeyboard() *Keyboard {
	return &Keyboard{InlineKeyboard: [][]Button{
		{
			{Text: "🩺 Что сейчас", CallbackData: ViewStatus},
			openButton(),
		},
		{
			{Text: "🔕 Тихо 2 ч", CallbackData: ActMute2h},
			{Text: "🔕 До утра", CallbackData: ActMute8h},
		},
	}}
}

// mutedKeyboard — под подтверждением тишины: единственное осмысленное
// действие здесь — передумать.
func mutedKeyboard() *Keyboard {
	return &Keyboard{InlineKeyboard: [][]Button{
		{
			{Text: "🔔 Снова говорить", CallbackData: ActUnmute},
			{Text: "🩺 Что сейчас", CallbackData: ViewStatus},
		},
	}}
}
