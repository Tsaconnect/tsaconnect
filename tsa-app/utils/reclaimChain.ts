// utils/reclaimChain.ts — which escrow the pool (reclaim) screen acts on.

const SONIC_CHAIN_IDS = [146, 14601];

/**
 * The chain the pool screen should show once /p2p/chains loads. A chain the
 * screen was explicitly opened on (`?chainId=`, e.g. the liquidity dashboard's
 * Withdraw) is kept even when the list omits it: falling back to Sonic would
 * strand an LP's pool balance behind the wrong chain (review I1); a chain the
 * API cannot serve shows its own balance error instead. Otherwise the current
 * selection if still listed, else Sonic, else the first listed chain.
 */
export function reclaimChainFor(list: { chainId: number }[], prev: number, requested?: number): number {
  if (requested) return requested;
  if (list.some((c) => c.chainId === prev)) return prev;
  const sonic = list.find((c) => SONIC_CHAIN_IDS.includes(c.chainId));
  return (sonic ?? list[0]).chainId;
}
