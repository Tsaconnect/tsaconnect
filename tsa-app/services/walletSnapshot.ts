// services/walletSnapshot.ts
//
// The wallet screen's last complete asset list, per wallet and network, kept on
// the device so the next open shows balances at once and refreshes them in
// place — the way established wallets feel instant even when the chains are
// slow to answer. Public balances only; never key material.

import type { NetworkType } from '@/constants/chains';
import { isWalletSnapshot, type WalletSnapshot } from '@/utils/walletAssets';
import { persistedCache, type PersistedCache } from './persistedCache';

// The screen always revalidates, so freshness only matters to callers that
// might skip a read; a day keeps an old copy from looking current.
const TTL_MS = 24 * 60 * 60 * 1000;

export function walletSnapshot<T>(wallet: string, network: NetworkType): PersistedCache<WalletSnapshot<T>> {
  return persistedCache(
    `wallet_screen_v1_${wallet}_${network}`,
    TTL_MS,
    isWalletSnapshot as (value: unknown) => value is WalletSnapshot<T>,
  );
}

/** Drop a wallet's stored lists on both networks (call on wallet removal). */
export async function forgetWalletSnapshots(wallet: string): Promise<void> {
  await Promise.all((['mainnet', 'testnet'] as const).map((n) => walletSnapshot(wallet, n).clear()));
}
