import { test, expect } from './guards';
import { expandAll, fixture, serve, waitRendered } from './helpers';

test.describe('страница статуса', () => {
  test('рисует карточки проектов с версиями, аптаймом и полосками дней', async ({ page }) => {
    await serve(page, fixture('healthy'));
    await page.goto('/');
    await waitRendered(page);

    await expect(page.locator('#hero')).toHaveClass(/hero up/);
    await expect(page.locator('#hero-title')).toHaveText('Все системы работают');
    await expect(page.locator('#fact-updated')).toHaveText('5 мин назад');
    // Счётчик на главном экране — по ключевым проверкам: он отвечает на
    // вопрос «сколько из того, чем пользуются, сейчас в порядке».
    await expect(page.locator('#fact-checks')).toHaveText('3 / 3');

    const projects = page.locator('.proj');
    await expect(projects).toHaveCount(2);
    await expect(projects.first().locator('.proj-name')).toHaveText('samoy.love');
    await expect(projects.nth(1).locator('.proj-name')).toHaveText('ChillHub');
    await expect(projects.nth(1).locator('.proj-head .chip').last()).toContainText('2/2');

    await expandAll(page);

    // Полоска аптайма — всегда ровно 90 календарных суток: сутки без замеров
    // остаются дыркой, а не сдвигают историю влево.
    const checks = page.locator('.chk');
    await expect(checks).toHaveCount(3);
    for (let i = 0; i < 3; i++) {
      await expect(checks.nth(i).locator('.bar')).toHaveCount(90);
    }

    // Проценты аптайма — то, ради чего страницу открывают.
    const foot = checks.first().locator('.chk-foot');
    await expect(foot).toContainText('24 ч 100%');
    await expect(foot).toContainText('7 дн 99.87%');
    await expect(foot).toContainText('90 дн 99.5%');
    await expect(foot).toContainText('стабильно');
    await expect(checks.first().locator('.lat')).toHaveText('142 мс');

    // Версии выкаченного — вторая половина смысла страницы: «что сейчас на проде».
    const deployed = projects.nth(1).locator('.info-col').filter({ hasText: 'выкачено' });
    await expect(deployed).toContainText('20260802-023037-df3d3b3');
    await expect(deployed).toContainText('20260802-025301-fd19154');
    await expect(projects.first().locator('.info-col')).toContainText('20260802-131605-01294f4');

    // Службы с их состоянием.
    const units = projects.nth(1).locator('.info-col').filter({ hasText: 'службы' });
    await expect(units).toContainText('Публичный API');
    await expect(units).toContainText('работает');

    await expect(page.locator('#alerts')).toBeHidden();
    await expect(page.locator('#incidents-block')).toBeHidden();
  });

  test('сбой виден наверху, в карточке и в списке инцидентов', async ({ page }) => {
    await serve(page, fixture('degraded'));
    await page.goto('/');
    await waitRendered(page);

    await expect(page.locator('#hero')).toHaveClass(/hero partial/);
    await expect(page.locator('#hero-title')).toHaveText('Частичный сбой');

    // Сломанное вынесено отдельным блоком выше проектов: искать красную
    // строку среди зелёных не должно быть нужно.
    const alerts = page.locator('.alert');
    await expect(alerts).toHaveCount(1);
    await expect(alerts.first()).toContainText('connect ECONNREFUSED');
    // Что это значит для пользователя — главное, ради чего сюда приходят.
    await expect(alerts.first().locator('.alert-impact')).toBeVisible();

    const broken = page.locator('.proj').nth(1);
    await expect(broken).toHaveClass(/degraded/);
    await expect(broken.locator('.proj-head .chip').last()).toContainText('1/2');
    await expect(broken.locator('.proj-head .chip').last()).toContainText('частичный сбой');
    // Проблемный проект раскрывается сам — за ним и пришли.
    await expect(broken).toHaveAttribute('open', '');

    const downCheck = broken.locator('.chk.down');
    await expect(downCheck).toHaveCount(1);
    // Причина падения обязана дойти до экрана: без неё карточка сообщает
    // «что-то не так» и ничего больше.
    await expect(downCheck.locator('.chk-err')).toHaveText('connect ECONNREFUSED');
    await expect(downCheck.locator('.since')).toContainText('недоступен');

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
    await expect(page.locator('.proj')).toHaveCount(0);
    await expect(page.locator('#hero-title')).toHaveText('Все системы работают');
    await expect(page.locator('#alerts')).toBeHidden();
    await expect(page.locator('#incidents-block')).toBeHidden();
    await expect(page.locator('h1')).toHaveText('Статус сервисов');
    await expect(page.locator('.foot')).toBeVisible();
  });

  test('сервис без истории показывает прочерки, а не NaN и не пустоту', async ({ page }) => {
    await serve(page, fixture('fresh'));
    await page.goto('/');
    await waitRendered(page);
    await expandAll(page);

    await expect(page.locator('.proj')).toHaveCount(1);
    const check = page.locator('.chk');
    await expect(check).toHaveCount(1);

    // Первый день жизни проверки: истории нет, времени ответа нет, срока
    // сертификата нет. Ровно на таких «нет» скрипт и падает обычно.
    await expect(check.locator('.chk-foot')).toContainText('24 ч —');
    await expect(check.locator('.chk-foot')).toContainText('90 дн —');
    await expect(check.locator('.lat')).toHaveText('—');
    await expect(check.locator('.cert')).toHaveCount(0);
    // Шкала остаётся на месте, просто целиком «нет данных», и рядом честно
    // сказано, за сколько суток данные вообще есть.
    await expect(check.locator('.bar')).toHaveCount(90);
    await expect(check.locator('.bar.none')).toHaveCount(90);
    await expect(check.locator('.cov')).toContainText('0 дн. из 90');

    const body = await page.locator('body').textContent();
    expect(body).not.toContain('NaN');
    expect(body).not.toContain('undefined');
  });

  test('устаревшие данные названы устаревшими, а не выданы за текущие', async ({ page }) => {
    await serve(page, fixture('stale'));
    await page.goto('/');
    await waitRendered(page);

    // Главная ложь статус-страницы — бодро показывать недельной давности «всё
    // работает». Возраст данных обязан быть на экране, и состояние тоже.
    await expect(page.locator('#hero')).toHaveClass(/hero stale/);
    await expect(page.locator('#hero-title')).toHaveText('Данные устарели');
    await expect(page.locator('#hero-note')).toContainText('7 дн');
    await expect(page.locator('#fact-updated')).toHaveText('7 дн назад');
    await expect(page.locator('.proj')).toHaveCount(2);

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

    await expect(page.locator('#hero')).toHaveClass(/hero down/);
    await expect(page.locator('#hero-title')).toHaveText('Не удалось загрузить данные проверок');
    await expect(page.locator('#hero-note')).toHaveText('HTTP 500');

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
