// Выгрузка результатов обхода в формате Prometheus.
//
// # Почему файл, а не HTTP-эндпоинт
//
// Агент — oneshot по systemd-таймеру: он просыпается раз в минуту, обходит
// сервисы и завершается. Слушать порт ему нечем — процесса между запусками
// просто нет, и Prometheus получал бы отказ в соединении 59 секунд из 60.
//
// Для таких задач в экосистеме есть штатный ответ: textfile-коллектор
// node_exporter. Агент кладёт готовый .prom рядом, экспортёр читает его при
// каждом scrape и отдаёт вместе с метриками хоста. Наружу при этом не
// открывается ничего нового: файл лежит на диске сервера, а node_exporter
// работает в контейнере без опубликованных портов.
//
// # Что здесь есть и чего нет
//
// Метрики повторяют то, что показывает сама страница: доступность проверок,
// время ответа, запас по сертификату, инциденты и свежесть данных. Это
// сознательно: страница и график должны отвечать одинаково, иначе спор
// «что сломалось» превращается в спор «какому источнику верить».
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultMetricsPath — каталог, который читает textfile-коллектор
// node_exporter (см. metrics.samoy.love/docker-compose.yml).
const defaultMetricsPath = "/var/lib/node_exporter/textfile/status-agent.prom"

// promFile собирает документ формата exposition 0.0.4.
//
// Отдельный тип нужен из-за правила формата: строки # HELP и # TYPE идут ОДИН
// раз на семейство и до его значений, а сами значения семейства — подряд.
// Поэтому строки не пишутся сразу, а копятся по семействам и склеиваются в
// конце: иначе порядок вывода диктовал бы порядок обхода данных, и одна
// перестановка цикла давала бы файл, который парсер вправе отбросить целиком.
type promFile struct {
	order   []string
	headers map[string]string
	lines   map[string][]string
}

func newPromFile() *promFile {
	return &promFile{headers: map[string]string{}, lines: map[string][]string{}}
}

func (p *promFile) family(name, help, typ string) {
	if _, ok := p.headers[name]; ok {
		return
	}
	p.order = append(p.order, name)
	p.headers[name] = fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n", name, escapeHelp(help), name, typ)
}

type label struct{ name, value string }

func (p *promFile) sample(name string, labels []label, v float64) {
	if _, ok := p.headers[name]; !ok {
		// Значение без объявленного семейства — ошибка программиста; молча
		// потерять его хуже, чем отдать без HELP.
		p.order = append(p.order, name)
		p.headers[name] = ""
	}
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(l.name)
			b.WriteString(`="`)
			b.WriteString(escapeLabel(l.value))
			b.WriteString(`"`)
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(formatFloat(v))
	p.lines[name] = append(p.lines[name], b.String())
}

func (p *promFile) String() string {
	var b strings.Builder
	for _, name := range p.order {
		b.WriteString(p.headers[name])
		for _, line := range p.lines[name] {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// escapeLabel убирает из значения метки то, чем можно подделать строку
// документа. Значения приходят из конфига, но конфиг правится руками, и
// опечатка не должна ломать разбор всего файла.
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func formatFloat(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// buildMetrics превращает результат обхода в текст экспозиции.
//
// Она принимает уже готовую сводку, а не лезет считать заново: расхождение
// между числом на странице и числом на графике — худшее, что может случиться
// со статус-страницей, поэтому источник у них ровно один.
func buildMetrics(out Summary, incidents []Incident, runDuration time.Duration, now time.Time) string {
	p := newPromFile()

	p.family("status_check_up", "Доступность проверки: 1 — отвечает как ожидалось", "gauge")
	p.family("status_check_response_seconds", "Время ответа проверки", "gauge")
	p.family("status_check_code", "Код ответа HTTP (0 — соединение не состоялось)", "gauge")
	p.family("status_check_uptime_ratio", "Доля успешных проверок за окно", "gauge")
	// «Медленно» — доступность, но с оговоркой, поэтому отдаём двумя метриками:
	// _up отвечает на вопрос «работает ли», _slow — «не деградировало ли».
	// Считать медленный ответ падением значит поднимать тревогу на пустом месте.
	p.family("status_check_slow", "Ответ медленнее порога: 1 — медленно", "gauge")
	p.family("status_check_critical", "Проверка критична для вердикта проекта", "gauge")
	p.family("status_cert_days_left", "Дней до истечения TLS-сертификата", "gauge")
	p.family("status_unit_active", "Состояние systemd-юнита: 1 — active", "gauge")

	// Порядок вывода фиксирован: одинаковый вход должен давать одинаковый
	// файл, иначе diff в тесте невозможен, а отладка превращается в гадание.
	for _, proj := range out.Projects {
		for _, c := range proj.Checks {
			ls := []label{{"project", proj.ID}, {"check", c.ID}, {"name", c.Name}}
			p.sample("status_check_up", ls, b2f(c.Status != "down"))
			p.sample("status_check_slow", ls, b2f(c.Status == "slow"))
			p.sample("status_check_critical", ls, b2f(c.Critical))
			p.sample("status_check_response_seconds", ls, float64(c.Ms)/1000)
			p.sample("status_check_code", ls, float64(c.Code))
			if c.CertDays != nil {
				p.sample("status_cert_days_left", []label{{"project", proj.ID}, {"check", c.ID}}, float64(*c.CertDays))
			}
			for _, w := range []string{"d1", "d7", "d90"} {
				v, ok := c.Uptime[w]
				if !ok || v == nil {
					continue
				}
				f, ok := toFloat(v)
				if !ok {
					continue
				}
				p.sample("status_check_uptime_ratio",
					[]label{{"project", proj.ID}, {"check", c.ID}, {"window", w}}, f/100)
			}
		}
		for _, u := range proj.Units {
			p.sample("status_unit_active",
				[]label{{"project", proj.ID}, {"unit", u.Name}}, b2f(u.Active))
		}
	}

	// Сводка по обходу. Общий статус на странице виден как «operational /
	// degraded / major / down»; в числах это удобнее сравнивать порогом.
	//
	// Считаем по КРИТИЧНЫМ проверкам: второстепенные (админка, service worker)
	// не должны двигать число, по которому строят алерты. Сколько их не в
	// порядке, видно отдельным счётчиком.
	var up, total, aux int
	for _, proj := range out.Projects {
		up += proj.Up
		total += proj.Total
		aux += proj.AuxDown
	}
	p.family("status_checks_total", "Критичных проверок в конфиге", "gauge")
	p.sample("status_checks_total", nil, float64(total))
	p.family("status_checks_up", "Критичных проверок доступно", "gauge")
	p.sample("status_checks_up", nil, float64(up))
	p.family("status_checks_aux_down", "Второстепенных проверок недоступно", "gauge")
	p.sample("status_checks_aux_down", nil, float64(aux))

	// Инциденты. Открытый инцидент — это «сломано прямо сейчас и уже какое-то
	// время»; закрытые нужны, чтобы видеть, как часто это повторяется.
	open := 0
	lastByService := map[string]int64{}
	var services []string
	for _, in := range incidents {
		if in.End == "" {
			open++
			continue
		}
		if _, ok := lastByService[in.Service]; !ok {
			lastByService[in.Service] = in.DurationMs
			services = append(services, in.Service)
		}
	}
	sort.Strings(services)

	p.family("status_incidents_open", "Незакрытых инцидентов сейчас", "gauge")
	p.sample("status_incidents_open", nil, float64(open))
	p.family("status_incidents_recorded", "Инцидентов в журнале (журнал ограничен по длине)", "gauge")
	p.sample("status_incidents_recorded", nil, float64(len(incidents)))
	p.family("status_incident_last_duration_seconds", "Длительность последнего завершённого инцидента", "gauge")
	for _, s := range services {
		p.sample("status_incident_last_duration_seconds", []label{{"service", s}}, float64(lastByService[s])/1000)
	}

	// Свежесть собственных данных. Если таймер агента встал, все метрики выше
	// замрут на последних значениях и будут выглядеть здоровыми — «всё зелено,
	// потому что никто не смотрит» ловится только этой парой.
	p.family("status_agent_run_timestamp_seconds", "Время последнего обхода", "gauge")
	p.sample("status_agent_run_timestamp_seconds", nil, float64(now.Unix()))
	p.family("status_agent_run_duration_seconds", "Сколько занял обход", "gauge")
	p.sample("status_agent_run_duration_seconds", nil, runDuration.Seconds())

	return p.String()
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case *float64:
		if t == nil {
			return 0, false
		}
		return *t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	}
	return 0, false
}

// writeMetrics кладёт файл атомарно: node_exporter читает каталог по своему
// расписанию и может попасть ровно в середину записи, а половина документа —
// это отброшенный файл и дырка в графике.
func writeMetrics(path string, content string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
