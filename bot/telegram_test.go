package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testBot поднимает поддельный Bot API: тесты не должны ходить в интернет,
// а токен в тестах — заведомо ненастоящий.
func testBot(t *testing.T, handler http.HandlerFunc) *Telegram {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	tg := newTelegram("test-token", time.Second)
	tg.base = srv.URL
	return tg
}

func TestSend(t *testing.T) {
	var gotPath string
	var body map[string]any
	tg := testBot(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	})

	if err := tg.Send(context.Background(), 173418650, "<b>привет</b>"); err != nil {
		t.Fatalf("отправка не удалась: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/sendMessage") {
		t.Errorf("вызван не sendMessage, а %s", gotPath)
	}
	if body["parse_mode"] != "HTML" {
		t.Errorf("разметка не HTML: %v", body["parse_mode"])
	}
	if body["chat_id"].(float64) != 173418650 {
		t.Errorf("сообщение ушло не туда: %v", body["chat_id"])
	}
	if body["text"] != "<b>привет</b>" {
		t.Errorf("текст искажён: %v", body["text"])
	}
}

func TestSendAPIError(t *testing.T) {
	tg := testBot(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"description":"bot was blocked by the user"}`))
	})
	err := tg.Send(context.Background(), 1, "текст")
	if err == nil {
		t.Fatal("отказ Telegram должен быть ошибкой, иначе бот будет считать сообщение доставленным")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("причина отказа потеряна: %v", err)
	}
}

func TestGetUpdates(t *testing.T) {
	var body map[string]any
	tg := testBot(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":11,"message":{"message_id":1,"chat":{"id":173418650},"text":"/status"}},
			{"update_id":12,"message":{"message_id":2,"chat":{"id":999},"text":"привет"}}
		]}`))
	})

	updates, err := tg.GetUpdates(context.Background(), 11, 25*time.Second)
	if err != nil {
		t.Fatalf("опрос не удался: %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("разобрано %d обновлений, ожидали 2", len(updates))
	}
	if updates[0].Message.Text != "/status" || updates[0].Message.Chat.ID != 173418650 {
		t.Errorf("сообщение разобрано неверно: %+v", updates[0].Message)
	}
	if body["offset"].(float64) != 11 {
		t.Errorf("offset не передан: %v", body["offset"])
	}
	if body["timeout"].(float64) != 25 {
		t.Errorf("таймаут длинного опроса не передан: %v", body["timeout"])
	}
}

// Токен лежит прямо в адресе запроса, а http.Client печатает адрес в тексте
// транспортной ошибки. Эта ошибка логируется на каждом моргании сети, то есть
// регулярно, — значит токена в ней быть не должно ни при каких условиях.
func TestTransportErrorHidesToken(t *testing.T) {
	const token = "123456:AA-secret-bot-token"
	// Сервер поднимаем и сразу гасим: получаем заведомо закрытый порт, то есть
	// настоящий транспортный сбой, не выходя в сеть.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	tg := newTelegram(token, time.Second)
	tg.base = srv.URL

	for _, c := range []struct {
		name string
		call func() error
	}{
		{"sendMessage", func() error { return tg.Send(context.Background(), 1, "текст") }},
		{"getUpdates", func() error {
			_, err := tg.GetUpdates(context.Background(), 0, time.Second)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if err == nil {
				t.Fatal("закрытый порт должен быть ошибкой")
			}
			if strings.Contains(err.Error(), token) {
				t.Fatalf("токен утёк в текст ошибки: %v", err)
			}
			if strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("адрес запроса остался в ошибке — вместе с ним вернётся и токен: %v", err)
			}
			if !strings.Contains(err.Error(), c.name) {
				t.Errorf("из ошибки пропал метод, по которому её опознают в журнале: %v", err)
			}
		})
	}
}

func TestGetUpdatesBrokenResponse(t *testing.T) {
	tg := testBot(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	})
	if _, err := tg.GetUpdates(context.Background(), 0, time.Second); err == nil {
		t.Fatal("страница ошибки вместо JSON должна быть ошибкой")
	}
}
