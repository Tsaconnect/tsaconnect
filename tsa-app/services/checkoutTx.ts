// Sending checkout transactions, and settling one whose confirmation timed
// out, so a retried payment neither loses a sent tx nor sends a second one.
import { signAndBroadcast, TxConfirmationTimeoutError } from './transaction';
import { getProvider } from './wallet';
import type { UnsignedTx } from './orderApi';

/** Parse blockchain/signing errors into user-friendly messages. */
export function friendlyTxError(err: any): string {
  const msg = err?.message || err?.toString() || '';
  if (msg.includes('INSUFFICIENT_FUNDS') || msg.includes('insufficient funds'))
    return 'Not enough S (Sonic) in your wallet to cover gas fees. Please top up your wallet with S and try again.';
  if (msg.includes('user rejected') || msg.includes('User denied') || msg.includes('ACTION_REJECTED'))
    return 'Transaction was cancelled.';
  if (msg.includes('nonce'))
    return 'Transaction conflict — please wait a moment and try again.';
  if (msg.includes('UNPREDICTABLE_GAS_LIMIT') || msg.includes('execution reverted'))
    return 'Transaction would fail on-chain. Please check your token balance and try again.';
  return msg;
}

/** A sent checkout tx: confirmed, or recorded when its confirmation timed out. */
export type SentTx = { hash: string; confirmed: boolean };

const STILL_CONFIRMING = 'Your payment was sent and is still confirming on Sonic. Wait a minute, then tap Try again.';

/**
 * Signs and sends a checkout tx. A confirmation timeout still hands the hash
 * to onTimeout — the tx may yet be mined, so a retry must settle it rather
 * than send another.
 */
export async function sendTx(tx: UnsignedTx, onTimeout: (sent: SentTx) => void): Promise<SentTx> {
  try {
    return { hash: await signAndBroadcast(tx, 'sonic'), confirmed: true };
  } catch (e: any) {
    if (e instanceof TxConfirmationTimeoutError) {
      onTimeout({ hash: e.txHash, confirmed: false });
      throw new Error(STILL_CONFIRMING);
    }
    throw new Error(friendlyTxError(e));
  }
}

/**
 * Settles a tx recorded on a confirmation timeout against the chain: confirmed
 * once mined, dropped (undefined, so it is sent afresh) when it reverted or
 * the network no longer knows it, and an error while it is still in flight.
 */
export async function settleSent(sent: SentTx | undefined): Promise<SentTx | undefined> {
  if (!sent || sent.confirmed) return sent;
  const provider = getProvider('sonic');
  const receipt = await provider.getTransactionReceipt(sent.hash);
  if (receipt) return receipt.status === 1 ? { hash: sent.hash, confirmed: true } : undefined;
  if (await provider.getTransaction(sent.hash)) throw new Error(STILL_CONFIRMING);
  return undefined;
}
