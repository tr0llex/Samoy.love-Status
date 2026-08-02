package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64 `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
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
	return t.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, nil)
}

func (t *Telegram) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var updates []Update
	err := t.call(ctx, "getUpdates", map[string]any{
		"offset":  offset,
		"timeout": int(timeout.Seconds()),
		// Ничего, кроме сообщений, боту не нужно: подписка на лишние типы
		// событий только тратит трафик.
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}
