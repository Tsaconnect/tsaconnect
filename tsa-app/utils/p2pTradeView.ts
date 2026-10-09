// utils/p2pTradeView.ts — pure decisions for the trade cockpit (testable
// without a render harness). The screen stays a thin orchestrator.
import { formatFiat, formatTokenAmount, type P2PTrade } from '@/services/p2p';
import { formatFeePct, tradeFee } from '@/services/p2pFee';
import { isPendingTradeTx, type PendingEscrowConfirm, type PendingTradeTx, type TradeTxMode } from '@/services/p2pPending';
import type { RunP2PTxResult } from '@/services/p2pTx';
import { shortAddress } from '@/utils/sendForm';

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
}

/** What the screen does with a trade action's outcome (the alert copy is
 *  txOutcomeAlert's job). The screen's actingRef stops a double tap before it
 *  reaches runP2PTx, so `busy` means another screen instance owns the run —
 *  this one just clears its spinner. */
export function tradeTxFollowUp(r: RunP2PTxResult<unknown, P2PTrade>): TradeTxFollowUp {
  switch (r.status) {
    case 'confirmed':
      return { trade: r.data, reload: true };
    case 'busy':
    case 'aborted':
      return { trade: undefined, reload: false };
    default:
      return { trade: undefined, reload: true };
  }
}

/** The mark-paid guardrail. After the window the seller may cancel at any
 *  moment; markPaid only wins if it lands first. */
export function markPaidConfirmMessage(token: string, windowEnded: boolean): string {
  const base =
    `Only tap "I have paid" after the transfer is sent. The seller will release your ${token} ` +
    'based on your confirmation, and this cannot be undone.';
  return windowEnded
    ? `${base}\n\nThe payment window has ended — marking paid now stops the seller cancelling, if it lands first.`
    : base;
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
  /** A mark-paid/release/cancel this device sent is still confirming: show
   *  that instead of offering the actions again. */
  confirmingTx: boolean;
  canCancel: boolean;
  canDispute: boolean;
  releasingDisabled: boolean;
}

/** Which cards and buttons the cockpit shows for this trade and viewer.
 *  PM flags keep the exact gates the screen had inline. `pendingMode` is the
 *  unresolved stashed tx for this trade (splitPendingTradeTxs), if any. */
export function tradeActions(
  trade: P2PTrade,
  role: 'buyer' | 'seller',
  nowMs: number,
  pendingMode?: TradeTxMode | null,
): TradeActions {
  const expired = new Date(trade.expiresAt).getTime() < nowMs;
  const pmActive = !!trade.isPm && !!trade.pmPayTo;
  const pmDormant = !!trade.isPm && !trade.pmPayTo;
  const pending = trade.status === 'pending_payment';
  const live = pending || trade.status === 'paid' || trade.status === 'releasing' || trade.status === 'disputed';
  const showPmPayTo = pmActive && role === 'buyer' && pending;
  // A lost open is the R4 banner; it doesn't hide anything the open gate below
  // doesn't already hide.
  const confirmingTx = !!pendingMode && pendingMode !== 'open';
  const openRecorded = !!trade.openTxHash; // a real hash or "reconciled"
  return {
    pmActive,
    pmDormant,
    showPmPayTo,
    showPmAutomated: pmActive && !showPmPayTo,
    // markPaid reverts on chain until the open is there (and the API refuses it).
    canMarkPaid: !pmActive && role === 'buyer' && pending && openRecorded && !confirmingTx,
    canRelease: !pmActive && role === 'seller' && trade.status === 'paid' && !confirmingTx,
    sellerAwaitingPayment: !pmActive && role === 'seller' && pending && !confirmingTx,
    // The API sends `payee` only to the two parties while live; a PM trade has
    // its own pay-to card.
    payToFor: !pmActive && live && trade.payee ? (role === 'buyer' ? 'direct-payer' : 'direct-payee') : null,
    // The seller judges the proof before releasing; both see it in a dispute.
    showProof:
      !!trade.proofImageUrl && ((role === 'seller' && trade.status === 'paid') || trade.status === 'disputed'),
    openUnconfirmed: pending && !openRecorded,
    confirmingTx,
    canCancel: expired && pending && !confirmingTx,
    canDispute: (pending || trade.status === 'paid') && !confirmingTx,
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

/** How long a sent trade action may sit unrecorded with the trade unmoved
 *  before it stops hiding the buttons. The reconciler moves a mined action
 *  within a minute, so by then the tx almost certainly never landed; sending
 *  again is safe because prepare's gas estimate refuses an action that already
 *  happened. A lost open has no TTL: the backfill or the expiry watchdog always
 *  settles it. */
export const PENDING_TRADE_TX_STALE_MS = 5 * 60_000;

function pendingTradeTxSettled(
  rec: PendingTradeTx,
  trade: Pick<P2PTrade, 'status' | 'openTxHash'>,
  nowMs: number,
): boolean {
  const stale = nowMs - rec.createdAt > PENDING_TRADE_TX_STALE_MS;
  switch (rec.mode) {
    case 'open':
      return !!trade.openTxHash || trade.status !== 'pending_payment';
    case 'mark_paid':
    case 'cancel_trade':
      return stale || trade.status !== 'pending_payment';
    case 'release':
      return stale || (trade.status !== 'paid' && trade.status !== 'releasing');
  }
}

/** This trade's stashed txs: the newest one still unresolved (to show), and
 *  the ones the trade has moved past (to drop from the store). */
export function splitPendingTradeTxs(
  recs: PendingEscrowConfirm[],
  trade: Pick<P2PTrade, 'id' | 'status' | 'openTxHash'>,
  nowMs: number,
): { active: PendingTradeTx | null; settled: PendingTradeTx[] } {
  let active: PendingTradeTx | null = null;
  const settled: PendingTradeTx[] = [];
  for (const rec of recs) {
    if (!isPendingTradeTx(rec) || rec.tradeId !== trade.id) continue;
    if (pendingTradeTxSettled(rec, trade, nowMs)) settled.push(rec);
    else if (!active || rec.createdAt > active.createdAt) active = rec;
  }
  return { active, settled };
}

const PENDING_TX_COPY: Record<TradeTxMode, { label: string; sent: string }> = {
  open: { label: 'Escrow', sent: '' },
  mark_paid: { label: 'Mark paid', sent: 'Your “I’ve paid” transaction was sent' },
  release: { label: 'Release', sent: 'Your release was sent' },
  cancel_trade: { label: 'Cancel', sent: 'Your cancel was sent' },
};

/** Card copy for a stashed trade tx. `label` titles the outcome alerts. */
export function pendingTradeTxCopy(
  rec: Pick<PendingTradeTx, 'mode' | 'txHash'>,
): { title: string; body: string; label: string } {
  const { label, sent } = PENDING_TX_COPY[rec.mode];
  if (rec.mode === 'open') {
    return {
      label,
      title: 'Escrow still confirming',
      body: 'Your escrow transaction was sent but isn’t recorded yet. It should be picked up shortly — ' +
        'or retry now. Nothing is sent again.',
    };
  }
  return {
    label,
    title: `Confirming ${shortAddress(rec.txHash)}…`,
    body: `${sent}. We’re waiting for the network to record it — don’t send it again. ` +
      'Retry to check now; nothing is sent again.',
  };
}
