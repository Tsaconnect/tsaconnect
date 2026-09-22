// hooks/useWalletHoldings.ts
//
// What the active wallet holds on every chain the picker can show — EVM chains
// through /wallet/balances, plus the Solana, Bitcoin and Sui sides from the same
// mnemonic — as flat Holding rows keyed by LiFi chain id.
//
// Read once per wallet and kept for a short while, so opening the picker for
// "You pay" and then "You receive" does not reload the same balances.

import { useEffect, useState } from 'react';
import { CHAINS, NONEVM_CHAINS, type ChainKey } from '@/constants/chains';
import { getWalletBalances, normalizeWalletBalances, type WalletBalanceWithChain } from '@/services/walletApi';
import { getActiveWallet } from '@/services/wallet';
import { getSolanaAddress, getSolanaBalances } from '@/services/chains/solanaWallet';
import { getBtcAddress, getBtcBalances } from '@/services/chains/btcWallet';
import { getSuiAddress, getSuiBalances } from '@/services/chains/suiWallet';
import { BTC_LIFI_CHAIN_ID, SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID } from '@/services/lifi';
import { isNativeLiFiToken, type Holding } from '@/services/assetCatalog';

const CACHE_MS = 60_000;
let cache: { wallet: string; at: number; holdings: Holding[] } | null = null;

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
 * other families' rows are trusted only for their native coin.
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
    trusted: fromSupportedList || native,
    symbol: row.symbol,
    name: row.name || row.symbol,
    decimals: row.decimals,
    balance,
    usdValue: Number(row.usdValue) || 0,
  };
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

export function useWalletHoldings(enabled: boolean): { holdings: Holding[]; loading: boolean } {
  // Starts empty, not from the cache: the cache may be another wallet's until
  // the active wallet is known below.
  const [holdings, setHoldings] = useState<Holding[]>([]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    (async () => {
      const wallet = await getActiveWallet();
      if (cancelled) return;
      if (!wallet) { setHoldings([]); return; }
      if (cache && cache.wallet === wallet && Date.now() - cache.at < CACHE_MS) {
        setHoldings(cache.holdings);
        return;
      }
      setLoading(true);
      const next = await loadHoldings(wallet);
      // A load where nothing answered is not remembered, so the next open retries.
      if (next) cache = { wallet, at: Date.now(), holdings: next };
      if (!cancelled) {
        setHoldings(next ?? []);
        setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [enabled]);

  return { holdings, loading };
}

/** Forget cached balances — after a swap, or in tests — so the next open re-reads them. */
export function invalidateWalletHoldings() {
  cache = null;
}
