// utils/poolCard.ts — the pinned "TSA Connect · Recommended" card on the P2P
// screen (TSA Liquidity spec §17, decision 12/14) and the buy screen it opens.
import type { PoolCard } from '@/services/liquidityApi';
import { formatMinorAsNaira } from '@/utils/stableBuyQuote';

export interface PoolCardView {
  title: string;
  badge: string;
  disabled: boolean;
  lines: string[];
  /** The IT buy screen with USDT on the pool chain preselected and pool=1. */
  route?: string;
}

// "1361.51" (naira, major units) → "₦1,361.51".
const naira = (major?: string) => (major ? `₦${formatMinorAsNaira(String(Math.round(Number(major) * 100)))}` : '');

/** null = no card: the pool is dormant, or she is on the sell side. */
export function poolCardView(card: PoolCard | null | undefined, adSide: 'sell' | 'buy'): PoolCardView | null {
  if (!card || !card.enabled || adSide !== 'sell') return null;
  const base = { title: card.name || 'TSA Connect', badge: card.badge || 'Recommended' };
  if (!card.available || !card.token || !card.chain) {
    return { ...base, disabled: true, lines: ['Unavailable right now'] };
  }
  const q = new URLSearchParams({
    asset: card.token.symbol,
    pool: '1',
    chainId: String(card.chain.id),
    chain: card.chain.key,
    tokenAddress: card.token.address,
  });
  return {
    ...base,
    disabled: false,
    lines: [
      `${naira(card.rate)} per ${card.token.symbol} · Funding fee ${card.fundingFeePercent}%`,
      `${naira(card.minNgn)} – ${naira(card.maxNgn)} · Instant`,
    ],
    route: `/wallet/paycrest/buy?${q.toString()}`,
  };
}

/** The buy screen's view of a pool card route. */
export function poolBuyParams(p: { pool?: string; chainId?: string; chain?: string; tokenAddress?: string; asset?: string }): {
  pool: boolean;
  want?: { token: string; chainId: number; chain: string; address: string };
} {
  const pool = p.pool === '1';
  const chainId = Number(p.chainId);
  if (!pool || !Number.isInteger(chainId) || chainId <= 0 || !p.chain || !p.tokenAddress) return { pool };
  return { pool, want: { token: (p.asset || 'USDT').toUpperCase(), chainId, chain: p.chain, address: p.tokenAddress } };
}
