// utils/lpCta.ts — the P2P screen's TSA Liquidity entry point (spec §17).
export const LP_APPLY_ROUTE = '/merchants/merchant-request?type=liquidity_provider';
export const LP_DASHBOARD_ROUTE = '/liquidity';

export interface LpCtaInput {
  isPm: boolean;
  loading: boolean;
  liquidityAvailable?: boolean;
  kind?: string;
}

/** null = no card. An LP goes to its dashboard; a PM never sees it (decision 11). */
export function lpCta(i: LpCtaInput): { title: string; subtitle: string; route: string } | null {
  if (i.loading || !i.liquidityAvailable) return null;
  if (i.kind === 'lp') return { title: 'Your liquidity', subtitle: 'Stock, limits and payouts', route: LP_DASHBOARD_ROUTE };
  if (i.isPm || i.kind === 'merchant') return null;
  return {
    title: 'Provide liquidity & earn',
    subtitle: 'Add USDT to the TSA Connect pool and get paid in naira when it sells.',
    route: LP_APPLY_ROUTE,
  };
}
