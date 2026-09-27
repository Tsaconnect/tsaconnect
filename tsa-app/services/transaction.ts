import { signTransaction, broadcastTransaction } from './wallet';
import { type ChainKey } from '../constants/chains';
import type { UnsignedTx } from './orderApi';

const TX_TIMEOUT_MS = 90_000;

/**
 * The transaction was broadcast but not confirmed in time. It may still be
 * mined, so the hash is kept: a caller that retries must report this tx, not
 * send another.
 */
export class TxConfirmationTimeoutError extends Error {
  constructor(public readonly txHash: string) {
    super('Transaction confirmation timed out');
    this.name = 'TxConfirmationTimeoutError';
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

  const receipt = await Promise.race([
    txResponse.wait(),
    new Promise<never>((_, reject) =>
      setTimeout(() => reject(new TxConfirmationTimeoutError(txResponse.hash)), TX_TIMEOUT_MS)
    ),
  ]);

  if (!receipt) throw new Error('No receipt returned');
  return receipt.hash;
}
