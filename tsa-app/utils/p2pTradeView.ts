// utils/p2pTradeView.ts — pure decisions for the trade cockpit (testable
// without a render harness). The screen stays a thin orchestrator.
import { formatFiat, formatTokenAmount, type P2PTrade } from '@/services/p2p';
import { formatFeePct, tradeFee } from '@/services/p2pFee';
import { isPendingTradeTx, type PendingEscrowConfirm, type PendingTradeTx, type TradeTxMode } from '@/services/p2pPending';
import { P2P_TX_COPY, type P2PFollowUp, type StashedConfirmResult } from '@/services/p2pTx';
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

/** The timed-out cancel's guardrail. The fiat payer (always the trade's
 *  buyer) loses their money if they cancel after paying; the seller gets the
 *  crypto back, which is only fair if the buyer never paid. */
export function timeoutCancelConfirm(role: 'buyer' | 'seller'): { title: string; message: string } {
  return role === 'buyer'
    ? {
        title: 'Cancel this trade?',
        message: "Only cancel if you have NOT sent the money. If you have, tap I've paid or open a dispute.",
      }
    : {
        title: 'Cancel this trade?',
        message: "This returns the crypto to you. Only cancel if the buyer hasn't paid.",
      };
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
  };
}

export interface TradeFeeLines {
  feeLabel: string;
  feeValue: string;
  receiveLabel: string;
  receiveValue: string;
}

/** Fee comes out of the crypto receiver, who is always the trade's buyer. The
 *  bps is platform + gas, so it is labelled "Fee". PM trades keep their own
 *  screen as it was: no fee lines. */
export function tradeFeeLines(trade: P2PTrade, role: 'buyer' | 'seller', decimals: number): TradeFeeLines | null {
  if (trade.isPm) return null;
  const fee = tradeFee(trade);
  if (!fee) return null;
  const pct = formatFeePct(trade.feeBps);
  return {
    feeLabel: pct ? `Fee (${pct})` : 'Fee',
    feeValue: `${formatTokenAmount(fee.fee, decimals, 4)} ${trade.token}`,
    receiveLabel: role === 'buyer' ? 'You receive' : 'They receive',
    receiveValue: `${formatTokenAmount(fee.net, decimals, 4)} ${trade.token}`,
  };
}

/** How long a sent trade action may sit unrecorded with the trade unmoved
 *  before the buttons come back (the stash and its retry stay). The reconciler
 *  moves a mined action within a minute, so by then the tx almost certainly
 *  never landed; sending again is safe because prepare's gas estimate refuses
 *  an action that already happened. A lost open never goes stale: the backfill
 *  or the expiry watchdog always settles it. */
export const PENDING_TRADE_TX_STALE_MS = 5 * 60_000;

/** The trade has moved past what this stashed tx was doing. */
function pendingTradeTxSettled(rec: PendingTradeTx, trade: Pick<P2PTrade, 'status' | 'openTxHash'>): boolean {
  switch (rec.mode) {
    case 'open':
      return !!trade.openTxHash || trade.status !== 'pending_payment';
    case 'mark_paid':
    case 'cancel_trade':
      return trade.status !== 'pending_payment';
    case 'release':
      return trade.status !== 'paid' && trade.status !== 'releasing';
    case 'dispute':
      // Cleared once the dispute is recorded (or the trade ended another way).
      return trade.status !== 'pending_payment' && trade.status !== 'paid';
  }
}

/** This trade's stashed txs: the newest one still unresolved (to show, with
 *  `stale` once it should stop hiding the buttons), and the ones the trade has
 *  moved past (to drop from the store). */
export function splitPendingTradeTxs(
  recs: PendingEscrowConfirm[],
  trade: Pick<P2PTrade, 'id' | 'status' | 'openTxHash'>,
  nowMs: number,
): { active: PendingTradeTx | null; settled: PendingTradeTx[]; stale: boolean } {
  let active: PendingTradeTx | null = null;
  const settled: PendingTradeTx[] = [];
  for (const rec of recs) {
    if (!isPendingTradeTx(rec) || rec.tradeId !== trade.id) continue;
    if (pendingTradeTxSettled(rec, trade)) settled.push(rec);
    else if (!active || rec.createdAt > active.createdAt) active = rec;
  }
  const stale = !!active && active.mode !== 'open' && nowMs - active.createdAt > PENDING_TRADE_TX_STALE_MS;
  return { active, settled, stale };
}

const PENDING_TX_COPY: Record<TradeTxMode, { label: string; sent: string }> = {
  open: { label: 'Escrow', sent: '' },
  mark_paid: { label: 'Mark paid', sent: 'Your “I’ve paid” transaction was sent' },
  release: { label: 'Release', sent: 'Your release was sent' },
  cancel_trade: { label: 'Cancel', sent: 'Your cancel was sent' },
  dispute: { label: 'Dispute', sent: 'Your dispute was sent' },
};

/** Card copy for a stashed trade tx. `label` titles the outcome alerts. A
 *  stale one sits above the buttons again, so it stops saying "don't send". */
export function pendingTradeTxCopy(
  rec: Pick<PendingTradeTx, 'mode' | 'txHash'>,
  stale = false,
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
    // shortAddress already elides the middle.
    title: `Confirming ${shortAddress(rec.txHash)}`,
    body: stale
      ? `${sent} but still isn’t recorded. Retry to check it now. If it never went through, you can send it again below.`
      : `${sent}. We’re waiting for the network to record it — don’t send it again. ` +
        'Retry to check now; nothing is sent again.',
  };
}

/** What happens to a trade whose open never landed: the server keeps the row
 *  until the expiry watchdog cancels it, and only then can the user open
 *  another trade on the same ad. */
export const OPEN_ABANDONED_NEXT = 'This trade will expire and cancel on its own; then you can open a new one.';

/** The escrow card while an open is unrecorded and this device has no stash
 *  to retry (another device opened it, or the stash was lost). */
export const OPEN_UNCONFIRMED_COPY =
  'Waiting for the network to confirm the escrow. If the open went through, it will be recorded here ' +
  'shortly. If it didn’t, nothing was locked. ' + OPEN_ABANDONED_NEXT;

/** A retried open the server saw revert: no escrow, and the watchdog ends the
 *  trade — there is nothing for the user to redo here. */
export const OPEN_REVERTED_ALERT = {
  title: 'Escrow failed',
  message: P2P_TX_COPY.revertedMessage('the escrow transaction', OPEN_ABANDONED_NEXT, 'nothing was locked'),
};

/** What the trade screen does after re-sending a stashed confirm
 *  (retryStashedConfirm). The server truth is reloaded whatever happened. */
export function stashedConfirmFollowUp(
  r: StashedConfirmResult,
  rec: Pick<PendingTradeTx, 'mode' | 'txHash'>,
): P2PFollowUp {
  const { label } = pendingTradeTxCopy(rec);
  switch (r.status) {
    case 'confirmed':
      return { toast: { type: 'success', message: `${label} confirmed` }, reload: true };
    case 'reverted':
      return {
        alert: rec.mode === 'open'
          ? OPEN_REVERTED_ALERT
          : { title: P2P_TX_COPY.failedTitle(label), message: P2P_TX_COPY.revertedMessage() },
        reload: true,
      };
    case 'pending':
      return { alert: { title: 'Still confirming', message: r.message }, reload: true };
  }
}
