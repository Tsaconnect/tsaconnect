// TSA Liquidity on the web (spec 2026-10-14): the owner's own LP position and
// the gas-free claim to their app wallet. Depositing, withdrawing and
// activating are signed by that wallet, so they stay in the app. Field names
// match tsa-api-go services/liquidity_vault_lp.go; token amounts are decimal
// strings in whole tokens.
import { api, ApiError } from '@/lib/api';

export type LiquidityMe = {
  status: 'none' | 'active' | 'paused';
  poolOpen: boolean;
  chain?: { id: number; key: string; name: string };
  token?: { symbol: string; address: string; decimals: number };
  wallet?: string;
  balanceReadable: boolean;
  principal?: string;
  /** Principal that may be withdrawn now: a deposit is locked for 24 hours. */
  withdrawable?: string;
  /** When the rest unlocks; absent when nothing is locked. */
  lockedUntil?: string;
  /** Withdrawn principal not yet paid out; omitted when zero. */
  pendingWithdrawal?: string;
  claimable?: string;
  earned?: string;
  sharePercent?: string;
  limits: { min: string; max: string };
  minClaim: string;
  paused: boolean;
  walletChanged: boolean;
  pool?: { totalLiquidity?: string; apr30d?: string };
};

export type LiquidityActivity = {
  kind: 'deposit' | 'withdrawal' | 'claim' | 'earned' | 'withdrawal_pending' | 'withdrawal_paid';
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

export const isZero = (v?: string) => !v || /^0*(\.0*)?$/.test(v);

/** The old LP wallet still holds a position or a pending withdrawal: activating a new one first would strand it. */
export function oldWalletHasFunds(me: LiquidityMe): boolean {
  return me.walletChanged && (!isZero(me.principal) || !isZero(me.claimable) || !isZero(me.pendingWithdrawal));
}

/** Only a pending withdrawal is left on the old wallet: nothing for the owner to do but wait. */
export function oldWalletOnlyPending(me: LiquidityMe): boolean {
  return me.walletChanged && isZero(me.principal) && isZero(me.claimable) && !isZero(me.pendingWithdrawal);
}

/** The API refuses a claim while the pool has no liquid USDT for it. */
export const CLAIM_UNAVAILABLE = 'claim_unavailable';

/** Why a gas-free claim was refused, as the owner sees it. */
export function claimErrorMessage(err: unknown): string {
  if (err instanceof ApiError && err.code === CLAIM_UNAVAILABLE) return 'Claims are not available right now. Try again later.';
  if (err instanceof ApiError && err.status === 429) return 'You can claim gas-free once every 8 hours. Claim from the app to do it now.';
  return err instanceof Error ? err.message : 'Could not claim.';
}

/** Money leaving the position is −; a paid pending withdrawal was already counted when it went pending. */
export function activitySign(kind: LiquidityActivity['kind']): string {
  if (kind === 'withdrawal' || kind === 'claim' || kind === 'withdrawal_pending') return '−';
  if (kind === 'withdrawal_paid') return '';
  return '+';
}
