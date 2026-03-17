// services/transaction.ts
// Shared sign-and-broadcast helper for escrow transactions
import { signTransaction, getProvider } from './wallet';
import type { UnsignedTx } from './orderApi';

const TX_TIMEOUT_MS = 90_000; // 90 seconds

/**
 * Sign an unsigned transaction locally, broadcast it, and wait for confirmation.
 * Returns the confirmed transaction hash.
 * Throws if signing, broadcast, or confirmation fails (including timeout and dropped txs).
 */
export async function signAndBroadcast(unsignedTx: UnsignedTx): Promise<string> {
  const dataHex = unsignedTx.data.startsWith('0x') ? unsignedTx.data : '0x' + unsignedTx.data;

  const signedTx = await signTransaction({
    to: unsignedTx.to,
    value: unsignedTx.value,
    data: dataHex,
    nonce: unsignedTx.nonce,
    gasPrice: unsignedTx.gasPrice,
    gasLimit: unsignedTx.gasLimit,
    chainId: parseInt(unsignedTx.chainId),
  });

  const provider = getProvider('sonic');
  const txResponse = await provider.broadcastTransaction(signedTx);

  // Race between tx confirmation and a timeout
  const receipt = await Promise.race([
    txResponse.wait(),
    new Promise<null>((_, reject) =>
      setTimeout(() => reject(new Error('Transaction timed out. Check your order list for status.')), TX_TIMEOUT_MS)
    ),
  ]);

  if (!receipt) {
    throw new Error('Transaction was dropped or replaced. Check your order list for status.');
  }

  return receipt.hash;
}
