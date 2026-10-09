// services/payRefundSend.ts
//
// Sending a TSA Pay refund from the business owner's TSA wallet. The same steps as a
// Paycrest deposit (services/paycrestDeposit.ts) — prepare an ERC-20 transfer, check it is
// exactly the one shown, check gas, sign — except the last: the signed transaction goes to
// POST /pay/merchant/refunds/:id/submit, where the server checks it, records its hash and
// only then broadcasts it. Never /wallet/submit-tx, and never retried here: once the
// server has recorded a send, a second one is refused, and the screen re-reads the refund.
import type { ChainKey } from '@/constants/chains';
import { formatNativeAmount, hasGasShortfall, preparedFeeNative } from '@/utils/sendForm';
import { refundOptionChain, sameAddress, unprovenWalletMessage, wrongWalletMessage } from '@/utils/payRefunds';
import { assertPreparedTransfer } from './paycrestDeposit';
import { submitPayRefund, type PayRefund, type PayRefundOption } from './payMerchantApi';
import { getSignableWallet, nativeBalance, signTransaction } from './wallet';
import { prepareSendTransaction } from './walletApi';

/** The device's wallet is not the one the server says sends this refund (or holds no key). */
export class RefundWrongWalletError extends Error {
  readonly fromAddress: string;
  constructor(fromAddress: string) {
    super(wrongWalletMessage(fromAddress));
    this.name = 'RefundWrongWalletError';
    this.fromAddress = fromAddress;
  }
}

/** The server names no wallet ("" fromAddress): the owner hasn't proven her TSA wallet yet. */
export class RefundWalletUnprovenError extends Error {
  constructor() {
    super(unprovenWalletMessage);
    this.name = 'RefundWalletUnprovenError';
  }
}

/**
 * Sign the refund as `option` and hand it to the server. Rejects with a sentence a screen
 * can show as it is. `fromAddress` is GET /refunds/:id's — the only wallet allowed to send it.
 */
export async function sendPayRefund(refundId: string, option: PayRefundOption, toAddress: string, fromAddress: string): Promise<PayRefund> {
  if (!fromAddress) throw new RefundWalletUnprovenError();
  if (!sameAddress(await getSignableWallet(), fromAddress)) throw new RefundWrongWalletError(fromAddress);

  const prepared = await prepareSendTransaction(option.tokenSymbol, toAddress, option.amountDisplay, option.chainId, {
    tokenAddress: option.tokenAddress,
    decimals: option.decimals,
    fromAddress,
  });
  if (!prepared.success || !prepared.data) throw new Error(prepared.message || 'Could not prepare the refund. Please try again.');
  const tx = prepared.data;
  assertPreparedTransfer(tx, { tokenAddress: option.tokenAddress, to: toAddress, amountBaseUnits: option.amountBaseUnits, chainId: option.chainId });

  // Refunds are mainnet money whatever the wallet's testnet toggle says. The gas read follows
  // the chainId the transfer is signed for; a chainKey naming another network reads as unknown.
  const chain = refundOptionChain(option);
  const wei = chain ? await nativeBalance(option.chainKey as ChainKey, fromAddress, 'mainnet') : null;
  const gas = wei === null ? null : Number(wei) / 1e18;
  const fee = preparedFeeNative(tx.gasPrice, tx.gasLimit);
  if (hasGasShortfall(gas, fee, 0, false)) {
    const symbol = chain?.nativeCurrency.symbol ?? 'gas';
    throw new Error(
      `Not enough ${symbol} on ${option.chainName} to pay the network fee — this refund needs about ${formatNativeAmount(fee)} ${symbol} ` +
        `and you have ${formatNativeAmount(gas ?? 0)}. Add a little ${symbol} and try again.`,
    );
  }

  const signedTx = await signTransaction(
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
  return submitPayRefund(refundId, { chainId: option.chainId, tokenSymbol: option.tokenSymbol, signedTx });
}
