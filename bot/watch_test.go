package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

// summaryAt собирает минимальный summary.json: одна проверка, один юнит,
// одна версия. Этого хватает, чтобы проверить все ветки дедупликации.
func summaryAt(updated time.Time, status string, since time.Time, unitActive bool, version string) *Summary {
	errText := ""
	if status != "up" {
		errText = "HTTP 502"
	}
	return &Summary{
		Updated: updated.Format(time.RFC3339),
		Overall: status,
		Projects: []Project{{
			ID:    "snakes",
			Title: "Snakes",
			Checks: []Check{{
				ID: "snakes", Name: "Клиент", Status: status,
				Since: since.Format(time.RFC3339), Error: errText,
			}},
			Units: []Unit{{
				Name: "snakes.service", Title: "Игровой сервер",
				Active: unitActive, State: unitStateText(unitActive),
				Since: since.Format(time.RFC3339),
			}},
			Builds: []Build{{
				Title: "Сервер и клиент", Version: version,
				At: since.Format(time.RFC3339),
			}},
		}},
	}
}

func unitStateText(active bool) string {
	if active {
		return "active / running"
	}
	return "failed"
}

func kinds(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, string(e.Kind)+":"+e.Key)
	}
	return out
}

func TestFirstRunStaysSilentWhenHealthy(t *testing.T) {
	st := newState()
	events := st.Apply(summaryAt(base, "up", base.Add(-time.Hour), true, "v1"), base, 15*time.Minute, 5*time.Minute)
	if len(events) != 0 {
		t.Fatalf("после старта не должно быть уведомлений о живых сервисах, пришло: %v", kinds(events))
	}
	// Версия запомнена молча — иначе перезапуск бота выглядел бы как релиз.
	if st.Versions["snakes::Сервер и клиент"] != "v1" {
		t.Error("версия не запомнена при первом наблюдении")
	}
}

func TestFirstRunReportsExistingOutage(t *testing.T) {
	st := newState()
	events := st.Apply(summaryAt(base, "down", base.Add(-time.Hour), true, "v1"), base, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Kind != KindDown {
		t.Fatalf("после перезапуска бот обязан сообщить о текущем сбое, пришло: %v", kinds(events))
	}
	if events[0].Reason != "HTTP 502" {
		t.Errorf("причина потеряна: %q", events[0].Reason)
	}
}

func TestDownThenUp(t *testing.T) {
	st := newState()
	st.Apply(summaryAt(base, "up", base.Add(-time.Hour), true, "v1"), base, 15*time.Minute, 5*time.Minute)

	fell := base.Add(time.Minute)
	events := st.Apply(summaryAt(fell, "down", fell, true, "v1"), fell, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Kind != KindDown {
		t.Fatalf("падение не замечено: %v", kinds(events))
	}

	recovered := fell.Add(10 * time.Minute)
	events = st.Apply(summaryAt(recovered, "up", recovered, true, "v1"), recovered, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Kind != KindUp {
		t.Fatalf("восстановление не замечено: %v", kinds(events))
	}
	if events[0].Duration != 10*time.Minute {
		t.Errorf("простой посчитан как %s, ожидали 10m", events[0].Duration)
	}
}

func TestSameStateNotRepeated(t *testing.T) {
	st := newState()
	fell := base
	st.Apply(summaryAt(fell, "down", fell, true, "v1"), fell, 15*time.Minute, 5*time.Minute)

	// Через минуту ничего не изменилось — молчим.
	quiet := fell.Add(time.Minute)
	if events := st.Apply(summaryAt(quiet, "down", fell, true, "v1"), quiet, 15*time.Minute, 5*time.Minute); len(events) != 0 {
		t.Fatalf("повтор того же состояния не должен уведомлять, пришло: %v", kinds(events))
	}
}

func TestReminderOnLongOutage(t *testing.T) {
	st := newState()
	fell := base
	st.Apply(summaryAt(fell, "down", fell, true, "v1"), fell, 15*time.Minute, 5*time.Minute)

	// Ровно через интервал напоминания — одно сообщение.
	later := fell.Add(15 * time.Minute)
	events := st.Apply(summaryAt(later, "down", fell, true, "v1"), later, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Kind != KindStillDown {
		t.Fatalf("напоминание не пришло: %v", kinds(events))
	}
	if events[0].Duration != 15*time.Minute {
		t.Errorf("в напоминании длительность %s, ожидали 15m", events[0].Duration)
	}

	// Сразу после напоминания — снова тишина.
	soon := later.Add(time.Minute)
	if events := st.Apply(summaryAt(soon, "down", fell, true, "v1"), soon, 15*time.Minute, 5*time.Minute); len(events) != 0 {
		t.Fatalf("напоминание повторилось раньше срока: %v", kinds(events))
	}

	// Ещё через интервал — следующее напоминание.
	next := later.Add(15 * time.Minute)
	events = st.Apply(summaryAt(next, "down", fell, true, "v1"), next, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Kind != KindStillDown {
		t.Fatalf("второе напоминание не пришло: %v", kinds(events))
	}
	if events[0].Duration != 30*time.Minute {
		t.Errorf("длительность %s, ожидали 30m от начала простоя", events[0].Duration)
	}
}

func TestReminderIntervalConfigurable(t *testing.T) {
	st := newState()
	st.Apply(summaryAt(base, "down", base, true, "v1"), base, time.Minute, 5*time.Minute)

	later := base.Add(2 * time.Minute)
	events := st.Apply(summaryAt(later, "down", base, true, "v1"), later, time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Kind != KindStillDown {
		t.Fatalf("с минутным интервалом напоминание должно прийти: %v", kinds(events))
	}
}

func TestVersionChange(t *testing.T) {
	st := newState()
	st.Apply(summaryAt(base, "up", base, true, "20260802-120000-abc1234"), base, 15*time.Minute, 5*time.Minute)

	next := base.Add(time.Minute)
	events := st.Apply(summaryAt(next, "up", next, true, "20260802-120500-def5678"), next, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Kind != KindRelease {
		t.Fatalf("смена версии не замечена: %v", kinds(events))
	}
	if events[0].Version != "20260802-120500-def5678" || events[0].Previous != "20260802-120000-abc1234" {
		t.Errorf("в событии не те версии: %+v", events[0])
	}
	if !events[0].At.Equal(next) {
		t.Errorf("время сборки %s, ожидали %s", events[0].At, next)
	}

	// Та же версия на следующем обходе — не событие.
	after := next.Add(time.Minute)
	if events := st.Apply(summaryAt(after, "up", next, true, "20260802-120500-def5678"), after, 15*time.Minute, 5*time.Minute); len(events) != 0 {
		t.Fatalf("неизменная версия не должна уведомлять: %v", kinds(events))
	}
}

func TestEmptyVersionIsNotRelease(t *testing.T) {
	st := newState()
	st.Apply(summaryAt(base, "up", base, true, "v1"), base, 15*time.Minute, 5*time.Minute)

	// Сервис не ответил на /version.json — версия пустая. Это сбой сбора
	// данных, а не откат: сообщать о нём релизом нельзя.
	next := base.Add(time.Minute)
	events := st.Apply(summaryAt(next, "up", next, true, ""), next, 15*time.Minute, 5*time.Minute)
	if len(events) != 0 {
		t.Fatalf("пустая версия не должна порождать событие: %v", kinds(events))
	}
	if st.Versions["snakes::Сервер и клиент"] != "v1" {
		t.Error("известная версия затёрта пустой")
	}
}

func TestUnitFailure(t *testing.T) {
	st := newState()
	st.Apply(summaryAt(base, "up", base.Add(-time.Hour), true, "v1"), base, 15*time.Minute, 5*time.Minute)

	next := base.Add(time.Minute)
	events := st.Apply(summaryAt(next, "up", next, false, "v1"), next, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Key != "unit:snakes.service" || events[0].Kind != KindDown {
		t.Fatalf("падение юнита не замечено: %v", kinds(events))
	}
}

func TestStaleAgentData(t *testing.T) {
	st := newState()
	st.Apply(summaryAt(base, "up", base, true, "v1"), base, 15*time.Minute, 5*time.Minute)

	// Агент замолчал: файл прежний, а время идёт. Все проверки в нём
	// по-прежнему зелёные — именно поэтому нужен отдельный сигнал.
	silent := base.Add(6 * time.Minute)
	events := st.Apply(summaryAt(base, "up", base, true, "v1"), silent, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Key != "data" || events[0].Kind != KindDown {
		t.Fatalf("молчание агента не замечено: %v", kinds(events))
	}

	// Агент ожил — приходит восстановление.
	alive := silent.Add(time.Minute)
	events = st.Apply(summaryAt(alive, "up", base, true, "v1"), alive, 15*time.Minute, 5*time.Minute)
	if len(events) != 1 || events[0].Key != "data" || events[0].Kind != KindUp {
		t.Fatalf("возвращение агента не замечено: %v", kinds(events))
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	st := newState()
	st.Offset = 42
	st.Apply(summaryAt(base, "down", base, true, "v1"), base, 15*time.Minute, 5*time.Minute)
	if err := saveState(path, st); err != nil {
		t.Fatalf("состояние не сохранено: %v", err)
	}

	// После перезапуска бот не должен повторять уведомление о том же сбое.
	restored := loadState(path)
	if restored.Offset != 42 {
		t.Errorf("offset потерян: %d", restored.Offset)
	}
	next := base.Add(time.Minute)
	if events := restored.Apply(summaryAt(next, "down", base, true, "v1"), next, 15*time.Minute, 5*time.Minute); len(events) != 0 {
		t.Fatalf("после перезапуска пришёл дубль: %v", kinds(events))
	}
}

func TestBrokenStateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{это не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := loadState(path)
	if st.Items == nil || st.Versions == nil {
		t.Fatal("из битого файла должно получиться пустое рабочее состояние")
	}
}
