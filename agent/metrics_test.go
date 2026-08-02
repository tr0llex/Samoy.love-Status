package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleSummary() Summary {
	days := 12
	return Summary{
		Updated: "2026-08-02T10:00:00Z",
		Overall: "degraded",
		Projects: []OutProject{{
			ID: "samoy", Title: "samoy.love", Up: 1, Total: 2,
			Checks: []OutCheck{
				{
					ID: "samoy", Name: "Главная", Status: "up", Ms: 120, Code: 200,
					CertDays: &days,
					Uptime:   map[string]any{"d1": float64(100), "d7": float64(99.5), "d90": nil},
				},
				{
					ID: "metro", Name: `Метро "кошачье"`, Status: "down", Ms: 0, Code: 0,
					Uptime: map[string]any{"d1": float64(50)},
				},
			},
			Units: []OutUnit{{Name: "nginx.service", Active: true}, {Name: "snakes.service", Active: false}},
		}},
	}
}

func TestBuildMetricsFormat(t *testing.T) {
	out := buildMetrics(sampleSummary(), nil, 1500*time.Millisecond, time.Unix(1754128800, 0))

	for _, want := range []string{
		"# TYPE status_check_up gauge",
		`status_check_up{project="samoy",check="samoy",name="Главная"} 1`,
		`status_check_up{project="samoy",check="metro",name="Метро \"кошачье\""} 0`,
		`status_check_response_seconds{project="samoy",check="samoy",name="Главная"} 0.12`,
		`status_check_code{project="samoy",check="metro",name="Метро \"кошачье\""} 0`,
		`status_cert_days_left{project="samoy",check="samoy"} 12`,
		`status_check_uptime_ratio{project="samoy",check="samoy",window="d1"} 1`,
		`status_check_uptime_ratio{project="samoy",check="samoy",window="d7"} 0.995`,
		`status_unit_active{project="samoy",unit="nginx.service"} 1`,
		`status_unit_active{project="samoy",unit="snakes.service"} 0`,
		"status_checks_total 2",
		"status_checks_up 1",
		"status_agent_run_timestamp_seconds 1754128800",
		"status_agent_run_duration_seconds 1.5",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет строки %q в:\n%s", want, out)
		}
	}

	// Окно без данных не должно превращаться в ноль: «нет измерений» и
	// «сервис лежал все 90 дней» — разные вещи.
	if strings.Contains(out, `window="d90"`) {
		t.Fatalf("пустое окно попало в метрики:\n%s", out)
	}
}

// TestBuildMetricsHelpOncePerFamily: node_exporter отбрасывает файл целиком,
// если HELP повторяется — самая дорогая опечатка в этом формате.
func TestBuildMetricsHelpOncePerFamily(t *testing.T) {
	out := buildMetrics(sampleSummary(), nil, time.Second, time.Now())
	if n := strings.Count(out, "# HELP status_check_up "); n != 1 {
		t.Fatalf("HELP для status_check_up встречается %d раз(а)", n)
	}
	if n := strings.Count(out, "# TYPE status_unit_active "); n != 1 {
		t.Fatalf("TYPE для status_unit_active встречается %d раз(а)", n)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, " ") {
			t.Fatalf("строка без значения: %q", line)
		}
	}
}

func TestBuildMetricsIncidents(t *testing.T) {
	incidents := []Incident{
		{Service: "metro", Start: "2026-08-02T09:00:00Z"}, // открытый
		{Service: "samoy", Start: "2026-08-01T09:00:00Z", End: "2026-08-01T09:05:00Z", DurationMs: 300000},
		{Service: "samoy", Start: "2026-07-01T09:00:00Z", End: "2026-07-01T09:01:00Z", DurationMs: 60000},
	}
	out := buildMetrics(sampleSummary(), incidents, time.Second, time.Now())

	for _, want := range []string{
		"status_incidents_open 1",
		"status_incidents_recorded 3",
		// Берётся ПЕРВЫЙ закрытый: журнал отсортирован от свежих к старым.
		`status_incident_last_duration_seconds{service="samoy"} 300`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет строки %q в:\n%s", want, out)
		}
	}
	if strings.Contains(out, `status_incident_last_duration_seconds{service="metro"}`) {
		t.Fatal("открытый инцидент попал в длительность завершённых")
	}
}

func TestBuildMetricsIsDeterministic(t *testing.T) {
	a := buildMetrics(sampleSummary(), nil, time.Second, time.Unix(1, 0))
	b := buildMetrics(sampleSummary(), nil, time.Second, time.Unix(1, 0))
	if a != b {
		t.Fatal("одинаковый вход дал разный файл")
	}
}

func TestWriteMetricsIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "status-agent.prom")
	if err := writeMetrics(path, "status_x 1\n"); err != nil {
		t.Fatalf("writeMetrics: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	if string(b) != "status_x 1\n" {
		t.Fatalf("содержимое: %q", b)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("временный файл остался рядом с готовым")
	}

	// Пустой путь — штатное «не писать»: агент должен уметь работать там, где
	// node_exporter не установлен.
	if err := writeMetrics("", "x"); err != nil {
		t.Fatalf("пустой путь вернул ошибку: %v", err)
	}
}

func TestМетрикиНеСчитаютМедленнуюПроверкуУпавшей(t *testing.T) {
	// В Prometheus по status_check_up строят алерты. Если медленный ответ
	// попадёт туда нулём, дежурного разбудит деградация, а не авария.
	out := Summary{
		Updated: "2026-08-02T12:00:00Z",
		Overall: "degraded",
		Projects: []OutProject{{
			ID: "p", Title: "P", Total: 1, Up: 1, Slow: 1, AuxDown: 1,
			Checks: []OutCheck{
				{ID: "slow", Name: "Медленная", Status: statusSlow, Critical: true},
				{ID: "down", Name: "Упавшая", Status: statusDown, Critical: false},
			},
		}},
	}
	got := buildMetrics(out, nil, time.Second, time.Now().UTC())

	for _, want := range []string{
		`status_check_up{project="p",check="slow",name="Медленная"} 1`,
		`status_check_slow{project="p",check="slow",name="Медленная"} 1`,
		`status_check_up{project="p",check="down",name="Упавшая"} 0`,
		`status_check_critical{project="p",check="down",name="Упавшая"} 0`,
		"status_checks_aux_down 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в метриках нет %q:\n%s", want, got)
		}
	}
}
