package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Связь «коммит — выкатка»: версия релиза обязана вести на тот самый коммит.
//
// Отдельный файл, потому что это отдельная граница доверия. Адрес приезжает из
// version.json чужого сервиса по сети, попадает в summary.json и оттуда — в
// разметку: в сообщение бота (parse_mode=HTML) и на публичную страницу.
// Проверки написаны от враждебного входа, а не от удобного.
//
// Вторая половина проверок — про ОТКАЗ строить ссылку. Она здесь важнее
// первой: неверная ссылка хуже отсутствующей, потому что отсутствие видно
// сразу, а «ведёт не туда» — только по клику.

// versionJSON собирает тело version.json ровно той формы, какой его пишет
// выкатка: список изменений — одной строкой с переводами строк.
func versionJSON(commit string, changelog ...string) string {
	m := map[string]any{
		"version": "release-20260803-101500-abc1234",
		"commit":  commit,
		"builtAt": "2026-08-03T10:15:00+03:00",
	}
	if len(changelog) > 0 {
		m["changelog"] = strings.Join(changelog, "\n")
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func serveVersion(t *testing.T, body string) (Build, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return Build{Title: "Сайт", Type: "url", Path: srv.URL}, srv.Client()
}

func TestАдресКоммитаВыводитсяИзСсылокНаPR(t *testing.T) {
	// Таблицы «сервис → репозиторий» нет и не будет: адрес приезжает от самой
	// выкатки вместе с релизом — в ссылках списка изменений, базу для которых
	// ставит тот же прогон, что собрал этот коммит.
	b, c := serveVersion(t, versionJSON("abc1234",
		"<b>Изменения</b>",
		`• Починить обрыв скачивания <a href="https://github.com/tr0llex/status.samoy.love/pull/26">#26</a>`,
	))

	got := buildInfo(b, c)
	const want = "https://github.com/tr0llex/status.samoy.love/commit/abc1234"
	if got.CommitURL != want {
		t.Errorf("ожидали адрес коммита %q, получили %q", want, got.CommitURL)
	}
	if got.Version != "release-20260803-101500-abc1234" {
		t.Errorf("версия не должна была измениться, получили %q", got.Version)
	}
}

func TestБезСсылокНаPRАдресаКоммитаНет(t *testing.T) {
	// Обычный, а не исключительный случай: генератор ставит ссылки, только
	// когда знает адрес репозитория (--link-base none, релиз без PR). Версия
	// при этом обязана доехать как доезжала — текстом.
	b, c := serveVersion(t, versionJSON("abc1234",
		"<b>Изменения</b>",
		"• Починить обрыв скачивания больших файлов",
	))

	got := buildInfo(b, c)
	if got.CommitURL != "" {
		t.Errorf("выдумывать репозиторий нечем, а ссылка появилась: %q", got.CommitURL)
	}
	if len(got.Changelog) != 1 {
		t.Errorf("список изменений при этом не должен пострадать: %q", got.Changelog)
	}
}

func TestБезСпискаИзмененийАдресаКоммитаНет(t *testing.T) {
	// version.json без changelog — это норма: поле необязательное.
	b, c := serveVersion(t, versionJSON("abc1234"))

	if got := buildInfo(b, c); got.CommitURL != "" {
		t.Errorf("ожидали пустой адрес, получили %q", got.CommitURL)
	}
}

func TestЦелиБезVersionJSONОстаютсяБезСсылки(t *testing.T) {
	// Версия цели типа release читается именем каталога, никакого version.json
	// у неё нет — значит нет ни коммита, ни адреса репозитория. Ссылки быть не
	// должно, а всё остальное обязано работать как работало.
	got := buildInfo(Build{Title: "Агент", Type: "file", Path: "/no/such/path"}, http.DefaultClient)
	if got.CommitURL != "" {
		t.Errorf("ожидали пустой адрес, получили %q", got.CommitURL)
	}
}

func TestРазныеРепозиторииВОдномСпискеОтменяютСсылку(t *testing.T) {
	// У релиза один репозиторий. Два разных адреса в одном списке означают
	// либо подделку, либо склеенный руками файл: верить нельзя ни одному.
	b, c := serveVersion(t, versionJSON("abc1234",
		`• Первое <a href="https://github.com/tr0llex/status.samoy.love/pull/26">#26</a>`,
		`• Второе <a href="https://github.com/evil/other/pull/1">#1</a>`,
	))

	if got := buildInfo(b, c); got.CommitURL != "" {
		t.Errorf("при расхождении баз ссылки быть не должно, получили %q", got.CommitURL)
	}
}

func TestАдресКоммитаНеСтроитсяПоЧужимДанным(t *testing.T) {
	// СПИСОК РАЗРЕШЁННОГО, А НЕ ЗАПРЕЩЁННОГО: ссылкой становится только
	// https-адрес github.com, собранный из проверенных кусков.
	cases := map[string]struct{ commit, item string }{
		"чужая схема": {"abc1234",
			`• Тема <a href="javascript:alert(1)/pull/1">#1</a>`},
		"http вместо https": {"abc1234",
			`• Тема <a href="http://github.com/tr0llex/repo/pull/1">#1</a>`},
		"чужой хост": {"abc1234",
			`• Тема <a href="https://gitlab.example/tr0llex/repo/pull/1">#1</a>`},
		"хост в поддомене": {"abc1234",
			`• Тема <a href="https://github.com.evil.example/o/r/pull/1">#1</a>`},
		"не ссылка на PR": {"abc1234",
			`• Тема <a href="https://github.com/tr0llex/repo/issues/1">#1</a>`},
		"выход вверх по пути": {"abc1234",
			`• Тема <a href="https://github.com/../../pull/1">#1</a>`},
		"коммит не sha": {"local",
			`• Тема <a href="https://github.com/tr0llex/repo/pull/1">#1</a>`},
		"коммит слишком короткий": {"abc12",
			`• Тема <a href="https://github.com/tr0llex/repo/pull/1">#1</a>`},
		"коммит в верхнем регистре": {"ABC1234",
			`• Тема <a href="https://github.com/tr0llex/repo/pull/1">#1</a>`},
		"коммита нет вовсе": {"",
			`• Тема <a href="https://github.com/tr0llex/repo/pull/1">#1</a>`},
		"кавычка в имени": {"abc1234",
			`• Тема <a href="https://github.com/tr0llex/re"po/pull/1">#1</a>`},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b, c := serveVersion(t, versionJSON(tc.commit, tc.item))
			got := buildInfo(b, c)
			if got.CommitURL != "" {
				t.Errorf("ожидали отказ от ссылки, получили %q", got.CommitURL)
			}
			// Отказ от украшения не должен стоить версии: ради неё
			// version.json и читается.
			if got.Version == "" {
				t.Error("версия обязана доехать в любом случае")
			}
		})
	}
}

func TestАдресКоммитаСобираетсяИзПроверенныхКусков(t *testing.T) {
	// commitURL ничего не переносит из чужой строки — он строит новую. Ни
	// разметке, ни второму атрибуту в результате взяться неоткуда.
	if got := commitURL("https://github.com/tr0llex/repo", "5486b2d"); got != "https://github.com/tr0llex/repo/commit/5486b2d" {
		t.Errorf("неожиданный адрес: %q", got)
	}
	if got := commitURL("", "5486b2d"); got != "" {
		t.Errorf("без базы ссылки быть не должно, получили %q", got)
	}
	if got := commitURL("https://github.com/tr0llex/repo", "5486b2d 5486b2d"); got != "" {
		t.Errorf("пробел в sha должен отменять ссылку, получили %q", got)
	}
}
