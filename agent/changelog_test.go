package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Ссылка на PR внутри пункта и пределы долгого хранения списков.
//
// Отдельный файл, потому что это отдельная граница доверия: текст пункта — это
// тема коммита, то есть чужой ввод (пишет всякий, у кого есть право мержа), а
// показывается он РАЗМЕТКОЙ в чате и на публичной странице. Проверки здесь
// написаны от враждебного входа, а не от удобного.

// prLink — та самая ссылка, ради которой всё и затевалось. Вид её задан
// генератором (deploy-kit/bin/changelog, --link-base): хвост «(#21)» темы
// снимается и возвращается в конец пункта кликабельным номером.
const prLink = `<a href="https://github.com/tr0llex/deploy-kit/pull/21">#21</a>`

func TestНормализацияСохраняетСсылкуНаPR(t *testing.T) {
	// Владелец просил ровно это: «• Завести dependabot одинаково во всех
	// репозиториях (#21)», где «#21» — ссылка. Санитайзер стоит ровно между
	// генератором и ботом, и без поблажки он бы ссылку и съел: разэкранирование
	// превратило бы её в текст, а обрезка — в огрызок тега.
	const subject = "Завести dependabot одинаково во всех репозиториях"

	got := normalizeChangelog([]string{"<b>Изменения</b>", "• " + subject + " " + prLink})
	if len(got) != 1 {
		t.Fatalf("пункт не разобран: %q", got)
	}
	if want := subject + " " + prLink; got[0] != want {
		t.Errorf("ссылка не доехала:\nбыло  %q\nстало %q", want, got[0])
	}
}

func TestНормализацияНеПропускаетЧужуюРазметку(t *testing.T) {
	// СПИСОК РАЗРЕШЁННОГО, А НЕ ЗАПРЕЩЁННОГО. Всё, что не совпало с единственным
	// разрешённым видом ссылки, обязано остаться текстом и уехать к читателю
	// экранированным.
	cases := map[string]string{
		"чужая схема":        `Тема <a href="javascript:alert(1)">#1</a>`,
		"лишний атрибут":     `Тема <a href="https://ok/pull/1" onclick="alert(1)">#1</a>`,
		"атрибут после":      `Тема <a href="https://ok/pull/1">#1</a onclick=x>`,
		"вложенный тег":      `Тема <a href="https://ok/pull/1"><b>#1</b></a>`,
		"незакрытый тег":     `Тема <a href="https://ok/pull/1">#1`,
		"ссылка в середине":  `Тема <a href="https://ok/pull/1">#1</a> и ещё хвост`,
		"две ссылки":         `Тема <a href="https://ok/pull/1">#1</a> <a href="https://evil/x">#2</a>`,
		"подпись не номер":   `Тема <a href="https://ok/pull/1">жми сюда</a>`,
		"без https":          `Тема <a href="http://ok/pull/1">#1</a>`,
		"чужой атрибут":      `Тема <a href="https://ok/pull/1" title="x">#1</a>`,
		"пункт без темы":     `<a href="https://ok/pull/1">#1</a>`,
		"скрипт перед":       `<script>alert(1)</script> тема <a href="https://ok/pull/1">#1</a>`,
		"пробел в теге":      `Тема < a href="https://ok/pull/1">#1</a>`,
		"амперсанд в адресе": `Тема <a href="https://ok/pull/1&x=y">#1</a>`,
	}
	for name, in := range cases {
		got := normalizeChangelog([]string{in})
		if len(got) != 1 {
			t.Errorf("%s: пункт потерялся целиком: %q", name, got)
			continue
		}
		// Проверяем не «текст очищен», а «ссылкой это не станет»: непризнанная
		// разметка остаётся в пункте обычным текстом и уезжает к читателю
		// экранированной — так и задумано, выбрасывать чужой ввод незачем. Ровно
		// эта же функция стоит у бота на выводе, и её отказ означает, что
		// кликабельной ссылки не будет ни в чате, ни на странице.
		if _, _, _, ok := splitRefLink(got[0]); ok {
			t.Errorf("%s: чужая разметка признана ссылкой: %q", name, got[0])
		}
	}
}

func TestМногострочныйПунктСклеиваетсяДоРазбораСсылки(t *testing.T) {
	// Тема приходит многострочной, если в сообщении коммита нет пустой строки
	// после первой. Склейка строк — правило старше ссылок, и ссылка после
	// склейки обязана опознаваться как обычная: иначе один и тот же пункт
	// показывался бы по-разному в зависимости от того, как его закоммитили.
	got := normalizeChangelog([]string{"Тема\nв две строки " + prLink})
	if len(got) != 1 {
		t.Fatalf("пункт не разобран: %q", got)
	}
	if got[0] != "Тема в две строки "+prLink {
		t.Errorf("строки не склеены или ссылка потеряна: %q", got[0])
	}
}

func TestНормализацияНеОтмываетЭкранированнуюСсылку(t *testing.T) {
	// Самая тонкая из атак и единственная, которую не видно, глядя на один шаг.
	// Тема коммита содержит ссылку ТЕКСТОМ: генератор её честно экранирует,
	// splitRefLink её (правильно) не признаёт, а разэкранирование делает из неё
	// настоящую разметку — и следующий читатель, бот, видит уже не текст, а
	// ссылку на чужой https-адрес под видом номера PR.
	//
	// Правило после нормализации простое: якорь в пункте есть только тот,
	// который поставил агент.
	in := `Тема &lt;a href="https://evil.example/x"&gt;#1&lt;/a&gt; ` + prLink
	got := normalizeChangelog([]string{in})
	if len(got) != 1 {
		t.Fatalf("пункт не разобран: %q", got)
	}
	if strings.Contains(got[0], "evil.example") {
		t.Errorf("чужой адрес отмыт в разметку: %q", got[0])
	}
	if n := strings.Count(got[0], "<a "); n != 1 {
		t.Errorf("якорей в пункте %d, ожидали ровно один (свой): %q", n, got[0])
	}
	if !strings.HasSuffix(got[0], prLink) {
		t.Errorf("своя ссылка потерялась: %q", got[0])
	}
	// Номер подделки остаётся видимым: он часть темы, терять его не за что.
	if !strings.Contains(got[0], "#1") {
		t.Errorf("номер из темы пропал вместе с подделкой: %q", got[0])
	}
}

func TestОтмытыйЯкорьСнимаетсяИзСерединыПункта(t *testing.T) {
	// Тот же приём отмывания, но подделка стоит НЕ В КОНЦЕ, а посреди темы.
	// Хвостовую splitRefLink разбирала и снимала, а на середину проверка не
	// смотрела вовсе: пункт уезжал в summary.json и releases.json НАСТОЯЩЕЙ
	// разметкой с чужим адресом. Правило «якорь в пункте есть только тот,
	// который поставил агент» обязано держаться для всей строки, а не для
	// хвоста: файл лежит в вебруте, и читателей у него больше одного.
	cases := []string{
		`Ссылка в середине &lt;a href="https://evil.example/x"&gt;#1&lt;/a&gt; хвост ` + prLink,
		`Верхний регистр &lt;A HREF="https://evil.example/x"&gt;#1&lt;/A&gt; хвост ` + prLink,
		`Незакрытый тег &lt;a href="https://evil.example/x"&gt;#1 хвост ` + prLink,
	}
	for _, in := range cases {
		got := normalizeChangelog([]string{in})
		if len(got) != 1 {
			t.Fatalf("пункт не разобран: %q", got)
		}
		if strings.Contains(got[0], "evil.example") {
			t.Errorf("чужой адрес отмыт в разметку: %q", got[0])
		}
		if n := strings.Count(strings.ToLower(got[0]), "<a"); n != 1 {
			t.Errorf("якорей в пункте %d, ожидали ровно один (свой): %q", n, got[0])
		}
		// Номер подделки — часть темы, и терять его не за что: снимаются теги,
		// а не то, что между ними.
		if !strings.Contains(got[0], "#1 ") {
			t.Errorf("номер из темы пропал вместе с подделкой: %q", got[0])
		}
		if !strings.HasSuffix(got[0], prLink) {
			t.Errorf("своя ссылка потерялась: %q", got[0])
		}
	}
}

func TestОбрезкаНеРежетСсылку(t *testing.T) {
	// Ссылка стоит В КОНЦЕ пункта, то есть ровно там, куда бьёт обрезка.
	// Половина тега — это отказ Telegram, а отказ Telegram — это молча не
	// пришедшее уведомление о релизе.
	long := strings.Repeat("тема ", 100) // 500 символов, вдвое больше предела
	got := normalizeChangelog([]string{long + prLink})
	if len(got) != 1 {
		t.Fatalf("пункт не разобран: %q", got)
	}
	if !strings.HasSuffix(got[0], prLink) {
		t.Errorf("ссылка обрезана вместе с темой: %q", got[0])
	}
	text := strings.TrimSuffix(got[0], " "+prLink)
	if n := utf8.RuneCountInString(text); n > changelogLineChars {
		t.Errorf("текст пункта на %d символов, предел %d", n, changelogLineChars)
	}
	if !strings.HasSuffix(text, "…") {
		t.Errorf("обрезка текста не отмечена многоточием: %q", text)
	}
}

func TestТекстРядомСоСсылкойРазэкранируетсяКакПрежде(t *testing.T) {
	// Поблажка ради ссылки не отменяет того, ради чего санитайзер вообще нужен:
	// генератор экранирует вывод, бот экранирует ещё раз при отправке, и без
	// разэкранирования здесь «go 1.22 <-- важно» доехало бы до читателя как
	// «go 1.22 &amp;lt;-- важно».
	got := normalizeChangelog([]string{"• поднять go до 1.22 &lt;-- важно " + prLink})
	if len(got) != 1 {
		t.Fatalf("пункт не разобран: %q", got)
	}
	if !strings.HasPrefix(got[0], "поднять go до 1.22 <-- важно") {
		t.Errorf("текст не разэкранирован: %q", got[0])
	}
	if !strings.HasSuffix(got[0], prLink) {
		t.Errorf("ссылка не сохранена: %q", got[0])
	}
}

func TestСорокКоммитовДоезжаютДоSummary(t *testing.T) {
	// Первая из двух обрезок решает всё: чего агент не положил в summary.json,
	// боту уже не увидеть никакими правками. Прежние двенадцать пунктов
	// означали, что сорокакоммитный релиз теряется здесь, ещё до чата.
	var in []string
	for i := 0; i < 40; i++ {
		in = append(in, fmt.Sprintf("• изменение номер %02d %s", i, strings.Repeat("тема ", 15)))
	}
	got := normalizeChangelog(in)
	if len(got) != 40 {
		t.Fatalf("до summary.json доехало %d пунктов из 40", len(got))
	}
	for i, s := range got {
		if !strings.HasPrefix(s, fmt.Sprintf("изменение номер %02d", i)) {
			t.Errorf("пункт %d потерян или переставлен: %q", i, s)
		}
	}
}

// ------------------------------------------------- пределы releases.json

func TestСпискиИзмененийОграниченыПоВсемуФайлу(t *testing.T) {
	// Поштучный предел умножается на число целей: девятнадцать целей по своему
	// потолку — это файл под два мегабайта, который лежит в вебруте и
	// переписывается раз в минуту. Общий потолок держит сам файл.
	releases := map[string][]Release{}
	for i := 0; i < 30; i++ {
		var h []Release
		for j := 0; j < releaseChangelogKeep; j++ {
			var cl changelogField
			for k := 0; k < 40; k++ {
				cl = append(cl, strings.Repeat("тема ", 24))
			}
			h = append(h, Release{
				Version:   fmt.Sprintf("v%d-%d", i, j),
				Seen:      time.Date(2026, 1, 1, 0, 0, j, 0, time.UTC).Format(time.RFC3339),
				Changelog: cl,
			})
		}
		releases[fmt.Sprintf("проект-%02d::Сайт", i)] = h
	}
	capReleaseChangelogs(releases)

	total := 0
	for _, h := range releases {
		for i, r := range h {
			for _, s := range r.Changelog {
				total += utf8.RuneCountInString(s)
			}
			// Дата и версия остаются на всю глубину: дорог только текст.
			if r.Version == "" || r.Seen == "" {
				t.Fatalf("запись %d потеряла версию или дату: %+v", i, r)
			}
		}
	}
	if total > releasesChangelogTotal {
		t.Errorf("файл весит %d символов списков, потолок %d", total, releasesChangelogTotal)
	}
	if total == 0 {
		t.Error("общий потолок вычистил вообще всё — история стала бесполезной")
	}
}

func TestОбщийПотолокСнимаетСпискиСоСтарыхЗаписей(t *testing.T) {
	// «Что приехало вчера» спрашивают, «что приехало в январе» — нет. И порядок
	// обязан быть определённым: обход карты в Go случаен, и без явной
	// сортировки файл терял бы при каждом запуске разные записи.
	big := func(when string) Release {
		var cl changelogField
		for i := 0; i < 60; i++ {
			cl = append(cl, strings.Repeat("тема ", 24))
		}
		return Release{Version: "v" + when, Seen: when, Changelog: cl}
	}
	mk := func() map[string][]Release {
		m := map[string][]Release{}
		for i := 0; i < 40; i++ {
			m[fmt.Sprintf("цель-%02d", i)] = []Release{
				big(fmt.Sprintf("2026-08-%02dT00:00:00Z", i%28+1)),
				big(fmt.Sprintf("2026-01-%02dT00:00:00Z", i%28+1)),
			}
		}
		return m
	}
	first, second := mk(), mk()
	capReleaseChangelogs(first)
	capReleaseChangelogs(second)

	for key, h := range first {
		for i := range h {
			if len(h[i].Changelog) != len(second[key][i].Changelog) {
				t.Fatalf("%s[%d]: два прогона на одних данных дали разный файл", key, i)
			}
		}
	}
	// Январские записи старше августовских, и снимать списки надо с них.
	janKept, augKept := 0, 0
	for _, h := range first {
		if len(h[0].Changelog) > 0 {
			augKept++
		}
		if len(h[1].Changelog) > 0 {
			janKept++
		}
	}
	if janKept >= augKept {
		t.Errorf("списки сняты не со старых записей: свежих осталось %d, старых %d", augKept, janKept)
	}
}
