// Агент статус-страницы: обходит сервисы, читает состояние systemd-юнитов и
// даты выкаток, копит историю и складывает готовый JSON для страницы.
//
// Запускается systemd-таймером раз в минуту. Работает на том же хосте, что и
// сами сервисы: это позволяет видеть юниты и версии, но означает, что при
// падении хоста страница будет недоступна — за внешнее наблюдение отвечает
// отдельный пробер в GitHub Actions, который шлёт уведомления в Telegram.
//
// Права root не нужны: systemctl show читается любым пользователем, каталоги
// с релизами доступны на чтение.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	httpTimeout   = 12 * time.Second
	rawWindow     = 7 * 24 * time.Hour
	hourlyKeep    = 90 * 24
	dailyKeep     = 365
	incidentsKeep = 50
	daysOnPage    = 90
	releasesKeep  = 20

	// Пауза перед подтверждающим запросом: достаточно, чтобы разойтись с
	// мгновенной сетевой икотой, и мало, чтобы уложиться в TimeoutStartSec.
	confirmDelay = 2 * time.Second

	// Порог для «крупного сбоя»: доля лежащих критичных проверок, при которой
	// «частичный» перестаёт быть честным словом.
	majorShare = 0.5
)

// ------------------------------------------------------------------ конфиг

type Config struct {
	Projects []Project `json:"projects"`
}

type Project struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle"`
	URL      string  `json:"url"`
	Accent   string  `json:"accent"`
	Checks   []Check `json:"checks"`
	Units    []Unit  `json:"units"`
	Builds   []Build `json:"builds"`
}

// Check описывает одну проверку.
//
// Признак «работает» здесь намеренно шире кода ответа: сервис, отвечающий 200
// пустым телом, HTML-заглушкой вместо скрипта или за восемь секунд, для
// пользователя не работает, а по одному лишь коду выглядит здоровым.
type Check struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note"`
	// Impact — что падение значит для пользователя. Показывается в блоке
	// сбоев: именно за этим на статус-страницу и приходят.
	Impact string `json:"impact"`
	URL    string `json:"url"`
	Expect int    `json:"expect"`
	Cert   string `json:"cert"`

	// Critical: падение критичной проверки роняет вердикт проекта. Умолчание —
	// true, поэтому здесь указатель: у обычного bool «не задано» неотличимо от
	// «неважная», и забытое поле молча превращало бы сервис во второстепенный.
	Critical *bool `json:"critical"`

	// SlowMs: медленный ответ — не «упал», но и не «работает». 0 — без порога.
	SlowMs int64 `json:"slowMs"`

	BodyIncludes string `json:"bodyIncludes"`
	ExpectType   string `json:"expectType"`

	// AllowHosts — куда, кроме исходного хоста, разрешено уводить редиректом.
	AllowHosts []string `json:"allowHosts"`

	// Steps — сценарий из нескольких запросов вместо одиночного GET.
	Steps []Step `json:"steps"`
}

// IsCritical: умолчание — критичная. Второстепенность должна быть решением,
// записанным в конфиг, а не следствием незаполненного поля.
func (c Check) IsCritical() bool { return c.Critical == nil || *c.Critical }

// Step — шаг сценарной проверки. Capture кладёт поля JSON-ответа в переменные,
// которые следующий шаг подставляет в URL по имени в фигурных скобках.
type Step struct {
	Name         string            `json:"name"`
	URL          string            `json:"url"`
	Expect       int               `json:"expect"`
	BodyIncludes string            `json:"bodyIncludes"`
	ExpectType   string            `json:"expectType"`
	Capture      map[string]string `json:"capture"`
}

type Unit struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Build описывает, откуда брать дату выкатки и версию.
//   - type "url"     — /version.json, который кладёт общий пайплайн: самый
//     точный источник, потому что отвечает сам работающий сервис, а не диск
//   - type "release" — путь это симлинк на каталог релиза; его имя и есть версия
//   - type "file"    — только время изменения файла, версии нет
//
// "url" появился, когда все проекты переехали на deploy-kit: до этого версия
// была известна лишь там, где релизы разложены каталогами.
type Build struct {
	Title string `json:"title"`
	Type  string `json:"type"`
	Path  string `json:"path"`
}

// ------------------------------------------------------------------ история

type sample [4]int64 // [unix, ok, ms, code]
type bucket [4]any   // [ключ, up, total, avgMs]

type Incident struct {
	Service    string `json:"service"`
	Name       string `json:"name"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Reason     string `json:"reason"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

type CheckState struct {
	Status   string `json:"status"`
	Since    string `json:"since"`
	Ms       int64  `json:"ms"`
	Code     int    `json:"code"`
	CertDays *int   `json:"certDays"`
}

type State struct {
	Updated  string                 `json:"updated"`
	Services map[string]*CheckState `json:"services"`
}

// ------------------------------------------------------------------ выдача

type OutCheck struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Note     string `json:"note"`
	Impact   string `json:"impact"`
	URL      string `json:"url"`
	Critical bool   `json:"critical"`
	SlowMs   int64  `json:"slowMs,omitempty"`
	// Steps — сколько шагов в сценарии; странице нужно лишь показать, что
	// проверка составная, а не перечислять их.
	Steps     int            `json:"steps,omitempty"`
	Status    string         `json:"status"`
	Since     string         `json:"since"`
	Ms        int64          `json:"ms"`
	Code      int            `json:"code"`
	Error     string         `json:"error"`
	CertDays  *int           `json:"certDays"`
	CertState string         `json:"certState,omitempty"`
	Uptime    map[string]any `json:"uptime"`
	// Days — ровно 90 ячеек по календарю, null там, где замеров не было.
	Days []*OutDay `json:"days"`
	// Coverage — за сколько из этих 90 суток замеры вообще есть. Без него
	// «100%» по трём наблюдавшимся дням неотличимо от честных девяноста.
	Coverage int     `json:"coverage"`
	Spark    []int64 `json:"spark"`
}

type OutDay struct {
	D     string `json:"d"`
	Up    int64  `json:"up"`
	Total int64  `json:"total"`
	AvgMs int64  `json:"avgMs"`
}

type OutUnit struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
	State  string `json:"state"`
	Since  string `json:"since"`
}

type OutBuild struct {
	Title   string `json:"title"`
	Version string `json:"version"`
	At      string `json:"at"`
	// URL — адрес самого выкаченного компонента, а не проекта целиком.
	// В сообщении о релизе полезно открыть именно то, что обновилось.
	URL     string    `json:"url,omitempty"`
	History []Release `json:"history,omitempty"`
}

// Release — одна запись в истории выкаток.
//
// At — время сборки по данным самого сервиса (или mtime симлинка релиза), а
// Seen — когда агент впервые увидел эту версию живой. Они расходятся: между
// сборкой и выкаткой проходит время, а если агент лежал, он заметит версию
// позже. Храним оба, потому что «когда собрали» и «когда поехало» — разные
// вопросы, и подменять один другим значит врать в истории.
type Release struct {
	Version string `json:"version"`
	At      string `json:"at,omitempty"`
	Seen    string `json:"seen"`
}

type OutProject struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	URL      string `json:"url"`
	Accent   string `json:"accent"`
	Status   string `json:"status"`
	// Up/Total — по критичным проверкам: именно они определяют вердикт.
	Up    int `json:"up"`
	Total int `json:"total"`
	// Сколько второстепенных проверок не в порядке. Вердикт они не роняют, но
	// умолчать о них тоже нельзя — иначе сломанная админка выглядит как ничто.
	AuxDown int        `json:"auxDown"`
	AuxSlow int        `json:"auxSlow"`
	Slow    int        `json:"slow"`
	Checks  []OutCheck `json:"checks"`
	Units   []OutUnit  `json:"units"`
	Builds  []OutBuild `json:"builds"`
}

type Summary struct {
	Updated   string       `json:"updated"`
	Overall   string       `json:"overall"`
	Projects  []OutProject `json:"projects"`
	Incidents []Incident   `json:"incidents"`
}

// ------------------------------------------------------------------ утилиты

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// Пишем через временный файл и переименование: страница может читать data
// в любой момент, и половинчатый JSON ей достаться не должен.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pct(up, total int64) *float64 {
	if total == 0 {
		return nil
	}
	v := float64(up) / float64(total) * 100
	v = float64(int(v*100+0.5)) / 100
	return &v
}

// ------------------------------------------------------------------ проверки

// Статусы проверки. «Медленно» — полноценное третье состояние, а не оттенок
// «работает»: сайт, отвечающий восемь секунд, для пользователя не работает,
// хотя код ответа безупречен.
const (
	statusUp   = "up"
	statusSlow = "slow"
	statusDown = "down"
)

// Тело читаем ради bodyIncludes и capture, но не целиком: манифест сборки
// бывает на мегабайты, а маркер и нужные поля лежат в начале.
const bodyReadLimit = 512 << 10

type result struct {
	check     Check
	status    string
	code      int
	ms        int64
	errText   string
	certDays  *int
	certState string
	attempts  int
}

func (r result) ok() bool { return r.status == statusUp || r.status == statusSlow }

// checkOnce — один проход проверки без повторов.
func checkOnce(c Check, client *http.Client) result {
	started := time.Now()
	var r result
	if len(c.Steps) > 0 {
		r = runSteps(c, client)
	} else {
		r = runStep(Step{
			URL:          c.URL,
			Expect:       c.Expect,
			BodyIncludes: c.BodyIncludes,
			ExpectType:   c.ExpectType,
		}, c, client, nil)
	}
	r.check = c
	r.ms = time.Since(started).Milliseconds()

	// Порог времени применяем только к успешному ответу: у упавшей проверки
	// «медленно» ничего не добавляет к «не работает».
	if r.status == statusUp && c.SlowMs > 0 && r.ms > c.SlowMs {
		r.status = statusSlow
		r.errText = fmt.Sprintf("ответ за %d мс при пороге %d мс", r.ms, c.SlowMs)
	}
	return r
}

// checkHTTP: сбой подтверждается вторым запросом.
//
// Одиночный оборванный коннект открывал инцидент, писал его в историю и слал
// тревогу в Telegram — а моргает не только сервис, но и дорога до него.
// Успех принимаем с первой попытки: подтверждать нечего.
func checkHTTP(c Check, client *http.Client) result {
	r := checkOnce(c, client)
	r.attempts = 1
	if r.status != statusDown {
		return r
	}
	time.Sleep(confirmDelay)
	second := checkOnce(c, client)
	second.attempts = 2
	if second.status != statusDown {
		// Первый заход соврал. В выдачу это не идёт — для страницы проверка
		// просто работает, — но в журнал попадает: если строка появляется
		// часто, моргает либо сервис, либо дорога до него, и это повод
		// разбираться, а не молча гасить повтором.
		log.Printf("проверка %s: первый заход дал %q, повтор прошёл", c.ID, r.errText)
	}
	return second
}

// runSteps — сценарий: несколько запросов подряд с передачей значений между
// ними. Одиночный GET проверяет, что endpoint отвечает; сценарий — что
// пользовательский путь проходим целиком.
func runSteps(c Check, client *http.Client) result {
	vars := map[string]string{}
	var last result
	for _, s := range c.Steps {
		last = runStep(s, c, client, vars)
		if last.status == statusDown {
			name := firstNonEmpty(s.Name, s.URL)
			last.errText = fmt.Sprintf("шаг «%s»: %s", name, last.errText)
			return last
		}
	}
	return last
}

// runStep выполняет один запрос и проверяет всё, что о нём известно: код,
// конечный хост после редиректов, тип содержимого и маркер в теле.
func runStep(s Step, c Check, client *http.Client, vars map[string]string) result {
	expect := s.Expect
	if expect == 0 {
		expect = 200
	}

	raw := s.URL
	for k, v := range vars {
		raw = strings.ReplaceAll(raw, "{"+k+"}", v)
	}
	if strings.Contains(raw, "{") && strings.Contains(raw, "}") {
		return result{status: statusDown, errText: "в URL осталась неподставленная переменная: " + raw}
	}

	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return result{status: statusDown, errText: err.Error()}
	}
	req.Header.Set("User-Agent", "samoylove-status-agent (+https://status.samoy.love)")

	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Timeout") {
			msg = fmt.Sprintf("таймаут %ds", int(httpTimeout.Seconds()))
		}
		return result{status: statusDown, errText: msg}
	}
	defer resp.Body.Close()

	r := result{status: statusUp, code: resp.StatusCode}

	if resp.StatusCode != expect {
		r.status = statusDown
		r.errText = fmt.Sprintf("HTTP %d вместо %d", resp.StatusCode, expect)
		return r
	}

	// Куда в итоге привёл редирект. Угнанный домен или кривой конфиг nginx
	// иначе выглядят полным здоровьем: чужой сервер ответил 200, и ладно.
	if from, err := url.Parse(raw); err == nil && resp.Request != nil && resp.Request.URL != nil {
		if to := resp.Request.URL.Hostname(); to != from.Hostname() && !allowedHost(to, c.AllowHosts) {
			r.status = statusDown
			r.errText = fmt.Sprintf("редирект увёл на посторонний хост %s", to)
			return r
		}
	}

	if s.ExpectType != "" {
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(strings.ToLower(ct), strings.ToLower(s.ExpectType)) {
			r.status = statusDown
			r.errText = fmt.Sprintf("Content-Type %q вместо %q", ct, s.ExpectType)
			return r
		}
	}

	needBody := s.BodyIncludes != "" || len(s.Capture) > 0
	if !needBody {
		return r
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyReadLimit))
	if err != nil {
		r.status = statusDown
		r.errText = "не удалось дочитать ответ: " + err.Error()
		return r
	}
	if s.BodyIncludes != "" && !strings.Contains(string(body), s.BodyIncludes) {
		r.status = statusDown
		r.errText = fmt.Sprintf("в ответе нет %q — код 200, но содержимое не то", s.BodyIncludes)
		return r
	}
	for name, field := range s.Capture {
		var doc map[string]any
		if json.Unmarshal(body, &doc) != nil {
			r.status = statusDown
			r.errText = "ответ не разбирается как JSON, брать " + field + " не из чего"
			return r
		}
		v, found := doc[field]
		if !found {
			r.status = statusDown
			r.errText = fmt.Sprintf("в ответе нет поля %q", field)
			return r
		}
		vars[name] = fmt.Sprint(v)
	}
	return r
}

func allowedHost(host string, allow []string) bool {
	for _, a := range allow {
		if strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}

// Состояния сертификата. Раньше любая беда давала nil, и плашка TLS просто
// исчезала — ровно в тот момент, когда она нужнее всего. «Истёк» и «не смогли
// проверить» — разные новости, и путать их нельзя.
const (
	certOK          = "ok"
	certExpired     = "expired"
	certInvalid     = "invalid"
	certUnreachable = "unreachable"
)

// certDaysLeft: соединяемся без проверки цепочки, чтобы сертификат достался
// нам даже когда он негоден, и уже потом судим о его пригодности отдельно.
// Иначе про истёкший сертификат нельзя сказать ничего, кроме «не вышло».
func certDaysLeft(host string) (*int, string) {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: httpTimeout},
		"tcp", host+":443",
		//nolint:gosec // G402: проверку делаем сами ниже — здесь она мешает узнать причину.
		&tls.Config{ServerName: host, InsecureSkipVerify: true},
	)
	if err != nil {
		return nil, certUnreachable
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, certUnreachable
	}
	leaf := certs[0]
	days := int(time.Until(leaf.NotAfter).Hours() / 24)

	if time.Now().After(leaf.NotAfter) {
		return &days, certExpired
	}

	roots := x509.NewCertPool()
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if sys, err := x509.SystemCertPool(); err == nil && sys != nil {
		roots = sys
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: inter,
	}); err != nil {
		return &days, certInvalid
	}
	return &days, certOK
}

// systemctl show читается без root. Пустой ActiveEnterTimestamp у остановленного
// юнита — норма, поэтому ошибка разбора времени не считается ошибкой проверки.
func unitState(name string) OutUnit {
	out := OutUnit{Name: name, State: "unknown"}
	cmd := exec.Command("systemctl", "show", name,
		"-p", "ActiveState", "-p", "SubState", "-p", "ActiveEnterTimestamp")
	b, err := cmd.Output()
	if err != nil {
		return out
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			fields[k] = v
		}
	}
	out.State = fields["ActiveState"]
	if sub := fields["SubState"]; sub != "" && sub != out.State {
		out.State = out.State + " / " + sub
	}
	out.Active = fields["ActiveState"] == "active"
	if ts := fields["ActiveEnterTimestamp"]; ts != "" {
		if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", ts); err == nil {
			out.Since = t.UTC().Format(time.RFC3339)
		}
	}
	return out
}

func buildInfo(b Build, client *http.Client) OutBuild {
	out := OutBuild{Title: b.Title}
	// Куда вести читателя за этим компонентом. У «url»-целей адрес известен
	// точно: version.json отдаёт сам сервис, значит его origin и есть адрес.
	// Для остальных ссылку подставит проект — здесь её взять неоткуда.
	if b.Type == "url" {
		if u, err := url.Parse(b.Path); err == nil && u.Scheme != "" && u.Host != "" {
			out.URL = u.Scheme + "://" + u.Host + "/"
		}
	}
	switch b.Type {
	case "url":
		// Отвечает сам сервис — значит, показана версия того, что реально
		// работает, а не того, что лежит на диске.
		resp, err := client.Get(b.Path)
		if err != nil {
			return out
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return out
		}
		var v struct {
			Version string `json:"version"`
			BuiltAt string `json:"builtAt"`
		}
		if json.NewDecoder(resp.Body).Decode(&v) == nil {
			out.Version = v.Version
			out.At = v.BuiltAt
		}
	case "release":
		// Симлинк вида current -> releases/20260801-225039-5486b2d:
		// имя каталога и есть версия — дата плюс короткий коммит.
		if target, err := os.Readlink(b.Path); err == nil {
			out.Version = filepath.Base(target)
		}
		if fi, err := os.Lstat(b.Path); err == nil {
			out.At = fi.ModTime().UTC().Format(time.RFC3339)
		}
	default:
		if fi, err := os.Stat(b.Path); err == nil {
			out.At = fi.ModTime().UTC().Format(time.RFC3339)
		}
	}
	return out
}

// ------------------------------------------------------------------ агрегация

func bumpBucket(buckets []bucket, key any, ok bool, ms int64, keep int) []bucket {
	if n := len(buckets); n > 0 && fmt.Sprint(buckets[n-1][0]) == fmt.Sprint(key) {
		last := &buckets[n-1]
		up := toInt((*last)[1])
		total := toInt((*last)[2])
		avg := toInt((*last)[3])
		if ok {
			up++
		}
		total++
		avg = (avg*(total-1) + ms) / total
		(*last)[1], (*last)[2], (*last)[3] = up, total, avg
	} else {
		var up int64
		if ok {
			up = 1
		}
		buckets = append(buckets, bucket{key, up, int64(1), ms})
	}
	if len(buckets) > keep {
		buckets = buckets[len(buckets)-keep:]
	}
	return buckets
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func uptimeFromBuckets(buckets []bucket, count int) *float64 {
	if len(buckets) > count {
		buckets = buckets[len(buckets)-count:]
	}
	var up, total int64
	for _, b := range buckets {
		up += toInt(b[1])
		total += toInt(b[2])
	}
	return pct(up, total)
}

// Аптайм считаем по календарному окну, а не по числу корзин.
//
// Корзина заводится только когда агент отработал. Раньше «за 90 дней» брало
// девяносто ПОСЛЕДНИХ корзин: если агент неделю лежал, окно незаметно
// растягивалось на девяносто семь суток, а сам простой агента ещё и улучшал
// цифру, потому что плохих замеров в нём не было. Теперь окно задаёт
// календарь, а дни без замеров честно остаются дырками.

// dayWindow возвращает ключи последних n суток, свежий — последним.
func dayWindow(now time.Time, n int) []string {
	keys := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		keys = append(keys, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return keys
}

func indexBuckets(buckets []bucket) map[string]bucket {
	idx := make(map[string]bucket, len(buckets))
	for _, b := range buckets {
		idx[fmt.Sprint(b[0])] = b
	}
	return idx
}

// uptimeOverDays — доступность за последние n календарных суток.
func uptimeOverDays(daily []bucket, now time.Time, n int) *float64 {
	idx := indexBuckets(daily)
	var up, total int64
	for _, key := range dayWindow(now, n) {
		if b, ok := idx[key]; ok {
			up += toInt(b[1])
			total += toInt(b[2])
		}
	}
	return pct(up, total)
}

// uptimeOverHours — то же для часового окна: ключ часовой корзины это unix
// начала часа.
func uptimeOverHours(hourly []bucket, now time.Time, hours int) *float64 {
	idx := indexBuckets(hourly)
	var up, total int64
	for i := 0; i < hours; i++ {
		key := fmt.Sprint(now.Add(-time.Duration(i) * time.Hour).Truncate(time.Hour).Unix())
		if b, ok := idx[key]; ok {
			up += toInt(b[1])
			total += toInt(b[2])
		}
	}
	return pct(up, total)
}

// daysForPage — ровно n ячеек по календарю; сутки без замеров это nil, и на
// шкале они выглядят дыркой, а не сдвигают всю историю влево.
func daysForPage(daily []bucket, now time.Time, n int) []*OutDay {
	idx := indexBuckets(daily)
	out := make([]*OutDay, 0, n)
	for _, key := range dayWindow(now, n) {
		b, ok := idx[key]
		if !ok || toInt(b[2]) == 0 {
			out = append(out, nil)
			continue
		}
		out = append(out, &OutDay{D: key, Up: toInt(b[1]), Total: toInt(b[2]), AvgMs: toInt(b[3])})
	}
	return out
}

func uptimeFromRaw(raw []sample, hours int) *float64 {
	from := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	var up, total int64
	for _, s := range raw {
		if s[0] >= from {
			total++
			up += s[1]
		}
	}
	return pct(up, total)
}

// ------------------------------------------------------------------ main

func main() {
	cfgPath := flag.String("config", "/etc/status-agent/status.json", "путь к конфигу")
	dataDir := flag.String("data", "/var/www/status/data", "куда складывать данные")
	metricsPath := flag.String("metrics", defaultMetricsPath,
		"куда класть .prom для textfile-коллектора node_exporter; пусто — не писать")
	flag.Parse()

	runStart := time.Now()

	var cfg Config
	if !readJSON(*cfgPath, &cfg) {
		log.Fatalf("не удалось прочитать конфиг %s", *cfgPath)
	}

	now := time.Now().UTC()
	client := &http.Client{Timeout: httpTimeout}

	state := State{Services: map[string]*CheckState{}}
	readJSON(filepath.Join(*dataDir, "state.json"), &state)
	if state.Services == nil {
		state.Services = map[string]*CheckState{}
	}
	var incidents []Incident
	readJSON(filepath.Join(*dataDir, "incidents.json"), &incidents)

	// История выкаток. Ключ — проект плюс название цели, потому что у проекта
	// целей несколько (сайт, API, админка) и версии у них свои.
	releases := map[string][]Release{}
	readJSON(filepath.Join(*dataDir, "releases.json"), &releases)

	// Все проверки — параллельно: последовательный обход десятка эндпоинтов
	// растянул бы запуск на секунды и смазал бы измерение времени ответа.
	type job struct {
		project *Project
		check   Check
	}
	var jobs []job
	for i := range cfg.Projects {
		for _, c := range cfg.Projects[i].Checks {
			jobs = append(jobs, job{&cfg.Projects[i], c})
		}
	}

	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			r := checkHTTP(j.check, client)
			if j.check.Cert != "" {
				r.certDays, r.certState = certDaysLeft(j.check.Cert)
			}
			results[i] = r
		}(i, j)
	}
	wg.Wait()

	byCheckID := map[string]OutCheck{}

	for i, j := range jobs {
		r := results[i]
		id := j.check.ID
		status := r.status

		rawPath := filepath.Join(*dataDir, "raw", id+".json")
		hourlyPath := filepath.Join(*dataDir, "hourly", id+".json")
		dailyPath := filepath.Join(*dataDir, "daily", id+".json")

		// В историю доступности «медленно» идёт как доступность: аптайм должен
		// оставаться про доступность, иначе он перестанет быть сравнимым с
		// собой же за прошлые месяцы. Деградацию видно по времени ответа.
		var raw []sample
		readJSON(rawPath, &raw)
		var okInt int64
		if r.ok() {
			okInt = 1
		}
		raw = append(raw, sample{now.Unix(), okInt, r.ms, int64(r.code)})
		cutoff := now.Add(-rawWindow).Unix()
		for len(raw) > 0 && raw[0][0] < cutoff {
			raw = raw[1:]
		}

		var hourly, daily []bucket
		readJSON(hourlyPath, &hourly)
		readJSON(dailyPath, &daily)
		hourly = bumpBucket(hourly, now.Truncate(time.Hour).Unix(), r.ok(), r.ms, hourlyKeep)
		daily = bumpBucket(daily, now.Format("2006-01-02"), r.ok(), r.ms, dailyKeep)

		_ = writeJSON(rawPath, raw)
		_ = writeJSON(hourlyPath, hourly)
		_ = writeJSON(dailyPath, daily)

		prev := state.Services[id]
		since := now.Format(time.RFC3339)
		if prev != nil && prev.Status == status {
			since = prev.Since
		} else if prev != nil {
			// Инцидент — только про недоступность. Переход «работает» →
			// «медленно» состояние меняет, но инцидентом не является, иначе
			// история засорится деградациями и в ней утонут настоящие падения.
			full := j.project.Title + " · " + j.check.Name
			switch {
			case status == statusDown:
				incidents = append([]Incident{{
					Service: id, Name: full, Start: since,
					Reason: firstNonEmpty(r.errText, "недоступен"),
				}}, incidents...)
			case prev.Status == statusDown:
				for k := range incidents {
					if incidents[k].Service == id && incidents[k].End == "" {
						incidents[k].End = since
						if t0, err := time.Parse(time.RFC3339, incidents[k].Start); err == nil {
							incidents[k].DurationMs = now.Sub(t0).Milliseconds()
						}
						break
					}
				}
			}
		}
		state.Services[id] = &CheckState{
			Status: status, Since: since, Ms: r.ms, Code: r.code, CertDays: r.certDays,
		}

		days := daysForPage(daily, now, daysOnPage)
		coverage := 0
		for _, d := range days {
			if d != nil {
				coverage++
			}
		}

		spark := []int64{}
		sfrom := 0
		if len(hourly) > 24 {
			sfrom = len(hourly) - 24
		}
		for _, b := range hourly[sfrom:] {
			spark = append(spark, toInt(b[3]))
		}

		byCheckID[id] = OutCheck{
			ID: id, Name: j.check.Name, Note: j.check.Note, Impact: j.check.Impact,
			URL:      firstNonEmpty(j.check.URL, stepsEntryURL(j.check)),
			Critical: j.check.IsCritical(), SlowMs: j.check.SlowMs, Steps: len(j.check.Steps),
			Status: status, Since: since, Ms: r.ms, Code: r.code, Error: r.errText,
			CertDays: r.certDays, CertState: r.certState,
			Uptime: map[string]any{
				"d1":  uptimeFromRaw(raw, 24),
				"d7":  uptimeOverHours(hourly, now, 24*7),
				"d90": uptimeOverDays(daily, now, 90),
			},
			Days: days, Coverage: coverage, Spark: spark,
		}
	}

	out := Summary{Updated: now.Format(time.RFC3339)}
	for _, p := range cfg.Projects {
		op := OutProject{
			ID: p.ID, Title: p.Title, Subtitle: p.Subtitle, URL: p.URL, Accent: p.Accent,
		}
		// Вердикт проекта считаем по критичным проверкам. Раньше все проверки
		// весили одинаково, и падение внутренней админки описывалось теми же
		// словами, что и падение игрового сервера, без которого не идут матчи.
		for _, c := range p.Checks {
			oc := byCheckID[c.ID]
			op.Checks = append(op.Checks, oc)
			switch {
			case !oc.Critical:
				switch oc.Status {
				case statusDown:
					op.AuxDown++
				case statusSlow:
					op.AuxSlow++
				}
			default:
				op.Total++
				switch oc.Status {
				case statusUp:
					op.Up++
				case statusSlow:
					op.Up++
					op.Slow++
				}
			}
		}
		for _, u := range p.Units {
			ou := unitState(u.Name)
			ou.Title = u.Title
			op.Units = append(op.Units, ou)
		}
		for _, b := range p.Builds {
			ob := buildInfo(b, client)
			if ob.URL == "" {
				ob.URL = p.URL
			}
			key := p.ID + "::" + b.Title
			releases[key] = recordRelease(releases[key], ob, now)
			ob.History = releases[key]
			op.Builds = append(op.Builds, ob)
		}
		op.Status = projectStatus(op)
		out.Projects = append(out.Projects, op)
	}

	out.Overall = overallStatus(out.Projects)

	sort.SliceStable(incidents, func(i, j int) bool { return incidents[i].Start > incidents[j].Start })
	if len(incidents) > incidentsKeep {
		incidents = incidents[:incidentsKeep]
	}
	out.Incidents = incidents
	if len(out.Incidents) > 10 {
		out.Incidents = out.Incidents[:10]
	}

	state.Updated = out.Updated
	if err := writeJSON(filepath.Join(*dataDir, "state.json"), state); err != nil {
		log.Fatalf("не удалось записать state.json: %v", err)
	}
	_ = writeJSON(filepath.Join(*dataDir, "incidents.json"), incidents)
	_ = writeJSON(filepath.Join(*dataDir, "releases.json"), releases)
	if err := writeJSON(filepath.Join(*dataDir, "summary.json"), out); err != nil {
		log.Fatalf("не удалось записать summary.json: %v", err)
	}

	var down, slow, aux int
	for _, p := range out.Projects {
		down += p.Total - p.Up
		slow += p.Slow
		aux += p.AuxDown
	}
	log.Printf(
		"проверок: %d, критичных недоступно: %d, медленных: %d, второстепенных недоступно: %d, состояние: %s",
		len(jobs), down, slow, aux, out.Overall,
	)

	// Метрики пишутся последними и не могут сорвать запуск: данные страницы
	// уже на диске, и падать из-за наблюдения за собой было бы обидно.
	if err := writeMetrics(*metricsPath, buildMetrics(out, incidents, time.Since(runStart), now)); err != nil {
		log.Printf("метрики не записаны (%s): %v", *metricsPath, err)
	}
}

// projectStatus — вердикт по критичным проверкам.
//
// Второстепенные проверки статус не меняют: они видны на странице и считаются
// в AuxDown, но админка, недоступная для публикации, не должна выглядеть как
// проблема у пользователей, которых она не касается.
func projectStatus(op OutProject) string {
	switch {
	case op.Total == 0:
		// Критичных проверок нет вовсе — судить не по чему.
		if op.AuxDown > 0 {
			return "degraded"
		}
		return statusUp
	case op.Up == 0:
		return statusDown
	case op.Up < op.Total:
		return "degraded"
	case op.Slow > 0:
		return "degraded"
	}
	return statusUp
}

// Общий статус: между «частичным» и «массовым» появилась ступень.
//
// Раньше «массовый сбой» требовал, чтобы легли ВСЕ проверки до единой: три
// полностью мёртвых проекта из четырёх описывались тем же словом, что и
// моргнувшая второстепенная проверка. Теперь смотрим на долю лежащих
// критичных проверок.
func overallStatus(projects []OutProject) string {
	var critical, down, slow, auxDown int
	for _, p := range projects {
		critical += p.Total
		down += p.Total - p.Up
		slow += p.Slow
		auxDown += p.AuxDown
	}
	switch {
	case critical == 0:
		return "operational"
	case down == critical:
		return "down"
	case float64(down)/float64(critical) >= majorShare:
		return "major"
	case down > 0:
		return "degraded"
	case slow > 0 || auxDown > 0:
		return "degraded"
	}
	return "operational"
}

// stepsEntryURL — адрес первого шага сценария: у составной проверки своего
// URL нет, а ссылка на странице нужна.
func stepsEntryURL(c Check) string {
	if len(c.Steps) > 0 {
		return c.Steps[0].URL
	}
	return ""
}

// recordRelease дописывает версию в историю, если она сменилась.
//
// Сравниваем только с самой свежей записью, а не ищем по всему списку: откат
// на предыдущую версию — это тоже событие выкатки, и он обязан попасть в
// историю отдельной строкой, а не быть проглочен как «уже видели».
func recordRelease(history []Release, b OutBuild, now time.Time) []Release {
	if b.Version == "" {
		return history
	}
	if len(history) > 0 && history[0].Version == b.Version {
		return history
	}
	history = append([]Release{{
		Version: b.Version,
		At:      b.At,
		Seen:    now.Format(time.RFC3339),
	}}, history...)
	if len(history) > releasesKeep {
		history = history[:releasesKeep]
	}
	return history
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
