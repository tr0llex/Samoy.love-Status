import { test, expect } from './guards';
import { fixture, serve, waitRendered } from './helpers';

// Статус чаще всего открывают с телефона и в тот момент, когда что-то уже
// сломалось. Горизонтальная прокрутка и уехавшие карточки — самый частый
// способ незаметно испортить именно этот сценарий.
test('на ширине телефона карточки помещаются в экран', async ({ page }) => {
  await serve(page, fixture('degraded'));
  await page.goto('/');
  await waitRendered(page);

  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow, 'горизонтальная прокрутка на телефоне').toBeLessThanOrEqual(1);

  const viewport = page.viewportSize()!;
  const cards = page.locator('.project');
  await expect(cards).toHaveCount(2);

  for (let i = 0; i < 2; i++) {
    const box = await cards.nth(i).boundingBox();
    expect(box!.x, `карточка ${i} уехала влево`).toBeGreaterThanOrEqual(-1);
    expect(box!.x + box!.width, `карточка ${i} шире экрана`).toBeLessThanOrEqual(viewport.width + 1);
  }

  // Полоска аптайма — самый широкий элемент карточки: 90 ячеек в ряд.
  const bars = page.locator('.row .bars').first();
  const barsBox = await bars.boundingBox();
  expect(barsBox!.width).toBeLessThanOrEqual(viewport.width);

  await expect(page.locator('#banner-text')).toBeVisible();
  await expect(page.locator('#incidents-block')).toBeVisible();
});
