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
npm run e2e            # сквозные тесты по локальной сборке
cd agent && go run . -config ../config/status.json -data ../tmp-data
```

## Сквозные тесты

`npm run e2e` собирает страницу, поднимает `preview` и проходит её как
пользователь. Браузер ставится один раз: `npx playwright install chromium`.

Данные проверок подменяются наборами из `e2e/fixtures/` — ждать, пока прод сам
сломается, чтобы узнать, переживёт ли это страница, плохой план. Часы в тестах
зафиксированы: страница печатает относительное время, и без этого тест зеленел
бы сегодня и краснел завтра сам по себе.

Покрыто: карточки проектов с версиями выкаченного, состоянием служб,
процентами аптайма и полосками на 90 суток; сбой доезжает до баннера, до
карточки, до причины падения и до списка инцидентов; пустой список проектов,
сервис без истории (прочерки вместо `NaN`), недельной давности данные и
недоступный `summary.json` — ни один из этих случаев не роняет страницу и не
оставляет её в состоянии «Загружаю данные…»; на ширине телефона карточки и
полоски аптайма помещаются в экран.

Сквозная проверка во всех тестах: любая ошибка в консоли браузера и любой
неудачный сетевой запрос валят тест. Страница, отдающая 200 с упавшим на
неожиданном поле скриптом, здесь красная — по коду ответа она выглядела бы живой.

Смоук по живому сайту запускается отдельно и руками, в CI на PR не висит. Он же
ловит молча умерший агент: данные старше часа — это красный прогон, а не
бодрое «всё работает» недельной давности.

```bash
npm run e2e:prod                          # https://status.samoy.love
E2E_PROD_URL=https://... npm run e2e:prod # другой адрес
```
