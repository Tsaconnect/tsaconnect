// utils/drawerSegments.ts
//
// The dashboard drawer lists its screens with react-navigation's
// DrawerItemList, which can't interleave rows that point elsewhere (TSA Pay,
// liquidity…). Splitting the drawer state into segments that each end at an
// anchor route lets the stock list render each run unchanged, with custom rows
// placed after the anchor.

/** The slice of a drawer navigation state DrawerItemList needs. */
export interface DrawerStateLike {
  index: number;
  routes: { key: string; name: string }[];
}

export interface DrawerSegment<S extends DrawerStateLike> {
  state: S;
  /** The anchor route this segment ends with, or null for the trailing run. */
  anchor: string | null;
}

/**
 * Splits `state` after each route named in `anchors` (in drawer order). Each
 * segment's index points at the focused route inside it, or -1 when the
 * focused route is in another segment, so exactly one row reads as active.
 */
export function drawerSegments<S extends DrawerStateLike>(state: S, anchors: readonly string[]): DrawerSegment<S>[] {
  const wanted = new Set(anchors);
  const out: DrawerSegment<S>[] = [];
  let start = 0;
  const cut = (end: number, anchor: string | null) => {
    if (end <= start && anchor === null) return;
    const routes = state.routes.slice(start, end);
    const index = state.index >= start && state.index < end ? state.index - start : -1;
    out.push({ state: { ...state, routes, index }, anchor });
    start = end;
  };
  state.routes.forEach((r, i) => {
    if (wanted.has(r.name)) cut(i + 1, r.name);
  });
  cut(state.routes.length, null);
  return out;
}
