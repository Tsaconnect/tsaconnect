// services/lifiFlow.ts
//
// Execute a quoted LiFi swap. EVM source: (optional) approve → wait mined →
// swap, with pending-nonce management and RPC fallback (extracted VERBATIM
// from screens/all-swap.tsx handleSwap so the All Swap screen and the Spot
// Trade desk share one implementation). Solana source: ed25519-sign LiFi's
// pre-built tx and broadcast via the nonevm path — no approve, no nonce
// (services/chains/solanaLifi.ts). Bitcoin source: pay LiFi's deposit address
// the quoted sats with the route's OP_RETURN memo attached
// (services/chains/btcLifi.ts). Throws Error with a user-facing message on any
// failure; resolves with the source-chain tx hash on success.

import { ethers } from 'ethers';
import { getChainByChainId } from '@/constants/chains';
import { fetchPendingNonce, signLiFiTransaction, withRpcFallback } from '@/services/wallet';
import { submitTransaction } from '@/services/walletApi';
import { buildERC20ApproveCalldata, isBtcLiFiChain, isSolanaLiFiChain, type LiFiQuote } from '@/services/lifi';
import { executeLiFiSolanaSwap } from '@/services/chains/solanaLifi';
import { executeLiFiBtcSwap } from '@/services/chains/btcLifi';
import { mapSubmitError } from '@/services/swapErrors';

export interface ExecuteLiFiSwapParams {
  quote: LiFiQuote;
  /** Symbol + decimals of the token being paid (the quote's from side). */
  fromSymbol: string;
  fromDecimals: number;
  /** Human-units amount the user entered (what the quote was fetched for). */
  fromAmount: string;
  walletAddress: string;
  /**
   * LiFi chain id the from-token lives on. Required to route non-EVM source
   * swaps — neither a Solana nor a Bitcoin quote's transactionRequest carries a
   * chainId to branch on.
   */
  fromChainId?: number;
  /** Progress labels ("Step 1 of 2: Approving token…", …). */
  onStage?: (label: string) => void;
}

export async function executeLiFiSwap({
  quote,
  fromSymbol,
  fromDecimals,
  fromAmount,
  walletAddress,
  fromChainId,
  onStage,
}: ExecuteLiFiSwapParams): Promise<string> {
  if (fromChainId !== undefined && isSolanaLiFiChain(fromChainId)) {
    return executeLiFiSolanaSwap({
      txDataB64: quote.transactionRequest.data,
      fromAmountRaw: ethers.parseUnits(fromAmount, fromDecimals).toString(),
      fromSymbol,
      walletAddress,
      onStage,
    });
  }

  if (fromChainId !== undefined && isBtcLiFiChain(fromChainId)) {
    return executeLiFiBtcSwap({
      txRequest: quote.transactionRequest,
      fromAmountRaw: ethers.parseUnits(fromAmount, fromDecimals).toString(),
      walletAddress,
      onStage,
    });
  }

  // EVM path: the quote must carry a full EVM transactionRequest. The fields
  // are optional on the type only because Solana routes omit them.
  const { chainId: evmChainId, to: evmTo, value: evmValue, gasLimit: evmGasLimit } = quote.transactionRequest;
  if (evmChainId === undefined || !evmTo || evmValue === undefined || !evmGasLimit) {
    throw new Error('Unsupported chain in route');
  }
  const chainKeyResult = getChainByChainId(evmChainId);
  if (!chainKeyResult) throw new Error('Unsupported chain in route');
  const chainKey = chainKeyResult.key;

  let nonce;
  try {
    nonce = await fetchPendingNonce(walletAddress, chainKey);
  } catch (e) {
    console.error('[swap] fetchPendingNonce failed', { chainKey, e });
    throw new Error('Could not reach the network. Check your connection and try again.');
  }

  const fromAmountWei = ethers.parseUnits(fromAmount, fromDecimals).toString();
  const humanAmount = String(Number(fromAmountWei) / Math.pow(10, fromDecimals));

  if (quote.approvalAddress && quote.approvalToken) {
    onStage?.('Step 1 of 2: Approving token…');
    const calldata = buildERC20ApproveCalldata(quote.approvalAddress, ethers.MaxUint256.toString());
    const approveTx = {
      to: quote.approvalToken,
      data: calldata,
      value: '0x0',
      chainId: evmChainId,
      nonce,
      gasLimit: '65000',
      ...(quote.transactionRequest.maxFeePerGas
        ? { maxFeePerGas: quote.transactionRequest.maxFeePerGas, maxPriorityFeePerGas: quote.transactionRequest.maxPriorityFeePerGas }
        : { gasPrice: quote.transactionRequest.gasPrice }),
    };
    const signedApprove = await signLiFiTransaction(approveTx);

    // submitTransaction does not throw on backend errors — it returns
    // { success: false, message }. Check explicitly so a failed approve
    // surfaces here instead of silently advancing into the swap step
    // with a stale nonce.
    let approveRes;
    try {
      // walletAddress is the wallet the nonce was fetched for — declare it so
      // the BE's signer guard validates against it (not the registered wallet).
      approveRes = await submitTransaction(signedApprove, 'approve', fromSymbol, quote.approvalToken, humanAmount, evmChainId, walletAddress);
    } catch (e) {
      console.error('[swap] approve submitTransaction threw', { chainKey, e });
      throw new Error('Token approval failed. The network may be congested — try again.');
    }
    const approveHash = approveRes.success ? approveRes.data?.txHash : undefined;
    if (!approveHash) {
      console.error('[swap] approve rejected by backend', { chainKey, approveRes });
      throw new Error(mapSubmitError(approveRes.message));
    }

    // Wait for the approve to land before submitting the swap. Without
    // this we'd push two pending txs from the same sender into the
    // mempool back-to-back. For wallets with EIP-7702 delegation (BSC
    // Maxwell+, Pectra), nodes cap delegated EOAs at 1 in-flight tx and
    // reject the second with "in-flight transaction limit reached for
    // delegated accounts". Waiting fixes that and prevents nonce drift
    // for everyone. The wait is wrapped in withRpcFallback so a flaky
    // primary RPC doesn't burn the full timeout when an alternate could
    // see the tx.
    onStage?.('Step 1 of 2: Waiting for approval to confirm…');
    let receipt;
    try {
      receipt = await withRpcFallback(chainKey, (p) =>
        p.waitForTransaction(approveHash, 1, 90_000),
      );
    } catch (e) {
      console.error('[swap] waitForTransaction failed', { chainKey, approveHash, e });
      throw new Error(`Lost connection while waiting for the approval (tx ${approveHash}). Refresh the quote and try again.`);
    }
    if (!receipt) {
      throw new Error(`Approval is taking longer than expected (tx ${approveHash}). Check the network and try again once it confirms.`);
    }
    if (receipt.status === 0) {
      console.error('[swap] approve reverted on-chain', { chainKey, approveHash, receipt });
      throw new Error(`Approval reverted on-chain (tx ${approveHash}). Refresh the quote and try again.`);
    }

    // Re-fetch the pending nonce after the wait rather than
    // optimistically incrementing — guards against the user submitting
    // an unrelated tx from another wallet UI during the ~3-6s wait.
    try {
      nonce = await fetchPendingNonce(walletAddress, chainKey);
    } catch (e) {
      console.error('[swap] fetchPendingNonce after approve failed; falling back to +1', { chainKey, e });
      nonce += 1;
    }
  }

  onStage?.(quote.approvalAddress ? 'Step 2 of 2: Executing swap…' : 'Executing swap…');
  const swapTx = { ...quote.transactionRequest, to: evmTo, value: evmValue, chainId: evmChainId, gasLimit: evmGasLimit, nonce };
  const signedSwap = await signLiFiTransaction(swapTx);
  let res;
  try {
    // feeDeferred: `humanAmount` is a quantity of fromSymbol, and the backend's
    // submit-time fallback would read it as dollars. The caller settles the fee
    // against a re-queried USD value instead (see settleSwapFee) — or, for the
    // brokered stablecoin ramp, charges no platform fee at all.
    res = await submitTransaction(
      signedSwap, 'swap', fromSymbol, evmTo, humanAmount, evmChainId, walletAddress,
      { feeDeferred: true },
    );
  } catch (e) {
    console.error('[swap] swap submitTransaction threw', { chainKey, e });
    throw new Error('Swap broadcast failed. The network may be congested — try again.');
  }

  const swapHash = res.success ? res.data?.txHash : undefined;
  if (!swapHash) {
    console.error('[swap] swap rejected by backend', { chainKey, res });
    throw new Error(mapSubmitError(res.message));
  }
  return swapHash;
}
