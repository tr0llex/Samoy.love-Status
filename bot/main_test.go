package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const owner = int64(173418650)

// recorder — поддельный Bot API, запоминающий отправленные тексты.
func recorder(t *testing.T, sent *[]string) *Telegram {
	t.Helper()
	return testBot(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &body)
		*sent = append(*sent, body.Text)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	})
}

func writeSummary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "summary.json")
	b, err := json.Marshal(summaryAt(time.Now().UTC(), "up", time.Now().UTC(), true, "v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func message(chat int64, text string) Update {
	u := Update{UpdateID: 1, Message: &Message{Text: text}}
	u.Message.Chat.ID = chat
	return u
}

func TestForeignChatIgnoredSilently(t *testing.T) {
	var sent []string
	tg := recorder(t, &sent)
	handleUpdate(context.Background(), tg, message(999, "/status"), owner, "samoy_love_bot", writeSummary(t))
	if len(sent) != 0 {
		t.Fatalf("на чужое сообщение бот ответил: %v", sent)
	}
}

func TestReplyToOwner(t *testing.T) {
	summary := writeSummary(t)
	cases := []struct {
		name string
		text string
		want string
	}{
		{"справка", "/help", "/status"},
		{"состояние", "/status", "Snakes"},
		{"версии", "/versions", "v1"},
		{"инциденты", "/incidents", "Инцидентов не было"},
		{"короткий псевдоним", "/s", "Snakes"},
		{"неизвестная команда", "/deploy", "Не знаю такой команды"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sent []string
			tg := recorder(t, &sent)
			handleUpdate(context.Background(), tg, message(owner, c.text), owner, "samoy_love_bot", summary)
			if len(sent) != 1 {
				t.Fatalf("ожидали один ответ, получили %d: %v", len(sent), sent)
			}
			if !strings.Contains(sent[0], c.want) {
				t.Errorf("в ответе нет %q:\n%s", c.want, sent[0])
			}
		})
	}
}

func TestPlainTextGetsNoReply(t *testing.T) {
	var sent []string
	tg := recorder(t, &sent)
	// Обычная реплика в чате — не повод отвечать: бот не собеседник.
	handleUpdate(context.Background(), tg, message(owner, "как дела"), owner, "samoy_love_bot", writeSummary(t))
	if len(sent) != 0 {
		t.Fatalf("бот ответил на обычный текст: %v", sent)
	}
}

func TestMissingDataIsReported(t *testing.T) {
	var sent []string
	tg := recorder(t, &sent)
	// Агент не создал файл — владелец должен узнать об этом, а не получить
	// пустой ответ.
	handleUpdate(context.Background(), tg, message(owner, "/status"), owner, "", filepath.Join(t.TempDir(), "нет.json"))
	if len(sent) != 1 || !strings.Contains(sent[0], "данные агента") {
		t.Fatalf("о недоступных данных не сообщено: %v", sent)
	}
}

func TestEnvDuration(t *testing.T) {
	t.Setenv("REMIND_INTERVAL", "3m")
	if got := envDuration("REMIND_INTERVAL", time.Hour); got != 3*time.Minute {
		t.Errorf("интервал из окружения не прочитан: %s", got)
	}
	// Опечатка в файле окружения не должна ронять бота: лучше значение по
	// умолчанию, чем служба, которая не стартует.
	t.Setenv("REMIND_INTERVAL", "пятнадцать минут")
	if got := envDuration("REMIND_INTERVAL", time.Hour); got != time.Hour {
		t.Errorf("при мусоре в окружении ожидали значение по умолчанию, получили %s", got)
	}
	t.Setenv("REMIND_INTERVAL", "")
	if got := envDuration("REMIND_INTERVAL", 15*time.Minute); got != 15*time.Minute {
		t.Errorf("без переменной ожидали значение по умолчанию, получили %s", got)
	}
}
