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
	Token string
	// Owner — id ЧАТА, куда бот пишет. OwnerUser — id ЧЕЛОВЕКА, чьи команды
	// и нажатия бот выполняет. В личной переписке это одно и то же число,
	// поэтому отдельной настройки обычно не требуется; в группе — разное,
	// и одного chat id для проверки прав не хватает (см. loadConfig).
	// OwnerUser == 0 означает «владелец неизвестен».
	Owner     int64
	OwnerUser int64
	Self      string
	DataDir   string
	State     string
	Metrics   string

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

	// Кто владелец — вопрос отдельный от того, в каком чате разговор.
	//
	// Нажатие на кнопку приносит только id пользователя, а сообщение — id
	// чата, и проверялись оба по одному и тому же TELEGRAM_CHAT_ID. В личной
	// переписке (id положительный) числа совпадают и всё сходится. Если же в
	// TELEGRAM_CHAT_ID стоит группа (id отрицательный), то кнопки перестают
	// работать вообще, а команды начинают работать у ЛЮБОГО участника чата:
	// «свой» означает не «владелец», а «в нужной комнате».
	//
	// Поэтому владельца можно задать явно. По умолчанию поведение прежнее.
	c.OwnerUser = owner
	if v := os.Getenv("TELEGRAM_OWNER_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return c, fmt.Errorf("TELEGRAM_OWNER_ID=%q не похож на id пользователя", v)
		}
		c.OwnerUser = id
	} else if owner < 0 {
		c.OwnerUser = 0
		log.Printf("TELEGRAM_CHAT_ID=%d похож на группу или канал: кнопки работать не будут, "+
			"а команды сможет отдавать любой участник чата. Задайте TELEGRAM_OWNER_ID со своим id пользователя.", owner)
	}
	return c, nil
}
