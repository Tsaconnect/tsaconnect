// utils/sendForm.ts
//
// The Send form's decisions, kept out of app/wallet/send.tsx so they can be
// tested without rendering it: which asset the form opens on, whether Review is
// allowed (and why not), and whether the wallet can pay the network fee.
import type { AnyChainKey } from '../constants/chains';
import type { WalletBalanceWithChain } from '../services/walletApi';

export interface SendAsset {
  symbol: string;
  chainKey: AnyChainKey;
}

const toNumber = (value: string | undefined): number => {
  const n = parseFloat(value ?? '');
  return Number.isFinite(n) ? n : 0;
};

/**
 * The asset the form opens on.
 *
 * An explicit token + chain (arriving from a token's detail page) always wins.
 * A token without a chain opens on the chain where that token is worth most.
 * With no preference, the most valuable holding — so someone holding USDT only
 * on BNB Chain doesn't land on an empty Sonic balance. Null when nothing is
 * held: the caller keeps its own default.
 */
export function pickDefaultSendAsset(
  balances: WalletBalanceWithChain[],
  preferred: { symbol?: string; chainKey?: AnyChainKey } = {},
): SendAsset | null {
  if (preferred.symbol && preferred.chainKey) {
    return { symbol: preferred.symbol, chainKey: preferred.chainKey };
  }
  const held = balances.filter(
    (b) => toNumber(b.balance) > 0 && (!preferred.symbol || b.symbol === preferred.symbol),
  );
  if (held.length === 0) return null;
  const best = held.reduce((a, b) => (toNumber(b.usdValue) > toNumber(a.usdValue) ? b : a));
  return { symbol: best.symbol, chainKey: best.chainKey };
}

/** The wallet's native-coin balance on a chain, or null when that row is absent. */
export function nativeBalanceOn(
  balances: WalletBalanceWithChain[],
  chainKey: AnyChainKey,
  nativeSymbol: string,
): number | null {
  const row = balances.find(
    (b) => b.chainKey === chainKey && b.symbol.toUpperCase() === nativeSymbol.toUpperCase(),
  );
  return row ? toNumber(row.balance) : null;
}

/** Gas price × gas limit of a prepared EVM tx, in whole native units (18 decimals). */
export function preparedFeeNative(gasPrice: string, gasLimit: string): number {
  try {
    return Number(BigInt(gasPrice || '0') * BigInt(gasLimit || '0')) / 1e18;
  } catch {
    return 0;
  }
}

/**
 * A native amount with two significant digits below 1 — a BNB fee of
 * 0.00000578 reads as "0.0000058", not "0.000006" or "5.78e-6".
 */
export function formatNativeAmount(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0';
  const places = n >= 1 ? 4 : Math.min(12, 1 - Math.floor(Math.log10(n)));
  return n.toFixed(places).replace(/\.?0+$/, '');
}

/**
 * Whether the native balance can't cover the fee (plus the amount, when the
 * amount is itself the native coin). An unknown balance is never a shortfall —
 * a failed balance read must not lock someone out of a send they can afford.
 */
export function hasGasShortfall(
  gasBalance: number | null,
  fee: number | null,
  sendAmount: number,
  sendingNative: boolean,
): boolean {
  if (gasBalance === null) return false;
  const needed = (fee ?? 0) + (sendingNative ? sendAmount : 0);
  return gasBalance <= 0 || gasBalance < needed;
}

export interface ReviewInput {
  address: string;
  addressValid: boolean;
  /** What the user typed, in the current input mode. */
  amount: string;
  /** The amount in token units. */
  tokenAmount: string;
  balance: string;
  /** Native balance for fees; null when unknown. */
  gasBalance: number | null;
  /** Estimated fee in native units; null until estimated. */
  fee: number | null;
  nativeSymbol: string;
  sendingNative: boolean;
}

/** Why Review is unavailable, as a sentence for the user — or null when it isn't. */
export function reviewBlocker(input: ReviewInput): string | null {
  const typed = Number(input.amount);
  if (!input.amount.trim() || !Number.isFinite(typed) || typed <= 0) return 'Enter an amount.';
  const sendAmount = toNumber(input.tokenAmount);
  if (sendAmount > toNumber(input.balance)) return 'The amount is more than your balance.';
  if (!input.address.trim()) return 'Enter a recipient address.';
  if (!input.addressValid) return 'That address isn’t valid on this network.';
  if (hasGasShortfall(input.gasBalance, input.fee, sendAmount, input.sendingNative)) {
    return `Not enough ${input.nativeSymbol} to pay the network fee.`;
  }
  return null;
}

export type SendOutcome = 'success' | 'pending' | 'reverted';

/**
 * What a bounded wait for the receipt means for the user. No receipt — the
 * wait timed out, or the read failed — is "on its way", never "failed": the
 * transaction may still mine, and calling it failed invites a second send.
 */
export function sendOutcome(receipt: { status: number | null } | null): SendOutcome {
  if (!receipt) return 'pending';
  return receipt.status === 0 ? 'reverted' : 'success';
}

/** Gas actually paid by a mined EVM tx, in whole native units (18 decimals). */
export function feePaidNative(gasUsed: bigint, gasPrice: bigint): number {
  return Number(gasUsed * gasPrice) / 1e18;
}

/** `0x7a3F…c91E` — long enough to recognise, short enough for one line. */
export function shortAddress(address: string): string {
  return address.length > 14 ? `${address.slice(0, 6)}…${address.slice(-4)}` : address;
}
