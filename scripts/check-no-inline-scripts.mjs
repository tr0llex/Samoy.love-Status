// Проверка: в собранных страницах нет исполняемых инлайн-скриптов.
//
// ЗАЧЕМ. Заголовок CSP для этого сайта живёт в deploy-kit
// (nginx/snippets/samoylove-security-headers.conf) и отдаёт script-src 'self'
// — без 'unsafe-inline'. Это работает ровно до тех пор, пока в сборке нет
// инлайн-скриптов. Astro же вклеивает мелкие собранные модули в HTML по
// умолчанию; отключено это одной строкой в astro.config.mjs
// (vite.build.assetsInlineLimit: 0).
//
// Строка в конфиге — предположение, которое ничем не держится: её можно
// случайно убрать при следующей правке, обновление Astro может изменить
// поведение, а новый компонент — приехать со своим is:inline. Во всех этих
// случаях страница СОБЕРЁТСЯ и выкатится, а на проде скрипт молча не
// выполнится: CSP заблокирует его, интерфейс частично перестанет работать,
// и в консоли будет ошибка, которую никто не увидит.
//
// Поэтому проверка машинная и стоит гейтом: пусть падает сборка, а не прод.
//
// ЧТО НЕ СЧИТАЕТСЯ НАРУШЕНИЕМ. <script type="application/ld+json"> и другие
// неисполняемые типы: браузер их не выполняет, script-src на них не
// распространяется. Разметка Schema.org поэтому остаётся инлайном.

import { readdir, readFile } from 'node:fs/promises';
import { join, relative } from 'node:path';

const DIST = 'dist';

// Типы, которые браузер НЕ исполняет как скрипт.
const NON_EXECUTABLE = /^(application\/(ld\+json|json)|text\/(template|plain))$/i;

async function htmlFiles(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...(await htmlFiles(p)));
    else if (e.name.endsWith('.html')) out.push(p);
  }
  return out;
}

const files = await htmlFiles(DIST);
if (files.length === 0) {
  console.error(`✗ в ${DIST}/ нет ни одной страницы — сборки не было?`);
  process.exit(1);
}

const offenders = [];
for (const file of files) {
  const html = await readFile(file, 'utf8');
  // Открывающий тег <script ...>, у которого нет атрибута src.
  const re = /<script\b((?:[^>"']|"[^"]*"|'[^']*')*)>/gi;
  for (const m of html.matchAll(re)) {
    const attrs = m[1];
    if (/\bsrc\s*=/i.test(attrs)) continue;
    const type = attrs.match(/\btype\s*=\s*["']([^"']+)["']/i)?.[1]?.trim();
    if (type && NON_EXECUTABLE.test(type)) continue;
    offenders.push({ file: relative('.', file), tag: m[0].slice(0, 120) });
  }
}

if (offenders.length > 0) {
  console.error('✗ В сборке есть исполняемые инлайн-скрипты.');
  console.error("  CSP на проде отдаёт script-src 'self' — они не выполнятся,");
  console.error('  и сломается это МОЛЧА, уже после выкатки.');
  console.error('');
  for (const o of offenders) console.error(`  ${o.file}: ${o.tag}`);
  console.error('');
  console.error('  Починка: убедитесь, что в astro.config.mjs стоит');
  console.error('  vite.build.assetsInlineLimit: 0, а у компонента нет is:inline.');
  process.exit(1);
}

console.log(`✓ Исполняемых инлайн-скриптов нет (проверено страниц: ${files.length})`);
