import { test, expect } from '../guards';

// На проде данные живые: числа и статусы заранее неизвестны. Поэтому здесь
// проверяется не содержимое, а то, что страница действительно ожила —
// дотянулась до summary.json и построила по нему карточки.
test('прод отдаёт живую страницу, а не просто 200', async ({ page }) => {
  await page.goto('/');

  await expect(page.locator('#banner')).not.toHaveClass(/loading/);
  await expect(page.locator('#banner-text')).not.toHaveText('Не удалось загрузить данные проверок');
  await expect(page.locator('#updated')).toHaveText(/проверено /);

  const projects = page.locator('.project');
  await expect.poll(() => projects.count()).toBeGreaterThan(0);
  await expect(page.locator('.row .bar').first()).toBeAttached();
  await expect(page.locator('.row .badge').first()).toHaveText(/работает|недоступен/);

  const body = await page.locator('body').textContent();
  expect(body).not.toContain('NaN');
  expect(body).not.toContain('Invalid Date');
});

test('данные проверок свежие, а не позавчерашние', async ({ page, request }) => {
  const res = await request.get('/data/summary.json');
  expect(res.status()).toBe(200);

  const data = await res.json();
  expect(Array.isArray(data.projects), 'в summary.json нет списка проектов').toBe(true);
  expect(data.projects.length).toBeGreaterThan(0);

  // Агент обходит проверки раз в минуту. Данные старше часа означают, что он
  // молча умер, — а страница при этом продолжает бодро показывать «всё работает».
  const ageMin = (Date.now() - Date.parse(data.updated)) / 60_000;
  expect(ageMin, `данные обновлялись ${Math.round(ageMin)} мин назад`).toBeLessThan(60);

  await page.goto('/');
  await expect(page.locator('#updated')).not.toHaveText(/дн назад/);
});
