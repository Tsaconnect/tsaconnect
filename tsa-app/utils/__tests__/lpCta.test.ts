import { lpCta, LP_APPLY_ROUTE, LP_DASHBOARD_ROUTE } from '@/utils/lpCta';

describe('lpCta', () => {
  it('offers the application to a non-PM, non-LP user when liquidity is available', () => {
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true })?.route).toBe(LP_APPLY_ROUTE);
  });
  it('sends an LP to the dashboard', () => {
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true, kind: 'lp' })?.route).toBe(LP_DASHBOARD_ROUTE);
  });
  it('offers the application to a payment merchant too (owner, 2026-10-13)', () => {
    expect(lpCta({ isPm: true, loading: false, liquidityAvailable: true, kind: 'merchant' })?.route).toBe(LP_APPLY_ROUTE);
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true, kind: 'merchant' })?.route).toBe(LP_APPLY_ROUTE);
  });
  it('sends a merchant who is also an LP to the LP dashboard', () => {
    expect(lpCta({ isPm: true, loading: false, liquidityAvailable: true, kind: 'merchant', lpStatus: 'active' })?.route).toBe(LP_DASHBOARD_ROUTE);
  });
  it('hides while loading and when the pool is not configured', () => {
    expect(lpCta({ isPm: false, loading: true, liquidityAvailable: true })).toBeNull();
    expect(lpCta({ isPm: false, loading: false })).toBeNull();
  });
});
