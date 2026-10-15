// utils/reclaimChain.ts — which escrow the pool (reclaim) screen acts on.
import { BSC_CHAIN_IDS, SONIC_CHAIN_IDS, sortP2PChains } from '@/utils/p2pChains';

export interface ReclaimChainHints {
  /** `?chainId=` the screen was opened on (e.g. the liquidity dashboard's Withdraw). */
  requested?: number;
  /** A chain the user tapped on this screen. */
  picked?: number;
  /** Chains where the wallet holds an escrow balance (available or locked). */
  funded?: readonly number[];
  /** The chain the screen shows now, kept once the user `engaged` with it. */
  current?: number;
  /** The user typed an amount on `current`: a late probe must not move her to
   *  another chain under it (whose decimals and balance differ). */
  engaged?: boolean;
}

/**
 * The chain the pool screen should show once /p2p/chains loads.
 *
 * A chain the screen was explicitly opened on is kept even when the list omits
 * it: falling back would strand an escrow balance behind the wrong chain
 * (review I1); a chain the API cannot serve shows its own balance error
 * instead. Then the user's own pick if still listed, then the chain she typed
 * an amount on if still listed, then a chain holding a
 * balance (BSC first), then BSC — where direct P2P lives — then Sonic, else the
 * first listed chain. Sonic stays selectable for withdraws of old balances.
 */
export function reclaimChainFor(list: { chainId: number }[], hints: ReclaimChainHints = {}): number {
  const { requested, picked, funded = [], current, engaged } = hints;
  if (requested) return requested;
  if (picked && list.some((c) => c.chainId === picked)) return picked;
  if (engaged && current && list.some((c) => c.chainId === current)) return current;
  const ordered = sortP2PChains(list);
  const pick =
    ordered.find((c) => funded.includes(c.chainId)) ??
    ordered.find((c) => BSC_CHAIN_IDS.includes(c.chainId)) ??
    ordered.find((c) => SONIC_CHAIN_IDS.includes(c.chainId)) ??
    ordered[0];
  return pick.chainId;
}

/** True when a /p2p/balance answer holds anything to reclaim or wait on. */
export function hasEscrowBalance(b: { available?: string; locked?: string } | null | undefined): boolean {
  const nonZero = (v?: string) => {
    try {
      return !!v && BigInt(v) > 0n;
    } catch {
      return false;
    }
  };
  return nonZero(b?.available) || nonZero(b?.locked);
}
