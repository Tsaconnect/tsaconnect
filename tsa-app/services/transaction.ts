import { signTransaction, broadcastTransaction } from './wallet';
import { type ChainKey } from '../constants/chains';
import type { UnsignedTx } from './orderApi';

const TX_TIMEOUT_MS = 90_000;

/**
 * The transaction was broadcast but its confirmation could not be seen — the
 * wait timed out, or the RPC failed while polling. It may still be mined, so
 * the hash is kept: a caller that retries must settle this tx, not send
 * another. A tx that reverted is not this error.
 */
export class TxNotConfirmedError extends Error {
  readonly txHash: string;
  constructor(txHash: string, message = 'Transaction confirmation timed out') {
    super(message);
    this.name = 'TxNotConfirmedError';
    this.txHash = txHash;
  }
}

/**
 * Sign a transaction locally and broadcast it on-chain.
 * @param chainKey - Which chain to broadcast on (e.g. 'sonic', 'bsc')
 */
export async function signAndBroadcast(
  unsignedTx: UnsignedTx,
  chainKey: ChainKey = 'sonic'
): Promise<string> {
  const txReq: Record<string, any> = {
    type: 0, // legacy transaction format
    to: unsignedTx.to,
    value: unsignedTx.value || '0x0',
    gasLimit: unsignedTx.gasLimit,
    gasPrice: unsignedTx.gasPrice,
    nonce: unsignedTx.nonce,
    chainId: unsignedTx.chainId,
  };
  if (unsignedTx.data && unsignedTx.data !== '0x') {
    txReq.data = unsignedTx.data.startsWith('0x') ? unsignedTx.data : `0x${unsignedTx.data}`;
  }

  const signed = await signTransaction(txReq);
  // Routes through fallback RPCs if the primary is unreachable.
  const txResponse = await broadcastTransaction(chainKey, signed);

  let receipt;
  try {
    receipt = await Promise.race([
      txResponse.wait(),
      new Promise<never>((_, reject) =>
        setTimeout(() => reject(new TxNotConfirmedError(txResponse.hash)), TX_TIMEOUT_MS)
      ),
    ]);
  } catch (e: any) {
    // ethers reports a mined-and-reverted tx as CALL_EXCEPTION: that one is
    // settled, and its reason is what the caller should show.
    if (e instanceof TxNotConfirmedError || e?.code === 'CALL_EXCEPTION') throw e;
    throw new TxNotConfirmedError(txResponse.hash, e?.message || 'Transaction not confirmed');
  }

  if (!receipt) throw new Error('No receipt returned');
  return receipt.hash;
}
