// services/txWait.ts
//
// Wait for a broadcast tx to MINE and SUCCEED. A mined-but-reverted receipt
// (status 0) used to count as done (R10): an approve that reverted let the
// deposit go ahead and fail on transferFrom. Shared by escrowFunding (approve)
// and p2pGas (sponsored top-up).
import { p2pChainKey, p2pNetwork } from './p2p';
import { getProvider } from './wallet';

export const DEFAULT_WAIT_MS = 90_000;

export class TxRevertedError extends Error {
  constructor(public readonly txHash: string) {
    super(`Transaction ${txHash} failed on chain`);
    this.name = 'TxRevertedError';
  }
}

/** Anything that can wait for a receipt — e.g. the ethers provider that
 *  accepted the broadcast (TransactionResponse.provider). */
export interface ReceiptSource {
  waitForTransaction(txHash: string): Promise<{ status?: number | null } | null>;
}

export async function waitMined(
  chainId: number,
  txHash: string,
  opts: { timeoutMs?: number; label?: string; provider?: ReceiptSource } = {},
): Promise<void> {
  // Default: the chain's primary RPC. Pass the provider that took the
  // broadcast when it may have been a fallback (primary down).
  const provider = opts.provider ?? getProvider(p2pChainKey(chainId), undefined, p2pNetwork(chainId));
  const label = opts.label ?? 'Transaction';
  let timer: ReturnType<typeof setTimeout> | undefined;
  // The race only stops US waiting: ethers keeps polling for the receipt in
  // the background after a timeout until it arrives. Harmless, but keep
  // timeouts short rather than relying on this to cancel anything.
  try {
    const receipt = await Promise.race([
      provider.waitForTransaction(txHash),
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error(`${label} confirmation timed out`)), opts.timeoutMs ?? DEFAULT_WAIT_MS);
      }),
    ]);
    if (!receipt || receipt.status !== 1) throw new TxRevertedError(txHash);
  } finally {
    if (timer) clearTimeout(timer);
  }
}
