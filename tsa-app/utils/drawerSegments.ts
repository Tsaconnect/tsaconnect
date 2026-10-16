// utils/drawerSegments.ts
//
// The dashboard drawer interleaves rows that point elsewhere (TSA Pay,
// liquidity…) with its own screens. The screens are split into runs that each
// end at an anchor route, and the custom rows go after the anchor. Each route
// keeps its index in the FULL drawer state, so "is this row focused" is always
// answered against the real state — never a trimmed copy (react-navigation's
// DrawerItemList crashes on a slice without the focused route).

export interface DrawerRouteLike {
  key: string;
  name: string;
}

export interface DrawerSegment<R extends DrawerRouteLike> {
  /** Routes in drawer order, each with its index in the full state. */
  routes: { route: R; index: number }[];
  /** The anchor route this run ends with, or null for the trailing run. */
  anchor: string | null;
}

/** Splits `routes` after each route named in `anchors` (in drawer order). */
export function drawerSegments<R extends DrawerRouteLike>(routes: readonly R[], anchors: readonly string[]): DrawerSegment<R>[] {
  const wanted = new Set(anchors);
  const out: DrawerSegment<R>[] = [];
  let run: { route: R; index: number }[] = [];
  routes.forEach((route, index) => {
    run.push({ route, index });
    if (wanted.has(route.name)) {
      out.push({ routes: run, anchor: route.name });
      run = [];
    }
  });
  if (run.length) out.push({ routes: run, anchor: null });
  return out;
}
