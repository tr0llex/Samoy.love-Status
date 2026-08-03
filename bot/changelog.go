package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Экран изменений: что уехало на прод и когда.
//
// До него список изменений можно было увидеть ровно один раз — в уведомлении о
// релизе, в момент выкатки. Дальше он терялся: сообщение уходит вверх ленты,
// ночную выкатку придерживает тишина, а спросить «что вчера уехало в metro»
// было не у кого. Сам бот ответить не может — git-истории выкаченных проектов
// на сервере нет.
//
// Зато есть журнал агента. releases.json он ведёт давно и по другому поводу
// (agent/main.go, recordRelease): по записи на каждую замеченную версию, ключ —
// «проект::цель». Это и есть история выкаток, к которой нужно было только
// приделать чтение.

// Release — одна запись журнала выкаток (agent/main.go, Release).
//
// Полей перечислено меньше, чем в файле, и это осознанно: файл пишет агент, его
// формат живёт своей жизнью, а лишнее поле не должно ронять разбор. Changelog в
// журнале появился позже Version и At, поэтому экран обязан рисоваться и без
// него — записи, сделанные до этого, не переписывает никто.
type Release struct {
	Version string `json:"version"`
	// At — когда собрали, Seen — когда агент увидел версию живой. Они
	// расходятся, и показываем то, что есть: сборку, а без неё — наблюдение.
	At        string         `json:"at"`
	Seen      string         `json:"seen"`
	Changelog changelogField `json:"changelog"`
}

// releasesPath — журнал выкаток лежит рядом со сводкой.
//
// Своей настройки у него нет намеренно: оба файла пишет один агент в один
// каталог (DATA_DIR), и вторая переменная окружения означала бы возможность
// задать их вразнобой — то есть бота, который рассказывает историю не того
// хозяйства, чей статус показывает.
func releasesPath(summaryPath string) string {
	return filepath.Join(filepath.Dir(summaryPath), "releases.json")
}

// loadReleases читает журнал выкаток.
//
// Отсутствие файла — не ошибка, а обычное состояние молодой установки: агент
// заводит журнал, когда впервые замечает смену версии.
func loadReleases(path string) (map[string][]Release, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string][]Release
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// service — одна выкатываемая цель глазами этой команды.
//
// Собирается из сводки, а не из журнала: сводка задаёт порядок и человеческие
// названия, а журнал знает только ключ «проект::цель». Порядок тот же, что на
// остальных экранах и на странице, — взгляд ищет сервис в одном месте.
type service struct {
	project string // id проекта: его и набирают в аргументе
	name    string // название цели: «Сервер и клиент»
	title   string // как показываем: «Snakes · Сервер и клиент»
	url     string
	history []Release
}

func services(s *Summary, rel map[string][]Release) []service {
	if s == nil {
		return nil
	}
	var out []service
	for _, p := range s.Projects {
		for _, b := range p.Builds {
			out = append(out, service{
				project: p.ID,
				name:    b.Title,
				title:   p.Title + " · " + b.Title,
				url:     firstNonEmptyStr(b.URL, p.URL),
				history: rel[p.ID+"::"+b.Title],
			})
		}
	}
	return out
}

// matchServices — какие цели владелец имел в виду.
//
// Написаний несколько намеренно: команду набирают с телефона, и требовать
// точного «snakes::Сервер и клиент» значит требовать заглядывать в конфиг
// агента. Точное совпадение важнее подстроки — иначе цель «api» отзывалась бы
// на каждый проект, у которого в названии есть «api».
//
// Одно имя может дать несколько целей, и это не ошибка: у проекта их бывает
// несколько (сайт, API, админка), а «/changelog metro» означает «покажи, что
// менялось в metro» — всё, что менялось.
func matchServices(all []service, q string) []service {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return all
	}
	var exact, loose []service
	for _, sv := range all {
		names := []string{
			sv.project, sv.name, sv.title,
			sv.project + "/" + sv.name,
			sv.project + "::" + sv.name,
		}
		matched := false
		for _, n := range names {
			if strings.EqualFold(n, q) {
				matched = true
				break
			}
		}
		if matched {
			exact = append(exact, sv)
			continue
		}
		if strings.Contains(strings.ToLower(sv.title), q) {
			loose = append(loose, sv)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return loose
}

const (
	// changelogReleases — сколько последних выкаток показываем по одной цели.
	// Больше пяти — это уже не «что недавно менялось», а чтение журнала, для
	// которого есть git.
	changelogReleases = 5
	// ПУНКТЫ БОЛЬШЕ НЕ СЧИТАЕМ. Здесь стояли changelogFarmItems = 3 и
	// changelogViewItems = 8, и всё сверх них сворачивалось в «…и ещё N
	// коммитов». Владелец сказал про эту строку прямо: она стоит строки и не
	// сообщает ничего — какой именно коммит уехал, по ней не узнать. Оправдание
	// у обрезки было ровно одно — лимит Telegram, а он снят не сокращением, а
	// разбиением на несколько сообщений (splitMessage). Раз длина больше не
	// повод, то и повода резать список нет.
	//
	// messageBudget — потолок ВСЕГО экрана, в единицах UTF-16.
	//
	// Это защита от вранья в releases.json, а не оформительский предел: файл
	// пишет агент, но читает его бот с диска, и запись «тридцать выкаток по
	// сотне пунктов» обязана стоить памяти ровно столько, сколько мы решили.
	// Раньше здесь стояло 3600 — чуть меньше одного сообщения, — и именно
	// поэтому обзор хозяйства обрывался на середине.
	//
	// 60 000 единиц — это около пятнадцати сообщений Telegram и втрое больше
	// самого длинного честного обзора: девятнадцать целей × (заголовок, строка
	// версии и до восьми пунктов) ≈ 22 000 единиц. Настоящий обзор на порядок
	// короче — медиана релиза три коммита.
	messageBudget = 60000
	// messageReserve — место под строку «…и ещё N целей». Резервируется
	// заранее: дописывать её в уже израсходованный бюджет значило бы врать
	// про предел ровно в том сообщении, которое из-за него и обрезано.
	messageReserve = 120
)

// utf16Len — длина строки в единицах UTF-16.
//
// Считает именно их, потому что их считает Telegram. Для кириллицы это столько
// же, сколько символов, а для эмодзи — вдвое больше: полоска доступности из
// четырнадцати квадратов стоит двадцать восемь единиц, а не четырнадцать.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

// budgeted — сборка сообщения с оглядкой на лимит.
//
// Как только очередной кусок не поместился, приём закрывается совсем:
// пропустить длинный блок и дописать следующий за ним короткий значило бы
// показать историю не по порядку, а этого из текста не видно.
type budgeted struct {
	b       strings.Builder
	used    int
	limit   int
	skipped int
}

func newBudgeted(limit int) *budgeted { return &budgeted{limit: limit} }

func (t *budgeted) add(s string) bool {
	n := utf16Len(s)
	if t.skipped > 0 || t.used+n > t.limit {
		t.skipped++
		return false
	}
	t.b.WriteString(s)
	t.used += n
	return true
}

func (t *budgeted) String() string { return t.b.String() }

// renderChangelog — экран изменений: по всему хозяйству или по одной цели.
//
// Журнал читается на каждый показ, как и сводка: между нажатиями кнопок агент
// успевает записать новую выкатку.
func renderChangelog(s *Summary, summaryPath, query string, now time.Time) (string, *Keyboard) {
	rel, err := loadReleases(releasesPath(summaryPath))
	if err != nil {
		// Битый журнал не повод не отвечать: версии и названия целей известны
		// из сводки, и экран честно скажет, что истории нет.
		log.Printf("журнал выкаток не прочитан: %v", err)
	}
	all := services(s, rel)

	if query == "" {
		return formatFarmChangelog(all, s, now), changelogKeyboard(s, ViewChangelog)
	}
	found := matchServices(all, query)
	if len(found) == 0 {
		return formatUnknownService(query, all), changelogKeyboard(s, ViewChangelog)
	}
	return formatServiceChangelog(found, s, now), changelogKeyboard(s, ViewChangelogOf+strings.ToLower(query))
}

// formatFarmChangelog — последняя выкатка каждой цели.
//
// Вопрос, на который отвечает экран без аргумента, — «что вообще происходило
// в хозяйстве»: по строке про версию и пара пунктов, чтобы понять, куда идти
// смотреть подробно.
func formatFarmChangelog(all []service, s *Summary, now time.Time) string {
	tail := "\n" + freshness(s, now)
	if len(all) == 0 {
		return "<b>Что менялось</b>\n\nНи одной выкатываемой цели в данных агента нет.\n" + tail
	}

	t := newBudgeted(messageBudget - utf16Len(tail) - messageReserve)
	t.add("<b>Что менялось</b>\n")
	for _, sv := range all {
		t.add(farmBlock(sv, now))
	}
	out := t.String()
	if t.skipped > 0 {
		out += fmt.Sprintf("\n<i>…и ещё %d %s — спросите по отдельности: /changelog имя</i>\n",
			t.skipped, pluralTargets(t.skipped))
	}
	return out + tail
}

func farmBlock(sv service, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n<b>%s</b>\n", link(sv.title, sv.url))
	if len(sv.history) == 0 {
		b.WriteString("  выкаток пока не записано\n")
		return b.String()
	}
	r := sv.history[0]
	b.WriteString("  " + releaseLine(r, now) + "\n")
	b.WriteString(itemLines(r, "  "))
	return b.String()
}

// formatServiceChangelog — история одной цели: несколько последних выкаток
// подряд, сверху свежая.
//
// В бюджет кладём по выкатке, а не блок целиком: у цели, которую катают часто,
// один блок сам по себе перерастает лимит, и складывать его целиком значило бы
// не показать вообще ничего. Самая свежая выкатка помещается всегда — ради неё
// команду и зовут.
func formatServiceChangelog(found []service, s *Summary, now time.Time) string {
	tail := "\n" + freshness(s, now)
	t := newBudgeted(messageBudget - utf16Len(tail) - messageReserve)
	for i, sv := range found {
		if i > 0 {
			t.add("\n")
		}
		t.add(fmt.Sprintf("<b>%s</b>\n", link(sv.title, sv.url)))
		if len(sv.history) == 0 {
			// Пустая история — обычное дело, а не поломка: журнал начинается с
			// первой замеченной СМЕНЫ версии, и у цели, которую не выкатывали
			// с тех пор, как завели агента, записей нет.
			t.add("\nВыкаток пока не записано — журнал начинается с первой смены версии.\n")
			continue
		}
		for j, r := range sv.history {
			if j >= changelogReleases {
				t.add(fmt.Sprintf("\n<i>…и ещё %d в журнале</i>\n", len(sv.history)-j))
				break
			}
			t.add("\n" + releaseLine(r, now) + "\n" + itemLines(r, ""))
		}
	}
	out := t.String()
	if t.skipped > 0 {
		// Не «не поместилось в сообщение» — в сообщения теперь помещается всё,
		// они просто идут одно за другим. Упереться можно только в потолок
		// против вранья в журнале, и об этом надо говорить честно.
		out += "\n<i>…дальше не показываю: история необычно длинная</i>\n"
	}
	return out + tail
}

// releaseLine — версия и когда она поехала.
//
// Версия обрезается по той же мерке, что и тема коммита. Длина её ничем не
// ограничена: строку пишет в releases.json агент, но берёт он её из чужого
// version.json. Строка без предела — единственная в экране, способная одна
// перерасти целое сообщение, а это как раз тот случай, в котором разбиение
// вынуждено ломать разметку (см. splitMessage).
func releaseLine(r Release, now time.Time) string {
	version := r.Version
	if version == "" {
		version = "неизвестна"
	}
	s := "<code>" + esc(cutRunes(version, changelogWidth)) + "</code>"
	if t, ok := parseTime(firstNonEmptyStr(r.At, r.Seen)); ok {
		s += fmt.Sprintf(" · %s (%s назад)", fmtTime(t), humanDur(now.Sub(t)))
	}
	return s
}

// itemLines — пункты изменений одной выкатки, с отступом.
//
// Разбор и оформление общие с уведомлением о релизе (changelogItems, тот же
// маркер, та же обрезка, та же ссылка на PR): владелец обязан увидеть здесь
// ровно тот же список, что приходил ему сообщением, — иначе экран читается как
// рассказ о другой выкатке. Экранирование, как и там, делается на выводе: текст
// остаётся чужим до последнего момента.
//
// ЧИСЛА ПУНКТОВ ЗДЕСЬ БОЛЬШЕ НЕТ. Список показывается целиком: длину держит не
// обрезка, а разбиение на сообщения при отправке.
func itemLines(r Release, indent string) string {
	items, tail := changelogItems(scanLines(r.Changelog))
	if len(items) == 0 {
		// Записи без списка — норма: цель, чья выкатка ничего не публикует в
		// version.json, и не обязана. Молчать об этом нельзя, иначе выкатка
		// выглядит как «уехало без единого изменения».
		return indent + "<i>изменения не записаны</i>\n"
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString(indent + "• " + it.render() + "\n")
	}
	// Чужой хвост («…и ещё 12 коммитов» от генератора) показываем как есть: это
	// его сообщение о его пределах, а не наша обрезка. Своего у экрана больше
	// нет — резать нечего.
	if tail != "" {
		b.WriteString(indent + esc(cutRunes(tail, changelogWidth)) + "\n")
	}
	return b.String()
}

// formatUnknownService — ответ на имя, которого нет.
//
// Список допустимых имён обязателен: опечатка в имени цели неотличима от
// «бот сломался», пока не видно, что бот вообще знает. Имя владельца
// экранируется — в ответ уезжает то, что он набрал.
func formatUnknownService(query string, all []service) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Не знаю цель «%s».\n", esc(cutRunes(strings.TrimSpace(query), changelogWidth)))
	if len(all) == 0 {
		b.WriteString("\nВыкатываемых целей в данных агента нет вовсе.")
		return b.String()
	}
	b.WriteString("\n<b>Есть такие</b>")
	seen := map[string]bool{}
	for _, sv := range all {
		if seen[sv.project] {
			continue
		}
		seen[sv.project] = true
		fmt.Fprintf(&b, "\n• <code>%s</code> — %s", esc(sv.project), esc(sv.title))
	}
	b.WriteString("\n\n/changelog без имени — по всему хозяйству.")
	return b.String()
}

// pluralTargets — склонение слова «цель». Правило то же, что у коммитов
// (pluralCommits): «…и ещё 2 целей» читается как поломка формата.
func pluralTargets(n int) string {
	if n < 0 {
		n = -n
	}
	switch h, t := n%100, n%10; {
	case h >= 11 && h <= 14:
		return "целей"
	case t == 1:
		return "цель"
	case t >= 2 && t <= 4:
		return "цели"
	default:
		return "целей"
	}
}
