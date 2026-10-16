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

// app/_layout.tsx declares a few screens by hand (to give them a header) and
// maps the rest of each list. A hand-declared screen in the wrong section, or
// one the mapped list still includes, crashes the root stack at launch
// ("Screen names must be unique") — and a protected screen declared in the
// public section would be reachable signed out.
describe('app/_layout.tsx', () => {
  const src = fs.readFileSync(path.join(APP_DIR, '_layout.tsx'), 'utf8');
  const [publicPart, protectedPart] = src.split('<Stack.Protected');
  const declared = (part: string) => [...part.matchAll(/<Stack\.Screen\s+name="([^"]+)"/g)].map((m) => m[1]);
  const handList = (name: string): string[] =>
    JSON.parse(src.match(new RegExp(`const ${name}[^=]*=\\s*(\\[[^\\]]*\\])`))![1]);

  it('hand-declares public screens only from PUBLIC_ROUTES, and leaves them out of the mapped list', () => {
    const hand = declared(publicPart);
    for (const name of hand) expect(PUBLIC_ROUTES as readonly string[]).toContain(name);
    expect([...hand].sort()).toEqual([...handList('HEADERED_PUBLIC_SCREENS')].sort());
  });

  // Signing out removes the protected screens and the stack falls back to its
  // first screen: it must be the entry route, never a hand-declared one like
  // "pay" (which then showed "This payment link isn't valid" after logout).
  it('declares the entry route first, so signing out lands on it', () => {
    expect(PUBLIC_ROUTES[0]).toBe('index');
    const mapped = publicPart.indexOf('PUBLIC_ROUTES.filter');
    const firstHand = publicPart.search(/<Stack\.Screen\s+name="/);
    expect(mapped).toBeGreaterThan(-1);
    expect(firstHand === -1 || mapped < firstHand).toBe(true);
  });

  it('hand-declares protected screens only from PROTECTED_ROUTES, and leaves them out of the mapped list', () => {
    const hand = declared(protectedPart);
    for (const name of hand) expect(PROTECTED_ROUTES as readonly string[]).toContain(name);
    expect([...hand].sort()).toEqual([...handList('HEADERED_LEAF_SCREENS')].sort());
  });
});
