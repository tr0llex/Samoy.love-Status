// Телеграм-бот статуса samoy.love: отвечает на команды владельца и сам
// сообщает о падениях, восстановлениях и новых версиях.
//
// Данные не собирает: единственный источник — summary.json, который раз в
// минуту пишет агент (agent/main.go). Второй независимый обход давал бы
// расхождения между страницей и ботом, а решать, кому из них верить,
// пришлось бы владельцу.
//
// Бот живёт на том же хосте, что и сервисы, поэтому про падение самого хоста
// он сообщить не может — это работа внешнего пробера в GitHub Actions
// (scripts/probe.mjs). Зато бот замечает, что данные перестали обновляться.
//
// Токен и chat id читаются из окружения (EnvironmentFile юнита), в
// репозитории их нет и быть не должно.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// staleAfter — с какого возраста данные агента считаются несвежими.
// Глобальная переменная, потому что порог нужен и логике уведомлений, и
// форматированию ответов; задаётся один раз при старте.
var staleAfter = 5 * time.Minute

func envDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("%s=%q не разобран как длительность, беру %s", name, v, def)
		return def
	}
	return d
}

func main() {
	dataDir := flag.String("data", "/var/www/status/data", "каталог с данными агента")
	statePath := flag.String("state", "/var/lib/samoy-bot/state.json", "файл состояния бота")
	remind := flag.Duration("remind", envDuration("REMIND_INTERVAL", 15*time.Minute),
		"как часто напоминать о продолжающемся простое")
	watch := flag.Duration("watch", envDuration("WATCH_INTERVAL", 30*time.Second),
		"как часто перечитывать данные агента")
	stale := flag.Duration("stale", envDuration("STALE_AFTER", 5*time.Minute),
		"с какого возраста данные агента считаются устаревшими")
	flag.Parse()

	staleAfter = *stale

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("нет TELEGRAM_BOT_TOKEN: положите его в файл окружения службы")
	}
	ownerRaw := os.Getenv("TELEGRAM_CHAT_ID")
	owner, err := strconv.ParseInt(ownerRaw, 10, 64)
	if err != nil {
		log.Fatal("нет корректного TELEGRAM_CHAT_ID: бот обязан знать, кому отвечать")
	}
	self := os.Getenv("TELEGRAM_BOT_USERNAME")

	summaryPath := *dataDir + "/summary.json"
	pollTimeout := 30 * time.Second
	tg := newTelegram(token, pollTimeout)

	// Состояние трогают оба цикла: опрос Telegram двигает offset, наблюдение
	// за данными — историю уведомлений. Файл один, поэтому и замок один.
	var mu sync.Mutex
	st := loadState(*statePath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)

	// --- цикл уведомлений ---
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(*watch)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			s, err := loadSummary(summaryPath)
			if err != nil {
				log.Printf("данные агента не прочитаны: %v", err)
				continue
			}
			mu.Lock()
			events := st.Apply(s, time.Now().UTC(), *remind, *stale)
			if len(events) > 0 {
				if err := saveState(*statePath, st); err != nil {
					log.Printf("состояние не сохранено: %v", err)
				}
			}
			mu.Unlock()

			for _, e := range events {
				if err := tg.Send(ctx, owner, formatEvent(e)); err != nil {
					log.Printf("уведомление не отправлено (%s %s): %v", e.Kind, e.Key, err)
					continue
				}
				log.Printf("уведомление: %s %s", e.Kind, e.Key)
			}
		}
	}()

	// --- цикл команд ---
	go func() {
		defer wg.Done()
		for {
			if ctx.Err() != nil {
				return
			}
			mu.Lock()
			offset := st.Offset
			mu.Unlock()

			updates, err := tg.GetUpdates(ctx, offset, pollTimeout)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Сеть моргает, Telegram иногда отвечает 502. Пауза нужна,
				// чтобы при затяжном сбое не молотить запросами впустую.
				log.Printf("опрос Telegram не удался: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}

			for _, u := range updates {
				mu.Lock()
				if u.UpdateID >= st.Offset {
					st.Offset = u.UpdateID + 1
				}
				mu.Unlock()
				handleUpdate(ctx, tg, u, owner, self, summaryPath)
			}
			if len(updates) > 0 {
				mu.Lock()
				err := saveState(*statePath, st)
				mu.Unlock()
				if err != nil {
					log.Printf("состояние не сохранено: %v", err)
				}
			}
		}
	}()

	log.Printf("бот запущен: данные %s, напоминание раз в %s", summaryPath, *remind)
	wg.Wait()
	log.Print("бот остановлен")
}

// handleUpdate отвечает на одно сообщение.
//
// Чужие чаты игнорируются молча: любой ответ незнакомцу — это подтверждение,
// что бот жив и слушает, и приглашение продолжать.
func handleUpdate(ctx context.Context, tg *Telegram, u Update, owner int64, self, summaryPath string) {
	if u.Message == nil || u.Message.Chat.ID != owner {
		return
	}
	word := parseCommand(u.Message.Text, self)
	if word == "" {
		return
	}
	cmd := resolveCommand(word)
	if cmd == "" {
		if err := tg.Send(ctx, owner, "Не знаю такой команды.\n\n"+formatHelp()); err != nil {
			log.Printf("ответ не отправлен: %v", err)
		}
		return
	}

	now := time.Now().UTC()
	var text string
	if cmd == CmdHelp {
		text = formatHelp()
	} else {
		s, err := loadSummary(summaryPath)
		if err != nil {
			log.Printf("данные агента не прочитаны: %v", err)
			text = "🔴 Не могу прочитать данные агента — похоже, он не работает"
		} else {
			switch cmd {
			case CmdStatus:
				text = formatStatus(s, now)
			case CmdVersions:
				text = formatVersions(s, now)
			case CmdIncidents:
				text = formatIncidents(s, now)
			}
		}
	}
	if err := tg.Send(ctx, owner, text); err != nil {
		log.Printf("ответ на /%s не отправлен: %v", cmd, err)
	}
}
