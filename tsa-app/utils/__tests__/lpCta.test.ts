import { lpCta, LP_APPLY_ROUTE, LP_DASHBOARD_ROUTE } from '@/utils/lpCta';

describe('lpCta', () => {
  it('offers the application to a non-PM, non-LP user when liquidity is available', () => {
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true })?.route).toBe(LP_APPLY_ROUTE);
  });
  it('sends an LP to the dashboard', () => {
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true, kind: 'lp' })?.route).toBe(LP_DASHBOARD_ROUTE);
  });
  it('hides for PMs, while loading, and when the pool is not configured', () => {
    expect(lpCta({ isPm: true, loading: false, liquidityAvailable: true })).toBeNull();
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true, kind: 'merchant' })).toBeNull();
    expect(lpCta({ isPm: false, loading: true, liquidityAvailable: true })).toBeNull();
    expect(lpCta({ isPm: false, loading: false })).toBeNull();
  });
});
