// services/chains/btcWallet.ts
// High-level BTC wallet operations the UI calls. The BTC address is derived
// from the SAME mnemonic the EVM wallet already uses (standard BIP-84 path,
// so an exported seed reproduces the same bc1… address in Sparrow/Electrum),
// then registered with the backend so the reconciler tracks it. Balances come
// from the dormant-safe /wallet/nonevm/* endpoints. Unlike Tron, the address
// is network-specific (bc1… mainnet vs tb1… testnet), so caches key on both.
import { formatUnits } from 'ethers';
import { getActiveWallet, getMnemonic } from '../wallet';
import { deriveBtcAddress } from './derivation';
import { getNonEVMBalances, registerNonEVMAddress } from './nonevmApi';
import { NONEVM_CHAINS } from '../../constants/chains';
import { getNetwork } from '../../hooks/useNetwork';
import type { WalletBalanceWithChain } from '../walletApi';

const BTC_KEY = 'btc';

// Derived BTC address is a pure function of (mnemonic, network) — cache per
// EVM wallet id + network to avoid re-deriving and re-reading SecureStore on
// every balance refresh.
const addressCache = new Map<string, string>();

/**
 * Derive the BTC address for a wallet (defaults to the active wallet) on the
 * currently selected network. Returns null when there's no wallet or its
 * mnemonic isn't available (e.g. a watch-only or key-only legacy wallet).
 */
export async function getBtcAddress(walletAddress?: string): Promise<string | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const network = getNetwork();
  const cacheKey = `${wallet}:${network}`;
  const cached = addressCache.get(cacheKey);
  if (cached) return cached;
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const btcAddress = deriveBtcAddress(mnemonic, network);
  addressCache.set(cacheKey, btcAddress);
  return btcAddress;
}

/** Fetch the BTC balance as a display row. */
export async function getBtcBalances(btcAddress: string): Promise<WalletBalanceWithChain[]> {
  const balances = await getNonEVMBalances(BTC_KEY, btcAddress);
  return balances.map((b) => {
    const human = formatUnits(b.raw || '0', b.decimals);
    const usdPrice = typeof b.usdPrice === 'number' ? b.usdPrice : 0;
    const usdValue = typeof b.usdValue === 'number' ? b.usdValue : parseFloat(human) * usdPrice;
    return {
      symbol: b.symbol,
      name: b.symbol === 'BTC' ? 'Bitcoin' : b.symbol,
      balance: human,
      usdPrice,
      usdValue: usdValue.toFixed(2),
      contractAddress: '',
      decimals: b.decimals,
      chainKey: BTC_KEY,
      chainName: NONEVM_CHAINS.btc.shortName,
    };
  });
}

/**
 * Derive the wallet's BTC address, register it with the backend in the
 * background (best-effort — a failed register must never block balances), and
 * return the address + balance rows. Returns null when the chain is dormant
 * server-side or no mnemonic is available, so callers can simply skip BTC.
 */
export async function loadBtcWallet(
  walletAddress?: string
): Promise<{ address: string; balances: WalletBalanceWithChain[] } | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const address = await getBtcAddress(wallet);
  if (!address) return null;

  registerNonEVMAddress(wallet, BTC_KEY, address).catch(() => {
    /* dormant chain or transient error — reconcile tracking is best-effort */
  });

  try {
    const balances = await getBtcBalances(address);
    return { address, balances };
  } catch {
    // Chain unavailable (dormant backend) or upstream error — surface the
    // address (receive still works) with no balance rows.
    return { address, balances: [] };
  }
}
