import { defineConfig, devices } from '@playwright/test';

// Смоук по живому сайту. Запускается руками (`npm run e2e:prod`) — например,
// сразу после выкатки. В CI на каждый PR не висит: он проверяет прод, а не диф.
export default defineConfig({
  testDir: './e2e/prod',
  retries: 1,
  workers: 1,
  reporter: 'list',
  timeout: 45_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL: process.env.E2E_PROD_URL ?? 'https://status.samoy.love',
    trace: 'retain-on-failure',
    locale: 'ru-RU',
    timezoneId: 'Europe/Moscow',
    ...devices['Desktop Chrome'],
  },
});
