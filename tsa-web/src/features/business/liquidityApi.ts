// TSA Liquidity on the web (spec 2026-10-14): the owner's own LP position and
// the gas-free claim to their app wallet. Depositing, withdrawing and
// activating are signed by that wallet, so they stay in the app. Field names
// match tsa-api-go services/liquidity_vault_lp.go; token amounts are decimal
// strings in whole tokens.
import { api } from '@/lib/api';

export type LiquidityMe = {
  status: 'none' | 'active' | 'paused';
  poolOpen: boolean;
  chain?: { id: number; key: string; name: string };
  token?: { symbol: string; address: string; decimals: number };
  wallet?: string;
  balanceReadable: boolean;
  principal?: string;
  claimable?: string;
  earned?: string;
  sharePercent?: string;
  limits: { min: string; max: string };
  minClaim: string;
  paused: boolean;
  walletChanged: boolean;
  pool?: { totalLiquidity: string; coveragePercent: string; apr30d: string };
};

export type LiquidityActivity = {
  kind: 'deposit' | 'withdrawal' | 'claim' | 'earned';
  amount: string;
  to?: string;
  txHash: string;
  at: string;
};

export const LIQUIDITY_KEY = ['liquidity-me'] as const;
export const LIQUIDITY_ACTIVITY_KEY = ['liquidity-activity'] as const;

export const getLiquidity = (signal?: AbortSignal) => api<LiquidityMe>('/liquidity/me', { signal });

export const listLiquidityActivity = (cursor: string | undefined, signal?: AbortSignal) =>
  api<{ items: LiquidityActivity[]; nextCursor?: string }>(
    `/liquidity/me/activity${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`,
    { signal },
  );

export const claimLiquidity = (idempotencyKey: string) =>
  api<{ txHash: string }>('/liquidity/claim-for', { method: 'POST', body: {}, headers: { 'Idempotency-Key': idempotencyKey } });

/** Grouped integer part, the fraction cut (not rounded) to maxFrac digits. */
export function tokenAmount(value: string | undefined, maxFrac = 4): string {
  if (!value) return '0';
  const [int, frac = ''] = value.split('.');
  const grouped = (int.replace(/^0+(?=\d)/, '') || '0').replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const cut = frac.slice(0, maxFrac).replace(/0+$/, '');
  return cut ? `${grouped}.${cut}` : grouped;
}

/** Exact comparison of two whole-token decimal strings. */
function units(v: string | undefined, decimals: number): bigint {
  const [int, frac = ''] = (v || '0').split('.');
  return BigInt(int || '0') * 10n ** BigInt(decimals) + BigInt((frac + '0'.repeat(decimals)).slice(0, decimals) || '0');
}

/** The gas-free claim needs the minimum and the account still on its LP wallet. */
export function canClaim(me: LiquidityMe): boolean {
  if (!me.token || me.walletChanged) return false;
  return units(me.claimable, me.token.decimals) >= units(me.minClaim, me.token.decimals);
}

const isZero = (v?: string) => !v || /^0*(\.0*)?$/.test(v);

/** The old LP wallet still holds a position: activating a new one first would strand it. */
export function oldWalletHasFunds(me: LiquidityMe): boolean {
  return me.walletChanged && (!isZero(me.principal) || !isZero(me.claimable));
}
