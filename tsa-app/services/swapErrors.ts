/**
 * Map raw RPC / backend error strings from /api/wallet/submit-tx to
 * user-friendly copy. Falls back to the raw message (truncated) so anything
 * unmatched is still actionable for support, but guards against showing a
 * stack trace or HTML blob verbatim.
 *
 * Both swap flows (LiFi cross-chain and the Sonic MCGP swap) hit the same
 * submit endpoint and surface the same underlying errors, so this is
 * shared by both screens.
 */
const MAX_RAW_LEN = 200;

export function mapSubmitError(raw: string | undefined | null): string {
  const text = raw ?? '';
  const low = text.toLowerCase();
  if (low.includes('in-flight transaction limit')) {
    // EIP-7702 mempool rule: delegated EOAs are capped at 1 pending tx.
    return 'Your wallet has a pending transaction on this chain. Wait for it to confirm or replace it before swapping again.';
  }
  if (low.includes('nonce too low')) {
    return 'Nonce out of sync — refresh the quote and try again.';
  }
  if (low.includes('insufficient funds') || low.includes('insufficient balance')) {
    return 'Not enough native token to pay gas on this chain.';
  }
  if (low.includes('underpriced')) {
    return 'Gas price too low for current network conditions — refresh the quote and try again.';
  }
  if (low.includes('transfer amount exceeds allowance')) {
    return 'Approval didn\'t confirm in time — try again, the network should catch up.';
  }
  if (low.includes('execution reverted')) {
    return text;
  }
  // Unmatched: long blobs or anything that looks like an internal/HTML/SQL
  // error get a generic message instead of being displayed verbatim.
  if (!text || text.length > MAX_RAW_LEN || /<[a-z!/]/i.test(text) || low.includes('sql:')) {
    return 'Swap failed. Please try again or contact support if the issue persists.';
  }
  return text;
}
