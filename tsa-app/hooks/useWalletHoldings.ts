// hooks/useWalletHoldings.ts
//
// What the active wallet holds on every chain the picker can show — EVM chains
// through /wallet/balances (our supported tokens) and /wallet/discovered-tokens
// (every other ERC-20, the same source as the wallet screen), plus the Solana,
// Bitcoin and Sui sides from the same mnemonic — as flat Holding rows keyed by
// LiFi chain id.
//
// Read once per wallet and kept for a short while, so opening the picker for
// "You pay" and then "You receive" does not reload the same balances. The last
// read is also kept on the device: an open with nothing fresh in memory shows
// those balances at once and swaps in the new read when it lands, instead of a
// spinner. A screen's picker prefetches on mount, so the read is usually done
// before the sheet opens.
//
// Discovery is its own lane: the indexer fan-out can take 25s on a cold cache,
// so the balances show (and `loading` ends) without it, and the discovered
// tokens join the list when they land.

import { useEffect, useMemo, useState } from 'react';
import { ethers } from 'ethers';
import { CHAINS, NONEVM_CHAINS, type ChainKey } from '@/constants/chains';
import {
  getDiscoveredTokens, getWalletBalances, normalizeWalletBalances, type DiscoveredToken, type WalletBalanceWithChain,
} from '@/services/walletApi';
import { getNetwork } from '@/hooks/useNetwork';
import { getActiveWallet } from '@/services/wallet';
import { getSolanaAddress, getSolanaBalances } from '@/services/chains/solanaWallet';
import { getBtcAddress, getBtcBalances } from '@/services/chains/btcWallet';
import { getSuiAddress, getSuiBalances } from '@/services/chains/suiWallet';
import { BTC_LIFI_CHAIN_ID, SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID } from '@/services/lifi';
import { isNativeLiFiToken, type Holding } from '@/services/assetCatalog';
import { persistedCache } from '@/services/persistedCache';

const CACHE_MS = 60_000;
let cache: { wallet: string; at: number; holdings: Holding[] } | null = null;
let inFlight: { wallet: string; read: Promise<Holding[] | null> } | null = null;
// Bumped by invalidateWalletHoldings, so a read that began before a trade
// cannot put pre-trade balances back.
let generation = 0;

interface StoredHoldings { wallet: string; holdings: Holding[] }
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

/** Discovered holdings, or null when the read failed (not "holds nothing"). */
async function loadDiscovered(wallet: string): Promise<Holding[] | null> {
  // Discovery indexes mainnets only.
  if (getNetwork() !== 'mainnet') return [];
  const res = await getDiscoveredTokens(undefined, wallet);
  if (!res.success || !res.data) return null;
  if (!res.data.providerAvailable) return [];
  return (res.data.tokens ?? []).flatMap(discoveredRow)
    .map(row => holdingFromRow(row, false))
    .filter((h): h is Holding => h !== null);
}

/** A discovered token as a balance row: never native, never from our list. */
function discoveredRow(t: DiscoveredToken): WalletBalanceWithChain[] {
  try {
    return [{
      chainKey: t.chain as ChainKey,
      chainName: t.chain,
      symbol: t.symbol,
      name: t.name,
      balance: ethers.formatUnits(t.balance, t.decimals),
      usdValue: String(Number.isFinite(t.usdValue) ? t.usdValue : 0),
      contractAddress: t.contractAddress,
      decimals: t.decimals,
      native: false,
    }];
  } catch {
    return []; // an unparseable raw balance
  }
}

/**
 * Discovered holdings the other reads do not already cover, matched by
 * contract. The indexer also reports our own tokens (USDC on Base); those keep
 * the curated row.
 */
export function newDiscoveries(known: Holding[], discovered: Holding[]): Holding[] {
  const seen = new Set(known.filter(h => h.address).map(h => `${h.chainId}:${h.address.toLowerCase()}`));
  return discovered.filter(h => !seen.has(`${h.chainId}:${h.address.toLowerCase()}`));
}

/** null when every chain family failed — an unknown, never "holds nothing". */
async function loadHoldings(wallet: string): Promise<Holding[] | null> {
  const [evm, sol, btc, sui] = await Promise.allSettled([
    // This client answers {success:false} rather than throwing; that is a
    // failed read, not an empty wallet.
    getWalletBalances(undefined, wallet).then(r => {
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

// The discovery lane: same one-read-per-wallet and freshness rules, kept in
// memory only. A stale copy for the same wallet is shown while it re-reads.
let discovered: { wallet: string; at: number; holdings: Holding[] } | null = null;
let discoveryInFlight: { wallet: string; read: Promise<Holding[] | null> } | null = null;

function readDiscovered(wallet: string): Promise<Holding[] | null> {
  if (discovered?.wallet === wallet && Date.now() - discovered.at < CACHE_MS) return Promise.resolve(discovered.holdings);
  if (discoveryInFlight?.wallet === wallet) return discoveryInFlight.read;
  const startedIn = generation;
  const read = loadDiscovered(wallet)
    .catch(() => null)
    .then(next => {
      if (next && startedIn === generation) discovered = { wallet, at: Date.now(), holdings: next };
      return next;
    })
    .finally(() => { if (discoveryInFlight?.read === read) discoveryInFlight = null; });
  discoveryInFlight = { wallet, read };
  return read;
}

const freshInMemory = (wallet: string) =>
  cache && cache.wallet === wallet && Date.now() - cache.at < CACHE_MS ? cache.holdings : null;

/** One read per wallet at a time: a prefetch and an open share it. */
function readHoldings(wallet: string): Promise<Holding[] | null> {
  if (inFlight?.wallet === wallet) return inFlight.read;
  const startedIn = generation;
  const read = loadHoldings(wallet)
    .then(next => {
      // A load where nothing answered is not remembered, so the next open retries.
      if (next && startedIn === generation) {
        cache = { wallet, at: Date.now(), holdings: next };
        stored.write({ wallet, holdings: next });
      }
      return next;
    })
    .finally(() => { if (inFlight?.read === read) inFlight = null; });
  inFlight = { wallet, read };
  return read;
}

/** The last balances this device saw for `wallet`, and whether they are recent enough to skip a read. */
async function storedHoldings(wallet: string): Promise<{ holdings: Holding[]; fresh: boolean } | null> {
  const hit = await stored.read();
  return hit && hit.value.wallet === wallet ? { holdings: hit.value.holdings, fresh: hit.fresh } : null;
}

/** Start reading the active wallet's balances before the picker opens. */
export async function prefetchWalletHoldings(): Promise<void> {
  const wallet = await getActiveWallet();
  if (!wallet) return;
  void readDiscovered(wallet); // not awaited: the balances never wait for it
  if (!freshInMemory(wallet)) await readHoldings(wallet);
}

export function useWalletHoldings(enabled: boolean): { holdings: Holding[]; loading: boolean } {
  // Starts empty, not from the cache: the cache may be another wallet's until
  // the active wallet is known below.
  const [holdings, setHoldings] = useState<Holding[]>([]);
  const [loading, setLoading] = useState(false);
  // Until the first read settles, "no holdings" means "not read yet" — the
  // wallet lookup itself is async, so `loading` alone starts out false.
  const [settled, setSettled] = useState(false);
  const [found, setFound] = useState<Holding[]>([]);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    const settle = (next: Holding[]) => { setHoldings(next); setSettled(true); };
    (async () => {
      const wallet = await getActiveWallet();
      if (cancelled) return;
      if (!wallet) { settle([]); return; }

      if (discovered?.wallet === wallet) setFound(discovered.holdings);
      readDiscovered(wallet).then(next => { if (next && !cancelled) setFound(next); });

      const fresh = freshInMemory(wallet);
      if (fresh) { settle(fresh); return; }

      // Something to show while the read is out: the device copy, written with every read.
      const earlier = await storedHoldings(wallet);
      if (cancelled) return;
      if (earlier) {
        settle(earlier.holdings);
        if (earlier.fresh) return;
      } else {
        setLoading(true);
      }
      const next = await readHoldings(wallet);
      if (cancelled) return;
      if (next) settle(next);
      else if (!earlier) settle([]);
      setLoading(false);
    })();
    return () => { cancelled = true; };
  }, [enabled]);

  const all = useMemo(() => [...holdings, ...newDiscoveries(holdings, found)], [holdings, found]);
  return { holdings: all, loading: enabled && (loading || !settled) };
}

/** Forget cached balances — after a swap, or in tests — so the next open re-reads them. */
export function invalidateWalletHoldings() {
  cache = null;
  inFlight = null;
  discovered = null;
  discoveryInFlight = null;
  generation += 1;
  stored.clear();
}
