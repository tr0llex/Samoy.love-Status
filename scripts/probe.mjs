// ВНЕШНИЙ СТОРОЖ. Саму статус-страницу теперь строит агент на сервере
// (agent/main.go); этот скрипт остался ради одной вещи, которую сервер про
// себя сделать не может, — сообщить, что он недоступен.
//
// Запускается из GitHub Actions, обходит те же эндпоинты снаружи и пишет в
// Telegram при смене состояния. Накопленная им история больше нигде не
// показывается и нужна только для того, чтобы отличать смену состояния от
// продолжающегося сбоя.
//
// Хранение с прореживанием, чтобы репозиторий не рос бесконечно:
//   data/raw/<id>.json     — сырые замеры, 7 суток   (~2000 точек)
//   data/hourly/<id>.json  — часовые агрегаты, 90 суток
//   data/daily/<id>.json   — суточные агрегаты, год
//   data/state.json        — текущее состояние и момент последней смены
//   data/incidents.json    — история падений
//   data/summary.json      — единственный файл, который читает страница
//
// Локальный прогон: npm run probe (уведомления не отправятся без токена).

import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { connect } from 'node:tls';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const DATA = join(ROOT, 'data');

const TIMEOUT_MS = 12_000;
const RAW_MS = 7 * 24 * 3600_000;
const HOURLY_KEEP = 90 * 24;
const DAILY_KEEP = 365;
const INCIDENTS_KEEP = 50;

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

const hourBucket = (ts) => Math.floor(ts / 3600_000) * 3600_000;
const dayKey = (ts) => new Date(ts).toISOString().slice(0, 10);

// ---------------------------------------------------------------- проверки

/** HTTP-проверка: код ответа, время, при необходимости — кусок тела. */
async function checkHttp(service) {
  const started = Date.now();
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  try {
    const res = await fetch(service.url, {
      signal: ctrl.signal,
      redirect: 'follow',
      headers: { 'user-agent': 'samoy-status-probe (+https://status.samoy.love)' },
    });
    const ms = Date.now() - started;
    let ok = res.status === (service.expect ?? 200);
    if (ok && service.bodyIncludes) {
      ok = (await res.text()).includes(service.bodyIncludes);
    }
    return { ok, code: res.status, ms, error: ok ? null : `HTTP ${res.status}` };
  } catch (e) {
    return {
      ok: false,
      code: 0,
      ms: Date.now() - started,
      error: e.name === 'AbortError' ? `таймаут ${TIMEOUT_MS / 1000}s` : String(e.message || e),
    };
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Сколько суток осталось сертификату. Отдельная проверка: истёкший сертификат
 * роняет сайт молча и разом — знать об этом надо заранее, а не в день Х.
 */
function checkCert(host) {
  return new Promise((resolve) => {
    const socket = connect(
      { host, port: 443, servername: host, timeout: TIMEOUT_MS },
      () => {
        const cert = socket.getPeerCertificate();
        socket.end();
        if (!cert?.valid_to) return resolve(null);
        resolve(Math.floor((Date.parse(cert.valid_to) - now) / 86_400_000));
      },
    );
    socket.on('error', () => resolve(null));
    socket.on('timeout', () => {
      socket.destroy();
      resolve(null);
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

/** Аптайм по сырым замерам за последние N часов. */
function uptimeRaw(raw, hours) {
  const from = now - hours * 3600_000;
  let up = 0, total = 0;
  for (const [ts, ok] of raw) {
    if (ts >= from) {
      total += 1;
      up += ok;
    }
  }
  return pct(up, total);
}

/** Аптайм по агрегатам за последние N корзин. */
function uptimeBuckets(buckets, count) {
  let up = 0, total = 0;
  for (const [, u, t] of buckets.slice(-count)) {
    up += u;
    total += t;
  }
  return pct(up, total);
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

// ---------------------------------------------------------------- основное

const config = JSON.parse(await readFile(join(ROOT, 'config/status.json'), 'utf8'));
const state = await readJson(join(DATA, 'state.json'), { services: {} });
const incidents = await readJson(join(DATA, 'incidents.json'), []);

// Проверки живут внутри проектов, но обходим их плоским списком: у каждой свой
// id, и история привязана именно к нему, а не к месту в дереве.
const flat = config.projects.flatMap((p) =>
  p.checks.map((c) => ({ ...c, project: p, fullName: `${p.title} · ${c.name}` })),
);

const results = await Promise.all(
  flat.map(async (s) => {
    const http = await checkHttp(s);
    const certDays = s.cert ? await checkCert(s.cert) : null;
    return { service: s, ...http, certDays };
  }),
);

const summaryServices = [];
const messages = [];

for (const r of results) {
  const s = r.service;
  const prev = state.services[s.id];
  const status = r.ok ? 'up' : 'down';

  // --- история
  const rawFile = join(DATA, 'raw', `${s.id}.json`);
  const hourlyFile = join(DATA, 'hourly', `${s.id}.json`);
  const dailyFile = join(DATA, 'daily', `${s.id}.json`);

  const raw = await readJson(rawFile, []);
  raw.push([now, r.ok ? 1 : 0, r.ms, r.code]);
  while (raw.length && raw[0][0] < now - RAW_MS) raw.shift();

  const hourly = bump(await readJson(hourlyFile, []), hourBucket(now), r.ok, r.ms, HOURLY_KEEP);
  const daily = bump(await readJson(dailyFile, []), dayKey(now), r.ok, r.ms, DAILY_KEEP);

  await writeJson(rawFile, raw);
  await writeJson(hourlyFile, hourly);
  await writeJson(dailyFile, daily);

  // --- смена состояния → инцидент + уведомление
  let since = prev?.since ?? new Date(now).toISOString();
  if (prev && prev.status !== status) {
    since = new Date(now).toISOString();
    if (status === 'down') {
      incidents.unshift({
        service: s.id,
        name: s.fullName,
        start: since,
        end: null,
        reason: r.error ?? 'недоступен',
      });
      messages.push(`🔴 <b>${s.fullName}</b> недоступен\n${r.error ?? ''}\n${s.url}`);
    } else {
      const open = incidents.find((i) => i.service === s.id && !i.end);
      if (open) {
        open.end = since;
        open.durationMs = Date.parse(since) - Date.parse(open.start);
        messages.push(
          `🟢 <b>${s.fullName}</b> снова работает\nПростой: ${humanDuration(open.durationMs)}`,
        );
      } else {
        messages.push(`🟢 <b>${s.fullName}</b> снова работает`);
      }
    }
  }

  // --- предупреждение о сертификате (один раз при пересечении порога)
  if (r.certDays !== null && r.certDays <= 14 && (prev?.certDays ?? 99) > 14) {
    messages.push(`⚠️ <b>${s.fullName}</b>: сертификат истекает через ${r.certDays} дн.`);
  }

  state.services[s.id] = { status, since, ms: r.ms, code: r.code, certDays: r.certDays };

  summaryServices.push({
    id: s.id,
    name: s.name,
    note: s.note ?? '',
    project: s.project.id,
    primary: !!s.primary,
    url: s.url,
    status,
    since,
    ms: r.ms,
    code: r.code,
    error: r.error,
    certDays: r.certDays,
    uptime: {
      d1: uptimeRaw(raw, 24),
      d7: uptimeBuckets(hourly, 24 * 7),
      d90: uptimeBuckets(daily, 90),
    },
    days: daily.slice(-90).map(([d, up, total, avgMs]) => ({ d, up, total, avgMs })),
    spark: hourly.slice(-24).map(([, , , avgMs]) => avgMs),
  });
}

const down = summaryServices.filter((s) => s.status === 'down').length;
const overall = down === 0 ? 'operational' : down === summaryServices.length ? 'down' : 'degraded';

// Проекты идут в том же порядке, что и в конфиге, — это и есть сортировка
// по важности. Статус проекта = худший among его проверок.
const projects = config.projects.map((p) => {
  const checks = summaryServices.filter((s) => s.project === p.id);
  const up = checks.filter((c) => c.status === 'up').length;
  return {
    id: p.id,
    title: p.title,
    subtitle: p.subtitle ?? '',
    url: p.url,
    accent: p.accent ?? null,
    status: up === checks.length ? 'up' : up === 0 ? 'down' : 'degraded',
    up,
    total: checks.length,
    checks,
  };
});

state.updated = new Date(now).toISOString();
await writeJson(join(DATA, 'state.json'), state);
await writeJson(join(DATA, 'incidents.json'), incidents.slice(0, INCIDENTS_KEEP));
await writeJson(join(DATA, 'summary.json'), {
  updated: state.updated,
  overall,
  projects,
  incidents: incidents.slice(0, 10),
});

// Бот молчит, пока всё работает: сообщения уходят ТОЛЬКО на смену состояния.
// Иначе он писал бы каждые 5 минут. Чтобы убедиться, что канал жив, запустите
// probe вручную с галкой notify_test — придёт сводка текущего состояния.
if (process.env.NOTIFY_TEST === 'true') {
  const lines = projects.map((p) => {
    const icon = p.status === 'up' ? '🟢' : p.status === 'down' ? '🔴' : '🟡';
    return `${icon} <b>${p.title}</b> — ${p.up}/${p.total}`;
  });
  messages.push(
    `📊 <b>Текущий статус</b>\n\n${lines.join('\n')}\n\n` +
      `Проверок: ${summaryServices.length}, недоступно: ${down}\n` +
      `https://status.samoy.love`,
  );
}

for (const m of messages) await notify(m);

console.log(
  `[probe] ${new Date(now).toISOString()} — ${summaryServices.length - down}/${summaryServices.length} up, состояние: ${overall}`,
);
for (const p of projects) {
  console.log(`  ${p.title} — ${p.up}/${p.total}`);
  for (const s of p.checks) {
    console.log(
      `    ${s.status === 'up' ? '✓' : '✗'} ${s.name.padEnd(20)} ${String(s.code).padStart(3)} ${String(s.ms).padStart(5)}ms` +
        (s.certDays !== null ? `  сертификат: ${s.certDays} дн.` : ''),
    );
  }
}

// Ненулевой код выхода не нужен: падение сервиса — это нормальный результат
// работы пробера, а не ошибка самого пробера. Иначе красный CI был бы всегда.
