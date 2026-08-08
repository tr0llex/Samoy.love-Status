package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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

	// EventsDir — журнал выкаток, который пишет deploy-kit
	// (deploy-kit/docs/events.md, §1). Пустая строка означает «журнал не
	// читать»: старый путь (релиз по разнице версий) продолжает работать сам
	// по себе, и снять новый можно одной строкой в файле окружения, не
	// выкатывая бота. Пока новый путь не отстоял несколько настоящих выкаток,
	// это единственная страховка, и стоит она ноль.
	EventsDir string
	// Groups — где лежит память о сообщениях прогонов: group → message_id и
	// исходы уже объявленных целей (контракт, §10).
	//
	// Отдельным файлом рядом с state.json, а НЕ полем State: структура State
	// живёт в watch.go, и добавлять туда поле посреди волны, когда файл правят
	// другие, значило бы потерять либо своё, либо чужое. Каталог тот же
	// (ReadWritePaths юнита), запись такая же атомарная. Слить обратно в
	// state.json — правка на одно поле, и её место — watch.go.
	Groups string

	Remind time.Duration
	Watch  time.Duration
	Stale  time.Duration
	// Events — как часто перечитывается журнал выкаток. Секунда, а не минута:
	// мгновенность и есть то единственное, ради чего событие заводилось вместо
	// наблюдения. Тик стоит один readdir по каталогу с десятком файлов.
	Events time.Duration

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
		EventsDir: envStr("EVENTS_DIR", defaultEventsDir),
		Remind:    envDuration("REMIND_INTERVAL", 15*time.Minute),
		Watch:     envDuration("WATCH_INTERVAL", 30*time.Second),
		Stale:     envDuration("STALE_AFTER", 5*time.Minute),
		Events:    envDuration("EVENTS_INTERVAL", inboxInterval),
		MiniApp:   envStr("MINIAPP_URL", "https://status.samoy.love/tg/"),
		StatusURL: envStr("STATUS_URL", "https://status.samoy.love/"),
	}
	// «off» — явное выключение чтения журнала. Отдельное слово, а не пустая
	// строка: пустое значение переменной в файле окружения чаще всего означает
	// «забыли дописать», и молча выключать по нему уведомления о выкатках
	// нельзя — тишина в чате читается как «не катились».
	if c.EventsDir == "off" || c.EventsDir == "-" {
		c.EventsDir = ""
	}
	c.Groups = envStr("GROUPS_FILE", filepath.Join(filepath.Dir(c.State), "deploy-groups.json"))
	// Ноль и отрицательное значение — не «читать без пауз», а опечатка:
	// time.NewTicker на таком просто паникует, и бот не пережил бы старта.
	if c.Events <= 0 {
		log.Printf("EVENTS_INTERVAL=%s не годится для тика, беру %s", c.Events, inboxInterval)
		c.Events = inboxInterval
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
