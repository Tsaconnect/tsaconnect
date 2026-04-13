import { signTransaction, getProvider } from './wallet';
import { type ChainKey } from '../constants/chains';
import type { UnsignedTx } from './orderApi';

const TX_TIMEOUT_MS = 90_000;

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
  const provider = getProvider(chainKey);
  const txResponse = await provider.broadcastTransaction(signed);

  const receipt = await Promise.race([
    txResponse.wait(),
    new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error('Transaction confirmation timed out')), TX_TIMEOUT_MS)
    ),
  ]);

  if (!receipt) throw new Error('No receipt returned');
  return receipt.hash;
}
