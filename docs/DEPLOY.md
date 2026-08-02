# Выкатка и настройка

Операционные подробности вынесены сюда: в README им место занимало каждую
пятую строку, а нужны они одному человеку на одном сервере.

## Выкатка

Общим пайплайном [deploy-kit](https://github.com/tr0llex/deploy-kit). Три
независимые цели: страница, агент и бот. Правка страницы не перезапускает сбор
метрик, обновление агента не ждёт пересборки статики, перезапуск бота не
задевает ни то, ни другое.

```bash
dk deploy status-site status-agent samoy-bot   # локально, тем же путём, что и CI
dk rollback status-site --list
```

Раскладка на сервере: релизы в `<корень>/releases/<версия>`, рабочая версия —
симлинк `current`. Страница живёт в `/var/www/status`, агент в
`/opt/status-agent`, systemd-юнит запускает его через `current` и потому не
правится при выкатке.

Откат идёт без пересборки: релизы уже лежат на сервере.

Данные проверок лежат в `/var/www/status/data` — **вне** каталога релизов,
поэтому история переживает любую выкатку.

## Первичная настройка (один раз)

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin status
sudo mkdir -p /var/www/status/data /var/www/status-acme /etc/status-agent
sudo chown -R status:status /var/www/status/data
sudo install -m 0644 config/status.json /etc/status-agent/status.json
sudo install -m 0644 deploy/systemd/status-agent.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now status-agent.timer

sudo certbot certonly --webroot -w /var/www/status-acme -d status.samoy.love
```

Бот — отдельный пользователь: данные статуса ему нужны только на чтение,
и юнит закрепляет это через `ReadOnlyPaths`.

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin samoy-bot
sudo install -d -m 0755 -o samoy-bot -g samoy-bot /var/lib/samoy-bot
sudo install -d -m 0755 /etc/samoy-bot
sudo install -m 0600 /dev/null /etc/samoy-bot/env   # дальше вписать токен и chat id
sudo install -m 0644 deploy/systemd/samoy-bot.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now samoy-bot.service
```

## Бот: секреты и проверка канала

Токен и chat id — в `/etc/samoy-bot/env` (0600, root), образец:
`deploy/samoy-bot.env.example`. В репозитории значений нет.

История уведомлений и `offset` Telegram лежат в
`/var/lib/samoy-bot/state.json` — перезапуск службы не превращается в
повторную рассылку обо всём, что лежит.

Проверить канал после выкатки:

```bash
sudo systemd-run --pipe --uid=samoy-bot \
  --property=EnvironmentFile=/etc/samoy-bot/env \
  /opt/samoy-bot/current/samoy-bot -selftest
```

Придёт та же сводка, что и по `/status`. Молчащий бот неотличим от
работающего, пока что-нибудь не упадёт, — а выяснять это в момент аварии
поздно.

Адрес мини-приложения переопределяется переменной `MINIAPP_URL`: пригодится,
чтобы проверить сборку через туннель. Telegram открывает мини-приложение
только по https; если адрес другой, кнопка остаётся, но становится обычной
ссылкой.

## Внешний сторож

Секреты репозитория: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`. Без них сторож
работает молча и только пишет данные.

Проверить канал: запустить workflow `probe` вручную с галкой `notify_test` —
сводка приходит всегда, независимо от того, чей сейчас голос.

Прогнать дед-мэн и режим «сторож говорит», не дожидаясь настоящей аварии,
можно подсунув заведомо протухший или недоступный ответ:

```bash
STATUS_SUMMARY_URL=http://127.0.0.1:9999/ node scripts/probe.mjs
```

## Локально

```bash
npm install
npm run dev
cd agent && go run . -config ../config/status.json -data ../tmp-data

# Боту нужны токен и chat id в окружении и данные, собранные агентом.
cd bot && TELEGRAM_BOT_TOKEN=… TELEGRAM_CHAT_ID=… \
  go run . -data ../tmp-data -state ../tmp-data/bot-state.json
```
