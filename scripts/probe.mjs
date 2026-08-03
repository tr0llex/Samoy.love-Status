// ВНЕШНИЙ СТОРОЖ. Саму статус-страницу строит агент на сервере
// (agent/main.go); этот скрипт нужен ради того, чего сервер про себя сделать
// не может, — сообщить, что он недоступен или замолчал.
//
// Запускается из GitHub Actions, обходит те же эндпоинты снаружи и пишет в
// Telegram. Правила оценки здесь ДОСЛОВНО те же, что в агенте: код ответа,
// тип содержимого, маркер в теле, конечный хост после редиректов, порог
// времени и подтверждение сбоя повтором. Раньше реализации расходились — в
// стороже была проверка тела, которой не знал агент, и включение её дало бы
// сторожа, шлющего тревогу, и страницу, уверяющую, что всё работает.
//
// Хранение с прореживанием, чтобы репозиторий не рос бесконечно:
//   data/raw/<id>.json     — сырые замеры, 7 суток
//   data/hourly/<id>.json  — часовые агрегаты, 90 суток
//   data/daily/<id>.json   — суточные агрегаты, год
//   data/state.json        — текущее состояние и момент последней смены
//   data/incidents.json    — история падений
//   data/summary.json      — сводка в формате агента плюс возраст обхода
//
// Локальный прогон: npm run probe (уведомления не отправятся без токена).

import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { connect } from 'node:tls';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));

// Конфиг и каталог данных переопределяются окружением — по той же причине,
// что и STATUS_SUMMARY_URL ниже: правила оценки написаны здесь и в агенте
// (agent/main.go), и единственный способ убедиться, что они не разъехались, —
// прогнать обе реализации по одному набору случаев и сравнить вердикты.
// См. scripts/conformance.test.mjs.
const DATA = process.env.STATUS_DATA ?? join(ROOT, 'data');
const CONFIG_PATH = process.env.STATUS_CONFIG ?? join(ROOT, 'config/status.json');

const TIMEOUT_MS = 12_000;
const RAW_MS = 7 * 24 * 3600_000;
const HOURLY_KEEP = 90 * 24;
const DAILY_KEEP = 365;
const INCIDENTS_KEEP = 50;
const DAYS_ON_PAGE = 90;

// Пауза перед подтверждающим запросом — как в агенте.
const CONFIRM_DELAY_MS = 2_000;

// Сколько обходов подряд надо увидеть новое состояние, прежде чем поверить в
// него и написать в Telegram. Сервис, моргающий раз в минуту, иначе
// превращает канал в шум, и настоящая авария в нём тонет.
const CONFIRM_RUNS = 2;

// Доля лежащих критичных проверок, при которой «частичный» перестаёт быть
// честным словом. Значение то же, что в агенте.
const MAJOR_SHARE = 0.5;

// Насколько устаревшие данные агента считаем молчанием. Агент ходит раз в
// минуту; десять пропущенных запусков — это уже не запаздывание.
//
// Порог описывает ВОЗРАСТ ДАННЫХ, а не скорость реакции: узнать о молчании
// агента раньше, чем GitHub запустит этот скрипт, нельзя, а запускает он его
// далеко не каждые пять минут (см. PROBE_ABSENT_MS ниже).
const AGENT_STALE_MS = 10 * 60_000;

// Сколько сторож обязан молчать, чтобы это перестало быть нормой GitHub.
//
// Расписание в probe.yml просит запуск раз в пять минут, но cron у GitHub —
// просьба, а не обязательство: под нагрузкой запуски пропускаются целыми
// часами. Замеренные промежутки между настоящими запусками за сутки — от часа
// до трёх с половиной. Шесть часов лежат за пределами всего наблюдавшегося и
// означают уже не разрежение расписания, а его остановку.
//
// Заметить это больше некому. Страница показывает данные агента, а не наши, и
// «внешней проверки не было полдня» из неё не следует никак: последняя сводка
// сторожа лежит в ветке ровно такой же на вид, свежая она или трёхчасовая.
// Поэтому вернувшийся после перерыва обход сообщает о нём сам — один раз.
const PROBE_ABSENT_MS = 6 * 3600_000;
// Адрес переопределяется переменной окружения — чтобы дед-мэн можно было
// прогнать против заведомо протухшего ответа, а не ждать настоящей аварии.
const STATUS_SUMMARY_URL =
  process.env.STATUS_SUMMARY_URL ?? 'https://status.samoy.love/data/summary.json';

const now = Date.now();

// ---------------------------------------------------------------- утилиты

async function readJson(file, fallback) {
  try {
    return JSON.parse(await readFile(file, 'utf8'));
  } catch {
    return fallback;
  }
}

async function writeJson(file, value) {
  await mkdir(dirname(file), { recursive: true });
  await writeFile(file, JSON.stringify(value), 'utf8');
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const hourBucket = (ts) => Math.floor(ts / 3600_000) * 3600_000;
const dayKey = (ts) => new Date(ts).toISOString().slice(0, 10);

// Критичность по умолчанию включена: второстепенность должна быть решением,
// записанным в конфиг, а не следствием незаполненного поля.
const isCritical = (check) => check.critical !== false;

// ---------------------------------------------------------------- проверки

const UP = 'up';
const SLOW = 'slow';
const DOWN = 'down';

/** Один запрос со всеми проверками, которые про него известны. */
async function runStep(step, check, vars) {
  const expect = step.expect ?? 200;

  let raw = step.url;
  for (const [k, v] of Object.entries(vars)) raw = raw.replaceAll(`{${k}}`, v);
  if (/\{[^}]+\}/.test(raw)) {
    return { status: DOWN, code: 0, error: `в URL осталась неподставленная переменная: ${raw}` };
  }

  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  try {
    const res = await fetch(raw, {
      signal: ctrl.signal,
      redirect: 'follow',
      headers: { 'user-agent': 'samoylove-status-probe (+https://status.samoy.love)' },
    });

    if (res.status !== expect) {
      return { status: DOWN, code: res.status, error: `HTTP ${res.status} вместо ${expect}` };
    }

    // Куда в итоге привёл редирект: угнанный домен или кривой конфиг nginx
    // иначе выглядят здоровьем — чужой сервер ответил 200, и ладно.
    const from = new URL(raw).hostname;
    const to = new URL(res.url).hostname;
    if (
      to !== from &&
      !(check.allowHosts ?? []).some((h) => h.toLowerCase() === to.toLowerCase())
    ) {
      return { status: DOWN, code: res.status, error: `редирект увёл на посторонний хост ${to}` };
    }

    if (step.expectType) {
      const ct = res.headers.get('content-type') ?? '';
      if (!ct.toLowerCase().includes(step.expectType.toLowerCase())) {
        return {
          status: DOWN,
          code: res.status,
          error: `Content-Type "${ct}" вместо "${step.expectType}"`,
        };
      }
    }

    if (!step.bodyIncludes && !step.capture) {
      return { status: UP, code: res.status, error: null };
    }

    const body = await res.text();
    if (step.bodyIncludes && !body.includes(step.bodyIncludes)) {
      return {
        status: DOWN,
        code: res.status,
        error: `в ответе нет "${step.bodyIncludes}" — код 200, но содержимое не то`,
      };
    }
    for (const [name, field] of Object.entries(step.capture ?? {})) {
      let doc;
      try {
        doc = JSON.parse(body);
      } catch {
        return {
          status: DOWN,
          code: res.status,
          error: `ответ не разбирается как JSON, брать ${field} не из чего`,
        };
      }
      if (!(field in doc)) {
        return { status: DOWN, code: res.status, error: `в ответе нет поля "${field}"` };
      }
      vars[name] = String(doc[field]);
    }
    return { status: UP, code: res.status, error: null };
  } catch (e) {
    return {
      status: DOWN,
      code: 0,
      error: e.name === 'AbortError' ? `таймаут ${TIMEOUT_MS / 1000}s` : String(e.message || e),
    };
  } finally {
    clearTimeout(timer);
  }
}

/** Один проход проверки: сценарий или одиночный запрос, плюс порог времени. */
async function checkOnce(check) {
  const started = Date.now();
  let r;
  if (check.steps?.length) {
    const vars = {};
    for (const step of check.steps) {
      r = await runStep(step, check, vars);
      if (r.status === DOWN) {
        r = { ...r, error: `шаг «${step.name ?? step.url}»: ${r.error}` };
        break;
      }
    }
  } else {
    r = await runStep(
      {
        url: check.url,
        expect: check.expect,
        bodyIncludes: check.bodyIncludes,
        expectType: check.expectType,
      },
      check,
      {},
    );
  }
  const ms = Date.now() - started;

  // Порог времени применяем только к успешному ответу: у упавшей проверки
  // «медленно» ничего не добавляет к «не работает».
  if (r.status === UP && check.slowMs > 0 && ms > check.slowMs) {
    return { ...r, ms, status: SLOW, error: `ответ за ${ms} мс при пороге ${check.slowMs} мс` };
  }
  return { ...r, ms };
}

/** Сбой подтверждается вторым запросом; успех принимаем сразу. */
async function checkService(check) {
  const first = await checkOnce(check);
  if (first.status !== DOWN) return { ...first, attempts: 1 };
  await sleep(CONFIRM_DELAY_MS);
  const second = await checkOnce(check);
  return { ...second, attempts: 2 };
}

/**
 * Сколько суток осталось сертификату и годен ли он вообще.
 *
 * Соединяемся без проверки цепочки, чтобы сертификат достался нам даже когда
 * он негоден: иначе про истёкший нельзя сказать ничего, кроме «не вышло», и
 * плашка просто исчезает — ровно тогда, когда она нужнее всего.
 */
function checkCert(host) {
  return new Promise((resolve) => {
    const done = (days, state) => resolve({ days, state });
    const socket = connect(
      { host, port: 443, servername: host, timeout: TIMEOUT_MS, rejectUnauthorized: false },
      () => {
        const cert = socket.getPeerCertificate();
        const authorized = socket.authorized;
        socket.end();
        if (!cert?.valid_to) return done(null, 'unreachable');
        const expiresAt = Date.parse(cert.valid_to);
        const days = Math.floor((expiresAt - now) / 86_400_000);
        if (expiresAt < now) return done(days, 'expired');
        if (!authorized) return done(days, 'invalid');
        done(days, 'ok');
      },
    );
    socket.on('error', () => done(null, 'unreachable'));
    socket.on('timeout', () => {
      socket.destroy();
      done(null, 'unreachable');
    });
  });
}

// ---------------------------------------------------------------- агрегация

/** Обновляет корзину агрегата на месте: счётчики + скользящее среднее. */
function bump(buckets, key, ok, ms, keep) {
  const last = buckets[buckets.length - 1];
  if (last && last[0] === key) {
    last[1] += ok ? 1 : 0;
    last[2] += 1;
    last[3] = Math.round((last[3] * (last[2] - 1) + ms) / last[2]);
  } else {
    buckets.push([key, ok ? 1 : 0, 1, ms]);
  }
  if (buckets.length > keep) buckets.splice(0, buckets.length - keep);
  return buckets;
}

const pct = (up, total) => (total ? Math.round((up / total) * 10000) / 100 : null);

// Аптайм считаем по календарному окну, а не по числу корзин: корзина заводится
// только когда сторож отработал, и «последние 90 корзин» незаметно
// растягивались бы на большее число суток, пряча собственные простои.
function dayWindow(n) {
  const keys = [];
  for (let i = n - 1; i >= 0; i--) keys.push(dayKey(now - i * 86_400_000));
  return keys;
}

const indexBuckets = (buckets) => new Map(buckets.map((b) => [String(b[0]), b]));

function uptimeOverDays(daily, n) {
  const idx = indexBuckets(daily);
  let up = 0;
  let total = 0;
  for (const key of dayWindow(n)) {
    const b = idx.get(key);
    if (b) {
      up += b[1];
      total += b[2];
    }
  }
  return pct(up, total);
}

function uptimeOverHours(hourly, hours) {
  const idx = indexBuckets(hourly);
  let up = 0;
  let total = 0;
  for (let i = 0; i < hours; i++) {
    const b = idx.get(String(hourBucket(now - i * 3600_000)));
    if (b) {
      up += b[1];
      total += b[2];
    }
  }
  return pct(up, total);
}

/** Ровно n ячеек по календарю; сутки без замеров — null, а не сдвиг истории. */
function daysForPage(daily, n) {
  const idx = indexBuckets(daily);
  return dayWindow(n).map((key) => {
    const b = idx.get(key);
    if (!b || !b[2]) return null;
    return { d: key, up: b[1], total: b[2], avgMs: b[3] };
  });
}

/** Аптайм по сырым замерам за последние N часов. */
function uptimeRaw(raw, hours) {
  const from = now - hours * 3600_000;
  let up = 0;
  let total = 0;
  for (const [ts, ok] of raw) {
    if (ts >= from) {
      total += 1;
      up += ok;
    }
  }
  return pct(up, total);
}

/** Вердикт проекта — по критичным проверкам. Те же правила, что в агенте. */
function projectStatus(p) {
  if (p.total === 0) return p.auxDown > 0 ? 'degraded' : UP;
  if (p.up === 0) return DOWN;
  if (p.up < p.total) return 'degraded';
  if (p.slow > 0) return 'degraded';
  return UP;
}

function overallStatus(projects) {
  let critical = 0;
  let down = 0;
  let slow = 0;
  let auxDown = 0;
  for (const p of projects) {
    critical += p.total;
    down += p.total - p.up;
    slow += p.slow;
    auxDown += p.auxDown;
  }
  // Критичных проверок нет вовсе — судить не по чему, кроме второстепенных.
  // Молчать о лежащей второстепенной здесь нельзя: агент в этом же случае
  // отвечает «частично» (agent/main.go, overallStatus), и сводки разошлись бы.
  if (critical === 0) return auxDown > 0 ? 'degraded' : 'operational';
  if (down === critical) return 'down';
  if (down / critical >= MAJOR_SHARE) return 'major';
  if (down > 0 || slow > 0 || auxDown > 0) return 'degraded';
  return 'operational';
}

// ---------------------------------------------------------------- телеграм

async function notify(text) {
  const token = process.env.TELEGRAM_BOT_TOKEN;
  const chat = process.env.TELEGRAM_CHAT_ID;
  if (!token || !chat) {
    console.log('[notify] пропущено (нет TELEGRAM_BOT_TOKEN/CHAT_ID):', text);
    return;
  }
  try {
    const res = await fetch(`https://api.telegram.org/bot${token}/sendMessage`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        chat_id: chat,
        text,
        parse_mode: 'HTML',
        disable_web_page_preview: true,
      }),
    });
    if (!res.ok) console.error('[notify] telegram ответил', res.status);
  } catch (e) {
    console.error('[notify] ошибка отправки:', e.message);
  }
}

const humanDuration = (ms) => {
  const min = Math.round(ms / 60_000);
  if (min < 60) return `${min} мин`;
  const h = Math.floor(min / 60);
  return `${h} ч ${min % 60} мин`;
};

const esc = (s) =>
  String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');

// Подпись со ссылкой на сам сервис: увидел «недоступен» — хочешь открыть и
// посмотреть, а не искать адрес руками. Пускаем только http(s).
const link = (text, url) =>
  typeof url === 'string' && /^https?:\/\//.test(url)
    ? `<a href="${esc(url)}">${esc(text)}</a>`
    : esc(text);

// ---------------------------------------------------------------- дед-мэн
//
// Жив ли агент — вопрос не только сам по себе. От ответа зависит, говорит ли
// сторож вообще: пока агент пишет свежие данные, о сервисах в Telegram
// рассказывает бот на сервере, и дублировать его незачем.
async function probeAgent() {
  try {
    const res = await fetch(`${STATUS_SUMMARY_URL}?t=${now}`, { cache: 'no-store' });
    if (!res.ok) return { problem: `статус-страница отвечает HTTP ${res.status}`, updated: null };
    const doc = await res.json();
    const age = now - Date.parse(doc.updated);
    if (!Number.isFinite(age)) {
      return { problem: 'в данных агента нет разбираемого времени обновления', updated: null };
    }
    if (age > AGENT_STALE_MS) {
      return { problem: `агент не обновлял данные ${humanDuration(age)}`, updated: doc.updated };
    }
    return { problem: null, updated: doc.updated };
  } catch (e) {
    return { problem: `статус-страница недоступна: ${e.message || e}`, updated: null };
  }
}

// ---------------------------------------------------------------- основное

const config = JSON.parse(await readFile(CONFIG_PATH, 'utf8'));
const state = await readJson(join(DATA, 'state.json'), { services: {} });
const incidents = await readJson(join(DATA, 'incidents.json'), []);
if (!state.services) state.services = {};

// Когда сторож приходил в прошлый раз. Читается ДО того, как state.updated
// перезапишется текущим моментом, — иначе промежуток посчитать не из чего.
// Пусто бывает только у первого обхода: ветки данных ещё нет.
const previousRunAt = typeof state.updated === 'string' ? state.updated : null;
const sinceLastRun = previousRunAt ? now - Date.parse(previousRunAt) : NaN;

// Проверки живут внутри проектов, но обходим их плоским списком: у каждой свой
// id, и история привязана именно к нему, а не к месту в дереве.
const flat = config.projects.flatMap((p) =>
  p.checks.map((c) => ({ ...c, project: p, fullName: `${p.title} · ${c.name}` })),
);

const results = await Promise.all(
  flat.map(async (s) => {
    const http = await checkService(s);
    const cert = s.cert ? await checkCert(s.cert) : { days: null, state: null };
    return { service: s, ...http, certDays: cert.days, certState: cert.state };
  }),
);

const summaryChecks = [];
const messages = [];

// Кто сейчас голос канала.
//
// Бот на сервере читает тот же summary.json и пишет о падениях сам — с
// юнитами, версиями и напоминаниями, которых у сторожа нет. Пока агент
// обновляет данные, сервер жив, а бот перезапускается systemd (Restart=always),
// поэтому сторож про сервисы молчит: иначе на каждое падение прилетало бы
// два одинаковых сообщения.
//
// Как только агент замолчал или страница недоступна снаружи — голос
// переходит к сторожу: в этот момент бот, скорее всего, молчит и он.
const agent = await probeAgent();
const agentAlive = !agent.problem;

for (const r of results) {
  const s = r.service;
  const prev = state.services[s.id] ?? {};
  const observed = r.status;
  const isUp = observed === UP || observed === SLOW;

  // --- история
  const rawFile = join(DATA, 'raw', `${s.id}.json`);
  const hourlyFile = join(DATA, 'hourly', `${s.id}.json`);
  const dailyFile = join(DATA, 'daily', `${s.id}.json`);

  const raw = await readJson(rawFile, []);
  raw.push([now, isUp ? 1 : 0, r.ms, r.code]);
  while (raw.length && raw[0][0] < now - RAW_MS) raw.shift();

  const hourly = bump(await readJson(hourlyFile, []), hourBucket(now), isUp, r.ms, HOURLY_KEEP);
  const daily = bump(await readJson(dailyFile, []), dayKey(now), isUp, r.ms, DAILY_KEEP);

  await writeJson(rawFile, raw);
  await writeJson(hourlyFile, hourly);
  await writeJson(dailyFile, daily);

  // --- подтверждение состояния, прежде чем в него поверить
  //
  // Новое состояние должно продержаться CONFIRM_RUNS обходов подряд. Иначе
  // сервис, моргающий раз в минуту, наплодит инцидентов и сообщений, среди
  // которых настоящая авария будет неразличима.
  let pending = prev.pending;
  let status = prev.status ?? observed;

  if (observed === status) {
    pending = undefined;
  } else if (pending?.status === observed) {
    pending = { status: observed, count: pending.count + 1 };
    if (pending.count >= CONFIRM_RUNS) {
      status = observed;
      pending = undefined;
    }
  } else {
    pending = { status: observed, count: 1 };
  }

  // --- смена подтверждённого состояния → инцидент, а при живом агенте молча
  //
  // Историю ведём всегда: инциденты нужны и в сводке, и на странице. А вот
  // писать в Telegram — только когда голос у нас (см. agentAlive выше).
  // Правила о том, что достойно сообщения, те же, что у бота: «медленно» не
  // будит владельца, потому что сервис отвечает.
  //
  // Первое наблюдение (prev.status пусто) лежащей проверки инцидент ОТКРЫВАЕТ —
  // ровно как у агента (agent/main.go, applyIncident: prev == nil). Раньше оно
  // молчало, и падение, заставшее сторожа без истории — первый запуск, новая
  // ветка с данными, вычищенный data/, — не попадало ни в историю, ни в
  // Telegram: до самого восстановления его как будто не было. А сторож нужен
  // именно тогда, когда агент мёртв, то есть чаще всего ровно в такой момент.
  let since = prev.since ?? new Date(now).toISOString();
  if (status !== prev.status) {
    since = new Date(now).toISOString();
    // Второстепенность — в заголовке, ровно как у бота: канал должен читаться
    // одним голосом, а не двумя разными форматами.
    const label = link(s.fullName, s.url) + (isCritical(s) ? '' : ' (второстепенная)');
    const reason = esc(s.impact || r.error || '');

    if (status === DOWN) {
      incidents.unshift({
        service: s.id,
        name: s.fullName,
        start: since,
        end: null,
        reason: r.error ?? 'недоступен',
      });
      if (!agentAlive) {
        const tag = isCritical(s) ? '🔴' : '🟠';
        messages.push(`${tag} <b>${label}</b> недоступен${reason ? `\n${reason}` : ''}`);
      }
    } else if (prev.status === DOWN) {
      const open = incidents.find((i) => i.service === s.id && !i.end);
      if (open) {
        open.end = since;
        open.durationMs = Date.parse(since) - Date.parse(open.start);
      }
      if (!agentAlive) {
        const took = open ? `\nпростой: ${humanDuration(open.durationMs)}` : '';
        messages.push(`🟢 <b>${label}</b> снова работает${took}`);
      }
    }
  }

  // --- сертификат: целиком зона сторожа, бот про сроки молчит
  //
  // Дублирования тут нет, поэтому пишем независимо от того, чей голос: бот
  // видит только certDays из сводки и никогда о них не сообщает, а сторож
  // ходит по TLS сам. Сообщаем один раз при пересечении порога.
  if (r.certState === 'expired' && prev.certState !== 'expired') {
    messages.push(`⛔ <b>${link(s.fullName, s.url)}</b>: сертификат ИСТЁК`);
  } else if (r.certState === 'invalid' && prev.certState !== 'invalid') {
    messages.push(`⚠️ <b>${link(s.fullName, s.url)}</b>: сертификат не проходит проверку`);
  } else if (r.certDays !== null && r.certDays <= 14 && (prev.certDays ?? 99) > 14) {
    messages.push(
      `⚠️ <b>${link(s.fullName, s.url)}</b>: сертификат истекает через ${r.certDays} дн.`,
    );
  }

  state.services[s.id] = {
    status,
    pending,
    since,
    ms: r.ms,
    code: r.code,
    certDays: r.certDays,
    certState: r.certState,
  };

  const days = daysForPage(daily, DAYS_ON_PAGE);
  summaryChecks.push({
    id: s.id,
    name: s.name,
    note: s.note ?? '',
    impact: s.impact ?? '',
    project: s.project.id,
    critical: isCritical(s),
    slowMs: s.slowMs ?? 0,
    steps: s.steps?.length ?? 0,
    url: s.url ?? s.steps?.[0]?.url ?? '',
    status,
    since,
    ms: r.ms,
    code: r.code,
    error: r.error,
    certDays: r.certDays,
    certState: r.certState,
    uptime: {
      d1: uptimeRaw(raw, 24),
      d7: uptimeOverHours(hourly, 24 * 7),
      d90: uptimeOverDays(daily, 90),
    },
    days,
    coverage: days.filter(Boolean).length,
    spark: hourly.slice(-24).map(([, , , avgMs]) => avgMs),
  });
}

// Проекты идут в том же порядке, что и в конфиге, — это и есть сортировка
// по важности. Вердикт проекта считается по критичным проверкам.
const projects = config.projects.map((p) => {
  const checks = summaryChecks.filter((s) => s.project === p.id);
  const crit = checks.filter((c) => c.critical);
  const aux = checks.filter((c) => !c.critical);
  const op = {
    id: p.id,
    title: p.title,
    subtitle: p.subtitle ?? '',
    url: p.url,
    accent: p.accent ?? null,
    up: crit.filter((c) => c.status === UP || c.status === SLOW).length,
    total: crit.length,
    slow: crit.filter((c) => c.status === SLOW).length,
    auxDown: aux.filter((c) => c.status === DOWN).length,
    auxSlow: aux.filter((c) => c.status === SLOW).length,
    checks,
  };
  return { ...op, status: projectStatus(op) };
});

const overall = overallStatus(projects);
state.updated = new Date(now).toISOString();

// ---------------------------------------------------------------- дед-мэн
//
// Единственное, чего сервер про себя сказать не может: что он замолчал.
// Страница честно покажет «данные устарели», но об этом никто не узнает, пока
// её не откроют. Молчащий агент означает, что мы ослепли, а не что всё хорошо.
//
// Сам обход уже сделан выше (probeAgent): его результат решал, говорить ли о
// сервисах. Здесь остаётся сравнить с прошлым разом и сообщить о переходе —
// один раз за эпизод, плюс одно сообщение о восстановлении.
{
  const previous = state.agent ?? {};
  if (agent.problem && !previous.problem) {
    messages.unshift(
      `👁 <b>Статус-страница ослепла</b>\n${esc(agent.problem)}\n` +
        'Сами сервисы при этом могут работать — просто их никто не смотрит. ' +
        'Дальше о падениях пишет внешняя проверка, а не бот на сервере.',
    );
  } else if (!agent.problem && previous.problem) {
    messages.push('👁 <b>Агент снова обновляет данные</b>\nГолос вернулся боту на сервере.');
  }
  state.agent = {
    problem: agent.problem,
    updated: agent.updated,
    checkedAt: new Date(now).toISOString(),
  };
}

// ---------------------------------------------------------------- дед-мэн сторожа
//
// У агента дед-мэн есть, у сторожа его не было: пропущенный запуск не оставляет
// следа нигде — ни в чате, ни в данных, ни на странице. Единственный, кто может
// об этом рассказать, — сам сторож, когда наконец приедет.
//
// Сообщение уходит независимо от того, чей сейчас голос: речь не о сервисах, а
// о том, что за прошедшие часы их не смотрел никто снаружи. Порог выбран выше
// любого наблюдавшегося промежутка, поэтому обычная неточность расписания GitHub
// в чат не попадает.
if (Number.isFinite(sinceLastRun) && sinceLastRun >= PROBE_ABSENT_MS) {
  messages.unshift(
    `⏳ <b>Внешняя проверка не запускалась ${humanDuration(sinceLastRun)}</b>\n` +
      'Расписание GitHub не выполнялось. Всё это время снаружи никто не смотрел ' +
      'ни на сервисы, ни на то, жив ли агент; замеров за этот промежуток нет.',
  );
}

await writeJson(join(DATA, 'state.json'), state);
await writeJson(join(DATA, 'incidents.json'), incidents.slice(0, INCIDENTS_KEEP));
await writeJson(join(DATA, 'summary.json'), {
  updated: state.updated,
  // Возраст самого сторожа рядом с тем, что он намерил. Сводка, пролежавшая
  // три часа, на вид ничем не отличается от сделанной минуту назад, а разница
  // между ними — это разница между «проверено» и «когда-то проверялось».
  // previousAt пуст только у первого обхода, gapMs — null при нём же.
  probe: {
    previousAt: previousRunAt,
    gapMs: Number.isFinite(sinceLastRun) ? sinceLastRun : null,
  },
  overall,
  projects,
  incidents: incidents.slice(0, 10),
});

// Сводка по запросу приходит всегда, независимо от того, чей сейчас голос:
// это ручная проверка, что канал жив. Запускается галкой notify_test.
if (process.env.NOTIFY_TEST === 'true') {
  const icon = { up: '🟢', down: '🔴', degraded: '🟠' };
  const lines = projects.map((p) => {
    const aux = p.auxDown ? ` (+${p.auxDown} второстеп.)` : '';
    return `${icon[p.status] ?? '⚪'} <b>${esc(p.title)}</b> — ${p.up}/${p.total}${aux}`;
  });
  const down = projects.reduce((n, p) => n + (p.total - p.up), 0);
  messages.push(
    `📊 <b>Текущий статус</b>\n\n${lines.join('\n')}\n\n` +
      `Критичных проверок недоступно: ${down}\nОбщее состояние: ${overall}\n` +
      'https://status.samoy.love',
  );
}

for (const m of messages) await notify(m);

const downCount = projects.reduce((n, p) => n + (p.total - p.up), 0);
console.log(
  `[probe] ${new Date(now).toISOString()} — состояние: ${overall}, критичных недоступно: ${downCount}`,
);
for (const p of projects) {
  console.log(`  ${p.title} — ${p.up}/${p.total}${p.auxDown ? ` (+${p.auxDown} второстеп.)` : ''}`);
  for (const s of p.checks) {
    const mark = s.status === UP ? '✓' : s.status === SLOW ? '~' : '✗';
    console.log(
      `   ${s.critical ? ' ' : '·'}${mark} ${s.name.padEnd(24)} ${String(s.code).padStart(3)} ${String(s.ms).padStart(5)}ms` +
        (s.certDays !== null ? `  сертификат: ${s.certDays} дн. (${s.certState})` : '') +
        (s.error ? `  ${s.error}` : ''),
    );
  }
}
console.log(
  agentAlive
    ? '  [голос] агент жив — о сервисах пишет бот, сторож молчит'
    : `  [голос] ${agent.problem} — о сервисах пишет сторож`,
);
// Настоящая частота обходов — то, чего не видно ни на странице, ни в данных.
// Печатается всегда: расписание в probe.yml просит пять минут, и расхождение
// между просьбой и реальностью должно быть видно в каждом прогоне.
console.log(
  Number.isFinite(sinceLastRun)
    ? `  [сторож] прошлый обход был ${humanDuration(sinceLastRun)} назад`
    : '  [сторож] прошлых обходов нет — это первый',
);

// Ненулевой код выхода не нужен: падение сервиса — это нормальный результат
// работы пробера, а не ошибка самого пробера. Иначе красный CI был бы всегда.
