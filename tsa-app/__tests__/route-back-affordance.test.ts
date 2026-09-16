// __tests__/route-back-affordance.test.ts
//
// Structural guard for the navigation fix on this branch.
//
// Every screen the user can reach needs a way out. Native Stack headers give
// that for free, so the failure mode always has the same shape: a screen (or a
// whole group) sets `headerShown: false` intending to draw its own chrome, and
// then draws none. That is how five merchant screens and several group roots
// ended up with no back control at all — invisible on Android, where the
// hardware button covers for it, and a dead end on iOS.
//
// This test walks app/ and fails when a screen hides its header without
// rendering a back control. Most route files are thin wrappers, so it follows
// the components they actually render (one JSX identifier at a time, depth 3)
// rather than only reading the route file. Screens that legitimately have no
// back — tab roots, the auth entry points, the lock screen — are listed
// explicitly, so adding one is a deliberate, reviewable act.
import fs from 'fs';
import path from 'path';

const ROOT = path.join(__dirname, '..');
const APP_DIR = path.join(ROOT, 'app');

/**
 * Screens with no back control ON PURPOSE.
 *
 * Tab roots (the tab bar is the navigation), pre-auth entry points (there is
 * nothing behind them), the linear onboarding pager (Skip/Next only), and the
 * lock screen (backing out of it is the thing it prevents).
 */
const NO_BACK_BY_DESIGN = new Set([
  'app/index.tsx', // auth check → role redirect, renders nothing
  'app/lock.tsx', // PIN gate
  'app/login.tsx',
  'app/signup.tsx',
  'app/order.tsx', // onboarding pager — Skip/Next, no back
  'app/payments.tsx', // onboarding pager — Skip/Next, no back
  'app/+not-found.tsx', // renders its own "go home" action
  'app/(dashboard)/(tabs)/(home)/home.tsx', // tab root
  'app/(dashboard)/(tabs)/(marketplace)/index.tsx', // tab root
  'app/(dashboard)/(tabs)/(services)/serviceshome.tsx', // tab root
  'app/(dashboard)/(tabs)/easyswap.tsx', // tab root
  'app/(dashboard)/(tabs)/tokenization.tsx', // tab root
]);

const rel = (p: string) => path.relative(ROOT, p).split(path.sep).join('/');
const read = (p: string) => fs.readFileSync(p, 'utf8');
const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

function walk(dir: string, match: (name: string) => boolean, acc: string[] = []): string[] {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    // expo-router does not route a test file, so neither may this. Scanning
    // them found no missing back buttons and did crash the walk: a test's own
    // import clause is not JSX and the identifier regex built from it is not
    // always a valid pattern.
    if (entry.name === '__tests__' || /\.test\.tsx?$/.test(entry.name)) continue;
    if (entry.isDirectory()) walk(full, match, acc);
    else if (match(entry.name)) acc.push(full);
  }
  return acc;
}

const routeFiles = walk(APP_DIR, (n) => n.endsWith('.tsx') && n !== '_layout.tsx').sort();
const layoutFiles = walk(APP_DIR, (n) => n === '_layout.tsx').sort();

/** The nearest `_layout.tsx` at or above a route — the one that owns its header. */
function nearestLayout(file: string): string | null {
  let dir = path.dirname(file);
  for (;;) {
    const candidate = path.join(dir, '_layout.tsx');
    if (fs.existsSync(candidate)) return candidate;
    if (dir === APP_DIR) return null;
    dir = path.dirname(dir);
  }
}

/** How a layout addresses a route: the route's path below the layout's directory. */
function routeName(file: string, layout: string): string {
  return path.relative(path.dirname(layout), file).replace(/\.tsx$/, '').split(path.sep).join('/');
}

const SCREEN_BLOCK = /<(?:Stack|Tabs|Drawer)\.Screen\b[\s\S]*?\/>/g;

/** Whether the owning layout hides the native header for this route. */
function layoutHidesHeader(layoutSource: string, name: string): boolean {
  const blocks = layoutSource.match(SCREEN_BLOCK) ?? [];
  const own = blocks.find((b) => new RegExp(`name=["']${escapeRe(name)}["']`).test(b));
  if (own) {
    if (/headerShown:\s*false/.test(own)) return true;
    if (/headerShown:\s*true/.test(own)) return false;
  }
  // Otherwise the group-level screenOptions decide (everything outside the
  // per-screen blocks).
  return /headerShown:\s*false/.test(layoutSource.replace(SCREEN_BLOCK, ''));
}

/** Resolve a local import specifier to a file on disk. */
function resolveImport(spec: string, fromDir: string): string | null {
  let base: string;
  if (spec.startsWith('@/')) base = path.join(ROOT, spec.slice(2));
  else if (spec.startsWith('.')) base = path.resolve(fromDir, spec);
  else return null; // node_modules — not ours
  for (const candidate of [`${base}.tsx`, `${base}.ts`, path.join(base, 'index.tsx')]) {
    if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) return candidate;
  }
  return null;
}

/** Local modules whose exports this file actually renders as JSX. */
function renderedImports(source: string, fromDir: string): string[] {
  const out: string[] = [];
  const importRe = /import\s+([\s\S]*?)\s+from\s+['"]([^'"]+)['"]/g;
  for (const [, clause, spec] of source.matchAll(importRe)) {
    const names = clause
      .replace(/[{}]/g, ',')
      .split(',')
      .map((n) => n.trim().split(/\s+as\s+/).pop()!.trim())
      .filter((n) => /^[A-Z]/.test(n));
    if (!names.some((n) => new RegExp(`<${n}\\b`).test(source))) continue;
    const resolved = resolveImport(spec, fromDir);
    if (resolved) out.push(resolved);
  }
  return out;
}

const BACK_CONTROL = /<ScreenHeader\b|icon="chevronLeft"|<HeaderBackButton\b/;

/** Does this file — or a component it renders — draw a back control? */
function drawsBackControl(file: string, depth = 3, seen = new Set<string>()): boolean {
  if (depth < 0 || seen.has(file)) return false;
  seen.add(file);
  const source = read(file);
  if (BACK_CONTROL.test(source)) return true;
  return renderedImports(source, path.dirname(file)).some((f) =>
    drawsBackControl(f, depth - 1, seen),
  );
}

describe('every reachable screen has a way back', () => {
  it('finds the route tree', () => {
    expect(routeFiles.length).toBeGreaterThan(100);
  });

  it.each(routeFiles.map((f) => [rel(f), f]))('%s', (relPath, file) => {
    const source = read(file);

    // A pure redirect never paints, so it needs no chrome.
    if (/<Redirect\b/.test(source)) return;
    if (NO_BACK_BY_DESIGN.has(relPath as string)) return;

    // A screen may opt its own native header back on.
    if (/headerShown:\s*true/.test(source)) return;

    const layout = nearestLayout(file);
    expect(layout).not.toBeNull();
    if (!layoutHidesHeader(read(layout!), routeName(file, layout!))) return;

    // Header hidden — the screen must bring its own back control.
    expect(drawsBackControl(file)).toBe(true);
  });
});

describe('constants/navigation.tsx is the only source of header chrome', () => {
  it('finds the layouts', () => {
    expect(layoutFiles.length).toBeGreaterThan(10);
  });

  // Every re-declaration of headerTintColor / headerTitleStyle is a chance for
  // one section's header to drift from the rest — that is how nine layouts each
  // grew their own copy of the same six properties, and how app/adverts ended up
  // with React Navigation's unstyled default. The hook owns them; a layout that
  // needs a variation passes a config to it.
  it.each(layoutFiles.map((l) => [rel(l), l]))('%s does not re-declare header chrome', (_r, file) => {
    const source = read(file as string);
    // The tab bar and the drawer style their own chrome; they are not Stacks.
    if (/<(Tabs|Drawer)\b/.test(source)) return;
    expect(source).not.toMatch(/headerTintColor:/);
    expect(source).not.toMatch(/headerTitleStyle:/);
  });
});
