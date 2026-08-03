// @ts-check
import { defineConfig } from 'astro/config';

import { dataDevEndpoint } from './scripts/data-dev-endpoint.mjs';
import { eventsDevEndpoint } from './scripts/events-dev-endpoint.mjs';

export default defineConfig({
  site: 'https://status.samoy.love',
  compressHTML: true,
  vite: {
    // В проде /e/<событие> и /data/ обслуживает nginx. Локально его нет, и без
    // заглушек каждое раскрытие проверки давало бы 404 в консоли, а сама
    // страница не нашла бы summary.json и показала бы пустоту.
    plugins: [eventsDevEndpoint(), dataDevEndpoint()],

    build: {
      // НОЛЬ — ЧТОБЫ СКРИПТЫ БЫЛИ ВНЕШНИМИ ФАЙЛАМИ, А НЕ ИНЛАЙНОМ.
      //
      // Astro вклеивает мелкие собранные скрипты прямо в HTML, и тогда CSP
      // обязан разрешать script-src 'unsafe-inline' — то есть не отличает наши
      // модули от чужого скрипта, вставленного через XSS. Сейчас инлайн-скриптов
      // в сборке нет, и заголовок в deploy-kit отдаёт script-src 'self'; ноль
      // здесь не даёт этому измениться от одного нового компонента.
      //
      // Проверяется машиной: `npm run check:no-inline-scripts`.
      assetsInlineLimit: 0,
    },
  },
});
