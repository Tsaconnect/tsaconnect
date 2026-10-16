import { drawerSegments } from '../drawerSegments';

const routes = ['(tabs)', 'orderlist', 'profile', 'settings', 'categories', 'reward'].map((name) => ({ key: `k-${name}`, name }));

describe('drawerSegments', () => {
  it('splits after each anchor and keeps every route once, in order, with its full-state index', () => {
    const segs = drawerSegments(routes, ['profile', 'settings', 'categories']);
    expect(segs.map((s) => s.anchor)).toEqual(['profile', 'settings', 'categories', null]);
    expect(segs.flatMap((s) => s.routes.map((r) => r.route.name))).toEqual(routes.map((r) => r.name));
    expect(segs.flatMap((s) => s.routes.map((r) => r.index))).toEqual([0, 1, 2, 3, 4, 5]);
  });

  it('ignores anchors that are not registered, and drops an empty trailing run', () => {
    expect(drawerSegments(routes, ['nope'])).toHaveLength(1);
    const segs = drawerSegments(routes, ['reward']);
    expect(segs.map((s) => s.anchor)).toEqual(['reward']);
  });
});
