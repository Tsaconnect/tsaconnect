// __tests__/native-module-async-import.test.ts
//
// Structural guard: no `await import('<native package>')` in screens.
//
// The project has no metro.config.js, so Expo's default async-require is
// active: in a dev build every dynamic `import()` becomes an HTTP GET to the
// Metro dev server for a split bundle chunk. When the device cannot reach the
// server at that instant — different network, tunnel dropped, server
// restarted, IP changed while backgrounded — expo's fetchAsync rejects with
// `LoadBundleFromServerRequestError: Could not load bundle`, and the button
// that triggered it does nothing.
//
// That is how "enable biometrics" died: pin-setup.tsx and settings.tsx both
// reached for expo-local-authentication through `await import()`, even though
// services/localAuth.ts already require()s it into the main bundle.
//
// Dynamic imports of RELATIVE modules are left alone — those exist to break
// import cycles (api.ts ↔ localAuth.ts) and are a different trade-off.
import fs from 'fs';
import path from 'path';

const ROOT = path.join(__dirname, '..');
const SCAN_DIRS = ['app', 'components', 'services', 'hooks'];

const rel = (p: string) => path.relative(ROOT, p).split(path.sep).join('/');

function walk(dir: string, acc: string[] = []): string[] {
  if (!fs.existsSync(dir)) return acc;
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) walk(full, acc);
    else if (/\.(ts|tsx)$/.test(entry.name)) acc.push(full);
  }
  return acc;
}

const sourceFiles = SCAN_DIRS.flatMap((d) => walk(path.join(ROOT, d))).sort();

// `import('expo-foo')` / `import("react-native-bar")` — bare specifiers only.
// `typeof import('...')` is a type position, never emitted, so it is excluded.
const DYNAMIC_NATIVE_IMPORT = /(?<!typeof\s)\bimport\(\s*['"](expo[-/][^'"]*|react-native[-/][^'"]*)['"]\s*\)/g;

describe('native modules are statically bundled', () => {
  it('has source files to scan', () => {
    expect(sourceFiles.length).toBeGreaterThan(0);
  });

  it('never reaches a native module through a dynamic import()', () => {
    const offenders: string[] = [];

    for (const file of sourceFiles) {
      const src = fs.readFileSync(file, 'utf8');
      for (const match of src.matchAll(DYNAMIC_NATIVE_IMPORT)) {
        const line = src.slice(0, match.index).split('\n').length;
        offenders.push(`${rel(file)}:${line} — ${match[0]}`);
      }
    }

    expect(offenders).toEqual([]);
  });
});
