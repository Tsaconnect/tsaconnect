import { drawerSegments } from '../drawerSegments';

const state = (index: number) => ({
  index,
  routes: ['(tabs)', 'orderlist', 'profile', 'settings', 'categories', 'reward'].map((name) => ({ key: `k-${name}`, name })),
});

describe('drawerSegments', () => {
  it('splits after each anchor and keeps every route exactly once, in order', () => {
    const segs = drawerSegments(state(0), ['profile', 'settings', 'categories']);
    expect(segs.map((s) => s.anchor)).toEqual(['profile', 'settings', 'categories', null]);
    expect(segs.flatMap((s) => s.state.routes.map((r) => r.name))).toEqual(state(0).routes.map((r) => r.name));
  });

  it('marks the focused route active in its own segment only', () => {
    const segs = drawerSegments(state(3), ['profile', 'settings', 'categories']); // settings focused
    expect(segs.map((s) => s.state.index)).toEqual([-1, 0, -1, -1]);
  });

  it('ignores anchors that are not registered', () => {
    const segs = drawerSegments(state(0), ['nope']);
    expect(segs).toHaveLength(1);
    expect(segs[0].anchor).toBeNull();
  });
});
