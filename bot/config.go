package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

// Настройки бота живут в одном месте — в окружении.
//
// Было пять: флаги командной строки, переменные окружения, аргументы в
// ExecStart юнита, файл /etc/samoylove-bot/env и константы в коде. Причём
// каждая длительность задавалась и флагом, и переменной — два способа задать
// одно и то же, и по строке запуска не понять, какой сработал.
//
// Теперь источник один. systemd и так читает EnvironmentFile ради секретов,
// значит остальное логично держать рядом: одно место, чтобы посмотреть, и
// одно, чтобы поправить. Флагом остался только -selftest — это действие, а не
// настройка.
type Config struct {
	Token   string
	Owner   int64
	Self    string
	DataDir string
	State   string
	Metrics string

	Remind time.Duration
	Watch  time.Duration
	Stale  time.Duration

	MiniApp   string
	StatusURL string
}

func (c Config) SummaryPath() string { return c.DataDir + "/summary.json" }

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// envDuration: неразобранное значение не роняет бота, но и не проходит молча.
// Опечатка в файле окружения не должна оставлять владельца без уведомлений.
func envDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("%s=%q не разобран как длительность, беру %s", name, v, def)
		return def
	}
	return d
}

// loadConfig собирает настройки и объясняет, чего не хватает.
func loadConfig() (Config, error) {
	c := Config{
		Token:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		Self:      os.Getenv("TELEGRAM_BOT_USERNAME"),
		DataDir:   envStr("DATA_DIR", "/var/www/status/data"),
		State:     envStr("STATE_FILE", "/var/lib/samoylove-bot/state.json"),
		Metrics:   envStr("METRICS_FILE", defaultBotMetricsPath),
		Remind:    envDuration("REMIND_INTERVAL", 15*time.Minute),
		Watch:     envDuration("WATCH_INTERVAL", 30*time.Second),
		Stale:     envDuration("STALE_AFTER", 5*time.Minute),
		MiniApp:   envStr("MINIAPP_URL", "https://status.samoy.love/tg/"),
		StatusURL: envStr("STATUS_URL", "https://status.samoy.love/"),
	}
	if c.Token == "" {
		return c, fmt.Errorf("нет TELEGRAM_BOT_TOKEN: положите его в файл окружения службы")
	}
	owner, err := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
	if err != nil {
		return c, fmt.Errorf("нет корректного TELEGRAM_CHAT_ID: бот обязан знать, кому отвечать")
	}
	c.Owner = owner
	return c, nil
}
