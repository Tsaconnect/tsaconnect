// utils/liquidityView.ts — pure helpers for the LP screen. All token math is
// string/BigInt over the token's own decimals (18 on BSC): no floats.
import { formatTokenAmount, parseTokenAmount } from '@/services/p2p';
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

const units = (v: string | undefined, d: number) => BigInt(parseTokenAmount(v || '0', d));

export const isZeroAmount = (v?: string) => !v || /^0*(\.0*)?$/.test(v);

export type LiquidityAction = 'deposit' | 'withdraw';

/**
 * Why `input` cannot be sent as a deposit or withdrawal, or null. Feedback
 * only: the API re-checks every limit on a fresh chain read when it prepares.
 */
export function liquidityAmountError(me: LiquidityMe, action: LiquidityAction, input: string): string | null {
  if (!me.token) return 'The pool is not available right now.';
  const d = me.token.decimals;
  const sym = me.token.symbol;
  const v = input.trim();
  const m = /^\d+(?:\.(\d+))?$/.exec(v);
  if (!m) return 'Enter a valid amount.';
  if ((m[1]?.length ?? 0) > d) return `Use at most ${d} decimal places.`;
  const amount = units(v, d);
  if (amount === 0n) return 'Enter an amount.';
  const principal = units(me.principal, d);
  if (action === 'withdraw') {
    const free = me.withdrawable ?? me.principal;
    if (amount <= units(free, d)) return null;
    return amount <= principal && me.lockedUntil
      ? `You can withdraw ${formatLiquidityAmount(free)} ${sym} now. The rest of your deposit unlocks ${new Date(me.lockedUntil).toLocaleString()}.`
      : `You can withdraw at most ${formatLiquidityAmount(free)} ${sym}.`;
  }
  if (amount < units(me.limits.min, d)) return `Add at least ${me.limits.min} ${sym}.`;
  const max = units(me.limits.max, d);
  if (principal + amount > max) {
    const room = max > principal ? max - principal : 0n;
    return `You can add at most ${formatTokenAmount(room.toString(), d, 4)} ${sym} (limit ${me.limits.max} ${sym}).`;
  }
  return null;
}

/** The gas-free claim pays the LP wallet; it needs the minimum and the account still on that wallet. */
export function canClaimGasFree(me: LiquidityMe): boolean {
  if (!me.token || me.walletChanged) return false;
  const d = me.token.decimals;
  return units(me.claimable, d) >= units(me.minClaim, d);
}

/** The API refuses Activate while the old LP wallet still holds a position. */
export const LIQUIDITY_OLD_WALLET_HAS_FUNDS = 'liquidity_old_wallet_has_funds';

/**
 * Activating a new wallet while the old one still holds principal or earnings
 * would strand them: the screen only signs as the current LP wallet.
 */
export function oldWalletHasFunds(me: LiquidityMe): boolean {
  return me.walletChanged && (!isZeroAmount(me.principal) || !isZeroAmount(me.claimable));
}

export function oldWalletFundsMessage(wallet?: string): string {
  return `Withdraw and claim from ${wallet || 'your old wallet'} first (switch to it on this device), then activate your new wallet.`;
}

/** Server refusals carry a "liquidity: " package prefix that is not for users. */
export function liquidityMessage(message: string | undefined, fallback: string): string {
  const m = (message ?? '').replace(/^liquidity:\s*/i, '').trim();
  return m ? m.charAt(0).toUpperCase() + m.slice(1) : fallback;
}
