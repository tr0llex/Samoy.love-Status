package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Тесты приёмника проверяют не «разбирается ли JSON», а то, ради чего он
// написан: событие не теряется, не показывается дважды и не превращает чужой
// файл в исполнение чужой воли.

const inboxTestNow = "2026-08-05T10:30:00Z"

func inboxNow(t *testing.T) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, inboxTestNow)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// inboxState — состояние с уже стоящим курсором. Пустой курсор означает «новая
// установка» и обрабатывается отдельно (см. TestInboxPrimeOnEmptyState),
// поэтому в остальных тестах он выставлен заведомо меньше любого имени.
func inboxState() *State {
	st := newState()
	st.InboxCursor = "0"
	st.OutboxCursor = "0"
	return st
}

// event собирает событие с полями по умолчанию; отдельные поля правит caller.
func inboxEvent(ms int64, app, kind string) map[string]any {
	e := map[string]any{
		"v":        1,
		"id":       strings.Repeat("a", 63) + "1",
		"kind":     kind,
		"app":      app,
		"at":       inboxTestNow,
		"source":   "ci",
		"group":    strings.Repeat("b", 64),
		"groupSeq": 1,
	}
	switch kind {
	case evSuccess, evPublished, evRollback:
		e["version"] = "release-20260805-130115-1a2b3c4"
	case evFailure:
		e["stage"] = "gates"
	case evRolledBack:
		e["version"] = "release-20260805-130115-1a2b3c4"
		e["stage"] = "health"
		e["reason"] = "health_failed"
	}
	// id обязан быть разным у разных событий, иначе сработает дедупликация.
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s", ms, app, kind)))
	e["id"] = hex.EncodeToString(h[:])
	return e
}

func inboxEventName(ms int64, app, kind string) string {
	return fmt.Sprintf("%013d-%s-%s.json", ms, app, kind)
}

// write кладёт событие в каталог под именем по контракту и возвращает имя.
func inboxWrite(t *testing.T, dir string, ms int64, app, kind string, e map[string]any) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	name := inboxEventName(ms, app, kind)
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o640); err != nil {
		t.Fatal(err)
	}
	return name
}

func inboxWriteDefault(t *testing.T, dir string, ms int64, app, kind string) string {
	t.Helper()
	return inboxWrite(t, dir, ms, app, kind, inboxEvent(ms, app, kind))
}

// Три выкатки в одну секунду — сценарий приёмки: наблюдение за версиями давало
// на них одно сообщение, приёмник обязан отдать три и в правильном порядке.
func TestInboxThreeInOneSecond(t *testing.T) {
	dir := t.TempDir()
	inboxWriteDefault(t, dir, 1785924102123, "snakes", evSuccess)
	inboxWriteDefault(t, dir, 1785924102456, "metro", evSuccess)
	inboxWriteDefault(t, dir, 1785924102789, "chillhub-site", evSuccess)

	st := inboxState()
	got := newInbox(dir).Poll(st, inboxNow(t))
	if len(got) != 3 {
		t.Fatalf("получено %d событий, ждали 3", len(got))
	}
	want := []string{"snakes", "metro", "chillhub-site"}
	for i, app := range want {
		if got[i].App != app {
			t.Errorf("событие %d: app=%s, ждали %s", i, got[i].App, app)
		}
	}
	if st.InboxCursor != inboxEventName(1785924102789, "chillhub-site", evSuccess) {
		t.Errorf("курсор %q", st.InboxCursor)
	}
}

// Порядок держится на имени файла, а не на порядке появления в каталоге и не
// на поле at: сортировка обязана быть лексикографической.
func TestInboxOrder(t *testing.T) {
	dir := t.TempDir()
	ms := []int64{1785924199999, 1785924100000, 1785924102123}
	for _, m := range ms {
		inboxWriteDefault(t, dir, m, "snakes", evSuccess)
	}
	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != 3 {
		t.Fatalf("получено %d событий", len(got))
	}
	prev := ""
	for _, e := range got {
		if e.File <= prev {
			t.Fatalf("порядок нарушен: %s после %s", e.File, prev)
		}
		prev = e.File
	}
}

// Транспорт повторяет доставку, и один id приезжает разными файлами. Курсор
// тут не помогает никак — отличить повтор можно только по id.
func TestInboxDuplicateID(t *testing.T) {
	dir := t.TempDir()
	e := inboxEvent(1785924102123, "snakes", evSuccess)
	inboxWrite(t, dir, 1785924102123, "snakes", evSuccess, e)
	inboxWrite(t, dir, 1785924102999, "snakes", evSuccess, e)

	in := newInbox(dir)
	st := inboxState()
	got := in.Poll(st, inboxNow(t))
	if len(got) != 1 {
		t.Fatalf("получено %d событий, ждали 1", len(got))
	}

	// После подтверждения Telegram id уезжает в долгую память, и тот же файл,
	// перечитанный после перезапуска, повторного сообщения не даёт.
	in.Confirmed(st, got[0].ID, inboxNow(t))
	st.InboxCursor = "0"
	if again := in.Poll(st, inboxNow(t)); len(again) != 0 {
		t.Fatalf("повтор после подтверждения: %d событий", len(again))
	}
}

// Испорченный файл не имеет права остановить журнал: иначе одна опечатка
// глушит уведомления навсегда.
func TestInboxBrokenFileDoesNotStopJournal(t *testing.T) {
	dir := t.TempDir()
	inboxWriteDefault(t, dir, 1785924102100, "snakes", evSuccess)
	broken := inboxEventName(1785924102200, "metro", evSuccess)
	if err := os.WriteFile(filepath.Join(dir, broken), []byte(`{"v":1,"kind":"succ`), 0o640); err != nil {
		t.Fatal(err)
	}
	inboxWriteDefault(t, dir, 1785924102300, "chillhub-site", evSuccess)

	st := inboxState()
	got := newInbox(dir).Poll(st, inboxNow(t))
	if len(got) != 2 {
		t.Fatalf("получено %d событий, ждали 2", len(got))
	}
	if st.InboxCursor != inboxEventName(1785924102300, "chillhub-site", evSuccess) {
		t.Errorf("курсор застрял на %q", st.InboxCursor)
	}
}

// Симлинк в каталоге событий не должен превращаться в чтение чужого файла
// процессом, который держит токен Telegram.
func TestInboxSymlinkIgnored(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.json")
	b, _ := json.Marshal(inboxEvent(1785924102123, "snakes", evSuccess))
	if err := os.WriteFile(secret, b, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, inboxEventName(1785924102123, "snakes", evSuccess))
	if err := os.Symlink(secret, link); err != nil {
		// На Windows создание ссылки требует прав, которых у обычного
		// пользователя нет. Молча признавать тест пройденным нельзя.
		t.Skipf("симлинк не создан: %v", err)
	}
	inboxWriteDefault(t, dir, 1785924102500, "metro", evSuccess)

	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != 1 || got[0].App != "metro" {
		t.Fatalf("симлинк прочитан: %+v", got)
	}
}

// Предел размера — ДО чтения. Большой файл не открывается вовсе.
func TestInboxTooBig(t *testing.T) {
	dir := t.TempDir()
	e := inboxEvent(1785924102123, "snakes", evSuccess)
	e["previous"] = strings.Repeat("x", inboxMaxFileBytes)
	inboxWrite(t, dir, 1785924102123, "snakes", evSuccess, e)
	inboxWriteDefault(t, dir, 1785924102500, "metro", evSuccess)

	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != 1 || got[0].App != "metro" {
		t.Fatalf("прочитан файл сверх предела: %+v", got)
	}
}

// Курсор переживает перезапуск: иначе рестарт службы означал бы повторное
// объявление всего, что лежит в каталоге.
func TestInboxCursorSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	inboxWriteDefault(t, dir, 1785924102123, "snakes", evSuccess)

	statePath := filepath.Join(t.TempDir(), "state.json")
	st := inboxState()
	in := newInbox(dir)
	got := in.Poll(st, inboxNow(t))
	if len(got) != 1 {
		t.Fatalf("получено %d событий", len(got))
	}
	// Отправка подтверждена: оба курсора и память об id ушли на диск.
	in.Confirmed(st, got[0].ID, inboxNow(t))
	st.OutboxCursor = got[0].File
	if err := saveState(statePath, st); err != nil {
		t.Fatal(err)
	}

	// Перезапуск.
	st2 := loadState(statePath)
	if st2.InboxCursor != got[0].File || st2.OutboxCursor != got[0].File {
		t.Fatalf("курсоры не пережили перезапуск: %+v", st2)
	}
	if _, ok := st2.RecentIDs[got[0].ID]; !ok {
		t.Fatal("id не пережил перезапуск")
	}
	in2 := newInbox(dir)
	if again := in2.Poll(st2, inboxNow(t)); len(again) != 0 {
		t.Fatalf("после перезапуска повторно принято %d событий", len(again))
	}

	// Новое событие после перезапуска приходит как обычно.
	inboxWriteDefault(t, dir, 1785924109999, "metro", evSuccess)
	if next := in2.Poll(st2, inboxNow(t)); len(next) != 1 {
		t.Fatalf("новое событие не принято: %d", len(next))
	}
}

// Неотправленное перечитывается с диска: очередь отправки живёт в памяти и
// умирает вместе с ботом.
func TestInboxRewindsToOutboxCursor(t *testing.T) {
	dir := t.TempDir()
	first := inboxWriteDefault(t, dir, 1785924102123, "snakes", evSuccess)
	inboxWriteDefault(t, dir, 1785924102456, "metro", evSuccess)

	st := inboxState()
	st.OutboxCursor = first
	st.InboxCursor = inboxEventName(1785924102456, "metro", evSuccess)

	got := newInbox(dir).Poll(st, inboxNow(t))
	if len(got) != 1 || got[0].App != "metro" {
		t.Fatalf("неотправленное не перечитано: %+v", got)
	}
}

// Состояния нет вовсе: лавина повторов хуже пропуска, поэтому журнал
// признаётся прочитанным, а курсор встаёт на последний файл.
func TestInboxPrimeOnEmptyState(t *testing.T) {
	dir := t.TempDir()
	inboxWriteDefault(t, dir, 1785924102123, "snakes", evSuccess)
	last := inboxWriteDefault(t, dir, 1785924102456, "metro", evSuccess)

	st := newState()
	in := newInbox(dir)
	if got := in.Poll(st, inboxNow(t)); len(got) != 0 {
		t.Fatalf("на первом запуске отдано %d событий", len(got))
	}
	if st.InboxCursor != last {
		t.Fatalf("курсор %q, ждали %q", st.InboxCursor, last)
	}
	inboxWriteDefault(t, dir, 1785924109999, "chillhub-site", evSuccess)
	if got := in.Poll(st, inboxNow(t)); len(got) != 1 {
		t.Fatalf("новое событие не принято: %d", len(got))
	}
}

// Суточная квота на цель: переполнение раздела убивает агента, а с ним всю
// статус-систему.
func TestInboxDailyQuota(t *testing.T) {
	dir := t.TempDir()
	var ms int64 = 1785924100000
	for i := 0; i < inboxMaxPerAppDay+5; i++ {
		inboxWriteDefault(t, dir, ms+int64(i), "snakes", evSuccess)
	}
	// Соседняя цель за те же сутки квотой не задета.
	inboxWriteDefault(t, dir, ms+1000, "metro", evSuccess)

	in := newInbox(dir)
	got := in.Poll(inboxState(), inboxNow(t))
	snakes, metro := 0, 0
	for _, e := range got {
		switch e.App {
		case "snakes":
			snakes++
		case "metro":
			metro++
		}
	}
	if snakes != inboxMaxPerAppDay {
		t.Errorf("принято %d событий цели, предел %d", snakes, inboxMaxPerAppDay)
	}
	if metro != 1 {
		t.Errorf("соседняя цель задета квотой: %d", metro)
	}
}

// Что читатель обязан отвергнуть целиком: без этих полей событие нельзя ни
// показать, ни сопоставить с прогоном.
func TestInboxRejects(t *testing.T) {
	cases := []struct {
		name  string
		patch func(map[string]any)
	}{
		{"чужая версия схемы", func(e map[string]any) { e["v"] = 2 }},
		{"id не sha256", func(e map[string]any) { e["id"] = "deadbeef" }},
		{"group не sha256", func(e map[string]any) { e["group"] = "ZZZ" }},
		{"groupSeq ноль", func(e map[string]any) { e["groupSeq"] = 0 }},
		{"groupSeq за потолком", func(e map[string]any) { e["groupSeq"] = inboxMaxGroupSeq + 1 }},
		{"чужой source", func(e map[string]any) { e["source"] = "curl" }},
		{"app не совпал с именем", func(e map[string]any) { e["app"] = "metro" }},
		{"kind не совпал с именем", func(e map[string]any) { e["kind"] = evFailure }},
		{"at не разобрано", func(e map[string]any) { e["at"] = "вчера" }},
		{"at пустое", func(e map[string]any) { e["at"] = "" }},
		{"id числом", func(e map[string]any) { e["id"] = 42 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			e := inboxEvent(1785924102123, "snakes", evSuccess)
			c.patch(e)
			inboxWrite(t, dir, 1785924102123, "snakes", evSuccess, e)
			if got := newInbox(dir).Poll(inboxState(), inboxNow(t)); len(got) != 0 {
				t.Fatalf("принято негодное событие: %+v", got)
			}
		})
	}
}

// Имена, которых для читателя не существует.
func TestInboxIgnoresForeignNames(t *testing.T) {
	dir := t.TempDir()
	b, _ := json.Marshal(inboxEvent(1785924102123, "snakes", evSuccess))
	names := []string{
		inboxEventName(1785924102123, "snakes", evSuccess) + ".tmp",
		"1785924102123-snakes-success.json.bak",
		"178592410212-snakes-success.json",   // 12 цифр
		"17859241021234-snakes-success.json", // 14 цифр
		"1785924102123-snakes-deleted.json",
		"1785924102123-SNAKES-success.json",
		"summary.json",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	st := inboxState()
	if got := newInbox(dir).Poll(st, inboxNow(t)); len(got) != 0 {
		t.Fatalf("принято %d событий с негодными именами: %+v", len(got), got)
	}
	if st.InboxCursor != "0" {
		t.Errorf("курсор сдвинут негодным именем: %q", st.InboxCursor)
	}
}

// Поля, которые не прошли проверку, исчезают — но событие остаётся: молчание в
// чате читается как «не катились».
func TestInboxDropsBadFieldsKeepsEvent(t *testing.T) {
	dir := t.TempDir()
	e := inboxEvent(1785924102123, "snakes", evFailure)
	e["stage"] = "квантовый переход"
	e["commitURL"] = "javascript:alert(1)"
	e["runURL"] = "https://evil.example/runs/1"
	e["version"] = "версия; rm -rf /"
	inboxWrite(t, dir, 1785924102123, "snakes", evFailure, e)

	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != 1 {
		t.Fatalf("событие потеряно из-за негодных полей")
	}
	ev := got[0]
	if ev.Stage != "" || ev.CommitURL != "" || ev.RunURL != "" || ev.Version != "" {
		t.Fatalf("негодное поле доехало: %+v", ev)
	}
	if ev.Kind != evFailure || ev.App != "snakes" {
		t.Fatalf("событие искажено: %+v", ev)
	}
}

func TestInboxURLAllowlist(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/tr0llex/snakes/commit/abc", "https://github.com/tr0llex/snakes/commit/abc"},
		{"https://GitHub.com/tr0llex/snakes/commit/abc", "https://GitHub.com/tr0llex/snakes/commit/abc"},
		{"http://github.com/tr0llex/snakes/commit/abc", ""},
		{"javascript:alert(1)", ""},
		{"//github.com/x", ""},
		{"https://github.com.evil.tld/x", ""},
		{"https://github.com@evil.tld/x", ""},
		{"https://user:pass@github.com/x", ""},
		{"https://github.com/" + strings.Repeat("x", inboxMaxURLSize), ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := cleanEventURL(c.in); got != c.want {
			t.Errorf("cleanEventURL(%q) = %q, ждали %q", c.in, got, c.want)
		}
	}
}

// Ссылка «на прогон» с машины разработчика может вести только куда-то не туда.
func TestInboxRunURLOnlyForCI(t *testing.T) {
	dir := t.TempDir()
	e := inboxEvent(1785924102123, "metro", evSuccess)
	e["source"] = "local"
	e["runURL"] = "https://github.com/tr0llex/metro-map/actions/runs/1"
	inboxWrite(t, dir, 1785924102123, "metro", evSuccess, e)

	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != 1 || got[0].RunURL != "" {
		t.Fatalf("runURL у локальной выкатки: %+v", got)
	}
}

// CR/LF подделывают строки journald, U+202E показывает в чате не то, что
// написано.
func TestInboxCleanEventText(t *testing.T) {
	// Управляющие символы записаны escape-последовательностями намеренно:
	// невидимый символ в исходнике теста — ровно та ловушка, от которой этот
	// тест и защищает.
	cases := []struct{ in, want string }{
		{"обычная строка", "обычная строка"},
		{"строка\r\nвторая", "строка  вторая"},
		{"с\x00нулём", "снулём"},
		{"пере\u202eворот", "переворот"},
		{"\u200fметка", "метка"},
		{"  поля  ", "поля"},
		{"\ufeffbom", "bom"},
	}
	for _, c := range cases {
		if got := cleanEventText(c.in); got != c.want {
			t.Errorf("cleanEventText(%q) = %q, ждали %q", c.in, got, c.want)
		}
	}
}

func TestInboxChangelogLimits(t *testing.T) {
	dir := t.TempDir()
	var items []string
	for i := 0; i < inboxMaxChangelog+5; i++ {
		items = append(items, fmt.Sprintf("Пункт %d", i))
	}
	items[0] = strings.Repeat("я", 400) // 400 символов, 800 байт
	items[1] = "строка\nс переводом"
	e := inboxEvent(1785924102123, "snakes", evSuccess)
	e["changelog"] = items
	inboxWrite(t, dir, 1785924102123, "snakes", evSuccess, e)

	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != 1 {
		t.Fatal("событие потеряно")
	}
	cl := got[0].Changelog
	if len(cl) != inboxMaxChangelog {
		t.Fatalf("пунктов %d, предел %d", len(cl), inboxMaxChangelog)
	}
	if len([]rune(cl[0])) > inboxMaxItemRunes || len(cl[0]) > inboxMaxItemBytes {
		t.Errorf("пункт не обрезан: %d символов, %d байт", len([]rune(cl[0])), len(cl[0]))
	}
	if strings.ContainsAny(cl[1], "\r\n") {
		t.Errorf("перевод строки доехал: %q", cl[1])
	}
}

// Поля, запрещённые для вида события, снимаются: «выкатка началась» со стадией
// провала врёт читателю о том, что произошло на проде.
func TestInboxKindRules(t *testing.T) {
	dir := t.TempDir()
	started := inboxEvent(1785924102100, "snakes", evStarted)
	started["stage"] = "health"
	started["version"] = "release-20260805-130115-1a2b3c4"
	inboxWrite(t, dir, 1785924102100, "snakes", evStarted, started)

	rollback := inboxEvent(1785924102200, "snakes", evRollback)
	rollback["stage"] = "units"
	rollback["reason"] = "health_failed"
	inboxWrite(t, dir, 1785924102200, "snakes", evRollback, rollback)

	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != 2 {
		t.Fatalf("получено %d событий", len(got))
	}
	if got[0].Stage != "" || got[0].Version != "" {
		t.Errorf("у started остались лишние поля: %+v", got[0])
	}
	if got[1].Stage != "" || got[1].Reason != "manual" {
		t.Errorf("ручной откат разобран как %+v", got[1])
	}
}

// Каталога может не быть до первой выкатки, и мусора в нём может быть сколько
// угодно: ни то, ни другое не роняет бота.
func TestInboxMissingDir(t *testing.T) {
	in := newInbox(filepath.Join(t.TempDir(), "нет-такого"))
	if got := in.Poll(inboxState(), inboxNow(t)); got != nil {
		t.Fatalf("из несуществующего каталога пришло %d событий", len(got))
	}
}

// Память об id живёт ровно столько же, сколько файл события: раньше — повтор в
// чате после перезапуска, дольше — рост состояния без предела.
func TestInboxTrimsRecentIDs(t *testing.T) {
	st := inboxState()
	st.RecentIDs = map[string]string{
		"свежий": inboxNow(t).Add(-time.Hour).UTC().Format(time.RFC3339),
		"старый": inboxNow(t).Add(-inboxRecentTTL - time.Hour).UTC().Format(time.RFC3339),
		"битый":  "позавчера",
	}
	newInbox(t.TempDir()).Poll(st, inboxNow(t))
	if _, ok := st.RecentIDs["свежий"]; !ok {
		t.Error("свежий id забыт")
	}
	if _, ok := st.RecentIDs["старый"]; ok {
		t.Error("старый id не выброшен")
	}
	if _, ok := st.RecentIDs["битый"]; ok {
		t.Error("битая запись не выброшена")
	}
}

// Образцы контракта разбираются как есть, а id и group пересчитываются от
// прообразов: сверять их с константой в своём коде значило бы проверять код
// самим собой.
func TestInboxGoldenSamples(t *testing.T) {
	src := os.Getenv("DK_EVENTS_GOLDEN")
	if src == "" {
		src = filepath.Join("..", "..", "deploy-kit", "docs", "events")
	}
	if _, err := os.Stat(src); err != nil {
		t.Skipf("каталог образцов недоступен (%s): %v", src, err)
	}

	samples := []struct {
		file     string
		name     string
		kind     string
		app      string
		idPre    string
		groupPre string
	}{
		{
			"example-success.json", "1785924102123-snakes-success.json", evSuccess, "snakes",
			"tr0llex/snakes|16542330981|1|snakes|success",
			"tr0llex/snakes|16542330981|1",
		},
		{
			"example-failure.json", "1785923700456-chillhub-site-failure.json", evFailure, "chillhub-site",
			"tr0llex/chillhub|16542331744|2|chillhub-site|failure",
			"tr0llex/chillhub|16542331744|2",
		},
		{
			"example-rolled-back.json", "1785925215123-metro-rolled_back.json", evRolledBack, "metro",
			"1785925215123|samoy-love|31415|metro|rolled_back",
			"1785925180000|samoy-love|31415",
		},
	}

	dir := t.TempDir()
	for _, s := range samples {
		b, err := os.ReadFile(filepath.Join(src, s.file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, s.name), b, 0o640); err != nil {
			t.Fatal(err)
		}
	}

	// Образцы старше срока памяти о них не бывают, но время события в них
	// фиксировано, поэтому «сейчас» берём из контракта, а не из часов машины.
	got := newInbox(dir).Poll(inboxState(), inboxNow(t))
	if len(got) != len(samples) {
		t.Fatalf("разобрано %d образцов из %d", len(got), len(samples))
	}
	byApp := map[string]DeployEvent{}
	for _, e := range got {
		byApp[e.App] = e
	}
	for _, s := range samples {
		e, ok := byApp[s.app]
		if !ok {
			t.Fatalf("образец %s не разобран", s.file)
		}
		if e.Kind != s.kind {
			t.Errorf("%s: kind=%s, ждали %s", s.file, e.Kind, s.kind)
		}
		if want := inboxSum(s.idPre); e.ID != want {
			t.Errorf("%s: id=%s, от прообраза %q выходит %s", s.file, e.ID, s.idPre, want)
		}
		if want := inboxSum(s.groupPre); e.Group != want {
			t.Errorf("%s: group=%s, от прообраза %q выходит %s", s.file, e.Group, s.groupPre, want)
		}
	}

	// Локальный образец: прогона нет, ссылки на прогон быть не может.
	if byApp["metro"].RunURL != "" {
		t.Errorf("у локального образца runURL=%q", byApp["metro"].RunURL)
	}
	if byApp["metro"].Reason != "health_failed" || byApp["metro"].Stage != "health" {
		t.Errorf("автооткат разобран без причины: %+v", byApp["metro"])
	}
	if n := len(byApp["snakes"].Changelog); n != 3 {
		t.Errorf("список изменений образца: %d пунктов", n)
	}
	if byApp["chillhub-site"].Version != "" {
		t.Errorf("у провала на гейтах появилась версия: %q", byApp["chillhub-site"].Version)
	}
}

func inboxSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
