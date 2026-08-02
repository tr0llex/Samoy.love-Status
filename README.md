# samoy-status

[![CI](https://github.com/tr0llex/Samoy.love-Status/actions/workflows/ci.yml/badge.svg)](https://github.com/tr0llex/Samoy.love-Status/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/tr0llex/Samoy.love-Status/branch/main/graph/badge.svg)](https://codecov.io/gh/tr0llex/Samoy.love-Status)


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

Версии пока точны только там, где релизы разложены каталогами (Snakes:
`current -> releases/20260801-225039-5486b2d`). Для остальных показывается
дата выкатки. Единый `/version.json` во всех сервисах — задача общего
пайплайна `deploy-kit`.

## Установка на сервер

```bash
# 1. Собрать агента (Go есть на сервере)
cd agent && go build -o status-agent .

# 2. Разложить
sudo useradd --system --no-create-home --shell /usr/sbin/nologin status
sudo mkdir -p /var/www/status/site /var/www/status/data /var/www/status-acme /etc/status-agent
sudo chown -R status:status /var/www/status/data
sudo install -m 0755 status-agent /usr/local/bin/status-agent
sudo install -m 0644 ../config/status.json /etc/status-agent/status.json
sudo install -m 0644 ../deploy/systemd/status-agent.{service,timer} /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now status-agent.timer

# 3. Страница
npm ci && npm run build
rsync -a --delete dist/ /var/www/status/site/

# 4. nginx (после того, как A-запись домена указывает на сервер)
sudo install -m 0644 deploy/nginx/status.samoy.love.conf /etc/nginx/sites-available/
sudo ln -sfn /etc/nginx/sites-available/status.samoy.love.conf /etc/nginx/sites-enabled/
sudo certbot certonly --webroot -w /var/www/status-acme -d status.samoy.love
sudo nginx -t && sudo systemctl reload nginx
```

Данные лежат в `/var/www/status/data` — **вне** каталога сайта, чтобы выкатка
с `--delete` не снесла историю.

## Внешний сторож

Секреты репозитория: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`.
Проверить канал: запустить workflow `probe` вручную с галкой `notify_test` —
придёт сводка текущего состояния.

## Локально

```bash
npm install
npm run dev
cd agent && go run . -config ../config/status.json -data ../tmp-data
```
