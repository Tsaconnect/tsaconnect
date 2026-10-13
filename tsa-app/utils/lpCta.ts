// utils/lpCta.ts — the P2P screen's TSA Liquidity entry point (spec §17).
export const LP_APPLY_ROUTE = '/merchants/merchant-request?type=liquidity_provider';
export const LP_DASHBOARD_ROUTE = '/liquidity';

export interface LpCtaInput {
  isPm: boolean;
  loading: boolean;
  liquidityAvailable?: boolean;
  kind?: string;
  /** The caller's LP row status from /p2p/pm/me `lp`, whatever the top level shows. */
  lpStatus?: string;
}

/**
 * null = no card. An LP (or a merchant who is also one) goes to the dashboard;
 * anyone else, merchants included, may apply (owner, 2026-10-13: one user may hold both).
 */
export function lpCta(i: LpCtaInput): { title: string; subtitle: string; route: string } | null {
  if (i.loading || !i.liquidityAvailable) return null;
  if (i.lpStatus || i.kind === 'lp') return { title: 'Your liquidity', subtitle: 'Stock, limits and payouts', route: LP_DASHBOARD_ROUTE };
  return {
    title: 'Provide liquidity & earn',
    subtitle: 'Add USDT to the TSA Connect pool and get paid in naira when it sells.',
    route: LP_APPLY_ROUTE,
  };
}
