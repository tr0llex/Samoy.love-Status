# samoy-status

[![CI](https://github.com/tr0llex/status.samoy.love/actions/workflows/ci.yml/badge.svg)](https://github.com/tr0llex/status.samoy.love/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/tr0llex/status.samoy.love/branch/main/graph/badge.svg)](https://codecov.io/gh/tr0llex/status.samoy.love)
[![прод](https://img.shields.io/website?url=https%3A%2F%2Fstatus.samoy.love&up_message=online&up_color=2ea043&down_message=offline&label=status.samoy.love)](https://status.samoy.love)

Статус-страница сервисов samoy.love — **https://status.samoy.love**

## Как устроено

Две независимые части, каждая делает то, чего не может другая.

**Агент на сервере** (`agent/`, Go) — раз в минуту обходит сервисы, читает
состояние systemd-юнитов и даты выкаток, копит историю и складывает готовый
JSON рядом со страницей. Он живёт на том же хосте, что и сами сервисы: только
так видно юниты и версии — но при падении хоста страница будет недоступна.

**Внешний сторож** (`scripts/probe.mjs`, GitHub Actions) — обходит те же
эндпоинты снаружи и пишет в Telegram при смене состояния. Это единственное,
что переживает падение сервера.

```
agent/main.go        агент: проверки, systemd, версии, история
config/status.json   единый конфиг для агента и сторожа
deploy/              nginx, systemd-юнит и таймер
scripts/probe.mjs    внешний сторож (только уведомления)
src/                 страница (Astro)
```

## Что показывается

Проекты сверху вниз по важности; внутри каждого — проверки со шкалой
доступности за 90 дней, аптайм за 24 часа / 7 дней / 90 дней, время ответа и
остаток срока TLS-сертификата. Ниже — состояние служб и что именно сейчас
выкачено: версия и дата сборки.

Внутренние метрики (диск, память, счётчики рестартов) наружу не отдаются.

Версии берутся из `/version.json`, который кладёт общий пайплайн: отвечает
сам работающий сервис, а не диск, — значит показано то, что реально крутится.
Там, где сервис такого файла не отдаёт (бинари админки лаунчера), версия
читается из имени каталога релиза.

## Выкатка

Общим пайплайном [deploy-kit](https://github.com/tr0llex/deploy-kit). Две
независимые цели: страница и агент. Правка страницы не перезапускает сбор
метрик, обновление агента не ждёт пересборки статики.

```bash
dk deploy status-site status-agent   # локально, тем же путём, что и CI
dk rollback status-site --list
```

Раскладка на сервере: релизы в `<корень>/releases/<версия>`, рабочая версия —
симлинк `current`. Страница живёт в `/var/www/status`, агент в
`/opt/status-agent`, systemd-юнит запускает его через `current` и потому не
правится при выкатке.

Откат идёт без пересборки: релизы уже лежат на сервере.

Данные проверок лежат в `/var/www/status/data` — **вне** каталога релизов,
поэтому история переживает любую выкатку.

### Первичная настройка (один раз)

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin status
sudo mkdir -p /var/www/status/data /var/www/status-acme /etc/status-agent
sudo chown -R status:status /var/www/status/data
sudo install -m 0644 config/status.json /etc/status-agent/status.json
sudo install -m 0644 deploy/systemd/status-agent.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now status-agent.timer

sudo certbot certonly --webroot -w /var/www/status-acme -d status.samoy.love
```

## Внешний сторож

Пробер `probe.yml` ходит по сервисам из GitHub Actions, а не с сервера, — и это
намеренно: агент на упавшем хосте не может сообщить, что хост упал. Данные
пробер пишет в ветку `status-data`.

Секреты репозитория: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`.
Проверить канал: запустить workflow `probe` вручную с галкой `notify_test` —
придёт сводка текущего состояния.

## Локально

```bash
npm install
npm run dev
cd agent && go run . -config ../config/status.json -data ../tmp-data
```
