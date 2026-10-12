// services/p2pTx.ts
//
// The one path every P2P on-chain action takes: sponsored gas (R3) → server
// prepare → sign as the acting wallet → broadcast on the entity's chain →
// wait for it to mine → server confirm. Outcomes are data, not alerts, so
// screens decide what to do and tests can assert ordering. Throws only for a
// sign/broadcast failure — nothing has reached the chain at that point.
//
// Once a tx is broadcast the user must never be told the action "failed"
// unless the chain says so (a reverted receipt): a lost confirm or a slow
// receipt may still be recorded by a retry or the server's chain reconciler.
//
// One run per action + entity at a time (`${action}:${tradeId ?? adId ??
// wallet}`): a double tap that slips past a screen's own `acting` flag gets
// { status: 'busy' } before any gas, prepare or signing, instead of a second
// tx that reverts and reads as "failed".
import {
  p2pChainKey,
  tradesApi,
  unsignedTxToTxLike,
  type ApiResponse,
  type P2PGasTarget,
  type P2PTrade,
  type UnsignedTx,
} from './p2p';
import { broadcastTransaction, signTransaction } from './wallet';
import { ensureP2PGas } from './p2pGas';
import { mapSubmitError, TRANSFER_COPY } from './submitErrors';
import { errorMessage, p2pErrorMessage, p2pKycCode } from './p2pErrors';
import { TxRevertedError, waitMined, type ReceiptSource } from './txWait';
import { addPending, removePending, type PendingEscrowConfirm } from './p2pPending';

// The API waits ~30s for the receipt on confirm, so the client wait only buys
// early revert detection and the `mined` copy — keep it under the server's.
const P2P_MINE_WAIT_MS = 25_000;

const inFlight = new Set<string>();

// What the confirm endpoints report when the receipt has status 0
// (p2p_escrow_service.go fetchReceiptAndCheckOK).
const SERVER_REVERT = /transaction reverted on chain/i;

/** The caller's part of a lost-confirm record; runP2PTx adds the hash, chain,
 *  wallet and time. */
export type P2PTxStash = Pick<PendingEscrowConfirm, 'mode' | 'token' | 'amount' | 'tradeId'>;

export type RunP2PTxParams<P extends { unsignedTx?: UnsignedTx | null }, C> = P2PGasTarget & {
  chainId: number;
  walletAddress: string;
  prepare: () => Promise<ApiResponse<P>>;
  confirm: (txHash: string, prepared: P) => Promise<ApiResponse<C>>;
  onStage?: (label: string) => void;
  onGasAlert?: (title: string, message: string) => void;
  /** Gas already ensured by the caller (e.g. before an approve) or no tx expected. */
  skipGas?: boolean;
  /** Stash the broadcast hash (services/p2pPending) BEFORE the confirm, so a
   *  killed app or a lost confirm can be retried without re-sending. Dropped
   *  again once the confirm records it or the tx reverts. */
  stash?: (prepared: P) => P2PTxStash;
};

export type RunP2PTxResult<P, C> =
  | { status: 'confirmed'; txHash: string; prepared: P; data?: C }
  /** prepare returned unsignedTx:null — the server finished it (buy-ad cancel). */
  | { status: 'no-tx'; prepared: P }
  /** Broadcast but not recorded. mined:false = we never saw the receipt, so it
   *  may still go through. Either way: do not send it again. */
  | { status: 'confirm-failed'; txHash: string; prepared: P; message: string; mined: boolean }
  /** Mined with status 0: nothing changed on chain; safe to try again. */
  | { status: 'reverted'; txHash: string; prepared: P }
  /** No gas and none could be sponsored; the gas alert has been shown. */
  | { status: 'aborted' }
  /** The same action on the same entity is already running; nothing was done. */
  | { status: 'busy' }
  | { status: 'refused'; response: ApiResponse<P> };

// The stash is a safety net: a storage failure must never block the confirm.
async function bestEffort(what: string, op: () => Promise<void>): Promise<void> {
  try {
    await op();
  } catch (err) {
    console.warn(`[p2pTx] ${what} failed`, err);
  }
}

/** Sign a prepared tx as `walletAddress` and broadcast it on `chainId`. A
 *  throw means nothing reached the chain, and its raw wallet/RPC text has been
 *  mapped to copy. Also returns the provider that accepted it — a fallback RPC
 *  when the primary is down — to wait for the receipt on. */
export async function signAndBroadcast(
  chainId: number,
  unsignedTx: UnsignedTx,
  walletAddress: string,
  stage: { onStage?: (label: string) => void; signing?: string; broadcasting?: string } = {},
): Promise<{ hash: string; provider?: ReceiptSource }> {
  try {
    stage.onStage?.(stage.signing ?? 'Signing…');
    const signed = await signTransaction(unsignedTxToTxLike(unsignedTx), walletAddress);
    stage.onStage?.(stage.broadcasting ?? 'Broadcasting…');
    const sent = await broadcastTransaction(p2pChainKey(chainId), signed);
    return { hash: sent.hash, provider: sent.provider ?? undefined };
  } catch (err) {
    // `raw` keeps the original for decoders (rawErrorText); the message is copy.
    throw Object.assign(new Error(mapSubmitError(errorMessage(err), TRANSFER_COPY)), { raw: errorMessage(err) });
  }
}

const WRONG_CHAIN = 'This transaction was built for a different network. Pull down to refresh and try again.';

export async function runP2PTx<P extends { unsignedTx?: UnsignedTx | null }, C>(
  p: RunP2PTxParams<P, C>,
): Promise<RunP2PTxResult<P, C>> {
  const key = `${p.action}:${p.tradeId ?? p.adId ?? p.walletAddress.toLowerCase()}`;
  if (inFlight.has(key)) return { status: 'busy' };
  inFlight.add(key);
  try {
    return await run(p);
  } finally {
    inFlight.delete(key);
  }
}

async function run<P extends { unsignedTx?: UnsignedTx | null }, C>(
  p: RunP2PTxParams<P, C>,
): Promise<RunP2PTxResult<P, C>> {
  const { chainId, walletAddress, prepare, confirm, onStage, onGasAlert, stash } = p;
  if (!p.skipGas) {
    const hasGas = await ensureP2PGas({ ...p, onAlert: onGasAlert });
    if (!hasGas) return { status: 'aborted' };
  }

  onStage?.('Preparing…');
  let prep: ApiResponse<P>;
  try {
    prep = await prepare();
  } catch (err) {
    prep = { success: false, message: errorMessage(err) };
  }
  if (!prep.success || !prep.data) return { status: 'refused', response: prep };
  const prepared = prep.data;
  if (!prepared.unsignedTx) return { status: 'no-tx', prepared };
  // Signing for one chain and broadcasting on another would fail at best.
  if (Number(prepared.unsignedTx.chainId) !== chainId) {
    return { status: 'refused', response: { success: false, message: WRONG_CHAIN } };
  }

  const { hash: txHash, provider: receipts } = await signAndBroadcast(chainId, prepared.unsignedTx, walletAddress, { onStage });

  if (stash) {
    // The builder runs inside the guard too: after a broadcast nothing may throw.
    await bestEffort('stash', () =>
      addPending({ ...stash(prepared), txHash, chainId, walletAddress, createdAt: Date.now() }));
  }
  const unstash = () => (stash ? bestEffort('unstash', () => removePending(txHash)) : Promise.resolve());

  onStage?.('Confirming…');
  let mined = true;
  try {
    await waitMined(chainId, txHash, { label: 'Transaction', timeoutMs: P2P_MINE_WAIT_MS, provider: receipts });
  } catch (err) {
    if (err instanceof TxRevertedError) {
      await unstash();
      return { status: 'reverted', txHash, prepared };
    }
    // A timeout or RPC error says nothing about the outcome; the server reads
    // the receipt through its own node, so still ask it.
    mined = false;
  }

  let conf: ApiResponse<C>;
  try {
    conf = await confirm(txHash, prepared);
  } catch (err) {
    conf = { success: false, message: errorMessage(err) };
  }
  if (conf.success) {
    await unstash();
    return { status: 'confirmed', txHash, prepared, data: conf.data };
  }
  if (SERVER_REVERT.test(conf.message ?? '')) {
    await unstash();
    return { status: 'reverted', txHash, prepared };
  }
  return {
    status: 'confirm-failed', txHash, prepared, mined,
    message: p2pErrorMessage(conf, 'The server could not record it yet.'),
  };
}

const REFRESH_AND_RETRY = 'Pull down to refresh, then try again.';

/** The one table of P2P tx-outcome copy. Screens and helpers build their
 *  alerts from it so a revert, a missing tx or a lost confirm reads the same
 *  everywhere. */
export const P2P_TX_COPY = {
  failedTitle: (label: string) => `${label} failed`,
  stillConfirmingTitle: (label: string) => `${label} sent — still confirming`,
  /** A mined status-0 tx: the chain rejected `what`, so nothing moved. `next`
   *  is what the user does now. */
  revertedMessage: (what = 'this transaction', next = REFRESH_AND_RETRY, effect = 'nothing changed') =>
    `The network rejected ${what}, so ${effect} (only the network fee was spent). ${next}`,
  /** prepare returned no unsignedTx where one was needed. */
  noTxMessage: (what = 'a transaction') => `The server did not return ${what}. Nothing was sent — please try again.`,
  /** Broadcast but not recorded. Never "failed": it may still go through. */
  stillConfirming: (r: { txHash: string; mined: boolean; message: string }) =>
    (r.mined
      ? `Your transaction is on chain, but we couldn't record it yet (${r.message}). `
      : "We couldn't confirm your transaction in time. It may still go through. ") +
    "Don't send it again — pull down to refresh in a minute. " +
    `If nothing changes, contact support with this hash: ${r.txHash}`,
};

export interface P2PFollowUpAlert {
  title: string;
  message: string;
  /** Offer "View trade" for this trade next to the dismiss button. */
  viewTradeId?: string;
  /** The dismiss button's text when "View trade" is offered (default OK). */
  dismissText?: string;
}

/** What a screen does after a P2P tx (or a retried confirm): data, so it can be
 *  tested, applied by utils/p2pFollowUp.applyFollowUp. A KYC refusal wins over
 *  everything else. */
export interface P2PFollowUp {
  alert?: P2PFollowUpAlert;
  /** Route to the Verify prompt (utils/kycRefusal). */
  kyc?: { code: string; message?: string };
  toast?: { type: 'success' | 'error' | 'info'; message: string };
  /** Refetch the server truth (anything may have moved on chain or in the DB). */
  reload: boolean;
  /** Go to this trade — after the alert's OK when there is an alert. */
  navigate?: string;
  /** A step that gates the next one (the approve before an open) may go on. */
  proceed?: boolean;
  /** The cancel was broadcast but not recorded: show it as sent and don't
   *  offer Cancel again until a later reload still shows the ad open. */
  cancelSent?: boolean;
}

type ResultFor<P, C, S extends RunP2PTxResult<P, C>['status']> = Extract<RunP2PTxResult<P, C>, { status: S }>;

/** `label` titles the default alerts; a status key replaces fields of that
 *  outcome's default (a function gets the narrowed result). Fields merge one
 *  by one, except that an override giving a `toast` and no `alert` key drops
 *  the default alert — a toast replaces it, never doubles it. */
export type P2PFollowUpOverrides<P, C> = { label: string } & {
  [S in RunP2PTxResult<P, C>['status']]?:
    | Partial<P2PFollowUp>
    | ((r: ResultFor<P, C, S>) => Partial<P2PFollowUp>);
};

function defaultFollowUp(label: string, r: RunP2PTxResult<unknown, unknown>): P2PFollowUp {
  switch (r.status) {
    case 'refused': {
      const code = p2pKycCode(r.response);
      if (code) return { kyc: { code, message: r.response.message }, reload: false };
      return { alert: { title: P2P_TX_COPY.failedTitle(label), message: p2pErrorMessage(r.response) }, reload: true };
    }
    case 'reverted':
      return { alert: { title: P2P_TX_COPY.failedTitle(label), message: P2P_TX_COPY.revertedMessage() }, reload: true };
    case 'confirm-failed':
      return { alert: { title: P2P_TX_COPY.stillConfirmingTitle(label), message: P2P_TX_COPY.stillConfirming(r) }, reload: true };
    case 'confirmed':
    case 'no-tx':
      return { reload: true };
    case 'aborted': // the gas alert has been shown
    case 'busy': // another run owns it
      return { reload: false };
  }
}

/** The follow-up for a runP2PTx outcome: the shared default per status, with
 *  the caller's overrides on top. */
export function p2pTxFollowUp<P, C>(r: RunP2PTxResult<P, C>, overrides: P2PFollowUpOverrides<P, C>): P2PFollowUp {
  const base = defaultFollowUp(overrides.label, r as RunP2PTxResult<unknown, unknown>);
  const o = overrides[r.status] as Partial<P2PFollowUp> | ((x: typeof r) => Partial<P2PFollowUp>) | undefined;
  const patch = (typeof o === 'function' ? o(r) : o) ?? {};
  const next = { ...base, ...patch };
  if (patch.toast && !('alert' in patch)) delete next.alert;
  return next;
}

export type StashedConfirmResult =
  | { status: 'confirmed'; data?: P2PTrade }
  /** The server saw a status-0 receipt: nothing changed, the action can be sent again. */
  | { status: 'reverted' }
  /** Not recorded yet; the stash stays for the next retry. */
  | { status: 'pending'; message: string };

const NOT_A_TRADE_TX = 'This transaction is not a trade action.';

/** The confirm endpoint for a stashed trade tx; null for a deposit/withdraw. */
function confirmStashed(rec: PendingEscrowConfirm, tradeId: string): Promise<ApiResponse<P2PTrade>> | null {
  switch (rec.mode) {
    case 'open':
      return tradesApi.confirmOpen(tradeId, rec.txHash, rec.walletAddress);
    case 'mark_paid':
      return tradesApi.confirmMarkPaid(tradeId, rec.txHash);
    case 'release':
      return tradesApi.confirmRelease(tradeId, rec.txHash);
    case 'cancel_trade':
      return tradesApi.confirmCancel(tradeId, rec.txHash);
    case 'dispute':
      return tradesApi.confirmRaiseDispute(tradeId, rec.txHash);
    default:
      return null;
  }
}

/** Re-send the confirm for a trade tx runP2PTx stashed (R4 and the lost
 *  mark-paid/release/cancel/dispute confirms) with its original hash — nothing is
 *  signed or broadcast again. Unstashes once the server records it or reports
 *  the revert; never throws. */
export async function retryStashedConfirm(rec: PendingEscrowConfirm): Promise<StashedConfirmResult> {
  const pending = (message: string): StashedConfirmResult => ({ status: 'pending', message });
  const tradeId = rec.tradeId;
  if (!tradeId) return pending(NOT_A_TRADE_TX);
  let conf: ApiResponse<P2PTrade>;
  try {
    const call = confirmStashed(rec, tradeId);
    if (!call) return pending(NOT_A_TRADE_TX);
    conf = await call;
  } catch (err) {
    conf = { success: false, message: errorMessage(err) };
  }
  if (conf.success) {
    await bestEffort('unstash', () => removePending(rec.txHash));
    return { status: 'confirmed', data: conf.data };
  }
  if (SERVER_REVERT.test(conf.message ?? '')) {
    await bestEffort('unstash', () => removePending(rec.txHash));
    return { status: 'reverted' };
  }
  return pending(p2pErrorMessage(conf, 'The server could not record it yet.'));
}
