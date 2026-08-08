package main

import (
	"encoding/json"
	"io"
	"log"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Приёмник событий выкатки.
//
// Бот узнаёт о релизе по разнице двух снимков version.json, и разница
// существует не всегда: три выкатки за минуту дают одно сообщение, выкатка с
// откатом — ноль, провал выкатки — ноль (версия не менялась, сравнивать
// нечего). Событие рождается там, где происходит, и не зависит от того, успел
// ли кто-нибудь заметить последствие. Здесь — сторона читателя: журнал на
// диске превращается в проверенные события, которые дальше отправляет outbox.
//
// Контракт журнала — deploy-kit/docs/events.md; ссылки на разделы ниже ведут
// туда. Отступать от него нельзя: по нему же собирается писатель.
//
// Каталог для бота — ТОЛЬКО на чтение. Читатель, который умеет удалять
// обработанное, умеет удалить и необработанное; чисткой занимается писатель, а
// «что я уже видел» отвечает курсор в state.json — он переживает перезапуск и
// не портит данные для второго читателя (агента).

const (
	// defaultEventsDir — журнал намеренно НЕ в DATA_DIR: тот каталог nginx
	// раздаёт наружу как /data/, и адреса прогонов, стадии провалов и история
	// неудачных выкаток уехали бы в открытый доступ целиком (§1).
	defaultEventsDir = "/var/lib/deploy-kit/events"

	// inboxInterval — тик приёмника. Секунда, а не минута: мгновенность и есть
	// то единственное, ради чего событие заводилось вместо наблюдения.
	// Стоимость тика — один readdir по каталогу, где в рабочий день десятки
	// файлов.
	inboxInterval = time.Second
)

// Потолки §8. Все до единого проверяются здесь, а не только у писателя:
// каталог наполняет другой пользователь, и разбирать его содержимое положено
// так, будто файл положил кто угодно (§9).
const (
	// inboxMaxFileBytes — больший файл не открывается вовсе: размер смотрится
	// через stat ДО чтения, иначе «предел» означал бы «сначала прочитать 2 ГиБ
	// в память, потом отказаться».
	inboxMaxFileBytes = 8 << 10

	// inboxMaxFilesPerTick — сколько файлов забирается за один тик. Не потолок
	// журнала, а защита такта: разбор пяти тысяч файлов в одном проходе занял
	// бы цикл целиком, а всё, что не влезло, честно доедет следующей секундой —
	// курсор для этого и нужен.
	inboxMaxFilesPerTick = 500

	// inboxMaxPerAppDay — событий в сутки UTC на одну цель. Двести — это
	// примерно сорок выкаток в день при пяти событиях на выкатку, втрое больше
	// самого шумного дня в истории хозяйства. Предел стоит и у писателя; здесь
	// он повторён потому, что писатель может оказаться сломанным (цикл,
	// крутящий dk deploy в ошибке), а бот из-за этого не должен превратиться в
	// генератор сообщений.
	inboxMaxPerAppDay = 200

	inboxMaxChangelog   = 20
	inboxMaxItemRunes   = 120
	inboxMaxItemBytes   = 512
	inboxMaxVersionSize = 128
	inboxMaxURLSize     = 300
	inboxMaxGroupSeq    = 10000

	// inboxRecentTTL — сколько помнится id принятого события. Ровно столько,
	// сколько живёт сам файл (§11): пока файл может быть перечитан, его id
	// обязан помниться, иначе после перезапуска бот повторит в чат всё, что
	// лежит в каталоге.
	inboxRecentTTL = 14 * 24 * time.Hour
)

// Виды событий (§4). Названы с приставкой, чтобы не путались с Kind — видом
// СООБЩЕНИЯ бота (watch.go): вид события и вид сообщения совпадают не всегда,
// started и published в чат по умолчанию не идут вовсе.
const (
	evStarted    = "started"
	evSuccess    = "success"
	evFailure    = "failure"
	evRolledBack = "rolled_back"
	evRollback   = "rollback"
	evPublished  = "published"
)

// eventFileRe — единственный вход в журнал. Всё, что не подошло, для читателя
// не существует: недописанный .tmp, имена со слэшем, «..», чужие файлы (§3).
// Тринадцать цифр фиксированной ширины — то, на чём держится курсор:
// лексикографический порядок имён совпадает с хронологическим только при
// одинаковой длине числа (§2).
var eventFileRe = regexp.MustCompile(
	`^([0-9]{13})-([a-z0-9][a-z0-9._-]{0,63})-(started|success|failure|rolled_back|rollback|published)\.json$`)

// hex64Re — форма id и group. Одна на CI и на локальную выкатку: внутрь никто
// не заглядывает, прообраз остаётся у писателя (§5).
var hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// appRe — тот же набор, что у имени каталога релиза: app попадает в имя файла,
// и это единственное, что стоит между чужой строкой и записью в соседний
// каталог (§4).
var appRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// versionRe — набор app плюс «+»: release-20260805-101502-1a2b3c4,
// manual-…, собственная версия проекта у целей с VERSION_CMD.
var versionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,127}$`)

var eventStages = map[string]bool{
	"gates": true, "preflight": true, "upload": true, "switch": true,
	"units": true, "health": true, "version": true, "neighbours": true,
}

var eventReasons = map[string]bool{
	"units_failed": true, "nginx_failed": true, "health_failed": true,
	"verify_failed": true, "version_mismatch": true, "neighbours_failed": true,
	"manual": true,
}

// eventHosts — куда разрешено вести ссылке из события. Список закрыт: link() в
// боте экранирует href, но схему не смотрит, а событие приезжает файлом, и
// подделать его может любой, кто пишет в каталог. Без проверки выкатка кладёт
// в чат ссылку javascript: или фишинговый адрес от имени бота, которому
// владелец доверяет по определению (§4).
var eventHosts = map[string]bool{"github.com": true}

// rawEvent — событие как оно лежит на диске, то есть недоверенные данные.
// Отдельный тип от DeployEvent намеренно: граница «разобрано» и «проверено»
// должна быть видна в системе типов, иначе непроверенное поле рано или поздно
// уедет в чат просто потому, что структура одна и та же.
type rawEvent struct {
	V         int      `json:"v"`
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	App       string   `json:"app"`
	At        string   `json:"at"`
	Source    string   `json:"source"`
	Group     string   `json:"group"`
	GroupSeq  int      `json:"groupSeq"`
	Version   string   `json:"version"`
	Previous  string   `json:"previous"`
	Changelog []string `json:"changelog"`
	CommitURL string   `json:"commitURL"`
	RunURL    string   `json:"runURL"`
	Stage     string   `json:"stage"`
	Reason    string   `json:"reason"`
}

// DeployEvent — проверенное событие выкатки.
//
// Всё, что здесь лежит, уже прошло проверку типа, длины, перечисления и
// очистку от управляющих символов: отправителю и форматтеру перепроверять
// нечего. Поля, не прошедшие проверку, ОТСУТСТВУЮТ — событие из-за них не
// теряется (см. clean).
type DeployEvent struct {
	// File — имя файла в журнале. Оно же ключ курсора: отправитель двигает
	// outboxCursor именно по нему, а не по времени из содержимого (§2, §10).
	File string

	ID       string
	Kind     string
	App      string
	At       time.Time
	Source   string
	Group    string
	GroupSeq int

	Version   string
	Previous  string
	Changelog []string
	CommitURL string
	RunURL    string
	Stage     string
	Reason    string
}

// Inbox — чтение журнала событий.
type Inbox struct {
	dir string

	// primed — первый тик уже отработал. На нём курсор приёма отматывается к
	// подтверждённой отправке (см. prime), и делать это на каждом тике нельзя.
	primed bool

	// pending — id, уже отданные отправителю, но ещё не подтверждённые
	// Telegram. Держится в памяти, потому что durable-ответ на вопрос «не
	// показывали ли мы это уже» — State.RecentIDs, а он пополняется только
	// после подтверждения (§10). Без pending транспорт, доставивший событие
	// дважды разными файлами в одну секунду, дал бы два сообщения: у файлов
	// разные имена, и курсор тут не помогает никак.
	pending map[string]time.Time

	// daily — счётчик событий на цель по суткам UTC, ключ «<сутки>|<app>».
	// Сутки берутся из имени файла, а не из поля at: имя — это ключ порядка, и
	// на нём же держится всё остальное.
	daily map[string]int
	// day — самые поздние сутки, которые встретились. События идут по
	// возрастанию имени, то есть времени, поэтому смена суток — сигнал, что
	// прежние счётчики больше не нужны.
	day string
}

func newInbox(dir string) *Inbox {
	if dir == "" {
		dir = defaultEventsDir
	}
	return &Inbox{
		dir:     dir,
		pending: map[string]time.Time{},
		daily:   map[string]int{},
	}
}

// Poll забирает из журнала всё, что появилось после курсора, и возвращает
// проверенные события в том же порядке, в каком они произошли.
//
// Отправкой Poll не занимается сознательно: приём и отправка разведены по двум
// курсорам, потому что очередь отправки живёт в памяти и переживает меньше,
// чем журнал на диске.
func (in *Inbox) Poll(st *State, now time.Time) []DeployEvent {
	if !in.primed {
		in.prime(st)
		in.primed = true
	}
	in.trimRecent(st, now)

	names, err := in.list()
	if err != nil {
		// Каталога может не быть до первой выкатки, и это не авария: бот
		// обязан работать на машине, где deploy-kit ещё не раскладывали.
		if !os.IsNotExist(err) {
			log.Printf("журнал событий не прочитан (%s): %v", in.dir, err)
		}
		return nil
	}

	// os.Root вместо голого пути — то же, что O_NOFOLLOW, только переносимо и
	// на весь путь сразу: каталог открывается один раз, а имена внутри него
	// разрешаются без выхода наружу. Симлинк на /etc/shadow, положенный в
	// каталог событий, не должен превращаться в чтение /etc/shadow процессом,
	// который держит токен Telegram.
	root, err := os.OpenRoot(in.dir)
	if err != nil {
		log.Printf("журнал событий не открыт (%s): %v", in.dir, err)
		return nil
	}
	defer func() { _ = root.Close() }()

	var out []DeployEvent
	taken := 0
	for _, name := range names {
		if name <= st.InboxCursor {
			continue
		}
		if taken >= inboxMaxFilesPerTick {
			break
		}
		taken++

		ev, ok := in.read(root, name, st, now)

		// Курсор двигается и на пропущенном файле. Испорченный файл не имеет
		// права остановить журнал: иначе одна опечатка писателя глушит
		// уведомления навсегда, а это ровно тот дефект, ради которого всё
		// затевалось.
		st.InboxCursor = name
		st.dirty = true

		if ok {
			out = append(out, ev)
		}
	}
	return out
}

// prime приводит курсор приёма в состояние «продолжаем с подтверждённого».
//
// Очередь отправки живёт в памяти: убитый бот теряет всё, что принял, но не
// отправил. Поэтому на старте inboxCursor берётся равным outboxCursor и
// неотправленное перечитывается с диска (§10). Ровно за этим журнал и лежит на
// диске — он переживает и лежачего бота, и лежачий Telegram.
func (in *Inbox) prime(st *State) {
	if st.OutboxCursor < st.InboxCursor {
		st.InboxCursor = st.OutboxCursor
		st.dirty = true
	}
	if st.InboxCursor != "" || st.OutboxCursor != "" {
		return
	}

	// Курсора нет вовсе: новая установка или потерянный state.json. Прочитать
	// журнал целиком значило бы вывалить в чат две недели истории разом —
	// сотни сообщений о выкатках, которые давно случились. Считаем журнал
	// прочитанным, но говорим об этом в лог: пропуск нескольких свежих событий
	// заметен, а лавина повторов — авария.
	names, err := in.list()
	if err != nil || len(names) == 0 {
		return
	}
	st.InboxCursor = names[len(names)-1]
	st.dirty = true
	log.Printf("журнал событий: курсора нет, %d файлов приняты за прочитанные до %s",
		len(names), st.InboxCursor)
}

// list — имена журнала, подходящие под шаблон, по возрастанию.
//
// os.ReadDir отдаёт записи отсортированными по имени, а имя события устроено
// так, что лексикографический порядок совпадает с хронологическим (§2).
// Поэтому отдельной сортировки по времени нет и быть не должно: разбор
// мусорного файла не имеет права влиять на продвижение по журналу.
func (in *Inbox) list() ([]string, error) {
	ents, err := os.ReadDir(in.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if eventFileRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// read разбирает один файл журнала. Второе значение — стоит ли отдавать
// событие дальше; ошибка здесь никогда не останавливает обход.
func (in *Inbox) read(root *os.Root, name string, st *State, now time.Time) (DeployEvent, bool) {
	m := eventFileRe.FindStringSubmatch(name)
	if m == nil {
		return DeployEvent{}, false
	}

	// Размер смотрится ДО открытия и ДО чтения, и заодно проверяется, что это
	// обычный файл: Lstat не идёт по симлинку, поэтому подсунутая ссылка
	// отсеивается здесь, а не после того, как бот прочитал, куда она ведёт.
	fi, err := root.Lstat(name)
	if err != nil {
		log.Printf("событие %s не прочитано: %v", name, err)
		return DeployEvent{}, false
	}
	if !fi.Mode().IsRegular() {
		log.Printf("событие %s пропущено: не обычный файл (%s)", name, fi.Mode())
		return DeployEvent{}, false
	}
	if fi.Size() > inboxMaxFileBytes {
		log.Printf("событие %s пропущено: %d байт при пределе %d", name, fi.Size(), inboxMaxFileBytes)
		return DeployEvent{}, false
	}

	f, err := root.Open(name)
	if err != nil {
		log.Printf("событие %s не открыто: %v", name, err)
		return DeployEvent{}, false
	}
	defer func() { _ = f.Close() }()

	// Файл мог подрасти между stat и open, поэтому читается на байт больше
	// предела: чтение до конца при обмане в stat означало бы предел, которого
	// нет.
	b, err := io.ReadAll(io.LimitReader(f, inboxMaxFileBytes+1))
	if err != nil {
		log.Printf("событие %s не дочитано: %v", name, err)
		return DeployEvent{}, false
	}
	if len(b) > inboxMaxFileBytes {
		log.Printf("событие %s пропущено: больше %d байт", name, inboxMaxFileBytes)
		return DeployEvent{}, false
	}

	var raw rawEvent
	if err := json.Unmarshal(b, &raw); err != nil {
		// Обрезанный файл, мусор, чужой формат — обычный вход, а не
		// исключительный. Разбор без паники, пропуск и запись в лог.
		log.Printf("событие %s не разобрано: %v", name, cleanEventText(err.Error()))
		return DeployEvent{}, false
	}

	ev, ok := clean(raw, name, m[2], m[3])
	if !ok {
		return DeployEvent{}, false
	}

	// Дубль отбрасывается молча: доставка повторяется по построению (три
	// попытки транспорта, повтор прогона одной кнопкой, перечитывание журнала
	// после перезапуска), и писать о ней в лог значило бы засорять его штатным
	// поведением. Два одинаковых сообщения о выкатке хуже одного: по чату
	// считают, сколько раз катились сегодня.
	if _, seen := st.RecentIDs[ev.ID]; seen {
		return DeployEvent{}, false
	}
	if _, seen := in.pending[ev.ID]; seen {
		return DeployEvent{}, false
	}

	ms, _ := strconv.ParseInt(m[1], 10, 64)
	if !in.allow(ev.App, ms) {
		// Переполнение раздела убивает агента, а с ним всю статус-систему
		// целиком. Отказ громкий: молча выбросить событие нельзя, тишина в
		// чате означает «не катились».
		log.Printf("событие %s отклонено: у цели %s уже %d событий за сутки",
			name, ev.App, inboxMaxPerAppDay)
		return DeployEvent{}, false
	}

	in.pending[ev.ID] = now
	return ev, true
}

// clean проверяет разобранное событие и вычищает из него всё, что не является
// текстом.
//
// Правило одно и оно важнее аккуратности: событие теряется ТОЛЬКО тогда, когда
// без поля его нельзя ни показать, ни сопоставить с прогоном (v, id, kind, app,
// at, source, group, groupSeq). Описательные поля — версия, стадия, причина,
// список изменений, ссылки — при непрохождении проверки просто исчезают, и
// сообщение выходит без них. Отвергнуть событие целиком из-за незнакомой
// стадии значило бы промолчать о провалившейся выкатке, а молчание в чате
// читается как «не катились».
func clean(raw rawEvent, file, fileApp, fileKind string) (DeployEvent, bool) {
	if raw.V != 1 {
		log.Printf("событие %s пропущено: версия схемы %d", file, raw.V)
		return DeployEvent{}, false
	}
	if !hex64Re.MatchString(raw.ID) {
		log.Printf("событие %s пропущено: id не sha256", file)
		return DeployEvent{}, false
	}
	if !hex64Re.MatchString(raw.Group) {
		log.Printf("событие %s пропущено: group не sha256", file)
		return DeployEvent{}, false
	}
	if raw.GroupSeq < 1 || raw.GroupSeq > inboxMaxGroupSeq {
		log.Printf("событие %s пропущено: groupSeq=%d", file, raw.GroupSeq)
		return DeployEvent{}, false
	}
	if raw.Source != "ci" && raw.Source != "local" {
		log.Printf("событие %s пропущено: source=%q", file, cleanEventText(raw.Source))
		return DeployEvent{}, false
	}

	// Имя файла и содержимое обязаны говорить одно и то же. Разойдясь, они
	// сломали бы всё, что считается по имени: суточную квоту на цель и
	// порядок. Дешёвая проверка на месте вместо дорогого расследования потом.
	if raw.App != fileApp || raw.Kind != fileKind {
		log.Printf("событие %s пропущено: имя файла не совпадает с содержимым", file)
		return DeployEvent{}, false
	}
	if !appRe.MatchString(raw.App) {
		log.Printf("событие %s пропущено: app=%q", file, cleanEventText(raw.App))
		return DeployEvent{}, false
	}

	at, ok := parseTime(raw.At)
	if !ok {
		log.Printf("событие %s пропущено: at=%q не RFC3339", file, cleanEventText(raw.At))
		return DeployEvent{}, false
	}

	ev := DeployEvent{
		File:     file,
		ID:       raw.ID,
		Kind:     raw.Kind,
		App:      raw.App,
		At:       at.UTC(),
		Source:   raw.Source,
		Group:    raw.Group,
		GroupSeq: raw.GroupSeq,
	}

	ev.Version = cleanVersion(raw.Version)
	ev.Previous = cleanVersion(raw.Previous)
	ev.CommitURL = cleanEventURL(raw.CommitURL)
	ev.RunURL = cleanEventURL(raw.RunURL)
	// Прогона у локальной выкатки нет, и ссылка «на прогон» с машины
	// разработчика может вести только куда-то не туда.
	if ev.Source != "ci" {
		ev.RunURL = ""
	}
	ev.Changelog = cleanChangelog(raw.Changelog)

	if eventStages[raw.Stage] {
		ev.Stage = raw.Stage
	}
	if eventReasons[raw.Reason] {
		ev.Reason = raw.Reason
	}
	applyKindRules(&ev)
	return ev, true
}

// applyKindRules снимает поля, запрещённые для этого вида события (§4).
//
// Запрещённое поле не повод отбросить событие, но и показывать его нельзя:
// «выкатка началась» со стадией провала или успех с причиной отката — это
// сообщение, которое врёт читателю о том, что произошло на проде.
func applyKindRules(ev *DeployEvent) {
	switch ev.Kind {
	case evStarted:
		ev.Version, ev.Previous = "", ""
		ev.Stage, ev.Reason = "", ""
	case evSuccess, evPublished:
		ev.Stage, ev.Reason = "", ""
	case evFailure:
		// Версии у провала может не быть вовсе: на гейтах она ещё не
		// посчитана, и требовать её значило бы либо врать заглушкой, либо
		// молчать о самом раннем классе провалов.
		ev.Reason = ""
	case evRolledBack:
		// Стадия и причина — здесь главное содержимое сообщения.
	case evRollback:
		// Ручной откат: причина известна заранее и одна, стадии нет.
		ev.Stage = ""
		ev.Reason = "manual"
	}
}

// cleanVersion: версия уезжает и в чат, и потенциально на публичную страницу,
// поэтому набор символов у неё закрытый, а не «любая строка до 128 байт».
func cleanVersion(s string) string {
	if len(s) > inboxMaxVersionSize || !versionRe.MatchString(s) {
		return ""
	}
	return s
}

// cleanEventURL пропускает только https и только известный хост.
//
// Адрес, не прошедший проверку, — не повод отбросить событие: поле просто
// выбрасывается, и версия в сообщении остаётся обычным текстом, ровно так же,
// как при отсутствии commitURL сегодня.
func cleanEventURL(raw string) string {
	if raw == "" || len(raw) > inboxMaxURLSize {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	// Учётные данные в адресе — способ показать один хост, а попасть на
	// другой; у настоящих ссылок на коммит их не бывает.
	if u.User != nil {
		return ""
	}
	if !eventHosts[strings.ToLower(u.Hostname())] {
		return ""
	}
	return raw
}

// cleanChangelog приводит список изменений к простому тексту в пределах §8.
//
// Разметки здесь не бывает по контракту: писатель разворачивает её обратно в
// текст, а обратно навешивает бот — один раз, в одном месте, из уже
// экранированного текста (format.go, formatChangelog). Недоверенный HTML из
// события нельзя ни экранировать (сломается разметка), ни не экранировать
// (это дыра).
func cleanChangelog(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if len(out) == inboxMaxChangelog {
			break
		}
		s := cleanEventText(it)
		if s == "" {
			continue
		}
		// Сначала байты, потом символы: кириллическая буква — один символ и
		// два байта, и предел «120 символов» без байтового потолка означал бы
		// 480 байт на четырёхбайтовых символах.
		s = cutRunes(cutBytes(s, inboxMaxItemBytes), inboxMaxItemRunes)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cleanEventText делает из недоверенной строки текст.
//
// Убирается три класса символов, и каждый — не гигиена, а известный приём:
//   - битые последовательности UTF-8: Telegram отвергает такое сообщение
//     целиком, и выкатка молча остаётся необъявленной;
//   - управляющие символы: CR и LF в поле подделывают строки journald, то есть
//     позволяют написать в журнал бота что угодно от его имени;
//   - U+202E и соседи: переворот направления текста показывает в чате не то,
//     что написано, — этим маскируют и адреса, и имена.
func cleanEventText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			// Перевод строки в поле события — либо ошибка писателя, либо
			// попытка нарисовать лишнюю строку в сообщении. Пробел сохраняет
			// читаемость, не давая ни того, ни другого.
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		case r >= 0x80 && r <= 0x9f:
			return -1
		case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
			return -1
		case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
			return -1
		case r == 0x200e || r == 0x200f: // LRM, RLM
			return -1
		case r == 0xfeff:
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// allow — суточная квота на цель. Считается по календарным суткам UTC и по
// каждой цели отдельно.
func (in *Inbox) allow(app string, ms int64) bool {
	day := time.UnixMilli(ms).UTC().Format("2006-01-02")
	if day > in.day {
		// События идут по возрастанию имени, то есть времени: наступили новые
		// сутки — прежние счётчики больше не понадобятся. Так карта не растёт
		// вместе с временем работы бота.
		in.day = day
		in.daily = map[string]int{}
	}
	key := day + "|" + app
	if in.daily[key] >= inboxMaxPerAppDay {
		return false
	}
	in.daily[key]++
	return true
}

// Confirmed вызывается отправителем после подтверждения Telegram: id уезжает в
// долгую память, а из pending уходит.
//
// Пополнение RecentIDs после отправки, а не при приёме, — осознанный выбор
// между двумя рисками. Пометив id при приёме, мы получили бы потерю сообщения
// при падении между приёмом и отправкой — а потерянная выкатка и есть то, что
// чинится этой работой. Пометив после отправки, мы получаем редкий дубль:
// падение ровно между ответом Telegram и записью состояния. Транзакции с чужим
// сервисом не бывает, третьего варианта нет, и выбран тот, где отказ громкий.
func (in *Inbox) Confirmed(st *State, id string, now time.Time) {
	if st.RecentIDs == nil {
		st.RecentIDs = map[string]string{}
	}
	st.RecentIDs[id] = now.UTC().Format(time.RFC3339)
	delete(in.pending, id)
	st.dirty = true
}

// trimRecent выбрасывает из памяти всё, что старше срока жизни файла события.
// Живёт здесь, а не в saveState: сроки диктует журнал, и держать их рядом с
// его чтением дешевле, чем искать по двум файлам, почему сутки именно
// четырнадцать.
func (in *Inbox) trimRecent(st *State, now time.Time) {
	for id, at := range st.RecentIDs {
		t, ok := parseTime(at)
		if !ok || now.Sub(t) > inboxRecentTTL {
			delete(st.RecentIDs, id)
			st.dirty = true
		}
	}
	for id, t := range in.pending {
		if now.Sub(t) > inboxRecentTTL {
			delete(in.pending, id)
		}
	}
}
