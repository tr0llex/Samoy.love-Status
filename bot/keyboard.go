package main

import "os"

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
// statusPageURL — сама страница. Нужна там, где речь о ней самой: например,
// когда бот сообщает, что данные агента перестали обновляться.
const statusPageURL = "https://status.samoy.love/"

const (
	ViewStatus    = "v:status"
	ViewVersions  = "v:versions"
	ViewIncidents = "v:incidents"
	ViewHelp      = "v:help"
)

// miniAppURL — адрес мини-приложения. Telegram открывает его внутри себя, без
// перехода в браузер, и требует https. Переопределяется переменной окружения,
// чтобы можно было проверить сборку с локального адреса через туннель.
func miniAppURL() string {
	if v := os.Getenv("MINIAPP_URL"); v != "" {
		return v
	}
	return "https://status.samoy.love/tg/"
}

// Кнопка мини-приложения работает только в личной переписке и только по
// https. Если адрес почему-то не https, отдаём обычную ссылку — пусть
// откроется в браузере, но кнопка не пропадёт.
func openButton() Button {
	url := miniAppURL()
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

// alertKeyboard — под уведомлением о падении. Здесь не до навигации: нужно
// быстро посмотреть подробности или открыть страницу.
func alertKeyboard() *Keyboard {
	return &Keyboard{InlineKeyboard: [][]Button{
		{
			{Text: "🩺 Что сейчас", CallbackData: ViewStatus},
			openButton(),
		},
	}}
}
