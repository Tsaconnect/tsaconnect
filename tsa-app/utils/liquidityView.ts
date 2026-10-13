// utils/liquidityView.ts — pure helpers for the LP screens. All token math is
// string/BigInt over the token's own decimals (18 on BSC): no floats.
import { parseTokenAmount } from '@/services/p2p';
import type { LiquidityMe } from '@/services/liquidityApi';

/** Group the integer part and cut the fraction to maxFrac digits, exactly. */
export function formatLiquidityAmount(value: string | undefined, maxFrac = 4): string {
  if (!value) return '0';
  const neg = value.startsWith('-');
  const [int, frac = ''] = (neg ? value.slice(1) : value).split('.');
  const grouped = (int.replace(/^0+(?=\d)/, '') || '0').replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const cut = frac.slice(0, maxFrac).replace(/0+$/, '');
  return `${neg ? '-' : ''}${grouped}${cut ? `.${cut}` : ''}`;
}

export type LiquidityStage = 'none' | 'applied' | 'rejected' | 'activate' | 'active' | 'paused';

export function liquidityStage(me: LiquidityMe): LiquidityStage {
  return me.status === 'approved' ? 'activate' : me.status;
}

export type LpDepositPlan =
  | { ok: true; amountBaseUnits: string; needsStockAd: boolean }
  | { ok: false; error: string };

const units = (v: string | undefined, d: number) => BigInt(parseTokenAmount(v || '0', d));

/**
 * What an LP deposit needs (API spec §8.3–§8.4): the exact amount the LP typed
 * and whether a stock ad must be (re)signed — none live, the live one paused
 * (review I3) or due. Limits here are feedback only; the API re-checks them on
 * a fresh chain read, and the funding target is computed from a fresh balance
 * at submit (runLpDeposit, review I4), never from this cached view.
 */
export function lpDepositPlan(me: LiquidityMe, input: string): LpDepositPlan {
  if (!me.token || me.status !== 'active') {
    return { ok: false, error: me.paused ? 'Your liquidity is paused. Contact support.' : 'Activate your liquidity first.' };
  }
  const d = me.token.decimals;
  const sym = me.token.symbol;
  let amount: bigint;
  try {
    amount = units(input.trim(), d);
  } catch {
    return { ok: false, error: 'Enter a valid amount.' };
  }
  const held = units(me.available, d) + units(me.locked, d);
  const needsStockAd = !me.voucher || me.voucher.status !== 'active' || me.voucher.renewDue;
  // Renew only: the API signs a stock ad without reading any balance, so a
  // cached view that has not caught up with a deposit that just landed must
  // not send the LP to deposit again (review I4).
  if (amount === 0n) {
    return needsStockAd ? { ok: true, amountBaseUnits: '0', needsStockAd } : { ok: false, error: 'Enter an amount.' };
  }
  if (amount > units(me.headroom, d)) {
    return { ok: false, error: `You can add at most ${formatLiquidityAmount(me.headroom)} ${sym} (limit ${me.limits.max} ${sym}).` };
  }
  if (held === 0n && amount < units(me.limits.min, d)) {
    return { ok: false, error: `Your first deposit must be at least ${me.limits.min} ${sym}.` };
  }
  return { ok: true, amountBaseUnits: amount.toString(), needsStockAd };
}

/** The LP's wallet on the pool chain: the pool token and the native gas coin. null = unread. */
export interface LpWalletView {
  token: string | null;
  native: string | null;
  nativeSymbol: string;
}

// Enough native coin for an approve and a deposit on BSC with room to spare.
const MIN_GAS_NATIVE = '0.0005';

/**
 * What Max fills in: the wallet's token balance, capped by the pool headroom.
 * null when the wallet balance is unknown.
 */
export function lpMaxAddable(me: LiquidityMe, wallet: LpWalletView): string | null {
  if (!me.token || wallet.token == null) return null;
  const d = me.token.decimals;
  const held = units(wallet.token, d);
  const headroom = units(me.headroom, d);
  const max = held < headroom ? held : headroom;
  return formatUnits(max > 0n ? max : 0n, d);
}

/**
 * A wallet-side reason the deposit cannot go through yet, or null. Unknown
 * balances never block: runLpDeposit and the chain have the last word.
 */
export function lpWalletShortfall(me: LiquidityMe, wallet: LpWalletView, input: string): string | null {
  if (!me.token) return null;
  const d = me.token.decimals;
  let amount: bigint;
  try {
    amount = units(input.trim(), d);
  } catch {
    return null;
  }
  if (amount > 0n && wallet.token != null && amount > units(wallet.token, d)) {
    return `Your wallet has ${formatLiquidityAmount(wallet.token)} ${me.token.symbol}. Add more to your wallet or enter a smaller amount.`;
  }
  if (amount > 0n && wallet.native != null && units(wallet.native, 18) < units(MIN_GAS_NATIVE, 18)) {
    return `You need a little ${wallet.nativeSymbol} in your wallet to pay the network fee.`;
  }
  return null;
}

function formatUnits(v: bigint, d: number): string {
  const s = v.toString().padStart(d + 1, '0');
  const int = s.slice(0, s.length - d);
  const frac = s.slice(s.length - d).replace(/0+$/, '');
  return frac ? `${int}.${frac}` : int;
}
