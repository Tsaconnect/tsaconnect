// utils/p2pChains.ts — the P2P network rules every P2P screen shares.
//
// Direct P2P lives on BSC; Sonic keeps Payment Merchant trades (and withdraws
// of old balances). So BSC sorts first everywhere, and the create screens only
// offer a network the API says is open to this caller for new ads.
//
// Pure: no React, no I/O.
import type { P2PChainInfo } from '@/services/p2p';

export const BSC_CHAIN_IDS: readonly number[] = [56, 97];
export const SONIC_CHAIN_IDS: readonly number[] = [146, 14601];

function chainRank(chainId: number): number {
  if (BSC_CHAIN_IDS.includes(chainId)) return 0;
  if (SONIC_CHAIN_IDS.includes(chainId)) return 1;
  return 2;
}

/** BSC first, then Sonic, then ascending chain id. Never mutates its input. */
export function sortP2PChains<T extends { chainId: number }>(chains: T[]): T[] {
  return [...chains].sort((a, b) => chainRank(a.chainId) - chainRank(b.chainId) || a.chainId - b.chainId);
}

/** Chains the caller may post a new ad on. A missing `adsOpen` is open: an
 *  API older than the flag still lists every chain as postable. */
export function openAdChains(chains: P2PChainInfo[]): P2PChainInfo[] {
  return chains.filter((c) => c.adsOpen !== false);
}

/** The tokens a new ad may use: every asset on an open chain. */
export function adTokenSymbols(chains: P2PChainInfo[]): string[] {
  return Array.from(new Set(openAdChains(chains).flatMap((c) => c.assets)));
}

/** The networks a new ad in `symbol` may go on, in display order. */
export function adChainsFor(chains: P2PChainInfo[], symbol: string): P2PChainInfo[] {
  return sortP2PChains(openAdChains(chains).filter((c) => c.assets.includes(symbol)));
}

export type ChainsLoad = 'loading' | 'ready' | 'failed';

/** What a create screen shows for its network step: the load state, or
 *  'none_open' when the chains loaded but none is open to this caller for new
 *  ads (every adsOpen false, or no assets) — an inline notice with Retry, never
 *  empty token chips and a balance that never loads. */
export type AdChainsView = ChainsLoad | 'none_open';

export function adChainsView(load: ChainsLoad, chains: P2PChainInfo[]): AdChainsView {
  if (load === 'ready' && adTokenSymbols(chains).length === 0) return 'none_open';
  return load;
}

export const NO_OPEN_NETWORK_TITLE = 'No network is open for new ads right now';

export type AdChainDecision = { kind: 'ok'; chainId: number } | { kind: 'block'; message: string };

/**
 * The chain a new ad is published on, or why publish must wait. Publish never
 * goes out without a chainId: the API defaults an absent one to Sonic, and the
 * amount would then be scaled with the wrong chain's decimals.
 */
export function adPublishChain(load: ChainsLoad, selectedChainId: number | null | undefined): AdChainDecision {
  if (load === 'failed') {
    return { kind: 'block', message: "We couldn't load the networks. Tap Retry, then publish." };
  }
  if (load === 'loading') {
    return { kind: 'block', message: 'Still loading networks — wait a moment and try again.' };
  }
  if (selectedChainId == null) {
    return { kind: 'block', message: 'No network is open for new ads in this token right now.' };
  }
  return { kind: 'ok', chainId: selectedChainId };
}

/**
 * The All Swap hand-off for a seller short of `symbol` on BSC mainnet: All Swap
 * opens with BSC `symbol` as the receive leg, and the user picks what to pay
 * with. Null anywhere else (LiFi has no testnet routes, and other chains are
 * not where direct P2P lives).
 */
export function bscTopUpSwapParams(
  chainId: number | null | undefined,
  symbol: string,
): { tab: 'allswap'; toSymbol: string; toChain: 'bsc' } | null {
  if (chainId !== 56) return null;
  return { tab: 'allswap', toSymbol: symbol, toChain: 'bsc' };
}

/** True when the wallet can't cover the ad: a known balance below the amount.
 *  An unknown balance ('-' / null) is not "short" — never nag on a failed read. */
export function walletShort(balanceHuman: string | null, amountHuman: string): boolean {
  if (balanceHuman == null || balanceHuman === '-') return false;
  const need = parseFloat(amountHuman || '0');
  const have = parseFloat(balanceHuman);
  return isFinite(need) && need > 0 && isFinite(have) && have < need;
}
