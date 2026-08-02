package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBotMetricsCountersAndFormat(t *testing.T) {
	m := newBotMetrics(filepath.Join(t.TempDir(), "samoy-bot.prom"), time.Unix(1000, 0))
	now := time.Unix(2000, 0)

	m.notified(string(KindDown), now)
	m.notified(string(KindDown), now)
	m.notified(string(KindUp), now)
	m.command(CmdStatus)
	m.sendFailed()
	m.pollFailed()
	m.pollFailed()

	out := m.render(time.Unix(3000, 0))
	for _, want := range []string{
		"# TYPE statusbot_notifications_total counter",
		`statusbot_notifications_total{kind="down"} 2`,
		`statusbot_notifications_total{kind="up"} 1`,
		`statusbot_commands_total{command="status"} 1`,
		"statusbot_send_failures_total 1",
		"statusbot_poll_failures_total 2",
		"statusbot_last_notification_timestamp_seconds 2000",
		"statusbot_start_timestamp_seconds 1000",
		"statusbot_heartbeat_timestamp_seconds 3000",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет строки %q в:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "# HELP statusbot_notifications_total "); n != 1 {
		t.Fatalf("HELP повторяется %d раз(а) — такой файл коллектор отбросит целиком", n)
	}
}

// TestBotMetricsNilIsSafe: бот обязан работать там, где выгрузка выключена,
// и обработчики команд не должны обрастать проверками ради этого.
func TestBotMetricsNilIsSafe(t *testing.T) {
	var m *botMetrics
	m.notified("down", time.Now())
	m.command("status")
	m.sendFailed()
	m.pollFailed()
	if s := m.render(time.Now()); s != "" {
		t.Fatalf("выключенная выгрузка вернула текст: %q", s)
	}
	if err := m.flush(time.Now()); err != nil {
		t.Fatalf("flush на nil вернул ошибку: %v", err)
	}
	if got := newBotMetrics("", time.Now()); got != nil {
		t.Fatal("пустой путь должен выключать выгрузку")
	}
}

func TestBotMetricsFlushIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "samoy-bot.prom")
	m := newBotMetrics(path, time.Unix(1000, 0))
	m.notified("up", time.Unix(1100, 0))

	if err := m.flush(time.Unix(1200, 0)); err != nil {
		t.Fatalf("flush: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	if !strings.Contains(string(b), `statusbot_notifications_total{kind="up"} 1`) {
		t.Fatalf("содержимое:\n%s", b)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("временный файл остался рядом с готовым")
	}

	// Повторная запись должна перекрывать файл целиком, а не дописывать.
	m.notified("up", time.Unix(1300, 0))
	if err := m.flush(time.Unix(1400, 0)); err != nil {
		t.Fatalf("повторный flush: %v", err)
	}
	b, _ = os.ReadFile(path)
	values := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "statusbot_start_timestamp_seconds ") {
			values++
		}
	}
	if values != 1 {
		t.Fatalf("файл дописан, а не перезаписан (%d значений):\n%s", values, b)
	}
	if !strings.Contains(string(b), `statusbot_notifications_total{kind="up"} 2`) {
		t.Fatalf("счётчик не вырос:\n%s", b)
	}
}

func TestBotMetricsLabelsAreEscaped(t *testing.T) {
	m := newBotMetrics(filepath.Join(t.TempDir(), "x.prom"), time.Unix(1, 0))
	m.command(`ста"тус`)
	out := m.render(time.Unix(2, 0))
	if !strings.Contains(out, `statusbot_commands_total{command="ста\"тус"} 1`) {
		t.Fatalf("кавычка в метке не экранирована:\n%s", out)
	}
}
