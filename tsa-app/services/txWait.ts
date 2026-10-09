// services/txWait.ts
//
// Wait for a broadcast tx to MINE and SUCCEED. A mined-but-reverted receipt
// (status 0) used to count as done (R10): an approve that reverted let the
// deposit go ahead and fail on transferFrom. Shared by escrowFunding (approve)
// and p2pGas (sponsored top-up).
import { p2pChainKey } from './p2p';
import { getProvider } from './wallet';

export const DEFAULT_WAIT_MS = 90_000;

export class TxRevertedError extends Error {
  constructor(public readonly txHash: string) {
    super(`Transaction ${txHash} failed on chain`);
    this.name = 'TxRevertedError';
  }
}

export async function waitMined(
  chainId: number,
  txHash: string,
  opts: { timeoutMs?: number; label?: string } = {},
): Promise<void> {
  const provider = getProvider(p2pChainKey(chainId));
  const label = opts.label ?? 'Transaction';
  let timer: ReturnType<typeof setTimeout> | undefined;
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
