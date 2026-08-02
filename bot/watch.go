package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Kind string

const (
	KindDown      Kind = "down"
	KindStillDown Kind = "still"
	KindUp        Kind = "up"
	KindRelease   Kind = "release"
)

// Event — то, о чём стоит написать владельцу.
type Event struct {
	Key  string
	Kind Kind
	// Title и URL идут вместе: уведомление без ссылки заставляет искать адрес
	// руками ровно тогда, когда некогда.
	Title    string
	URL      string
	Reason   string
	Duration time.Duration
	Version  string
	Previous string
	At       time.Time
}

// Item — что бот уже знает про одну наблюдаемую сущность.
//
// Notified хранится отдельно от Since: Since отвечает на вопрос «сколько
// лежит», Notified — «когда я последний раз про это писал». Слить их в одно
// поле нельзя, иначе напоминание о длительном простое либо не придёт, либо
// начнёт врать о длительности.
type Item struct {
	Down     bool   `json:"down"`
	Since    string `json:"since"`
	Notified string `json:"notified"`
}

// State переживает перезапуск бота: иначе рестарт службы (или выкатка новой
// версии) означал бы повторное уведомление обо всём, что лежит, и молчание
// про версии, которые сменились, пока бота не было.
type State struct {
	// Offset — с какого update_id продолжать длинный опрос Telegram.
	Offset   int64             `json:"offset"`
	Items    map[string]*Item  `json:"items"`
	Versions map[string]string `json:"versions"`
}

func newState() *State {
	return &State{Items: map[string]*Item{}, Versions: map[string]string{}}
}

func loadState(path string) *State {
	st := newState()
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	if json.Unmarshal(b, st) != nil {
		return newState()
	}
	if st.Items == nil {
		st.Items = map[string]*Item{}
	}
	if st.Versions == nil {
		st.Versions = map[string]string{}
	}
	return st
}

// saveState пишет через временный файл: бот может быть убит в любой момент,
// а недочитанный state.json означал бы шквал повторных уведомлений.
func saveState(path string, st *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// target — одна наблюдаемая сущность в плоском виде: проверка, юнит или
// свежесть самих данных. Приводим их к общему виду, чтобы логика дедупликации
// была одна на всех и не расползалась по трём похожим веткам.
type target struct {
	key    string
	title  string
	url    string
	reason string
	down   bool
	since  time.Time
}

func targets(s *Summary, now time.Time, stale time.Duration) []target {
	var out []target
	for _, p := range s.Projects {
		for _, c := range p.Checks {
			since, ok := parseTime(c.Since)
			if !ok {
				since = now
			}
			// Падением считаем ТОЛЬКО "down". Появившееся состояние "slow"
			// означает, что сервис отвечает, просто дольше порога, — будить
			// этим владельца и открывать инцидент нельзя. Иначе любая
			// сетевая просадка ночью читалась бы как авария.
			title := p.Title + " · " + c.Name
			if !c.Critical {
				title += " (второстепенная)"
			}
			out = append(out, target{
				key:    "check:" + c.ID,
				title:  title,
				url:    firstNonEmptyStr(c.URL, p.URL),
				reason: firstNonEmptyStr(c.Impact, c.Error),
				down:   c.Status == "down",
				since:  since,
			})
		}
		for _, u := range p.Units {
			since, ok := parseTime(u.Since)
			if !ok {
				since = now
			}
			out = append(out, target{
				key:    "unit:" + u.Name,
				title:  p.Title + " · " + u.Title,
				url:    p.URL,
				reason: "состояние юнита: " + u.State,
				down:   !u.Active,
				since:  since,
			})
		}
	}

	// Свежесть данных — такая же наблюдаемая сущность.
	//
	// Если агент перестал ходить по сервисам, все проверки в файле остаются
	// зелёными навсегда. Молчащий бот в этот момент выглядит как «всё
	// хорошо», хотя на деле никто уже не смотрит.
	updated, ok := parseTime(s.Updated)
	if !ok {
		updated = now
	}
	age := now.Sub(updated)
	out = append(out, target{
		key:    "data",
		title:  "Данные статуса",
		url:    statusPageURL,
		reason: fmt.Sprintf("агент не обновлял summary.json %s", humanDur(age)),
		down:   age >= stale,
		since:  updated,
	})
	return out
}

// Apply сверяет свежий summary.json с тем, что бот уже знает, и возвращает
// события, о которых стоит написать. Состояние меняется прямо здесь: событие
// считается доставленным, как только оно попало в список.
//
// Правила:
//   - первое наблюдение молчит, если всё хорошо, но кричит, если уже лежит:
//     после перезапуска бота владелец должен узнать о текущем сбое, но не
//     получить простыню «все сервисы живы»;
//   - повторное сообщение об одном и том же простое приходит не чаще
//     remind — иначе при часовом падении придёт сотня одинаковых строк;
//   - версия при первом наблюдении запоминается молча: бот не должен
//     объявлять релизом то, что просто увидел впервые.
func (st *State) Apply(s *Summary, now time.Time, remind, stale time.Duration) []Event {
	var events []Event

	for _, t := range targets(s, now, stale) {
		prev, seen := st.Items[t.key]
		switch {
		case !seen:
			item := &Item{Down: t.down, Since: t.since.UTC().Format(time.RFC3339)}
			if t.down {
				item.Notified = now.UTC().Format(time.RFC3339)
				events = append(events, Event{
					Key: t.key, Kind: KindDown, Title: t.title, Reason: t.reason, At: now,
				})
			}
			st.Items[t.key] = item

		case prev.Down != t.down:
			since, ok := parseTime(prev.Since)
			if !ok {
				since = t.since
			}
			kind := KindUp
			if t.down {
				kind = KindDown
			}
			events = append(events, Event{
				Key: t.key, Kind: kind, Title: t.title, Reason: t.reason,
				Duration: now.Sub(since), At: now,
			})
			prev.Down = t.down
			prev.Since = t.since.UTC().Format(time.RFC3339)
			prev.Notified = now.UTC().Format(time.RFC3339)

		case t.down:
			last, ok := parseTime(prev.Notified)
			if !ok || now.Sub(last) >= remind {
				since, ok := parseTime(prev.Since)
				if !ok {
					since = t.since
				}
				events = append(events, Event{
					Key: t.key, Kind: KindStillDown, Title: t.title, Reason: t.reason,
					Duration: now.Sub(since), At: now,
				})
				prev.Notified = now.UTC().Format(time.RFC3339)
			}
		}
	}

	for _, p := range s.Projects {
		for _, b := range p.Builds {
			if b.Version == "" {
				continue
			}
			key := p.ID + "::" + b.Title
			prev, seen := st.Versions[key]
			if seen && prev != b.Version {
				at, ok := parseTime(b.At)
				if !ok {
					at = now
				}
				events = append(events, Event{
					Key: key, Kind: KindRelease, Title: p.Title + " · " + b.Title,
					Version: b.Version, Previous: prev, At: at,
				})
			}
			st.Versions[key] = b.Version
		}
	}

	return events
}

// firstNonEmptyStr — первое непустое из перечисленного.
//
// В уведомлении полезнее человеческая формулировка последствия («матчи не
// идут»), чем текст ошибки; но если impact в конфиге не заполнен, показать
// нужно хоть что-то.
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
