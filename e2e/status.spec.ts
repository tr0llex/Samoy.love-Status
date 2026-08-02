import { test, expect } from './guards';
import { fixture, serve, waitRendered } from './helpers';

test.describe('страница статуса', () => {
  test('рисует карточки проектов с версиями, аптаймом и полосками дней', async ({ page }) => {
    await serve(page, fixture('healthy'));
    await page.goto('/');
    await waitRendered(page);

    await expect(page.locator('#banner')).toHaveClass(/banner up/);
    await expect(page.locator('#banner-text')).toHaveText('Все системы работают');
    await expect(page.locator('#updated')).toHaveText('проверено 5 мин назад');

    const projects = page.locator('.project');
    await expect(projects).toHaveCount(2);
    await expect(projects.first().locator('.p-title')).toHaveText('samoy.love');
    await expect(projects.nth(1).locator('.p-title')).toHaveText('ChillHub');
    await expect(projects.nth(1).locator('.p-count')).toHaveText('2/2');

    // Полоска аптайма — всегда ровно 90 суток: недостающие дни добиваются
    // пустыми ячейками, иначе шкала прыгала бы по мере накопления истории.
    const rows = page.locator('.row');
    await expect(rows).toHaveCount(3);
    for (let i = 0; i < 3; i++) {
      await expect(rows.nth(i).locator('.bar')).toHaveCount(90);
    }

    // Проценты аптайма — то, ради чего страницу открывают.
    const foot = rows.first().locator('.row-foot');
    await expect(foot).toContainText('24 ч: 100%');
    await expect(foot).toContainText('7 дн: 99.87%');
    await expect(foot).toContainText('90 дн: 99.5%');
    await expect(foot).toContainText('без сбоев с');
    await expect(rows.first().locator('.latency')).toHaveText('142 мс');
    await expect(rows.first().locator('.badge')).toHaveText('работает');

    // Версии выкаченного — вторая половина смысла страницы: «что сейчас на проде».
    const deployed = projects.nth(1).locator('.info-col').filter({ hasText: 'выкачено' });
    await expect(deployed).toContainText('20260802-023037-df3d3b3');
    await expect(deployed).toContainText('20260802-025301-fd19154');
    await expect(projects.first().locator('.info-col')).toContainText('20260802-131605-01294f4');

    // Службы с их состоянием.
    const units = projects.nth(1).locator('.info-col').filter({ hasText: 'службы' });
    await expect(units).toContainText('Публичный API');
    await expect(units).toContainText('работает');

    await expect(page.locator('#incidents-block')).toBeHidden();
  });

  test('сбой виден в баннере, в карточке и в списке инцидентов', async ({ page }) => {
    await serve(page, fixture('degraded'));
    await page.goto('/');
    await waitRendered(page);

    await expect(page.locator('#banner')).toHaveClass(/banner partial/);
    await expect(page.locator('#banner-text')).toHaveText('Частичный сбой');

    const broken = page.locator('.project').nth(1);
    await expect(broken).toHaveClass(/partial/);
    await expect(broken.locator('.p-count')).toHaveText('1/2');
    await expect(broken.locator('.badge').first()).toHaveText('частичный сбой');

    const downRow = broken.locator('.row.down');
    await expect(downRow).toHaveCount(1);
    await expect(downRow.locator('.badge')).toHaveText('недоступен');
    // Причина падения обязана дойти до экрана: без неё карточка сообщает
    // «что-то не так» и ничего больше.
    await expect(downRow.locator('.err')).toHaveText('connect ECONNREFUSED');
    await expect(downRow.locator('.row-foot')).toContainText('недоступен с');

    // Остановленная служба не должна отображаться работающей.
    await expect(broken.locator('.info-col').filter({ hasText: 'службы' })).toContainText(
      'inactive / dead',
    );

    await expect(page.locator('#incidents-block')).toBeVisible();
    const incidents = page.locator('.incident');
    await expect(incidents).toHaveCount(2);
    await expect(incidents.first()).toHaveClass(/open/);
    await expect(incidents.first()).toContainText('продолжается');
    await expect(incidents.nth(1)).toHaveClass(/closed/);
    await expect(incidents.nth(1)).toContainText('45 мин');
  });
});

test.describe('данные, на которых страница обычно и ломается', () => {
  test('пустой список проектов не роняет страницу', async ({ page }) => {
    await serve(page, fixture('empty'));
    await page.goto('/');
    await waitRendered(page);

    // Ноль проектов — штатное состояние свежей установки, а не повод для
    // «Загружаю данные…» навсегда или белого экрана.
    await expect(page.locator('.project')).toHaveCount(0);
    await expect(page.locator('#banner-text')).toHaveText('Все системы работают');
    await expect(page.locator('#incidents-block')).toBeHidden();
    await expect(page.locator('h1')).toHaveText('Статус сервисов');
    await expect(page.locator('.foot')).toBeVisible();
  });

  test('сервис без истории показывает прочерки, а не NaN и не пустоту', async ({ page }) => {
    await serve(page, fixture('fresh'));
    await page.goto('/');
    await waitRendered(page);

    await expect(page.locator('.project')).toHaveCount(1);
    const row = page.locator('.row');
    await expect(row).toHaveCount(1);

    // Первый день жизни проверки: истории нет, времени ответа нет, срока
    // сертификата нет. Ровно на таких «нет» скрипт и падает обычно.
    await expect(row.locator('.row-foot')).toContainText('24 ч: —');
    await expect(row.locator('.row-foot')).toContainText('90 дн: —');
    await expect(row.locator('.latency')).toHaveText('—');
    await expect(row.locator('.cert')).toHaveCount(0);
    // Шкала остаётся на месте, просто целиком «нет данных».
    await expect(row.locator('.bar')).toHaveCount(90);
    await expect(row.locator('.bar.none')).toHaveCount(90);

    const body = await page.locator('body').textContent();
    expect(body).not.toContain('NaN');
    expect(body).not.toContain('undefined');
  });

  test('устаревшие данные видно по странице, а не только по логам', async ({ page }) => {
    await serve(page, fixture('stale'));
    await page.goto('/');
    await waitRendered(page);

    // Главная ложь статус-страницы — бодро показывать недельной давности «всё
    // работает». Возраст данных обязан быть на экране.
    await expect(page.locator('#updated')).toHaveText('проверено 7 дн назад');
    await expect(page.locator('.project')).toHaveCount(2);
    await expect(page.locator('.row').first().locator('.row-foot')).toContainText('62 дн назад');

    const body = await page.locator('body').textContent();
    expect(body).not.toContain('NaN');
    expect(body).not.toContain('Invalid Date');
  });

  test('недоступные данные дают честную ошибку, а не вечное «загружаю»', async ({
    page,
    problems,
  }) => {
    await page.clock.setFixedTime(new Date('2026-08-02T15:05:00.000Z'));
    await page.route('**/data/summary.json*', (route) => route.fulfill({ status: 500, body: '' }));

    await page.goto('/');

    await expect(page.locator('#banner')).toHaveClass(/banner down/);
    await expect(page.locator('#banner-text')).toHaveText('Не удалось загрузить данные проверок');
    await expect(page.locator('#updated')).toHaveText('HTTP 500');

    // 500 здесь — сам сценарий, его и браузер записывает в консоль. Всё
    // остальное по-прежнему должно быть чисто: страница обязана поймать ошибку
    // загрузки, а не свалиться следом с исключением в скрипте.
    const unexpected = problems.network.filter((entry) => !/summary\.json/.test(entry));
    expect(unexpected, 'посторонние неудачные запросы').toEqual([]);
    problems.network.length = 0;

    const noise = problems.console.filter((entry) => !/Failed to load resource.*500/.test(entry));
    expect(noise, 'страница не пережила недоступность данных').toEqual([]);
    problems.console.length = 0;
  });
});
