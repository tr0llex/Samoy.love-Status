import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, type Page } from '@playwright/test';

/**
 * Момент, относительно которого страница считает «сколько минут назад».
 *
 * Данные в наборах записаны абсолютными датами, а страница печатает
 * относительные («5 мин назад»). Без фиксации часов такой тест сначала
 * зеленеет, а через сутки краснеет сам по себе — и никто не будет знать,
 * сломалось что-то или просто прошло время.
 */
export const NOW = new Date('2026-08-02T15:05:00.000Z');

export type Summary = Record<string, unknown>;

export function fixture(name: string): Summary {
  return JSON.parse(readFileSync(join(import.meta.dirname, 'fixtures', `${name}.json`), 'utf8'));
}

/**
 * Подсовывает странице набор данных вместо живого summary.json.
 *
 * Подмена — единственный способ проверить редкие состояния: пустой список,
 * устаревшие данные, сервис в дауне. Ждать, пока прод сам сломается, чтобы
 * узнать, переживёт ли это страница, — плохой план.
 */
export async function serve(page: Page, data: Summary | string): Promise<void> {
  const body = typeof data === 'string' ? readFileSync(data, 'utf8') : JSON.stringify(data);
  await page.clock.setFixedTime(NOW);
  await page.route('**/data/summary.json*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body }),
  );
}

/** Ждёт, пока данные приехали и главный экран перестал быть «загружаю». */
export async function waitRendered(page: Page): Promise<void> {
  await expect(page.locator('#hero')).not.toHaveClass(/loading/);
}

/**
 * Раскрывает все карточки проектов.
 *
 * Здоровые проекты свёрнуты намеренно: десять строк со стопроцентным аптаймом
 * заслоняют единственную, где он не стопроцентный. Но проверять содержимое
 * карточки надо, поэтому в тестах раскрываем явно.
 */
export async function expandAll(page: Page): Promise<void> {
  await page.evaluate(() => {
    document.querySelectorAll('details.proj').forEach((d) => d.setAttribute('open', ''));
  });
}
