// Счётчики событий интерфейса.
//
// Что уходит на сервер: пустой POST на /e/<имя события>. Ни тела, ни
// параметров, ни cookie, ни идентификатора — ни сессии, ни посетителя.
// Сервер отвечает 204 и пишет строку вида
//
//     status.samoy.love "POST /e/check_expand HTTP/2.0" 204 0 0.000
//
// IP и User-Agent в этом журнале отсутствуют физически: их нет в log_format.
// Связать два события одного человека не по чему.
//
// Список имён закрыт и на сервере (nginxlog.yml в metrics.samoy.love):
// незнакомое сворачивается в один ряд "other". Добавляя событие сюда,
// добавьте его и там.
//
// В мини-приложении Telegram (pages/tg.astro) этого нет намеренно: оно
// открывается внутри чужого клиента, и считать там нажатия — считать чужую
// аудиторию.

export const SITE_EVENTS = ['check_expand'] as const;

export type SiteEvent = (typeof SITE_EVENTS)[number];

/**
 * Отправить событие. Никогда не бросает: страница статуса обязана работать,
 * даже когда всё остальное лежит, — тем более из-за счётчика.
 */
export function trackEvent(event: SiteEvent): void {
  if (typeof navigator === 'undefined') return;

  const url = `/e/${event}`;

  try {
    if (typeof navigator.sendBeacon === 'function') {
      navigator.sendBeacon(url, new Blob([], { type: 'text/plain' }));
      return;
    }
    if (typeof fetch === 'function') {
      void fetch(url, { method: 'POST', keepalive: true }).catch(() => {});
    }
  } catch {
    // Блокировщик, офлайн — событие теряется молча.
  }
}
