// Агент и сторож обязаны судить одинаково.
//
// Правила оценки написаны дважды: в agent/main.go для того, кто живёт на
// сервере, и в scripts/probe.mjs для того, кто смотрит снаружи. Так сделано
// намеренно — внешняя проверка не должна зависеть от того же кода и той же
// машины, иначе она перестаёт быть внешней. Плата за это — расхождение,
// которое ничем не ловится: bodyIncludes однажды уже появился только у
// сторожа, и заметить это можно было лишь глазами.
//
// Здесь обе реализации проходят по одному набору случаев на одном локальном
// сервере, и их вердикты сравниваются. Расхождение падает в CI, а не всплывает
// в аварию.
//
//   node --test scripts/conformance.test.mjs

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const GO = process.platform === 'win32' ? 'go.exe' : 'go';

// Порог «медленно» и задержка медленного ответа разведены с запасом: тест не
// должен зависеть от того, успела ли машина за 50 мс.
const SLOW_MS = 300;
const SLOW_DELAY_MS = 900;

/**
 * Случаи подобраны так, чтобы каждый бил в отдельное правило. Ожидание
 * записано явно: если обе реализации ошибутся одинаково, сравнение их между
 * собой промолчит, а этот столбец — нет.
 */
const CASES = [
  { id: 'ok', path: '/ok', want: 'up', note: 'обычный успешный ответ' },
  { id: 'notfound', path: '/404', want: 'down', note: 'код ответа не тот, что ждём' },
  {
    id: 'body',
    path: '/wrong-body',
    want: 'down',
    bodyIncludes: 'всё хорошо',
    note: 'ответ 200 с текстом ошибки в теле',
  },
  {
    id: 'type',
    path: '/wrong-type',
    want: 'down',
    expectType: 'application/json',
    note: 'HTML вместо JSON',
  },
  { id: 'slow', path: '/slow', want: 'slow', slowMs: SLOW_MS, note: 'отвечает дольше порога' },
  { id: 'empty', path: '/empty', want: 'down', bodyIncludes: 'ok', note: 'пустое тело при 200' },
];

function startServer() {
  const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    switch (url.pathname) {
      case '/ok':
        res.writeHead(200, { 'content-type': 'text/plain; charset=utf-8' });
        return res.end('всё хорошо');
      case '/404':
        res.writeHead(404, { 'content-type': 'text/plain' });
        return res.end('нет такой страницы');
      case '/wrong-body':
        res.writeHead(200, { 'content-type': 'text/plain; charset=utf-8' });
        return res.end('Internal Server Error: база недоступна');
      case '/wrong-type':
        res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
        return res.end('<html><body>ошибка</body></html>');
      case '/slow':
        return setTimeout(() => {
          res.writeHead(200, { 'content-type': 'text/plain; charset=utf-8' });
          res.end('всё хорошо');
        }, SLOW_DELAY_MS);
      case '/empty':
        res.writeHead(200, { 'content-type': 'text/plain' });
        return res.end('');
      default:
        res.writeHead(500);
        return res.end('нет такого случая');
    }
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => resolve({ server, port: server.address().port }));
  });
}

function configFor(port) {
  return {
    projects: [
      {
        id: 'conformance',
        title: 'Соответствие',
        url: `http://127.0.0.1:${port}/ok`,
        checks: CASES.map((c) => ({
          id: c.id,
          name: c.id,
          url: `http://127.0.0.1:${port}${c.path}`,
          ...(c.bodyIncludes ? { bodyIncludes: c.bodyIncludes } : {}),
          ...(c.expectType ? { expectType: c.expectType } : {}),
          ...(c.slowMs ? { slowMs: c.slowMs } : {}),
        })),
      },
    ],
  };
}

function run(cmd, args, opts) {
  return new Promise((resolve, reject) => {
    // Без shell: путь к node на Windows лежит в «Program Files», и оболочка
    // разрывает его по пробелу. Имя go при этом указываем с расширением —
    // без оболочки PATHEXT не применяется.
    const p = spawn(cmd, args, { ...opts, stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '';
    let err = '';
    p.stdout.on('data', (d) => (out += d));
    p.stderr.on('data', (d) => (err += d));
    p.on('error', reject);
    p.on('close', (code) =>
      code === 0
        ? resolve({ out, err })
        : reject(new Error(`${cmd} вышел с кодом ${code}\n${err}${out}`)),
    );
  });
}

/** Вердикты из summary.json, который пишут обе реализации. */
async function verdicts(dataDir) {
  const doc = JSON.parse(await readFile(join(dataDir, 'summary.json'), 'utf8'));
  const out = {};
  for (const p of doc.projects) {
    for (const c of p.checks) out[c.id] = c.status;
  }
  return out;
}

test('агент и сторож судят одинаково', async (t) => {
  const { server, port } = await startServer();
  const dir = await mkdtemp(join(tmpdir(), 'conformance-'));
  t.after(async () => {
    server.close();
    await rm(dir, { recursive: true, force: true });
  });

  const configPath = join(dir, 'status.json');
  await writeFile(configPath, JSON.stringify(configFor(port)), 'utf8');

  const agentData = join(dir, 'agent');
  const probeData = join(dir, 'probe');

  // Агент. Метрики уводим в тот же временный каталог: в /var/lib его писать
  // некуда, а падать из-за этого тест не должен.
  await run(
    GO,
    ['run', '.', '-config', configPath, '-data', agentData, '-metrics', join(dir, 'agent.prom')],
    {
      cwd: join(ROOT, 'agent'),
    },
  );

  // Сторож. Адрес сводки уводим в никуда осознанно: дед-мэн к сравнению
  // правил отношения не имеет, а без переменной он полез бы в прод.
  await run(process.execPath, [join(ROOT, 'scripts/probe.mjs')], {
    cwd: ROOT,
    env: {
      ...process.env,
      STATUS_CONFIG: configPath,
      STATUS_DATA: probeData,
      STATUS_SUMMARY_URL: `http://127.0.0.1:${port}/404`,
      TELEGRAM_BOT_TOKEN: '',
      TELEGRAM_CHAT_ID: '',
    },
  });

  const fromAgent = await verdicts(agentData);
  const fromProbe = await verdicts(probeData);

  for (const c of CASES) {
    assert.equal(fromAgent[c.id], c.want, `агент: ${c.note}`);
    assert.equal(fromProbe[c.id], c.want, `сторож: ${c.note}`);
  }
  assert.deepEqual(fromAgent, fromProbe, 'вердикты агента и сторожа разошлись');
});
