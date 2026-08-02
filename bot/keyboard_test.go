package main

import (
	"strings"
	"testing"
)

func TestКлавиатураПомечаетТекущийЭкран(t *testing.T) {
	kb := navKeyboard(ViewVersions)
	var marked []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.Text, "· ") {
				marked = append(marked, b.Text)
			}
		}
	}
	if len(marked) != 1 {
		t.Fatalf("помечен должен быть ровно один экран, получили %v", marked)
	}
	if !strings.Contains(marked[0], "Версии") {
		t.Errorf("помечен не тот экран: %q", marked[0])
	}
}

func TestКнопкаОбновитьВедётНаТотЖеЭкран(t *testing.T) {
	// Иначе «Обновить» на экране инцидентов молча возвращало бы на статус.
	for _, view := range []string{ViewStatus, ViewVersions, ViewIncidents} {
		kb := navKeyboard(view)
		var found bool
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if strings.Contains(b.Text, "Обновить") {
					found = true
					if b.CallbackData != view {
						t.Errorf("на экране %s «Обновить» ведёт на %s", view, b.CallbackData)
					}
				}
			}
		}
		if !found {
			t.Errorf("на экране %s нет кнопки «Обновить»", view)
		}
	}
}

func TestКнопкаМиниПриложенияТребуетHTTPS(t *testing.T) {
	// Telegram открывает мини-приложение только по https. Если адрес другой,
	// кнопка обязана остаться, но уже обычной ссылкой — иначе она молча
	// перестала бы работать.
	// Адрес приходит из конфига — единственного места настроек.
	t.Cleanup(func() { applyConfig(Config{MiniApp: "https://status.samoy.love/tg/"}) })

	applyConfig(Config{MiniApp: "https://status.samoy.love/tg/"})
	if b := openButton(); b.WebApp == nil || b.URL != "" {
		t.Errorf("для https ожидали web_app, получили %+v", b)
	}
	applyConfig(Config{MiniApp: "http://localhost:4331/tg/"})
	if b := openButton(); b.WebApp != nil || b.URL == "" {
		t.Errorf("для http ожидали обычную ссылку, получили %+v", b)
	}
}

func TestНеизвестнаяКнопкаНеЛомаетЭкран(t *testing.T) {
	// Сообщение могло быть отправлено прошлой версией бота с другими кнопками.
	if got := viewOf("несуществующая"); got != ViewStatus {
		t.Errorf("неизвестная команда должна вести на статус, получили %q", got)
	}
}

func TestЭкранСправкиНеЧитаетДанные(t *testing.T) {
	// Справка обязана открываться, даже когда агент не работает: это
	// единственный экран, которому данные не нужны.
	got := renderView(ViewHelp, "/нет/такого/файла.json", base)
	if !strings.Contains(got, "Статус samoy.love") {
		t.Errorf("справка не отрисовалась: %q", got)
	}
}

func TestЭкранСообщаетОНедоступныхДанных(t *testing.T) {
	got := renderView(ViewStatus, "/нет/такого/файла.json", base)
	if !strings.Contains(got, "данные агента") && !strings.Contains(got, "не работает") {
		t.Errorf("о нечитаемых данных надо сказать прямо: %q", got)
	}
}
