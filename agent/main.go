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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
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

type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Note   string `json:"note"`
	URL    string `json:"url"`
	Expect int    `json:"expect"`
	Cert   string `json:"cert"`
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

type sample [4]int64  // [unix, ok, ms, code]
type bucket [4]any    // [ключ, up, total, avgMs]

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
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Note     string         `json:"note"`
	URL      string         `json:"url"`
	Status   string         `json:"status"`
	Since    string         `json:"since"`
	Ms       int64          `json:"ms"`
	Code     int            `json:"code"`
	Error    string         `json:"error"`
	CertDays *int           `json:"certDays"`
	Uptime   map[string]any `json:"uptime"`
	Days     []OutDay       `json:"days"`
	Spark    []int64        `json:"spark"`
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
}

type OutProject struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Subtitle string     `json:"subtitle"`
	URL      string     `json:"url"`
	Accent   string     `json:"accent"`
	Status   string     `json:"status"`
	Up       int        `json:"up"`
	Total    int        `json:"total"`
	Checks   []OutCheck `json:"checks"`
	Units    []OutUnit  `json:"units"`
	Builds   []OutBuild `json:"builds"`
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

type result struct {
	check    Check
	ok       bool
	code     int
	ms       int64
	errText  string
	certDays *int
}

func checkHTTP(c Check, client *http.Client) result {
	started := time.Now()
	expect := c.Expect
	if expect == 0 {
		expect = 200
	}
	req, err := http.NewRequest(http.MethodGet, c.URL, nil)
	if err != nil {
		return result{check: c, ms: 0, errText: err.Error()}
	}
	req.Header.Set("User-Agent", "samoy-status-agent (+https://status.samoy.love)")
	resp, err := client.Do(req)
	ms := time.Since(started).Milliseconds()
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Timeout") {
			msg = fmt.Sprintf("таймаут %ds", int(httpTimeout.Seconds()))
		}
		return result{check: c, ms: ms, errText: msg}
	}
	defer resp.Body.Close()
	ok := resp.StatusCode == expect
	r := result{check: c, ok: ok, code: resp.StatusCode, ms: ms}
	if !ok {
		r.errText = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return r
}

// Срок жизни сертификата: истекший роняет сайт разом и молча, знать надо заранее.
func certDaysLeft(host string) *int {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: httpTimeout},
		"tcp", host+":443",
		&tls.Config{ServerName: host},
	)
	if err != nil {
		return nil
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	d := int(time.Until(certs[0].NotAfter).Hours() / 24)
	return &d
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
	flag.Parse()

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
				r.certDays = certDaysLeft(j.check.Cert)
			}
			results[i] = r
		}(i, j)
	}
	wg.Wait()

	byCheckID := map[string]OutCheck{}
	downCount := 0

	for i, j := range jobs {
		r := results[i]
		id := j.check.ID
		status := "down"
		if r.ok {
			status = "up"
		}
		if !r.ok {
			downCount++
		}

		rawPath := filepath.Join(*dataDir, "raw", id+".json")
		hourlyPath := filepath.Join(*dataDir, "hourly", id+".json")
		dailyPath := filepath.Join(*dataDir, "daily", id+".json")

		var raw []sample
		readJSON(rawPath, &raw)
		var okInt int64
		if r.ok {
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
		hourly = bumpBucket(hourly, now.Truncate(time.Hour).Unix(), r.ok, r.ms, hourlyKeep)
		daily = bumpBucket(daily, now.Format("2006-01-02"), r.ok, r.ms, dailyKeep)

		_ = writeJSON(rawPath, raw)
		_ = writeJSON(hourlyPath, hourly)
		_ = writeJSON(dailyPath, daily)

		prev := state.Services[id]
		since := now.Format(time.RFC3339)
		if prev != nil && prev.Status == status {
			since = prev.Since
		} else if prev != nil {
			full := j.project.Title + " · " + j.check.Name
			if status == "down" {
				incidents = append([]Incident{{
					Service: id, Name: full, Start: since,
					Reason: firstNonEmpty(r.errText, "недоступен"),
				}}, incidents...)
			} else {
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

		days := make([]OutDay, 0, daysOnPage)
		from := 0
		if len(daily) > daysOnPage {
			from = len(daily) - daysOnPage
		}
		for _, b := range daily[from:] {
			days = append(days, OutDay{
				D: fmt.Sprint(b[0]), Up: toInt(b[1]), Total: toInt(b[2]), AvgMs: toInt(b[3]),
			})
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
			ID: id, Name: j.check.Name, Note: j.check.Note, URL: j.check.URL,
			Status: status, Since: since, Ms: r.ms, Code: r.code, Error: r.errText,
			CertDays: r.certDays,
			Uptime: map[string]any{
				"d1":  uptimeFromRaw(raw, 24),
				"d7":  uptimeFromBuckets(hourly, 24*7),
				"d90": uptimeFromBuckets(daily, 90),
			},
			Days: days, Spark: spark,
		}
	}

	out := Summary{Updated: now.Format(time.RFC3339)}
	for _, p := range cfg.Projects {
		op := OutProject{
			ID: p.ID, Title: p.Title, Subtitle: p.Subtitle, URL: p.URL, Accent: p.Accent,
		}
		for _, c := range p.Checks {
			oc := byCheckID[c.ID]
			op.Checks = append(op.Checks, oc)
			op.Total++
			if oc.Status == "up" {
				op.Up++
			}
		}
		for _, u := range p.Units {
			ou := unitState(u.Name)
			ou.Title = u.Title
			op.Units = append(op.Units, ou)
		}
		for _, b := range p.Builds {
			op.Builds = append(op.Builds, buildInfo(b, client))
		}
		switch {
		case op.Up == op.Total:
			op.Status = "up"
		case op.Up == 0:
			op.Status = "down"
		default:
			op.Status = "degraded"
		}
		out.Projects = append(out.Projects, op)
	}

	switch {
	case downCount == 0:
		out.Overall = "operational"
	case downCount == len(jobs):
		out.Overall = "down"
	default:
		out.Overall = "degraded"
	}

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
	if err := writeJSON(filepath.Join(*dataDir, "summary.json"), out); err != nil {
		log.Fatalf("не удалось записать summary.json: %v", err)
	}

	log.Printf("проверок: %d, недоступно: %d, состояние: %s", len(jobs), downCount, out.Overall)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
