import { test, expect } from '../guards';

// На проде данные живые: числа и статусы заранее неизвестны. Поэтому здесь
// проверяется не содержимое, а то, что страница действительно ожила —
// дотянулась до summary.json и построила по нему карточки.
//
// Прогоняется после каждой выкатки страницы. Проверка выкатки в deploy-kit
// смотрит на код ответа, а статус-страница отдаёт 200 и когда данных нет,
// и когда скрипт упал на неожиданном поле, — на экране при этом пусто.
// Селекторы те же, что в наборе на фикстурах: разъехавшись, они превратили бы
// смоук в проверку того, что сервер вообще отвечает.

test('прод отдаёт живую страницу, а не просто 200', async ({ page }) => {
  await page.goto('/');

  // Главный экран заполнен: значит скрипт дошёл до конца, а не умер на
  // середине разбора данных.
  await expect(page.locator('#hero-title')).not.toHaveText('');
  await expect(page.locator('#fact-updated')).not.toHaveText('—');

  const projects = page.locator('.proj');
  await expect.poll(() => projects.count()).toBeGreaterThan(0);

  // Полоски дней рисуются по истории — самая хрупкая часть разбора.
  await expect(page.locator('.bar').first()).toBeAttached();

  const body = await page.locator('body').textContent();
  expect(body).not.toContain('NaN');
  expect(body).not.toContain('Invalid Date');
  expect(body).not.toContain('undefined');
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
  await expect(page.locator('#hero-title')).not.toHaveText('Данные устарели');
});

test('агент читает тот конфиг, что лежит в репозитории', async ({ request }) => {
  // Конфиг проверок едет в артефакте агента, но читается по пути из юнита.
  // Пока юниты ставились руками, свежий релиз мог лежать на диске, а агент —
  // читать копию годовой давности. Снаружи это выглядело как «выкатка прошла,
  // но ничего не изменилось», и полдня никто не понимал почему.
  //
  // Признак критичности появился вместе с той правкой конфига: если его нет,
  // значит агент читает что-то более старое, чем текущий релиз.
  const res = await request.get('/data/summary.json');
  const data = await res.json();

  for (const p of data.projects) {
    expect(p.id, 'у проекта нет id').toBeTruthy();
    expect(p.title, `у проекта ${p.id} нет названия`).toBeTruthy();
    expect(Array.isArray(p.checks)).toBe(true);
    for (const c of p.checks) {
      expect(typeof c.critical, `у проверки ${c.id} нет признака критичности`).toBe('boolean');
    }
  }
});

test('мини-приложение для Telegram тоже открывается', async ({ page }) => {
  // Отдельная страница со своей разметкой: сломать её правкой основной проще
  // всего, а заметить — только открыв бота.
  await page.goto('/tg/');
  await expect(page.locator('#hero-title')).not.toHaveText('');
  const body = await page.locator('body').textContent();
  expect(body).not.toContain('NaN');
  expect(body).not.toContain('Invalid Date');
});
