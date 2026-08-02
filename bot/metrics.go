// Счётчики бота в формате Prometheus.
//
// # Зачем боту метрики
//
// Молчащий бот неотличим от работающего до первой аварии — ровно та же
// причина, по которой у выкатки есть -selftest. Счётчики отвечают на вопросы,
// которые иначе выясняются в самый неудачный момент: доходят ли уведомления,
// не отвечает ли Telegram отказами, шлём ли мы напоминания об одном и том же
// простое чаще, чем договаривались.
//
// # Почему файл, а не эндпоинт
//
// Бот принципиально никуда не слушает: он сам ходит в Telegram длинным
// опросом (см. .deploy-kit/bot.env — HEALTH у цели нет и не будет). Открывать
// ради метрик первый в его жизни порт значит завести новую поверхность там,
// где её сознательно не было. Файл для textfile-коллектора node_exporter даёт
// то же самое и ничего не открывает.
//
// Счётчики живут в памяти и обнуляются при перезапуске — для counter это
// штатно, rate() распознаёт сброс. Отдельная метка времени heartbeat нужна
// потому, что файл переживает сам процесс: без неё остановленный бот выглядел
// бы вечно живым с последними значениями.
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultBotMetricsPath — каталог textfile-коллектора node_exporter.
const defaultBotMetricsPath = "/var/lib/node_exporter/textfile/samoylove-bot.prom"

// botMetrics — счётчики одного процесса.
//
// Все методы безопасны на nil-приёмнике: бот должен работать и там, где
// выгрузка выключена (локальный запуск, хост без node_exporter), не обрастая
// проверками на каждой строке вызова.
type botMetrics struct {
	path string

	mu             sync.Mutex
	notifications  map[string]float64
	commands       map[string]float64
	sendFailures   float64
	pollFailures   float64
	lastNotifiedAt int64
	startedAt      int64
}

func newBotMetrics(path string, now time.Time) *botMetrics {
	if path == "" {
		return nil
	}
	return &botMetrics{
		path:          path,
		notifications: map[string]float64{},
		commands:      map[string]float64{},
		startedAt:     now.Unix(),
	}
}

// notified — уведомление ушло владельцу. kind повторяет вид события
// (down/still/up/release), а не текст: текст пересказывать метрике незачем.
func (m *botMetrics) notified(kind string, now time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.notifications[kind]++
	m.lastNotifiedAt = now.Unix()
	m.mu.Unlock()
}

// sendFailed — Telegram не принял сообщение. Считается отдельно от отправок:
// «уведомлений ноль» и «уведомления не уходят» — разные аварии.
func (m *botMetrics) sendFailed() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.sendFailures++
	m.mu.Unlock()
}

// command — владелец что-то спросил у бота.
func (m *botMetrics) command(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.commands[name]++
	m.mu.Unlock()
}

// pollFailed — не удался длинный опрос Telegram.
func (m *botMetrics) pollFailed() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.pollFailures++
	m.mu.Unlock()
}

// render собирает документ экспозиции. Значения одного семейства идут подряд
// и после своих # HELP/# TYPE: иначе парсер вправе отбросить весь файл.
func (m *botMetrics) render(now time.Time) string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	notifications := copyCounts(m.notifications)
	commands := copyCounts(m.commands)
	sendFailures, pollFailures := m.sendFailures, m.pollFailures
	lastNotifiedAt, startedAt := m.lastNotifiedAt, m.startedAt
	m.mu.Unlock()

	var b strings.Builder
	writeFamily(&b, "statusbot_notifications_total",
		"Отправленные уведомления по виду события", "counter", "kind", notifications)
	writeFamily(&b, "statusbot_commands_total",
		"Обработанные команды владельца", "counter", "command", commands)

	writeSingle(&b, "statusbot_send_failures_total",
		"Сообщения, которые Telegram не принял", "counter", sendFailures)
	writeSingle(&b, "statusbot_poll_failures_total",
		"Неудачные опросы Telegram", "counter", pollFailures)
	writeSingle(&b, "statusbot_last_notification_timestamp_seconds",
		"Когда ушло последнее уведомление (0 — ни одного с запуска)", "gauge", float64(lastNotifiedAt))
	writeSingle(&b, "statusbot_start_timestamp_seconds",
		"Когда процесс запустился", "gauge", float64(startedAt))
	writeSingle(&b, "statusbot_heartbeat_timestamp_seconds",
		"Когда бот последний раз обновлял этот файл", "gauge", float64(now.Unix()))
	return b.String()
}

func copyCounts(src map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func writeFamily(b *strings.Builder, name, help, typ, labelName string, values map[string]float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	// Сортировка не косметика: файл переписывается каждые полминуты, и без
	// неё его невозможно ни сравнить глазами, ни проверить тестом.
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s{%s=\"%s\"} %s\n", name, labelName, escapeLabel(k), formatFloat(values[k]))
	}
}

func writeSingle(b *strings.Builder, name, help, typ string, v float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n",
		name, escapeHelp(help), name, typ, name, formatFloat(v))
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func formatFloat(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// flush пишет файл атомарно: коллектор читает каталог по своему расписанию и
// вполне может попасть в середину записи, а половина документа — это
// отброшенный файл и дырка в графике.
func (m *botMetrics) flush(now time.Time) error {
	if m == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(m.render(now)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
