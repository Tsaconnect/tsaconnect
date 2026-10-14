import { lpCta, LP_DASHBOARD_ROUTE } from '@/utils/lpCta';

describe('lpCta', () => {
  it('sends everyone to the liquidity screen, which offers Activate', () => {
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true })).toMatchObject({
      title: 'Provide liquidity & earn', route: LP_DASHBOARD_ROUTE,
    });
    expect(lpCta({ isPm: true, loading: false, liquidityAvailable: true, kind: 'merchant' })?.route).toBe(LP_DASHBOARD_ROUTE);
  });
  it('names it "Your liquidity" for an LP, merchants included (owner, 2026-10-13)', () => {
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true, kind: 'lp' })?.title).toBe('Your liquidity');
    expect(lpCta({ isPm: true, loading: false, liquidityAvailable: true, kind: 'merchant', lpStatus: 'paused' })?.title).toBe('Your liquidity');
  });
  it('treats a retired application as not yet an LP', () => {
    expect(lpCta({ isPm: false, loading: false, liquidityAvailable: true, lpStatus: 'applied' })?.title).toBe('Provide liquidity & earn');
  });
  it('hides while loading and when the pool is not configured', () => {
    expect(lpCta({ isPm: false, loading: true, liquidityAvailable: true })).toBeNull();
    expect(lpCta({ isPm: false, loading: false })).toBeNull();
  });
});
