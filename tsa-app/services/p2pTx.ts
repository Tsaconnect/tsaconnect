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
import { p2pChainKey, unsignedTxToTxLike, type ApiResponse, type UnsignedTx } from './p2p';
import { broadcastTransaction, signTransaction } from './wallet';
import { ensureP2PGas } from './p2pGas';
import { mapSubmitError, TRANSFER_COPY } from './submitErrors';
import { p2pErrorMessage } from './p2pErrors';
import { TxRevertedError, waitMined, type ReceiptSource } from './txWait';
import { addPending, removePending, type PendingEscrowConfirm } from './p2pPending';

// The API waits ~30s for the receipt on confirm, so the client wait only buys
// early revert detection and the `mined` copy — keep it under the server's.
export const P2P_MINE_WAIT_MS = 25_000;

const inFlight = new Set<string>();

// What the confirm endpoints report when the receipt has status 0
// (p2p_escrow_service.go fetchReceiptAndCheckOK).
const SERVER_REVERT = /transaction reverted on chain/i;

/** The gas sponsor sizes each action for one entity, so the id is required
 *  where the API needs it (POST /p2p/gas/top-up 400s without it). */
export type P2PGasTarget =
  | { action: 'approve' | 'deposit' | 'withdraw'; adId?: undefined; tradeId?: undefined }
  | { action: 'open_trade' | 'cancel_ad'; adId: string; tradeId?: undefined }
  | { action: 'mark_paid' | 'release' | 'cancel_trade' | 'dispute'; tradeId: string; adId?: undefined };

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

function errorMessage(err: unknown): string {
  return (err as { message?: string } | undefined)?.message ?? String(err);
}

// The stash is a safety net: a storage failure must never block the confirm.
async function bestEffort(what: string, op: () => Promise<void>): Promise<void> {
  try {
    await op();
  } catch (err) {
    console.warn(`[p2pTx] ${what} failed`, err);
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
  const { chainId, walletAddress, action, adId, tradeId, prepare, confirm, onStage, onGasAlert, stash } = p;
  if (!p.skipGas) {
    const hasGas = await ensureP2PGas({ chainId, walletAddress, action, adId, tradeId, onStage, onAlert: onGasAlert });
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

  let txHash: string;
  let receipts: ReceiptSource | undefined;
  try {
    onStage?.('Signing…');
    const signed = await signTransaction(unsignedTxToTxLike(prepared.unsignedTx), walletAddress);
    onStage?.('Broadcasting…');
    const sent = await broadcastTransaction(p2pChainKey(chainId), signed);
    txHash = sent.hash;
    // The provider that accepted it — a fallback RPC when the primary is down.
    receipts = sent.provider ?? undefined;
  } catch (err) {
    throw new Error(mapSubmitError(errorMessage(err), TRANSFER_COPY));
  }

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

/** The alert for an outcome, or null when there is nothing to say. */
export function txOutcomeAlert(
  label: string,
  r: RunP2PTxResult<unknown, unknown>,
): { title: string; message: string } | null {
  const support = (hash: string) =>
    `Don't send it again — pull down to refresh in a minute. ` +
    `If nothing changes, contact support with this hash: ${hash}`;
  switch (r.status) {
    case 'refused':
      return { title: `${label} failed`, message: p2pErrorMessage(r.response) };
    case 'reverted':
      return {
        title: `${label} failed`,
        message: 'The network rejected this transaction, so nothing changed (only the network fee was spent). ' +
          'Pull down to refresh, then try again.',
      };
    case 'confirm-failed':
      return {
        title: `${label} sent — still confirming`,
        message: r.mined
          ? `Your transaction is on chain, but we couldn't record it yet (${r.message}). ${support(r.txHash)}`
          : `We couldn't confirm your transaction in time. It may still go through. ${support(r.txHash)}`,
      };
    default:
      return null;
  }
}
