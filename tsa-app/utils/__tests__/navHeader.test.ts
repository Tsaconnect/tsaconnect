import { isAtTabRoot, showDrawerHeader, type NestedNavState } from '../navHeader';

// Shapes mirror what React Navigation hands an options callback for the
// drawer's "(tabs)" route: tab navigator state, whose focused route carries the
// tab's own stack state.
const tabsAt = (stackIndex: number, tabIndex = 0): NestedNavState => ({
  index: tabIndex,
  routes: [
    { state: { index: stackIndex, routes: [{}, {}] } },
    { state: { index: 0, routes: [{}] } },
  ],
});

describe('isAtTabRoot', () => {
  it('treats an unrendered navigator as the root, so the header never flashes in', () => {
    expect(isAtTabRoot(undefined)).toBe(true);
  });

  it('is root when the focused tab has pushed nothing', () => {
    expect(isAtTabRoot(tabsAt(0))).toBe(true);
  });

  it('is not root once the focused tab pushes a screen', () => {
    expect(isAtTabRoot(tabsAt(1))).toBe(false);
    expect(isAtTabRoot(tabsAt(3))).toBe(false);
  });

  it('reads the FOCUSED tab, not the first one', () => {
    // Tab 0 is deep in a stack, but tab 1 is focused and sits at its root.
    expect(isAtTabRoot({ ...tabsAt(2), index: 1 })).toBe(true);
  });

  it('treats a tab that has no stack state yet as root', () => {
    expect(isAtTabRoot({ index: 0, routes: [{}] })).toBe(true);
  });

  it('defaults a missing index to 0 rather than crashing', () => {
    expect(isAtTabRoot({ routes: [{ state: { routes: [{}] } }] })).toBe(true);
    expect(isAtTabRoot({ routes: [{ state: { index: 2, routes: [{}] } }] })).toBe(false);
  });

  it('survives an empty or malformed state', () => {
    expect(isAtTabRoot({})).toBe(true);
    expect(isAtTabRoot({ index: 5, routes: [] })).toBe(true);
  });
});

describe('showDrawerHeader', () => {
  it('shows on tab roots and hides on pushed screens', () => {
    expect(showDrawerHeader(tabsAt(0))).toBe(true);
    expect(showDrawerHeader(tabsAt(1))).toBe(false);
  });
});
