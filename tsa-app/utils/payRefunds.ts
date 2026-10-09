// utils/payRefunds.ts
//
// Pure helpers for a TSA Pay refund in the app: the list's order, the options, whether her
// wallet is the one that must send it, whether she holds enough of the token, and the
// sentences the screens show. No imports with side effects, so it tests without mocks.
import type { PayRefundDetail, PayRefundOption, PayRefundStatus } from '@/services/payMerchantApi';
import type { WalletBalance } from '@/services/walletApi';
import { shortAddress } from './sendForm';

const RANK: Record<PayRefundStatus, number> = {
  requested: 0, needs_review: 1, submitted: 2, confirmed: 3, failed: 3, cancelled: 3,
};

/** Waiting for her first, then those under review or on their way, then the rest — newest first within each. */
export function sortRefundsOpenFirst<T extends { status: PayRefundStatus; createdAt: string }>(rows: T[]): T[] {
  return [...rows].sort((a, b) => RANK[a.status] - RANK[b.status] || Date.parse(b.createdAt) - Date.parse(a.createdAt));
}

export const optionKey = (o: Pick<PayRefundOption, 'chainId' | 'tokenSymbol'>) => `${o.chainId}:${o.tokenSymbol}`;

/** The customer's own deposit network and token, else the first offered. */
export const defaultOption = (options: PayRefundOption[]) => options.find((o) => o.isDefault) ?? options[0];

export const sameAddress = (a?: string | null, b?: string | null) => !!a && !!b && a.toLowerCase() === b.toLowerCase();

/** The server names one wallet; anything else on this device must not sign the refund. */
export const wrongWalletMessage = (fromAddress: string) =>
  `This refund is sent from your TSA wallet ${shortAddress(fromAddress)}. Switch to that wallet in Wallet → Manage Wallet, then come back.`;

/** The server names no wallet ("" fromAddress) until the owner proves the one on this device. */
export const unprovenWalletMessage =
  'Prove this wallet to send refunds. TSA Connect checks that the wallet on this device is yours before any money leaves it.';

/** The network refused the last send; the server retries it (plans review I6). */
export const refundBroadcastNote = (reason: string) => `Couldn't broadcast: ${reason} — top up gas; we retry automatically.`;

export type RefundFunding =
  | { kind: 'unknown' }
  | { kind: 'empty' }
  | { kind: 'short'; have: string }
  | { kind: 'enough'; have: string };

/** "12.4999999" at 6 decimals → 12499999n (extra digits dropped); not a number → null. */
function baseUnits(amount: string, decimals: number): bigint | null {
  const m = /^(\d+)(?:\.(\d+))?$/.exec(amount.trim());
  if (!m) return null;
  return BigInt(m[1] + (m[2] ?? '').slice(0, decimals).padEnd(decimals, '0'));
}

/**
 * Her balance of the option's token against the exact refund. Unknown balances never block
 * (a failed read must not lock her out); the send still fails loudly if the funds aren't there.
 */
export function refundFunding(balances: WalletBalance[] | null, option: PayRefundOption): RefundFunding {
  if (!balances) return { kind: 'unknown' };
  const row =
    balances.find((b) => b.contractAddress && b.contractAddress.toLowerCase() === option.tokenAddress.toLowerCase()) ??
    balances.find((b) => !b.native && b.symbol.toUpperCase() === option.tokenSymbol.toUpperCase());
  const have = row?.balance ?? '0';
  const units = baseUnits(have, option.decimals);
  if (units === null) return { kind: 'unknown' };
  if (units === 0n) return { kind: 'empty' };
  return units < BigInt(option.amountBaseUnits) ? { kind: 'short', have } : { kind: 'enough', have };
}

/** Where a refund stands once there is nothing for her to send. */
export function refundStatusLine(d: Pick<PayRefundDetail, 'refund' | 'payerFirstName'>): string {
  const r = d.refund;
  const who = d.payerFirstName || 'Your customer';
  switch (r.status) {
    case 'submitted':
      return r.lastBroadcastError
        ? `Sent to ${who}, but the network hasn't taken it yet. ${refundBroadcastNote(r.lastBroadcastError)}`
        : `Sent to ${who}. Waiting for the network to confirm it.`;
    case 'confirmed':
      return `${who} received ${r.credited || r.amount} ${r.tokenSymbol ?? 'USD'}.`;
    case 'failed':
      return `Nothing was refunded: ${r.failureReason || 'the transaction failed'}. Ask for the refund again from your dashboard on the web.`;
    case 'needs_review':
      return 'TSA Connect is checking this transfer. We will be in touch.';
    case 'cancelled':
      return 'This refund request was cancelled.';
    default:
      // Waiting, but no network can carry it right now (no options from the server).
      return "This refund can't be sent from the app right now. Please try again later.";
  }
}
