// app/__tests__/routeAccess.test.ts
//
// Every route in the root stack is classified public or protected. A new
// screen added to app/ fails this until someone decides whether a signed-out
// user may see it — the default is not "yes by omission".
import fs from 'fs';
import path from 'path';
import { PROTECTED_ROUTES, PUBLIC_ROUTES } from '@/constants/routeAccess';

const APP_DIR = path.join(__dirname, '..');
const isScreen = (f: string) => /\.tsx?$/.test(f) && !f.startsWith('_') && !f.includes('.test.');

/** Route names as the root Stack sees them: a directory with a _layout is one route; without one, each file is. */
function rootRouteNames(): string[] {
  const names: string[] = [];
  for (const entry of fs.readdirSync(APP_DIR, { withFileTypes: true })) {
    if (entry.name === '__tests__') continue;
    if (entry.isFile()) {
      if (isScreen(entry.name)) names.push(entry.name.replace(/\.tsx?$/, ''));
      continue;
    }
    const dir = path.join(APP_DIR, entry.name);
    if (fs.existsSync(path.join(dir, '_layout.tsx'))) {
      names.push(entry.name);
      continue;
    }
    const walk = (d: string, prefix: string) => {
      for (const e of fs.readdirSync(d, { withFileTypes: true })) {
        if (e.name === '__tests__') continue;
        if (e.isDirectory()) walk(path.join(d, e.name), `${prefix}/${e.name}`);
        else if (isScreen(e.name)) names.push(`${prefix}/${e.name.replace(/\.tsx?$/, '')}`);
      }
    };
    walk(dir, entry.name);
  }
  return names.sort();
}

describe('root route access', () => {
  it('classifies every root route exactly once', () => {
    const classified = [...PUBLIC_ROUTES, ...PROTECTED_ROUTES];
    expect(new Set(classified).size).toBe(classified.length);
    expect([...classified].sort()).toEqual(rootRouteNames());
  });

  it('keeps the entry route public — it is where a guarded screen falls back to', () => {
    expect(PUBLIC_ROUTES).toContain('index');
    expect(PUBLIC_ROUTES).toContain('login');
  });
});
