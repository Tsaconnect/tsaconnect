// services/signPreparedTransaction.ts
//
// Signing a transfer the server prepared (POST /wallet/prepare-tx), shared by the Paycrest
// deposit and the TSA Pay refund so the signed shape can't drift between them.
import { signTransaction } from './wallet';
import type { PreparedTransaction } from './walletApi';

/** Sign `tx` as a legacy transaction with exactly its prepared fields, by `fromAddress`'s key. */
export function signPreparedTransaction(tx: PreparedTransaction, fromAddress: string): Promise<string> {
  return signTransaction(
    {
      type: 0, // legacy transaction format, as everywhere else in the app
      to: tx.to,
      data: tx.data,
      value: tx.value,
      gasLimit: tx.gasLimit,
      gasPrice: tx.gasPrice,
      nonce: tx.nonce,
      chainId: tx.chainId,
    },
    fromAddress,
  );
}
