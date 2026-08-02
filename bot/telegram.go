package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Bot API — обычный HTTP с JSON, поэтому библиотека не нужна: из всего
// протокола боту требуются два метода, getUpdates и sendMessage.
//
// Работаем длинным опросом, а не вебхуком: вебхук потребовал бы публичного
// эндпоинта, места в конфиге nginx и сертификата ради одного чата с одним
// человеком. Опрос ничего наружу не открывает.
type Telegram struct {
	token  string
	base   string // вынесен в поле, чтобы тесты подставляли свой сервер
	client *http.Client
}

func newTelegram(token string, pollTimeout time.Duration) *Telegram {
	return &Telegram{
		token: token,
		base:  "https://api.telegram.org",
		// Таймаут клиента заведомо больше таймаута длинного опроса, иначе
		// каждый холостой опрос обрывался бы ошибкой.
		client: &http.Client{Timeout: pollTimeout + 30*time.Second},
	}
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int64 `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

// CallbackQuery — нажатие на инлайн-кнопку.
//
// Telegram ждёт ответа на каждое нажатие: пока его нет, у пользователя
// крутится часик на кнопке. Отвечать надо даже когда делать нечего.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
	From    struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

// Кнопки. Кнопка либо шлёт callback_data обратно боту, либо открывает
// мини-приложение прямо внутри Telegram — второе требует https-адреса.
type Button struct {
	Text         string  `json:"text"`
	CallbackData string  `json:"callback_data,omitempty"`
	WebApp       *WebApp `json:"web_app,omitempty"`
	URL          string  `json:"url,omitempty"`
}

type WebApp struct {
	URL string `json:"url"`
}

type Keyboard struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

func (t *Telegram) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/%s", t.base, t.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var out apiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("%s: неожиданный ответ (%d)", method, resp.StatusCode)
	}
	if !out.OK {
		return fmt.Errorf("%s: telegram отказал: %s", method, out.Description)
	}
	if result != nil {
		return json.Unmarshal(out.Result, result)
	}
	return nil
}

// Send отправляет сообщение владельцу. Разметка HTML, а не Markdown:
// в версиях и причинах сбоев попадаются подчёркивания и звёздочки, и
// экранировать их в Markdown больнее, чем три спецсимвола HTML.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	return t.SendWith(ctx, chatID, text, nil)
}

// SendWith — сообщение с кнопками под ним.
func (t *Telegram) SendWith(ctx context.Context, chatID int64, text string, kb *Keyboard) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	return t.call(ctx, "sendMessage", payload, nil)
}

// Edit переписывает уже отправленное сообщение.
//
// Благодаря этому «Обновить» не плодит новые сообщения: статус меняется прямо
// в том, которое владелец уже читает, — переписка не превращается в ленту из
// двадцати почти одинаковых карточек.
func (t *Telegram) Edit(ctx context.Context, chatID, messageID int64, text string, kb *Keyboard) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	err := t.call(ctx, "editMessageText", payload, nil)
	// Если содержимое не изменилось, Telegram отвечает ошибкой. Это не сбой:
	// владелец нажал «Обновить», а с прошлого раза ничего не поменялось.
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

// AnswerCallback гасит «часики» на нажатой кнопке. Текст, если он задан,
// всплывает короткой плашкой поверх чата.
func (t *Telegram) AnswerCallback(ctx context.Context, id, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
	}, nil)
}

func (t *Telegram) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var updates []Update
	err := t.call(ctx, "getUpdates", map[string]any{
		"offset":  offset,
		"timeout": int(timeout.Seconds()),
		// Кроме сообщений нужны нажатия на кнопки; на остальные типы событий
		// не подписываемся, чтобы не тратить трафик впустую.
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}
