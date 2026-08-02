import { defineConfig, devices } from '@playwright/test';

// Прогон идёт против ЛОКАЛЬНОЙ прод-сборки, а не против сайта: тест должен
// падать от изменений в этой ветке, а не от того, что сейчас на проде.
const PORT = 4331;

// Смоук по проду живёт рядом, но в локальный прогон попадать не должен.
// Ignore нужен на КАЖДОМ проекте: свой testIgnore у проекта перекрывает
// верхнеуровневый, и без этого прод-спеки молча уезжают в локальный прогон.
const PROD = /[\\/]prod[\\/]/;

export default defineConfig({
  testDir: './e2e',
  testIgnore: PROD,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  timeout: 30_000,
  expect: { timeout: 7_000 },

  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    // Страница печатает даты и относительное время. Локаль и пояс машины
    // не должны решать, зелёный прогон или красный.
    locale: 'ru-RU',
    timezoneId: 'Europe/Moscow',
  },

  projects: [
    {
      name: 'desktop',
      use: { ...devices['Desktop Chrome'] },
      testIgnore: [PROD, /mobile\.spec\.ts$/],
    },
    {
      name: 'mobile',
      use: { ...devices['Pixel 7'] },
      testIgnore: PROD,
      testMatch: /mobile\.spec\.ts$/,
    },
  ],

  webServer: {
    command: `npm run build && npm run preview -- --port ${PORT} --host 127.0.0.1`,
    url: `http://127.0.0.1:${PORT}/`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
