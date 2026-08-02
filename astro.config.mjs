// @ts-check
import { defineConfig } from 'astro/config';

import { eventsDevEndpoint } from './scripts/events-dev-endpoint.mjs';

export default defineConfig({
  site: 'https://status.samoy.love',
  compressHTML: true,
  vite: {
    // В проде /e/<событие> обслуживает nginx. Локально его нет, и без
    // заглушки каждое раскрытие проверки давало бы 404 в консоли.
    plugins: [eventsDevEndpoint()],
  },
});
