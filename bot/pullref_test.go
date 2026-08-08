package main

import (
	"strings"
	"testing"
)

// Номер PR приезжает голым: генератор списка отдаёт «тема <a …>#46</a>», но
// доставка события срезает разметку намеренно — в событии обязан лежать
// простой текст. До этой правки читатель видел «#46» и уйти по нему не мог.
func TestНомерPRСтановитсяСсылкой(t *testing.T) {
	got := formatChangelog(
		[]string{"Разделить сборку данных и отрисовку в магазине косметики #46"},
		"https://github.com/tr0llex/snakes",
	)
	if !strings.Contains(got, `<a href="https://github.com/tr0llex/snakes/pull/46">#46</a>`) {
		t.Errorf("номер PR остался без ссылки:\n%s", got)
	}
	// Номер уезжает в ссылку целиком, а не остаётся ещё и текстом рядом.
	if strings.Count(got, "#46") != 1 {
		t.Errorf("номер задвоился:\n%s", got)
	}
}

// Адрес репозитория берётся только из проверенного адреса коммита. Чужая
// строка в событии не должна становиться адресом ссылки.
func TestАдресРепозиторияТолькоИзПроверенногоКоммита(t *testing.T) {
	cases := map[string]string{
		"https://github.com/tr0llex/snakes/commit/4de6bf4c2a836bacfe89a78edabd5203da4a26c9": "https://github.com/tr0llex/snakes",
		"https://evil.example/tr0llex/snakes/commit/4de6bf4":                                "",
		"javascript:alert(1)":                         "",
		"https://github.com/tr0llex/snakes/tree/main": "",
		"": "",
	}
	for in, want := range cases {
		if got := repoFromCommitURL(in); got != want {
			t.Errorf("repoFromCommitURL(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

// Без известного репозитория пункт остаётся текстом — ссылки в никуда быть не
// должно.
func TestБезРепозиторияНомерОстаётсяТекстом(t *testing.T) {
	got := formatChangelog([]string{"починить обрыв закачки #12"}, "")
	if strings.Contains(got, "<a href") {
		t.Errorf("появилась ссылка без известного репозитория:\n%s", got)
	}
	if !strings.Contains(got, "#12") {
		t.Errorf("номер потерялся вместе со ссылкой:\n%s", got)
	}
}

// «#5» посреди темы номером PR не является: уводить читателя по нему некуда.
func TestНомерВСерединеТемыНеСсылка(t *testing.T) {
	got := formatChangelog(
		[]string{"починить #5 из списка задач"},
		"https://github.com/tr0llex/snakes",
	)
	if strings.Contains(got, "<a href") {
		t.Errorf("номер в середине темы стал ссылкой:\n%s", got)
	}
}

// Ссылка, которая всё-таки приехала в строке, остаётся своей: путь «файл
// собран мимо агента» её сохраняет, и второй раз линковать нечего.
func TestПриехавшаяСсылкаНеПерестраивается(t *testing.T) {
	in := `обновить nginx до 1.24 <a href="https://github.com/tr0llex/deploy-kit/pull/21">#21</a>`
	got := formatChangelog([]string{in}, "https://github.com/tr0llex/snakes")
	if !strings.Contains(got, "deploy-kit/pull/21") {
		t.Errorf("приехавшая ссылка потеряна:\n%s", got)
	}
	if strings.Contains(got, "snakes/pull/21") {
		t.Errorf("ссылку перестроили на чужой репозиторий:\n%s", got)
	}
}
