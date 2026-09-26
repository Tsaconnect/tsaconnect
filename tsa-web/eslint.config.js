import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import tseslint from 'typescript-eslint';
import { defineConfig, globalIgnores } from 'eslint/config';

const noWebStorage = 'The session is cookies and UI state lives in the URL or components; nothing is kept in web storage.';

export default defineConfig([
  globalIgnores(['dist', 'playwright-report', 'test-results']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, tseslint.configs.recommended, reactHooks.configs.flat.recommended, reactRefresh.configs.vite],
    languageOptions: { ecmaVersion: 2022, globals: globals.browser },
    rules: {
      'no-restricted-globals': ['error',
        { name: 'localStorage', message: noWebStorage }, { name: 'sessionStorage', message: noWebStorage }, { name: 'indexedDB', message: noWebStorage }],
      'no-restricted-properties': ['error',
        { object: 'window', property: 'localStorage', message: noWebStorage },
        { object: 'window', property: 'sessionStorage', message: noWebStorage },
        { object: 'window', property: 'indexedDB', message: noWebStorage },
        { object: 'globalThis', property: 'localStorage', message: noWebStorage },
        { object: 'globalThis', property: 'sessionStorage', message: noWebStorage },
        { object: 'globalThis', property: 'indexedDB', message: noWebStorage }],
    },
  },
  // Test helpers are never hot-reloaded; they share fixtures and builders with components.
  { files: ['src/test/**/*.{ts,tsx}', '**/*.test.{ts,tsx}'], rules: { 'react-refresh/only-export-components': 'off' } },
]);
