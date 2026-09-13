/**
 * Map raw RPC / backend error strings from /api/wallet/submit-tx to
 * user-friendly copy. Falls back to a caller-supplied sentence so anything
 * unmatched is still safe to show, and guards against putting a stack trace,
 * an HTML blob or an RPC's internals in front of a user.
 *
 * Every flow that broadcasts through that endpoint shares these errors — both
 * swaps (LiFi cross-chain and the Sonic MCGP desk), the Paycrest sell, a plain
 * wallet send and instant pay — so the mapping is shared. What differs is the
 * copy: a swap is quoted and a transfer is not, which changes what we can
 * honestly tell someone to do next. Hence SubmitErrorCopy.
 */
const MAX_RAW_LEN = 200;

export interface SubmitErrorCopy {
  /** Shown when the failure can't be put into plainer words. */
  fallback: string;
  /** What to do next, as a complete sentence, after a transient failure. */
  retry: string;
}

/**
 * A quoted swap. "Try again" is not enough here: a LiFi quote carries a route,
 * an expected output and a validity window, so re-submitting against the old
 * one can execute at a price the user never agreed to, or fail on slippage.
 */
export const SWAP_COPY: SubmitErrorCopy = {
  fallback: 'Swap failed. Please try again or contact support if the issue persists.',
  retry: 'Refresh the quote and try again.',
};

/** A plain transfer — an amount and a destination, with no quote to refresh. */
export const TRANSFER_COPY: SubmitErrorCopy = {
  fallback: 'Could not send. Please try again or contact support if the issue persists.',
  retry: 'Please try again.',
};

export function mapSubmitError(
  raw: string | undefined | null,
  copy: SubmitErrorCopy = SWAP_COPY,
): string {
  const text = raw ?? '';
  const low = text.toLowerCase();
  if (low.includes('in-flight transaction limit')) {
    // EIP-7702 mempool rule: delegated EOAs are capped at 1 pending tx.
    return 'Your wallet has a pending transaction on this chain. Wait for it to confirm before trying again.';
  }
  // The node already holds this exact signed transaction: it is in the mempool
  // and will mine. Telling the user it failed invites a duplicate send, which
  // is the one outcome worth protecting against here.
  if (low.includes('already known')) {
    return 'This transaction has already been submitted and is confirming. Check your transaction history before sending again.';
  }
  if (low.includes('nonce too low')) {
    return `Your wallet was out of step with the network. ${copy.retry}`;
  }
  if (low.includes('insufficient funds') || low.includes('insufficient balance')) {
    return 'Not enough native token to pay gas on this chain.';
  }
  // The chain repriced between preparing the transaction and broadcasting it,
  // so the gas price it was signed with no longer covers the base fee. The send
  // flow already retries this once with a freshly-priced transaction; reaching
  // here means fees are still climbing.
  if (low.includes('less than block base fee')) {
    return `Network fees rose while you were confirming. ${copy.retry}`;
  }
  // Covers both "transaction underpriced" (below the pool's floor) and
  // "replacement transaction underpriced" (one of ours is already pending at
  // this nonce). Neither is retried automatically — see STALE_TX_ERRORS.
  if (low.includes('underpriced')) {
    return `Gas price too low for current network conditions. ${copy.retry}`;
  }
  if (low.includes('transfer amount exceeds allowance')) {
    return 'Approval didn\'t confirm in time — try again, the network should catch up.';
  }
  // A revert reason is the most useful thing we can show, so it passes through
  // — but through the same size and shape guard as everything else. An ABI blob
  // is not copy, and callers that want to decode one read the raw message.
  if (low.includes('execution reverted') && isShowable(text)) {
    return text;
  }
  return isShowable(text) ? text : copy.fallback;
}

/**
 * Whether a raw message can be shown as-is: short enough to read, and not an
 * internal error that happens to have reached the client.
 */
function isShowable(text: string): boolean {
  return (
    text.length > 0 &&
    text.length <= MAX_RAW_LEN &&
    !/<[a-z!/]/i.test(text) &&
    !text.toLowerCase().includes('sql:')
  );
}

/**
 * Unwrap a broadcast response into a tx hash, or throw a message fit to show.
 *
 * Screens render whatever this throws, verbatim. Having one of these means a
 * new send flow cannot forget the mapping and leak "maxFeePerGas: 20006000
 * baseFee: 20098000" into a red box — which is how it leaked from two screens
 * that each hand-rolled the same three lines.
 */
export function txHashOrThrow(
  res: { success?: boolean; message?: string; data?: { txHash?: string } | null },
  copy: SubmitErrorCopy = TRANSFER_COPY,
): string {
  const hash = res?.data?.txHash;
  if (!res?.success || !hash) {
    throw new Error(mapSubmitError(res?.message, copy));
  }
  return hash;
}
