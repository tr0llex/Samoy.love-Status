package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Структуры повторяют формат summary.json, который пишет агент (agent/main.go).
//
// Бот сознательно не делает собственных проверок: два независимых обхода
// расходились бы во времени, и на странице и в телеграме было бы разное
// состояние одного и того же сервиса. Единственный источник правды — файл
// агента, бот его только читает и пересказывает.
//
// Описаны не все поля: истории по дням и спарклайны нужны странице, а в
// сообщении их всё равно не показать.

type Summary struct {
	Updated   string     `json:"updated"`
	Overall   string     `json:"overall"`
	Projects  []Project  `json:"projects"`
	Incidents []Incident `json:"incidents"`
}

type Project struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	URL    string  `json:"url"`
	Status string  `json:"status"`
	Up     int     `json:"up"`
	Total  int     `json:"total"`
	Checks []Check `json:"checks"`
	Units  []Unit  `json:"units"`
	Builds []Build `json:"builds"`
}

type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"`
	Since  string `json:"since"`
	Ms     int64  `json:"ms"`
	Code   int    `json:"code"`
	Error  string `json:"error"`
	// Уровень доступности за сутки/неделю/90 дней. Значение может быть null,
	// пока замеров нет, поэтому указатель, а не число.
	Uptime   map[string]*float64 `json:"uptime"`
	CertDays *int                `json:"certDays"`
}

type Unit struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
	State  string `json:"state"`
	Since  string `json:"since"`
}

type Build struct {
	Title   string `json:"title"`
	Version string `json:"version"`
	At      string `json:"at"`
}

type Incident struct {
	Service    string `json:"service"`
	Name       string `json:"name"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Reason     string `json:"reason"`
	DurationMs int64  `json:"durationMs"`
}

func loadSummary(path string) (*Summary, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// parseTime разбирает время в формате, которым пишет агент (RFC3339).
// Пустая или битая строка — не ошибка: у остановленного юнита времени
// запуска просто нет, и сообщение должно собраться без него.
func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
