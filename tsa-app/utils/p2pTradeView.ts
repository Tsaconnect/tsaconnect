// utils/p2pTradeView.ts — pure decisions for the trade cockpit (testable
// without a render harness). The screen stays a thin orchestrator.
import { formatFiat, formatTokenAmount, type P2PTrade } from '@/services/p2p';
import { formatFeePct, tradeFee } from '@/services/p2pFee';
import type { RunP2PTxResult } from '@/services/p2pTx';

export const RELEASE_CONFIRM_TITLE = 'Have you checked the money is in your account?';

export function releaseConfirmMessage(
  t: Pick<P2PTrade, 'fiatAmount' | 'fiatCurrency' | 'tokenAmount' | 'token'>,
  decimals: number,
): string {
  return (
    `Open your bank or wallet app and check that ${formatFiat(t.fiatAmount)} ${t.fiatCurrency} from the buyer ` +
    `has arrived. Don't rely on a screenshot or an SMS. Releasing sends ` +
    `${formatTokenAmount(t.tokenAmount, decimals, 4)} ${t.token} to the buyer and can't be undone.`
  );
}

export interface TradeTxFollowUp {
  /** The trade the server recorded, to show before the reload lands. */
  trade?: P2PTrade;
  /** Refetch the server truth (anything may have moved on chain or in the DB). */
  reload: boolean;
  /** Another run of the same action owns the spinner — don't clear it. */
  keepActing: boolean;
}

/** What the screen does with a trade action's outcome (the alert copy is
 *  txOutcomeAlert's job). */
export function tradeTxFollowUp(r: RunP2PTxResult<unknown, P2PTrade>): TradeTxFollowUp {
  switch (r.status) {
    case 'confirmed':
      return { trade: r.data, reload: true, keepActing: false };
    case 'busy':
      return { trade: undefined, reload: false, keepActing: true };
    case 'aborted':
      return { trade: undefined, reload: false, keepActing: false };
    default:
      return { trade: undefined, reload: true, keepActing: false };
  }
}

export interface TradeActions {
  pmActive: boolean;
  pmDormant: boolean;
  showPmPayTo: boolean;
  showPmAutomated: boolean;
  canMarkPaid: boolean;
  canRelease: boolean;
  sellerAwaitingPayment: boolean;
  payToFor: 'direct-payer' | 'direct-payee' | null;
  showProof: boolean;
  openUnconfirmed: boolean;
  canCancel: boolean;
  canDispute: boolean;
  releasingDisabled: boolean;
}

/** Which cards and buttons the cockpit shows for this trade and viewer.
 *  PM flags keep the exact gates the screen had inline. */
export function tradeActions(trade: P2PTrade, role: 'buyer' | 'seller', nowMs: number): TradeActions {
  const expired = new Date(trade.expiresAt).getTime() < nowMs;
  const pmActive = !!trade.isPm && !!trade.pmPayTo;
  const pmDormant = !!trade.isPm && !trade.pmPayTo;
  const pending = trade.status === 'pending_payment';
  const live = pending || trade.status === 'paid' || trade.status === 'releasing' || trade.status === 'disputed';
  const showPmPayTo = pmActive && role === 'buyer' && pending;
  return {
    pmActive,
    pmDormant,
    showPmPayTo,
    showPmAutomated: pmActive && !showPmPayTo,
    canMarkPaid: !pmActive && role === 'buyer' && pending,
    canRelease: !pmActive && role === 'seller' && trade.status === 'paid',
    sellerAwaitingPayment: !pmActive && role === 'seller' && pending,
    // The API sends `payee` only to the two parties while live; a PM trade has
    // its own pay-to card.
    payToFor: !pmActive && live && trade.payee ? (role === 'buyer' ? 'direct-payer' : 'direct-payee') : null,
    // The seller judges the proof before releasing; both see it in a dispute.
    showProof:
      !!trade.proofImageUrl && ((role === 'seller' && trade.status === 'paid') || trade.status === 'disputed'),
    openUnconfirmed: pending && !trade.openTxHash,
    canCancel: expired && pending,
    canDispute: pending || trade.status === 'paid',
    releasingDisabled: trade.status === 'releasing',
  };
}

export interface TradeFeeLines {
  feeLabel: string;
  feeValue: string;
  receiveLabel: string;
  receiveValue: string;
}

/** Fee comes out of the crypto receiver, who is always the trade's buyer. */
export function tradeFeeLines(trade: P2PTrade, role: 'buyer' | 'seller', decimals: number): TradeFeeLines | null {
  const fee = tradeFee(trade);
  if (!fee) return null;
  const pct = formatFeePct(trade.feeBps);
  return {
    feeLabel: pct ? `Platform fee (${pct})` : 'Platform fee',
    feeValue: `${formatTokenAmount(fee.fee, decimals, 4)} ${trade.token}`,
    receiveLabel: role === 'buyer' ? 'You receive' : 'They receive',
    receiveValue: `${formatTokenAmount(fee.net, decimals, 4)} ${trade.token}`,
  };
}
