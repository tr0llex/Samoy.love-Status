// Плоский конфиг ESLint 10. Проверяются две вещи: внешний сторож (Node) и
// клиентский скрипт внутри страницы (браузер) — у них разные глобальные
// объекты, поэтому и блока два.
import js from '@eslint/js';
import globals from 'globals';
import astro from 'eslint-plugin-astro';

export default [
  { ignores: ['dist/', '.astro/', 'node_modules/', 'stage/', 'data/'] },

  js.configs.recommended,
  ...astro.configs.recommended,

  {
    files: ['scripts/**/*.mjs', '*.config.mjs', '*.config.js'],
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: globals.node,
    },
  },

  {
    // Скрипты внутри .astro исполняются в браузере.
    files: ['**/*.astro'],
    languageOptions: { globals: globals.browser },
  },
];
