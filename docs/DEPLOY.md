# Выкатка и настройка

Операционные подробности вынесены сюда: в README им место занимало каждую
пятую строку, а нужны они одному человеку на одном сервере.

## Выкатка

Общим пайплайном [deploy-kit](https://github.com/tr0llex/deploy-kit). Три
независимые цели: страница, агент и бот. Правка страницы не перезапускает сбор
метрик, обновление агента не ждёт пересборки статики, перезапуск бота не
задевает ни то, ни другое.

```bash
dk deploy status-site status-agent samoylove-bot   # локально, тем же путём, что и CI
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
sudo useradd --system --no-create-home --shell /usr/sbin/nologin samoylove-bot
sudo install -d -m 0755 -o samoylove-bot -g samoylove-bot /var/lib/samoylove-bot
sudo install -d -m 0755 /etc/samoylove-bot
sudo install -m 0600 /dev/null /etc/samoylove-bot/env   # дальше вписать токен и chat id
sudo install -m 0644 deploy/systemd/samoylove-bot.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now samoylove-bot.service
```

## Переезд бота на новое имя (один раз)

Бот переименован из `samoy-bot` в `samoylove-bot`. Пути, юнит, пользователь и
каталог секретов сменились вместе с ним, а на хосте остались старые — выкатка
падает на `chown: invalid group`. Один раз нужно сделать это руками:

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin samoylove-bot
sudo install -d -m 0755 -o samoylove-bot -g samoylove-bot /var/lib/samoylove-bot
sudo install -d -m 0755 /etc/samoylove-bot

# Секреты переносим, а не заводим заново: chat id и токен те же.
sudo mv /etc/samoy-bot/env /etc/samoylove-bot/env
sudo chmod 0600 /etc/samoylove-bot/env

# Историю уведомлений тоже: без неё бот при старте объявит заново всё,
# что сейчас лежит, и повторит все известные ему версии.
sudo mv /var/lib/samoy-bot/state.json /var/lib/samoylove-bot/state.json
sudo chown samoylove-bot:samoylove-bot /var/lib/samoylove-bot/state.json

sudo install -m 0644 deploy/systemd/samoylove-bot.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl disable --now samoy-bot.service
sudo systemctl enable --now samoylove-bot.service
```

После этого выкатка бота проходит обычным порядком. Старое можно убрать:

```bash
sudo rm -rf /opt/samoy-bot /var/lib/samoy-bot /etc/samoy-bot
sudo rm -f /etc/systemd/system/samoy-bot.service
sudo userdel samoy-bot
```

## Бот: секреты и проверка канала

Токен и chat id — в `/etc/samoylove-bot/env` (0600, root), образец:
`deploy/samoylove-bot.env.example`. В репозитории значений нет.

История уведомлений и `offset` Telegram лежат в
`/var/lib/samoylove-bot/state.json` — перезапуск службы не превращается в
повторную рассылку обо всём, что лежит.

Проверить канал после выкатки:

```bash
sudo systemd-run --pipe --uid=samoylove-bot \
  --property=EnvironmentFile=/etc/samoylove-bot/env \
  /opt/samoylove-bot/current/samoylove-bot -selftest
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
