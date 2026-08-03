/**
 * Раздача /data/ для dev- и preview-сервера.
 *
 * В проде этот путь обслуживает nginx из /var/www/status/data — каталога ВНЕ
 * релиза: данные пишет агент, и выкатка страницы их не трогает, чтобы история
 * проверок пережила деплой. Ровно поэтому data/ не лежит в public/: попав
 * туда, он уехал бы внутрь артефакта и стал бы копией, которая устаревает с
 * первой же выкаткой.
 *
 * Локально nginx нет, и без этой заглушки страница получала бы 404 на
 * summary.json и показывала пустоту — не «ошибка», а именно пустоту, что
 * читается как «всё упало». Заглушка отдаёт тот же каталог data/, который
 * заполняет агент (`dk run status-agent`), и ничего не кеширует: между
 * запусками агента файл меняется, а страница должна показывать свежий.
 */

import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { join, normalize, extname } from 'node:path';

const TYPES = {
  '.json': 'application/json; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8',
};

/**
 * @param {string} dataDir каталог с данными агента, по умолчанию ./data
 * @returns {import('vite').Plugin}
 */
export function dataDevEndpoint(dataDir = 'data') {
  const root = join(process.cwd(), dataDir);

  const middleware = async (req, res, next) => {
    const path = (req.url || '').split('?')[0];
    if (!path.startsWith('/data/')) return next();

    // normalize + проверка префикса, а не просто join: без неё
    // /data/../../.env вылезал бы за пределы каталога данных. Локальный
    // сервер доступен только с этой машины, но обход каталога — не тот
    // класс ошибок, который стоит заводить даже в dev.
    const target = normalize(join(root, decodeURIComponent(path.slice('/data/'.length))));
    if (!target.startsWith(root)) {
      res.statusCode = 403;
      res.end();
      return;
    }

    try {
      const info = await stat(target);
      if (!info.isFile()) throw new Error('not a file');
      res.statusCode = 200;
      res.setHeader('Content-Type', TYPES[extname(target)] ?? 'application/octet-stream');
      res.setHeader('Cache-Control', 'no-store');
      createReadStream(target).pipe(res);
    } catch {
      // 404 с внятным телом: пустой ответ здесь неотличим от «сервер молчит»,
      // а причина почти всегда одна и та же — агент ещё не запускался.
      res.statusCode = 404;
      res.setHeader('Content-Type', 'application/json; charset=utf-8');
      res.end(JSON.stringify({ error: `нет ${path} — соберите данные: dk run status-agent` }));
    }
  };

  return {
    name: 'samoylove-status-data-dev-endpoint',
    configureServer(server) {
      server.middlewares.use(middleware);
    },
    configurePreviewServer(server) {
      server.middlewares.use(middleware);
    },
  };
}
