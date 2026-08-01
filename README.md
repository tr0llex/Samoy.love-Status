# samoy-status

Статус-страница сервисов samoy.love — **https://status.samoy.love**

Проверки идут из GitHub Actions каждые 5 минут, страница живёт на GitHub Pages.
Это сделано намеренно: вся остальная инфраструктура стоит на одном сервере, и
статус-страница на нём была бы недоступна ровно тогда, когда нужна.

## Как устроено

```
config/services.json     что проверяем
scripts/probe.mjs        обход сервисов, история, инциденты, Telegram
data/                    результаты (коммитит сам пробер)
src/                     страница (Astro)
```

Пробер раз в 5 минут дергает каждый сервис, замеряет время ответа и остаток
срока TLS-сертификата, копит историю с прореживанием (сырые замеры 7 суток →
часовые агрегаты 90 суток → суточные за год) и складывает всё в
`data/summary.json`. Страница читает этот файл напрямую с
`raw.githubusercontent.com`, поэтому свежие данные не требуют пересборки сайта.

При смене состояния сервиса (упал / поднялся) и при приближении срока
сертификата уходит сообщение в Telegram.

## Что проверяется

Четыре сайта (samoy.love, metro, launcher, snakes), игровой сервер Snakes
(`/healthz`), API админки лаунчера, публичное API манифестов, service worker
метро — и срок действия TLS-сертификата каждого домена.

Добавить сервис — дописать объект в `config/services.json`:

```json
{
  "id": "новый",
  "name": "Отображаемое имя",
  "note": "Короткое пояснение",
  "group": "services",
  "url": "https://example.com/health",
  "expect": 200,
  "cert": "example.com",
  "bodyIncludes": "ok"
}
```

`cert` и `bodyIncludes` — необязательные.

## Настройка (один раз)

1. Репозиторий должен быть **публичным** — тогда 288 запусков cron в сутки не
   тратят лимит минут.
2. Settings → Pages → Source: **GitHub Actions**.
3. Settings → Secrets and variables → Actions:
   - `TELEGRAM_BOT_TOKEN` — токен от [@BotFather](https://t.me/BotFather)
   - `TELEGRAM_CHAT_ID` — свой id можно узнать у [@userinfobot](https://t.me/userinfobot)
4. DNS: `status.samoy.love` → CNAME на `<username>.github.io`
   (файл `public/CNAME` уже в репозитории).
5. Если репозиторий назван иначе, поправить ссылку на данные в
   `src/pages/index.astro` (константа `DATA_URL`) или задать `PUBLIC_DATA_URL`.

> GitHub отключает cron в публичных репозиториях после 60 дней без активности.
> Здесь это не грозит: пробер сам коммитит данные каждые 5 минут.

## Локально

```bash
npm install
npm run probe    # обойти сервисы прямо сейчас (без Telegram)
npm run dev      # страница на localhost
npm run build
```
