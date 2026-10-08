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
  if (amount === 0n) {
    return needsStockAd && held > 0n
      ? { ok: true, amountBaseUnits: '0', needsStockAd }
      : { ok: false, error: 'Enter an amount.' };
  }
  if (amount > units(me.headroom, d)) {
    return { ok: false, error: `You can add at most ${formatLiquidityAmount(me.headroom)} ${sym} (limit ${me.limits.max} ${sym}).` };
  }
  if (held === 0n && amount < units(me.limits.min, d)) {
    return { ok: false, error: `Your first deposit must be at least ${me.limits.min} ${sym}.` };
  }
  return { ok: true, amountBaseUnits: amount.toString(), needsStockAd };
}
