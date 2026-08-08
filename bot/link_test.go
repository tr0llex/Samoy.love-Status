package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// Ссылка на PR в пункте списка и раскладка длинного ответа по сообщениям.
//
// Отдельный файл, потому что это отдельная граница доверия: текст пункта — это
// тема коммита, то есть чужой ввод (пишет всякий, у кого есть право мержа), а
// уходит он В РАЗМЕТКЕ, с parse_mode=HTML. Проверки написаны от враждебного
// входа, а не от удобного.

// prLink — вид, в котором ссылка приезжает от агента (agent/main.go,
// refLinkHTML). Генератор снимает с темы хвост «(#21)» и возвращает его в
// конец пункта кликабельным номером.
const prLink = `<a href="https://github.com/tr0llex/deploy-kit/pull/21">#21</a>`

func TestСсылкаНаPRДоезжаетДоСообщения(t *testing.T) {
	// Владелец просил ровно это: «• Завести dependabot одинаково во всех
	// репозиториях (#21)», где «#21» — ссылка. Бот экранирует всё, что рисует,
	// и без поблажки ссылка доехала бы до чата текстом «<a href=…>».
	const subject = "Завести dependabot одинаково во всех репозиториях"

	got := formatChangelog([]string{"<b>Изменения</b>", "• " + subject + " " + prLink}, "")
	if want := "\n• " + subject + " " + prLink; !strings.Contains(got, want) {
		t.Errorf("ссылка не доехала до сообщения:\nожидали %q\nполучили %s", want, got)
	}
	if strings.Contains(got, "&lt;a href") {
		t.Errorf("ссылка экранирована и стала текстом:\n%s", got)
	}

	// Тот же пункт на экране «/changelog имя» — слово в слово: экран и
	// сообщение рассказывают про одну выкатку.
	screen := itemLines(release("v1", base, "• "+subject+" "+prLink), "")
	if !strings.Contains(screen, subject+" "+prLink) {
		t.Errorf("экран разошёлся с сообщением на ссылке:\n%s", screen)
	}
}

func TestБотНеПропускаетЧужуюРазметку(t *testing.T) {
	// СПИСОК РАЗРЕШЁННОГО, А НЕ ЗАПРЕЩЁННОГО, и бот проверяет сам: путь
	// «summary.json собран мимо агента» описан в summary.go как рабочий, а
	// releases.json бот и вовсе читает с диска. Положиться на то, что кто-то
	// выше по течению уже проверил, — значит положиться на предположение.
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
		"чистый скрипт":      `Тема <script>alert(1)</script>`,
		"картинка":           `Тема <img src=x onerror=alert(1)>`,
	}
	for name, in := range cases {
		got := formatChangelog([]string{in}, "")
		// Единственная разметка, которой позволено остаться, — своя ссылка.
		// Всё прочее обязано доехать до чата экранированным текстом.
		body := strings.TrimPrefix(got, "<b>Изменения</b>")
		if strings.Contains(body, "<a ") || strings.Contains(body, "<script") || strings.Contains(body, "<img") {
			t.Errorf("%s: чужая разметка уехала в сообщение как есть:\n%s", name, got)
		}
		if !strings.Contains(body, "&lt;") {
			t.Errorf("%s: разметка не экранирована — где-то её проглотили целиком:\n%s", name, got)
		}
	}
}

func TestЭкранированнаяСсылкаОстаётсяТекстом(t *testing.T) {
	// Зеркало агентской защиты от отмывания, но с другого конца. У агента
	// разэкранирование опасно тем, что после него текст читает СЛЕДУЮЩИЙ
	// разбор. У бота следующего нет: он экранирует и отправляет, поэтому
	// подделка обязана просто доехать текстом — рядом с настоящей ссылкой и не
	// смешавшись с ней.
	in := `Тема &lt;a href="https://evil.example/x"&gt;#1&lt;/a&gt; ` + prLink
	got := formatChangelog([]string{in}, "")

	if strings.Contains(got, `<a href="https://evil.example/x">`) {
		t.Errorf("подделка стала ссылкой:\n%s", got)
	}
	if !strings.Contains(got, "&lt;a href=") {
		t.Errorf("подделка не доехала текстом — её проглотили молча:\n%s", got)
	}
	if !strings.Contains(got, prLink) {
		t.Errorf("настоящая ссылка потерялась из-за соседства с подделкой:\n%s", got)
	}
	if n := strings.Count(got, `<a href=`); n != 1 {
		t.Errorf("ссылок в сообщении %d, ожидали ровно одну:\n%s", n, got)
	}
}

func TestДлинныйАдресНеСъедаетТему(t *testing.T) {
	// Читатель видит «#21», а не адрес: считать ширину пункта вместе с
	// невидимым адресом значило бы наказывать тему за длину чужой ссылки.
	subject := changelogSubject120(t)
	got := formatChangelog([]string{"• " + subject + " " + prLink}, "")
	if !strings.Contains(got, "• "+subject+" "+prLink) {
		t.Errorf("тема предельной длины обрезана из-за адреса ссылки:\n%s", got)
	}
}

// ------------------------------------------------ раскладка по сообщениям

func TestРаскладкаНеТеряетНиОднойСтроки(t *testing.T) {
	// Условие раскладки одно и главное: ничего не потеряно, порядок сохранён.
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("• строка %03d %s", i, strings.Repeat("тема ", 12)))
	}
	text := strings.Join(lines, "\n")

	parts := splitMessage(text, telegramTextLimit)
	if len(parts) < 2 {
		t.Fatalf("текст на %d единиц не разложен: частей %d", utf16Len(text), len(parts))
	}
	prevPart := 0
	for i := 0; i < 300; i++ {
		want := fmt.Sprintf("• строка %03d", i)
		found := -1
		for j, p := range parts {
			if strings.Contains(p, want) {
				found = j
				break
			}
		}
		if found < 0 {
			t.Fatalf("строка %03d потеряна при раскладке", i)
		}
		if found < prevPart {
			t.Fatalf("строка %03d уехала в часть раньше предыдущей: порядок нарушен", i)
		}
		prevPart = found
	}
	for i, p := range parts {
		if n := utf16Len(p); n > telegramTextLimit {
			t.Errorf("часть %d: %d единиц UTF-16 — Telegram её не примет", i+1, n)
		}
		if i > 0 && !strings.HasPrefix(p, fmt.Sprintf("<i>продолжение (%d/%d)</i>\n", i+1, len(parts))) {
			t.Errorf("часть %d не помечена продолжением:\n%s", i+1, p[:60])
		}
	}
}

func TestКороткийОтветНеРазъезжается(t *testing.T) {
	// Раскладка заводится ради длинных ответов и не должна трогать обычные:
	// два сообщения вместо одного — это шум, которого никто не просил.
	if parts := splitMessage("<b>Всё работает</b>\nи ничего не сломалось", telegramTextLimit); len(parts) != 1 {
		t.Errorf("короткий ответ разъехался на %d сообщений", len(parts))
	}
	if parts := splitMessage("", telegramTextLimit); len(parts) != 0 {
		t.Errorf("из пустого текста собралось %d сообщений", len(parts))
	}
}

func TestОднаОгромнаяСтрокаНеЛомаетРазметку(t *testing.T) {
	// На честных данных строки такой длины нет: и пункт, и версия обрезаны по
	// changelogWidth. Значит, это чужой файл — и лучше показать текст без
	// оформления, чем не показать ничего. Половина тега — негодный HTML, то
	// есть отказ Telegram, то есть молчание в ответ на команду.
	line := "<b>" + strings.Repeat("я & ", 4000) + "</b>"
	parts := splitMessage("<b>Шапка</b>\n"+line, telegramTextLimit)
	if len(parts) < 2 {
		t.Fatalf("строка на %d единиц не разложена", utf16Len(line))
	}
	for i, p := range parts {
		if n := utf16Len(p); n > telegramTextLimit {
			t.Errorf("часть %d: %d единиц UTF-16", i+1, n)
		}
		if !utf8.ValidString(p) {
			t.Errorf("часть %d разрезала символ UTF-8", i+1)
		}
		if strings.Count(p, "<b>") != strings.Count(p, "</b>") {
			t.Errorf("часть %d разрезана посреди разметки:\n%s", i+1, p[:80])
		}
		// Сущность разрезать тоже нельзя: «&am» — негодная разметка.
		if strings.Contains(p, "&am\n") || strings.HasSuffix(p, "&am") || strings.HasSuffix(p, "&") {
			t.Errorf("часть %d оборвана посреди сущности:\n%s", i+1, p[len(p)-40:])
		}
	}
	// Текст не потерян, потеряно только оформление одной строки.
	joined := strings.Join(parts, "")
	if n := strings.Count(joined, "&amp;"); n != 4000 {
		t.Errorf("амперсандов доехало %d из 4000", n)
	}
}

// ---------------------------------------------------------------- отправка

func TestНеудачаПосредиРядаНеСчитаетсяУспехом(t *testing.T) {
	// Половина списка выглядит как весь список — заметить пропажу владельцу
	// нечем. Поэтому отказ на любой части обязан быть отказом всей отправки:
	// иначе цикл уведомлений запишет «отправлено» и пойдёт дальше.
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("строка %03d %s", i, strings.Repeat("тема ", 12)))
	}
	text := strings.Join(lines, "\n")
	if len(splitMessage(text, telegramTextLimit)) < 3 {
		t.Fatal("подготовка теста: нужен ответ хотя бы из трёх частей")
	}

	calls := 0
	tg := testBot(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"ok":false,"description":"message is too long"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	})

	err := tg.SendLong(context.Background(), owner, text, nil)
	if err == nil {
		t.Fatal("отказ на второй части выдан за успешную отправку")
	}
	if !strings.Contains(err.Error(), "часть 2") {
		t.Errorf("в ошибке не сказано, какая часть не ушла: %v", err)
	}
	// И дальше не идём: следующие части в чат уже не попадают, иначе владелец
	// получил бы список с дырой посередине и без всякого признака дыры.
	if calls != 2 {
		t.Errorf("после отказа отправлено ещё %d частей", calls-2)
	}
}
