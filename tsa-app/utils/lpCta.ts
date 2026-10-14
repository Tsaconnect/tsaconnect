// utils/lpCta.ts — the TSA Liquidity entry point (spec 2026-10-14 §6). The
// dashboard itself offers Activate, so everyone goes there.
export const LP_DASHBOARD_ROUTE = '/liquidity';

export interface LpCtaInput {
  isPm: boolean;
  loading: boolean;
  liquidityAvailable?: boolean;
  kind?: string;
  /** The caller's LP row status from /p2p/pm/me `lp`, whatever the top level shows. */
  lpStatus?: string;
}

/** null = no card. One user may be a merchant and an LP (owner, 2026-10-13). */
export function lpCta(i: LpCtaInput): { title: string; subtitle: string; route: string } | null {
  if (i.loading || !i.liquidityAvailable) return null;
  if (i.lpStatus === 'active' || i.lpStatus === 'paused' || i.kind === 'lp') {
    return { title: 'Your liquidity', subtitle: 'Balance, earnings and claims', route: LP_DASHBOARD_ROUTE };
  }
  return {
    title: 'Provide liquidity & earn',
    subtitle: 'Add USDT to the TSA Connect pool and earn a share of every sale.',
    route: LP_DASHBOARD_ROUTE,
  };
}
