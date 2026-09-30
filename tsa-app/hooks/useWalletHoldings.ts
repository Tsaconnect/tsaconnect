// hooks/useWalletHoldings.ts
//
// What the active wallet holds on every chain the picker can show — EVM chains
// through /wallet/balances, plus the Solana, Bitcoin and Sui sides from the same
// mnemonic — as flat Holding rows keyed by LiFi chain id.
//
// Read once per wallet (and network) and kept for a short while, so opening
// the picker for "You pay" and then "You receive" does not reload the same
// balances. The last read is also kept on the device: an open with nothing
// fresh in memory shows those balances at once and swaps in the new read when
// it lands, instead of a spinner. A screen's picker prefetches on mount, so
// the read is usually done before the sheet opens.
//
// Callers normally follow the app's testnet toggle (the default). A caller
// whose balances must mean something specific regardless of the toggle — the
// marketplace checkout picker, always Sonic mainnet — pins `network`
// explicitly; the cache is keyed on wallet+network so the two never collide.

import { useEffect, useState } from 'react';
import { CHAINS, NONEVM_CHAINS, type ChainKey, type NetworkType } from '@/constants/chains';
import { getWalletBalances, normalizeWalletBalances, type WalletBalanceWithChain } from '@/services/walletApi';
import { getActiveWallet } from '@/services/wallet';
import { getSolanaAddress, getSolanaBalances } from '@/services/chains/solanaWallet';
import { getBtcAddress, getBtcBalances } from '@/services/chains/btcWallet';
import { getSuiAddress, getSuiBalances } from '@/services/chains/suiWallet';
import { BTC_LIFI_CHAIN_ID, SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID } from '@/services/lifi';
import { isNativeLiFiToken, type Holding } from '@/services/assetCatalog';
import { persistedCache } from '@/services/persistedCache';
import { getNetwork } from '@/hooks/useNetwork';

const CACHE_MS = 60_000;
const cacheKey = (wallet: string, network: NetworkType) => `${wallet}|${network}`;
let cache: { key: string; at: number; holdings: Holding[] } | null = null;
let inFlight: { key: string; read: Promise<Holding[] | null> } | null = null;
// Bumped by invalidateWalletHoldings, so a read that began before a trade
// cannot put pre-trade balances back.
let generation = 0;

interface StoredHoldings { wallet: string; network: NetworkType; holdings: Holding[] }
const stored = persistedCache(
  'wallet_holdings_v1',
  CACHE_MS,
  (v): v is StoredHoldings =>
    typeof (v as StoredHoldings)?.wallet === 'string' && Array.isArray((v as StoredHoldings).holdings),
);

const NON_EVM_CHAIN_IDS: Record<string, number> = {
  solana: SOLANA_LIFI_CHAIN_ID,
  btc: BTC_LIFI_CHAIN_ID,
  sui: SUI_LIFI_CHAIN_ID,
};

/** The chain's own coin symbol (ETH, BNB, S, SOL…). */
function nativeSymbolFor(chainKey: string): string | undefined {
  const nonEvm = NONEVM_CHAINS[chainKey as keyof typeof NONEVM_CHAINS];
  return (nonEvm ?? CHAINS[chainKey as ChainKey])?.nativeCurrency?.symbol;
}

/**
 * A balance row as a Holding, or null when its chain has no LiFi id here.
 * `fromSupportedList` rows come from our own curated token list (trusted);
 * other families' rows are trusted only for their native coin or a token the
 * API marks as listed — a discovered coin's symbol is whatever its creator chose.
 */
export function holdingFromRow(row: WalletBalanceWithChain, fromSupportedList: boolean): Holding | null {
  const chainId = NON_EVM_CHAIN_IDS[row.chainKey] ?? CHAINS[row.chainKey as ChainKey]?.chainId;
  const balance = Number(row.balance);
  if (!chainId || !Number.isFinite(balance) || balance <= 0) return null;
  // Native when the API says so, when the address is the chain's native form
  // (Sui reports SUI as 0x00…02::sui::SUI), or — for a row with no address —
  // when the symbol IS the chain's coin. A missing address alone never is.
  const native = row.native === true || (row.contractAddress
    ? isNativeLiFiToken({ chainId, address: row.contractAddress })
    : row.symbol.toUpperCase() === nativeSymbolFor(row.chainKey)?.toUpperCase());
  return {
    chainId,
    address: native ? '' : row.contractAddress || '',
    native,
    trusted: fromSupportedList || native || row.listed === true,
    symbol: row.symbol,
    name: row.name || row.symbol,
    decimals: row.decimals,
    balance,
    usdValue: Number(row.usdValue) || 0,
  };
}

/** null when every chain family failed — an unknown, never "holds nothing". */
async function loadHoldings(wallet: string, network: NetworkType): Promise<Holding[] | null> {
  const [evm, sol, btc, sui] = await Promise.allSettled([
    // This client answers {success:false} rather than throwing; that is a
    // failed read, not an empty wallet.
    getWalletBalances(undefined, wallet, network).then(r => {
      if (!r.success || !r.data) throw new Error(r.message || 'balances unavailable');
      return normalizeWalletBalances(r.data);
    }),
    getSolanaAddress(wallet).then(a => (a ? getSolanaBalances(a) : [])),
    getBtcAddress(wallet).then(a => (a ? getBtcBalances(a) : [])),
    getSuiAddress(wallet).then(a => (a ? getSuiBalances(a) : [])),
  ]);
  const results = [evm, sol, btc, sui];
  if (results.every(r => r.status === 'rejected')) return null;
  // A family that fails to load just contributes nothing; the picker still works.
  const rowsOf = (r: PromiseSettledResult<WalletBalanceWithChain[]>) => (r.status === 'fulfilled' ? r.value : []);
  return [
    ...rowsOf(evm).map(row => holdingFromRow(row, true)),
    ...[sol, btc, sui].flatMap(rowsOf).map(row => holdingFromRow(row, false)),
  ].filter((h): h is Holding => h !== null);
}

const freshInMemory = (wallet: string, network: NetworkType) =>
  cache && cache.key === cacheKey(wallet, network) && Date.now() - cache.at < CACHE_MS ? cache.holdings : null;

/** One read per wallet+network at a time: a prefetch and an open share it. */
function readHoldings(wallet: string, network: NetworkType): Promise<Holding[] | null> {
  const key = cacheKey(wallet, network);
  if (inFlight?.key === key) return inFlight.read;
  const startedIn = generation;
  const read = loadHoldings(wallet, network)
    .then(next => {
      // A load where nothing answered is not remembered, so the next open retries.
      if (next && startedIn === generation) {
        cache = { key, at: Date.now(), holdings: next };
        stored.write({ wallet, network, holdings: next });
      }
      return next;
    })
    .finally(() => { if (inFlight?.read === read) inFlight = null; });
  inFlight = { key, read };
  return read;
}

/** The last balances this device saw for `wallet` on `network`, and whether they are recent enough to skip a read. */
async function storedHoldings(wallet: string, network: NetworkType): Promise<{ holdings: Holding[]; fresh: boolean } | null> {
  const hit = await stored.read();
  return hit && hit.value.wallet === wallet && hit.value.network === network
    ? { holdings: hit.value.holdings, fresh: hit.fresh }
    : null;
}

/**
 * Start reading the active wallet's balances before the picker opens. Follows
 * the app's testnet toggle unless `network` pins it (the checkout picker
 * always pins 'mainnet' — see the file header).
 */
export async function prefetchWalletHoldings(network: NetworkType = getNetwork()): Promise<void> {
  const wallet = await getActiveWallet();
  if (!wallet || freshInMemory(wallet, network)) return;
  await readHoldings(wallet, network);
}

export function useWalletHoldings(
  enabled: boolean,
  network: NetworkType = getNetwork(),
): { holdings: Holding[]; loading: boolean } {
  // Starts empty, not from the cache: the cache may be another wallet's (or
  // network's) until the active wallet is known below.
  const [holdings, setHoldings] = useState<Holding[]>([]);
  const [loading, setLoading] = useState(false);
  // Until the first read settles, "no holdings" means "not read yet" — the
  // wallet lookup itself is async, so `loading` alone starts out false.
  const [settled, setSettled] = useState(false);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    const settle = (next: Holding[]) => { setHoldings(next); setSettled(true); };
    (async () => {
      const wallet = await getActiveWallet();
      if (cancelled) return;
      if (!wallet) { settle([]); return; }
      const fresh = freshInMemory(wallet, network);
      if (fresh) { settle(fresh); return; }

      // Something to show while the read is out: the device copy, written with every read.
      const earlier = await storedHoldings(wallet, network);
      if (cancelled) return;
      if (earlier) {
        settle(earlier.holdings);
        if (earlier.fresh) return;
      } else {
        setLoading(true);
      }
      const next = await readHoldings(wallet, network);
      if (cancelled) return;
      if (next) settle(next);
      else if (!earlier) settle([]);
      setLoading(false);
    })();
    return () => { cancelled = true; };
  }, [enabled, network]);

  return { holdings, loading: enabled && (loading || !settled) };
}

/** Forget cached balances — after a swap, or in tests — so the next open re-reads them. */
export function invalidateWalletHoldings() {
  cache = null;
  inFlight = null;
  generation += 1;
  stored.clear();
}
