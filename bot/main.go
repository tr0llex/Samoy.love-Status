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
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// staleAfter — с какого возраста данные агента считаются несвежими.
// Глобальная переменная, потому что порог нужен и логике уведомлений, и
// форматированию ответов; задаётся один раз при старте.
var staleAfter = 5 * time.Minute

// Состояние и его замок — на уровне пакета: к ним обращаются оба цикла и
// обработчики нажатий. Раньше это были локальные переменные main, и добавить
// действие, меняющее состояние, было некуда.
var (
	mu        sync.Mutex
	botState  *State
	statePath string
)

// metrics — счётчики процесса. nil, пока main их не завёл: все методы
// безопасны на nil-приёмнике, поэтому тесты обработчиков ничего не настраивают.
var metrics *botMetrics

func main() {
	// Единственный флаг — действие, а не настройка: всё остальное живёт в
	// окружении, одним местом (см. config.go).
	selftest := flag.Bool("selftest", false,
		"отправить владельцу текущее состояние и выйти — проверка канала после выкатки")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	staleAfter = cfg.Stale
	applyConfig(cfg)

	// Счётчики — пакетная переменная, а не параметр каждой функции: наблюдение
	// не должно просачиваться в подписи обработчиков команд, которые про него
	// ничего знать не обязаны.
	metrics = newBotMetrics(cfg.Metrics, time.Now())
	if err := metrics.flush(time.Now()); err != nil {
		log.Printf("метрики не записаны (%s): %v", cfg.Metrics, err)
	}

	summaryPath := cfg.SummaryPath()
	pollTimeout := 30 * time.Second
	tg := newTelegram(cfg.Token, pollTimeout)

	// Состояние трогают оба цикла и нажатия на кнопки. Файл один — замок один.
	statePath = cfg.State
	botState = loadState(cfg.State)
	st := botState

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Проверка канала после выкатки. Молчащий бот неотличим от работающего,
	// пока что-нибудь не упадёт, — а выяснять это в момент аварии поздно.
	if *selftest {
		if err := sendCurrentStatus(ctx, tg, cfg.Owner, summaryPath); err != nil {
			log.Fatalf("проверка не прошла: %v", err)
		}
		log.Print("проверка прошла: сводка отправлена владельцу")
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// --- цикл уведомлений ---
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(cfg.Watch)
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
			now := time.Now().UTC()
			mu.Lock()
			events := st.Apply(s, now, cfg.Remind, cfg.Stale)
			if len(events) > 0 {
				if err := saveState(cfg.State, st); err != nil {
					log.Printf("состояние не сохранено: %v", err)
				}
			}
			muted, until := st.Muted(now)
			mu.Unlock()

			// Тишина глушит только шум: напоминания о том, что и так уже
			// известно. Само падение, восстановление и новая версия проходят
			// всегда — иначе «тихо до утра» означало бы «не сообщай мне о
			// новых авариях», а просили не этого.
			if muted {
				kept := events[:0]
				for _, e := range events {
					if e.Kind != KindStillDown {
						kept = append(kept, e)
					}
				}
				if len(kept) != len(events) {
					log.Printf("тишина до %s: придержал %d напоминаний",
						until.Format(time.RFC3339), len(events)-len(kept))
				}
				events = kept
			}

			for _, e := range events {
				if err := tg.SendWith(ctx, cfg.Owner, formatEvent(e), alertKeyboard(e.Project)); err != nil {
					metrics.sendFailed()
					log.Printf("уведомление не отправлено (%s %s): %v", e.Kind, e.Key, err)
					continue
				}
				metrics.notified(string(e.Kind), time.Now().UTC())
				log.Printf("уведомление: %s %s", e.Kind, e.Key)
			}

			// Файл переписывается на каждом обходе, а не только при событии:
			// по отметке heartbeat видно, что бот жив, даже когда всё спокойно
			// и уведомлять не о чем.
			if err := metrics.flush(time.Now()); err != nil {
				log.Printf("метрики не записаны: %v", err)
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
				metrics.pollFailed()
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
				handleUpdate(ctx, tg, u, cfg.Owner, cfg.Self, summaryPath)
			}
			if len(updates) > 0 {
				mu.Lock()
				err := saveState(cfg.State, st)
				mu.Unlock()
				if err != nil {
					log.Printf("состояние не сохранено: %v", err)
				}
			}
		}
	}()

	log.Printf("бот запущен: данные %s, напоминание раз в %s", summaryPath, cfg.Remind)
	wg.Wait()
	log.Print("бот остановлен")
}

// sendCurrentStatus отправляет владельцу то же, что он получил бы по /status.
func sendCurrentStatus(ctx context.Context, tg *Telegram, owner int64, summaryPath string) error {
	s, err := loadSummary(summaryPath)
	if err != nil {
		return err
	}
	return tg.SendWith(ctx, owner, formatStatus(s, time.Now().UTC()), statusKeyboard(s))
}

// handleUpdate отвечает на одно сообщение.
//
// Чужие чаты игнорируются молча: любой ответ незнакомцу — это подтверждение,
// что бот жив и слушает, и приглашение продолжать.
func handleUpdate(ctx context.Context, tg *Telegram, u Update, owner int64, self, summaryPath string) {
	// Нажатие на кнопку: перерисовываем тот же экран на месте.
	if q := u.CallbackQuery; q != nil {
		handleCallback(ctx, tg, q, owner, summaryPath)
		return
	}
	if u.Message == nil || u.Message.Chat.ID != owner {
		return
	}
	word := parseCommand(u.Message.Text, self)
	if word == "" {
		return
	}
	cmd := resolveCommand(word)
	if cmd == "" {
		if err := tg.SendWith(ctx, owner, "Не знаю такой команды.\n\n"+formatHelp(), navKeyboard(ViewHelp)); err != nil {
			log.Printf("ответ не отправлен: %v", err)
		}
		return
	}

	// Команды логируются: без этого не понять, дошло ли сообщение до бота,
	// когда владельцу кажется, что тот молчит. Текст не пишем — в журнале
	// ему делать нечего.
	metrics.command(cmd)
	log.Printf("команда /%s", cmd)

	view := viewOf(cmd)
	text, kb := renderView(view, summaryPath, time.Now().UTC())
	if err := tg.SendWith(ctx, owner, text, kb); err != nil {
		metrics.sendFailed()
		log.Printf("ответ на /%s не отправлен: %v", cmd, err)
	}
}
