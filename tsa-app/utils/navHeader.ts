// utils/navHeader.ts
//
// Which header owns a screen.
//
// The dashboard nests three navigators: a Drawer holds the Tabs, and each tab
// is its own Stack. The drawer draws the app header (hamburger · TSA Connect ·
// bell · avatar); a screen pushed inside a tab draws its own (back · title ·
// actions). Showing both stacks two nav bars on top of each other and eats
// ~120px before any content — so the drawer header stands down as soon as a tab
// pushes past its root, and the pushed screen's own header takes over.
//
// Kept pure and navigator-agnostic so it can be tested without mounting React
// Navigation. It reads the partial navigation state React Navigation hands to
// an options callback, which is undefined until the nested navigator has
// rendered at least once.

/** The slice of React Navigation's (partial) state this needs. */
export interface NestedNavState {
  index?: number;
  routes?: { state?: NestedNavState }[];
}

/**
 * True when the focused tab is showing its ROOT screen — nothing pushed on top.
 *
 * Undefined state means the nested navigator hasn't rendered yet, which only
 * happens on the tab root, so it counts as root: the drawer header must be
 * visible on first paint, not flash in afterwards.
 */
export function isAtTabRoot(state?: NestedNavState): boolean {
  if (!state) return true;
  const focusedTab = state.routes?.[state.index ?? 0];
  const tabStack = focusedTab?.state;
  if (!tabStack) return true;
  return (tabStack.index ?? 0) === 0;
}

/** The drawer header shows only on tab roots; pushed screens bring their own. */
export function showDrawerHeader(state?: NestedNavState): boolean {
  return isAtTabRoot(state);
}
