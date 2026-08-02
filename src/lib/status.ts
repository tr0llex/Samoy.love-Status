// Форма summary.json и всё, что считается по ней без участия разметки.
//
// Модуль общий для полной страницы и мини-приложения в Telegram: у них разная
// вёрстка, но вопрос «что означают эти данные» должен решаться одинаково.
// Иначе в Telegram и в браузере со временем разъедутся вердикты — ровно та
// беда, которую уже чинили между агентом и сторожем.

export type Day = { d: string; up: number; total: number; avgMs: number };
export type Uptime = { d1: number | null; d7: number | null; d90: number | null };
export type CheckStatus = 'up' | 'slow' | 'down';
export type CertState = 'ok' | 'expired' | 'invalid' | 'unreachable';

export type Check = {
  id: string;
  name: string;
  note?: string;
  /** Что падение значит для пользователя — главное, ради чего сюда приходят. */
  impact?: string;
  url: string;
  /** Второстепенные проверки видны, но вердикт проекта не роняют. */
  critical: boolean;
  slowMs?: number;
  steps?: number;
  status: CheckStatus;
  since: string;
  ms: number | null;
  code: number;
  error?: string | null;
  certDays: number | null;
  certState?: CertState | null;
  uptime: Uptime | null;
  /** Ровно 90 ячеек по календарю; null — сутки, за которые замеров нет. */
  days: (Day | null)[] | null;
  /** За сколько из этих 90 суток данные вообще есть. */
  coverage?: number;
  spark: (number | null)[] | null;
};

export type Unit = {
  name: string;
  title?: string;
  active: boolean;
  state?: string;
  since?: string;
};
export type Release = { version: string; at?: string; seen: string };
export type Build = { title: string; version?: string; at?: string; history?: Release[] };

export type Project = {
  id: string;
  title: string;
  subtitle?: string;
  url: string;
  accent?: string | null;
  status: 'up' | 'down' | 'degraded';
  /** up/total — по критичным проверкам: именно они определяют вердикт. */
  up: number;
  total: number;
  auxDown?: number;
  auxSlow?: number;
  slow?: number;
  checks: Check[];
  units?: Unit[];
  builds?: Build[];
};

export type Incident = {
  service: string;
  name: string;
  start: string;
  end?: string | null;
  reason: string;
  durationMs?: number;
};

export type Summary = {
  updated: string;
  /** major — ступень между «частичным» и «массовым». */
  overall: 'operational' | 'degraded' | 'major' | 'down';
  projects: Project[];
  incidents?: Incident[];
};

/** Данные старше этого срока считаем недостоверными. */
export const STALE_MS = 5 * 60_000;

// ------------------------------------------------------------------ строки

// Всё, что приходит из summary.json, попадает в innerHTML. Имена и заметки
// берутся из конфига, но текст ошибки — это err.Error() из агента, куда
// попадает ответ чужого сервера. Экранируем на входе в разметку.
export const esc = (v: unknown) =>
  String(v ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');

/** В href идёт значение из конфига; javascript:-схеме там делать нечего. */
export function safeUrl(u: string | null | undefined) {
  try {
    const parsed = new URL(String(u), location.origin);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : '#';
  } catch {
    return '#';
  }
}

/** Акцент проекта уезжает в CSS-переменную: пускаем только безобидное. */
export const safeColor = (c: string | null | undefined) =>
  c && /^[a-zA-Z0-9 .,()%#/-]+$/.test(c) ? c : 'var(--accent)';

export const fmtMs = (ms: number | null | undefined) => (ms == null ? '—' : `${ms} мс`);

export const fmtPct = (v: number | null | undefined) =>
  v == null ? '—' : `${v.toFixed(2).replace(/\.?0+$/, '')}%`;

/** Сколько времени прошло — без хвоста «назад», для подстановки в фразу. */
export function elapsed(iso: string) {
  const min = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60_000));
  if (min < 1) return 'меньше минуты';
  if (min < 60) return `${min} мин`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h} ч`;
  return `${Math.floor(h / 24)} дн`;
}

export const relTime = (iso: string) => {
  const min = Math.round((Date.now() - Date.parse(iso)) / 60_000);
  return min < 1 ? 'только что' : `${elapsed(iso)} назад`;
};

// Точное время — по Москве и с явной подписью: сервер живёт в UTC, браузер
// читателя в своей зоне, и «16:45» без пояснения означает разное на разных
// экранах. В подсказке всегда одна и та же шкала.
const MSK = 'Europe/Moscow';

export function mskExact(iso: string | null | undefined) {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = new Date(t).toLocaleString('ru-RU', {
    timeZone: MSK,
    day: 'numeric',
    month: 'long',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
  return `${s} МСК`;
}

/** Атрибут title с точным московским временем — для любой относительной даты. */
export const exactAttr = (iso: string | null | undefined) => {
  const s = mskExact(iso);
  return s ? ` title="${esc(s)}"` : '';
};

export function fmtDate(iso: string) {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  const time = d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
  if (sameDay) return `сегодня ${time}`;
  const date = d.toLocaleDateString('ru-RU', {
    day: '2-digit',
    month: '2-digit',
    year: '2-digit',
  });
  return `${date} ${time}`;
}

export function duration(ms: number) {
  const min = Math.round(ms / 60_000);
  if (min < 60) return `${min} мин`;
  const h = Math.floor(min / 60);
  return `${h} ч ${min % 60} мин`;
}

// ------------------------------------------------------------------ смысл

// Ступени полоски дня. Различаем не только «100% / 0% / всё остальное»: день
// с одной сбойной минутой из 1440 и день, лежавший наполовину, — разное.
export function dayClass(d: Day | null) {
  if (!d || !d.total) return 'none';
  const ratio = d.up / d.total;
  if (ratio === 1) return 'up';
  if (ratio === 0) return 'down';
  if (ratio >= 0.99) return 'g1';
  if (ratio >= 0.9) return 'g2';
  return 'g3';
}

/**
 * Аптайм ТОЛЬКО по критичным проверкам.
 *
 * Второстепенная проверка не должна двигать число, которое читают как
 * «насколько всё было доступно людям».
 */
export function uptimeOf(checks: Check[]) {
  let up = 0;
  let total = 0;
  for (const c of checks) {
    if (!c.critical) continue;
    for (const d of c.days ?? []) {
      if (!d) continue;
      up += d.up;
      total += d.total;
    }
  }
  return total ? (up / total) * 100 : null;
}

export type Overall = 'up' | 'aux' | 'partial' | 'major' | 'down' | 'stale';

export const HERO_TEXT: Record<Overall, string> = {
  up: 'Все системы работают',
  aux: 'Работает, есть замечания',
  partial: 'Частичный сбой',
  major: 'Крупный сбой',
  down: 'Всё лежит',
  stale: 'Данные устарели',
};

const OVERALL_MAP: Record<string, Overall> = {
  operational: 'up',
  degraded: 'partial',
  major: 'major',
  down: 'down',
};

/**
 * Состояние для главного экрана.
 *
 * Замерший агент не должен выглядеть как «всё работает», а «частичный сбой»
 * при всех исправных ключевых проверках читался бы противоречиво — рядом
 * стоит «7 / 7 в норме».
 */
export function heroState(data: Summary): Overall {
  if (Date.now() - Date.parse(data.updated) > STALE_MS) return 'stale';
  const mapped = OVERALL_MAP[data.overall] ?? 'partial';
  if (mapped === 'partial') {
    const touched = data.projects.some((p) =>
      p.checks.some((c) => c.critical && c.status !== 'up'),
    );
    if (!touched) return 'aux';
  }
  return mapped;
}

/** Порядок в списке сбоев — по тяжести для пользователя. */
export function troubleRank(c: Check) {
  if (c.critical && c.status === 'down') return 0;
  if (c.critical && c.status === 'slow') return 1;
  return c.status === 'down' ? 2 : 3;
}

export function troubles(data: Summary) {
  return data.projects
    .flatMap((p) => p.checks.filter((c) => c.status !== 'up').map((c) => ({ p, c })))
    .sort((a, b) => troubleRank(a.c) - troubleRank(b.c));
}
