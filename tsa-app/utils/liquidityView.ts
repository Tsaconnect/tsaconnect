// utils/liquidityView.ts — pure helpers for the LP screen. All token math is
// string/BigInt over the token's own decimals (18 on BSC): no floats.
import { formatTokenAmount, parseTokenAmount } from '@/services/p2p';
import type { FiatCurrency, FiatHolding, FiatPosition, FiatWithdrawal, LiquidityMe } from '@/services/liquidityApi';
import type { WithdrawalOutcome } from '@/services/liquidityTx';

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
  if (!limitsKnown(me)) return 'Deposits are not available right now. Try again shortly.';
  if (amount < units(me.limits.min, d)) return `Add at least ${me.limits.min} ${sym}.`;
  const max = units(me.limits.max, d);
  if (principal + amount > max) {
    const room = max > principal ? max - principal : 0n;
    return `You can add at most ${formatTokenAmount(room.toString(), d, 4)} ${sym} (limit ${me.limits.max} ${sym}).`;
  }
  return null;
}

/** A deposit above the wallet's own balance, when that balance is known (decimal strings). */
export function overBalanceError(input: string, balance: string | null | undefined, sym = 'USDT', decimals = 18): string | null {
  if (balance == null || !/^\d+(?:\.\d+)?$/.test(input.trim())) return null;
  return units(input.trim(), decimals) > units(balance, decimals)
    ? `You have ${formatLiquidityAmount(balance)} ${sym} in this wallet.`
    : null;
}

/** The pool's deposit limits were read (older APIs omit the flag). */
export const limitsKnown = (me: LiquidityMe) => me.limitsReadable !== false && !!me.limits.min && !!me.limits.max;

/** The gas-free claim pays the LP wallet; it needs the minimum and the account still on that wallet. */
export function canClaimGasFree(me: LiquidityMe): boolean {
  if (!me.token || me.walletChanged) return false;
  const d = me.token.decimals;
  return units(me.claimable, d) >= units(me.minClaim, d);
}

/** The API refuses Activate while the old LP wallet still holds a position. */
export const LIQUIDITY_OLD_WALLET_HAS_FUNDS = 'liquidity_old_wallet_has_funds';

/**
 * Activating a new wallet while the old one still holds principal, earnings
 * or a pending withdrawal would strand them: the screen only signs as the
 * current LP wallet.
 */
export function oldWalletHasFunds(me: LiquidityMe): boolean {
  return me.walletChanged
    && (!isZeroAmount(me.principal) || !isZeroAmount(me.claimable) || !isZeroAmount(me.pendingWithdrawal));
}

/** Only a pending withdrawal is left: the LP cannot act on it, so the copy asks nothing of her. */
export function oldWalletFundsMessage(me: LiquidityMe): string {
  if (isZeroAmount(me.principal) && isZeroAmount(me.claimable) && !isZeroAmount(me.pendingWithdrawal)) {
    return 'A withdrawal from your old wallet is still pending. You can switch wallets once it completes.';
  }
  return `Withdraw and claim from ${me.wallet || 'your old wallet'} first (switch to it on this device), then activate your new wallet.`;
}

/** Server refusals carry a "liquidity: " package prefix that is not for users. */
export function liquidityMessage(message: string | undefined, fallback: string): string {
  const m = (message ?? '').replace(/^liquidity:\s*/i, '').trim();
  return m ? m.charAt(0).toUpperCase() + m.slice(1) : fallback;
}

/** The API refuses a claim (gas-free or signed) while the pool has no liquid USDT for it. */
export const LIQUIDITY_CLAIM_UNAVAILABLE = 'claim_unavailable';
export const CLAIM_UNAVAILABLE_MESSAGE = 'Claims are not available right now. Try again later.';

/** A claim refusal as the LP sees it: the liquidity refusal always reads the same neutral way. */
export function claimRefusalMessage(code: string | undefined, message: string | undefined): string {
  return code === LIQUIDITY_CLAIM_UNAVAILABLE ? CLAIM_UNAVAILABLE_MESSAGE : liquidityMessage(message, 'Try again in a moment.');
}

/**
 * The toast for a mined withdrawal, from the split decoded off its receipt.
 * Without one (receipt unreadable) it stays neutral rather than guess.
 */
export function withdrawalMessage(me: LiquidityMe, w: WithdrawalOutcome | undefined): string {
  if (!w || !me.token) return 'Withdrawal submitted.';
  const d = me.token.decimals;
  const sym = me.token.symbol;
  const show = (u: bigint) => `${formatLiquidityAmount(formatTokenAmount(u.toString(), d, d))} ${sym}`;
  if (w.pending === 0n) return 'Withdrawal sent to your wallet';
  if (w.paidNow === 0n) return `Withdrawal submitted. ${show(w.pending)} pending.`;
  return `Withdrawal submitted. ${show(w.paidNow)} sent now, ${show(w.pending)} pending.`;
}

/** Symbol for a currency code from the desk's list; falls back to the code itself. */
export function currencySymbol(code: string, currencies: FiatCurrency[] = []): string {
  return currencies.find((c) => c.code === code)?.symbol || '';
}

/** ₦13,650 / KES 1,200 — grouped, up to 2 decimals; the code stands in for an unknown symbol. */
export function formatCurrency(value: string | undefined, code: string, currencies: FiatCurrency[] = []): string {
  const sym = currencySymbol(code, currencies);
  const amt = formatLiquidityAmount(value, 2);
  return sym ? `${sym}${amt}` : `${code} ${amt}`;
}

/** USDT base units (decimal string) -> whole-token decimal string, exact. */
export function baseUnitsToAmount(base: string | undefined, decimals: number): string {
  if (!base) return '0';
  return formatLiquidityAmount(formatTokenAmount(base, decimals, decimals), decimals);
}

/** a × b as an exact decimal string, truncated (not rounded) to `dp` places. No floats. */
export function multiplyDecimal(a: string, b: string, dp = 2): string | null {
  const re = /^\d+(?:\.\d+)?$/;
  if (!re.test(a.trim()) || !re.test(b.trim())) return null;
  const [ai, af = ''] = a.trim().split('.');
  const [bi, bf = ''] = b.trim().split('.');
  const scale = af.length + bf.length;
  const prod = (BigInt(ai + af) * BigInt(bi + bf)).toString().padStart(scale + 1, '0');
  const int = prod.slice(0, prod.length - scale) || '0';
  const frac = prod.slice(prod.length - scale).padEnd(dp, '0').slice(0, dp);
  return dp > 0 ? `${int}.${frac}` : int;
}

/** `input` is a valid non-zero amount with at most `dp` decimals. */
export function isValidAmount(input: string, dp: number): boolean {
  const m = /^\d+(?:\.(\d+))?$/.exec(input.trim());
  return !!m && (m[1]?.length ?? 0) <= dp && !isZeroAmount(input.trim());
}

/** a > b for non-negative decimal strings. */
export function exceedsAmount(a: string, b: string | undefined, dp = 18): boolean {
  return units(a.trim(), dp) > units(b || '0', dp);
}

export type FiatActivityItem =
  | { type: 'deposit'; id: string; at: string; currency: string; fiat: string; usdt: string }
  | { type: 'withdrawal'; id: string; at: string; w: FiatWithdrawal };

/** Deposits and exits as one newest-first list. */
export function fiatActivity(p: Pick<FiatPosition, 'deposits' | 'withdrawals'>): FiatActivityItem[] {
  const items: FiatActivityItem[] = [
    ...p.deposits.map((d): FiatActivityItem => ({ type: 'deposit', id: `d-${d.id}`, at: d.createdAt, currency: d.currency, fiat: d.fiat, usdt: d.units })),
    ...p.withdrawals.map((w): FiatActivityItem => ({ type: 'withdrawal', id: `w-${w.id}`, at: w.requestedAt, w })),
  ];
  return items.sort((x, y) => Date.parse(y.at) - Date.parse(x.at));
}

/** Currencies with a balance, in API order. */
export const heldPositions = (p: Pick<FiatPosition, 'positions'>): FiatHolding[] =>
  p.positions.filter((h) => !isZeroAmount(h.principal));
