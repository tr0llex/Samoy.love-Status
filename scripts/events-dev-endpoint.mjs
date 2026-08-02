/**
 * Приёмник /e/<событие> для dev- и preview-сервера.
 *
 * В проде этот путь обслуживает nginx: отвечает 204 и пишет строку в отдельный
 * журнал (snippets/samoylove-events.conf в deploy-kit). Локально nginx нет, и
 * без этой заглушки каждое нажатие давало бы 404 в консоли — сначала у того,
 * кто разрабатывает, а потом и в сквозных тестах, которые справедливо считают
 * ошибку в консоли поломкой.
 *
 * Заглушка ничего не пишет и никуда не ходит: её задача — вернуть тот же 204,
 * что вернул бы прод, чтобы поведение клиента можно было проверить локально.
 */

/** Тот же шаблон имени, что и в nginx: только он определяет, что 204, а что 404. */
const EVENT_PATH = /^\/e\/[a-z][a-z0-9_]{2,39}$/;

/** @returns {import('vite').Plugin} */
export function eventsDevEndpoint() {
  const middleware = (req, res, next) => {
    const path = (req.url || '').split('?')[0];
    if (!path.startsWith('/e/')) return next();

    if (EVENT_PATH.test(path)) {
      res.statusCode = 204;
      res.setHeader('Cache-Control', 'no-store');
      res.end();
      return;
    }

    res.statusCode = 404;
    res.end();
  };

  return {
    name: 'samoylove-events-dev-endpoint',
    configureServer(server) {
      server.middlewares.use(middleware);
    },
    configurePreviewServer(server) {
      server.middlewares.use(middleware);
    },
  };
}
