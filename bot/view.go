package main

import (
	"context"
	"log"
	"time"
)

// Экран — это то, что нарисовано в сообщении сейчас. Команда и нажатие на
// кнопку приводят к одному и тому же экрану, поэтому отрисовка одна на оба
// пути: иначе /status и кнопка «Статус» со временем разъехались бы.

func viewOf(cmd string) string {
	switch cmd {
	case CmdVersions:
		return ViewVersions
	case CmdIncidents:
		return ViewIncidents
	case CmdHelp:
		return ViewHelp
	default:
		return ViewStatus
	}
}

// renderView собирает текст экрана. Данные читаются на каждый показ: между
// нажатиями кнопок агент успевает записать новое состояние, и показывать
// закэшированное значило бы врать в ответ на «Обновить».
func renderView(view, summaryPath string, now time.Time) string {
	if view == ViewHelp {
		return formatHelp()
	}
	s, err := loadSummary(summaryPath)
	if err != nil {
		log.Printf("данные агента не прочитаны: %v", err)
		return "🔴 Не могу прочитать данные агента — похоже, он не работает"
	}
	switch view {
	case ViewVersions:
		return formatVersions(s, now)
	case ViewIncidents:
		return formatIncidents(s, now)
	default:
		return formatStatus(s, now)
	}
}

// handleCallback обрабатывает нажатие на инлайн-кнопку.
//
// Отвечать Telegram надо всегда и как можно раньше: пока ответа нет, на
// кнопке у владельца крутятся часики. Поэтому сначала гасим их, а потом уже
// перерисовываем сообщение.
func handleCallback(ctx context.Context, tg *Telegram, q *CallbackQuery, owner int64, summaryPath string) {
	if q.From.ID != owner {
		// Чужому не отвечаем содержимым, но часики гасим: иначе кнопка у него
		// будет «висеть», и это само по себе подсказка, что бот живой.
		_ = tg.AnswerCallback(ctx, q.ID, "")
		return
	}
	if err := tg.AnswerCallback(ctx, q.ID, ""); err != nil {
		log.Printf("нажатие не подтверждено: %v", err)
	}
	if q.Message == nil {
		return
	}

	view := q.Data
	switch view {
	case ViewStatus, ViewVersions, ViewIncidents, ViewHelp:
	default:
		// Кнопка из сообщения, отправленного прошлой версией бота.
		view = ViewStatus
	}

	log.Printf("кнопка %s", view)
	text := renderView(view, summaryPath, time.Now().UTC())
	if err := tg.Edit(ctx, q.Message.Chat.ID, q.Message.MessageID, text, navKeyboard(view)); err != nil {
		log.Printf("экран %s не перерисован: %v", view, err)
	}
}
