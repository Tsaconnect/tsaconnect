// services/chains/tronWallet.ts
// High-level Tron wallet operations the UI calls. The Tron address is derived
// from the SAME mnemonic the EVM wallet already uses (standard path m/44'/195',
// so an exported seed reproduces the same address in TronLink), then registered
// with the backend so the reconciler tracks it. Balances come from the
// dormant-safe /wallet/nonevm/* endpoints.
import { formatUnits } from 'ethers';
import { getActiveWallet, getMnemonic } from '../wallet';
import { deriveTronAddress } from './derivation';
import { getNonEVMBalances, registerNonEVMAddress, type NonEVMBalance } from './nonevmApi';
import { NONEVM_CHAINS } from '../../constants/chains';
import type { WalletBalanceWithChain } from '../walletApi';

const TRON_KEY = 'tron';

// Derived Tron address is a pure function of the mnemonic, so cache per EVM
// wallet address to avoid re-deriving (and re-reading SecureStore) on every
// balance refresh.
const addressCache = new Map<string, string>();

/**
 * Derive the Tron address for a wallet (defaults to the active wallet). Returns
 * null when there's no wallet or its mnemonic isn't available (e.g. a
 * watch-only or key-only legacy wallet).
 */
export async function getTronAddress(walletAddress?: string): Promise<string | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const cached = addressCache.get(wallet);
  if (cached) return cached;
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const tronAddress = deriveTronAddress(mnemonic);
  addressCache.set(wallet, tronAddress);
  return tronAddress;
}

// Stablecoins are pinned to $1; TRX has no price feed yet (deferred per spec),
// so it shows a balance with no USD value rather than a wrong one.
function usdPriceFor(symbol: string): number {
  return symbol === 'USDT' || symbol === 'USDC' ? 1 : 0;
}

function toBalanceRow(b: NonEVMBalance): WalletBalanceWithChain {
  const human = formatUnits(b.raw || '0', b.decimals);
  const usdPrice = usdPriceFor(b.symbol);
  return {
    symbol: b.symbol,
    name: b.symbol,
    balance: human,
    usdPrice,
    usdValue: (parseFloat(human) * usdPrice).toFixed(2),
    contractAddress: b.tokenAddress || '',
    decimals: b.decimals,
    chainKey: TRON_KEY,
    chainName: NONEVM_CHAINS.tron.shortName,
  };
}

/** Fetch Tron balances (TRX + configured TRC-20s) as display rows. */
export async function getTronBalances(tronAddress: string): Promise<WalletBalanceWithChain[]> {
  const balances = await getNonEVMBalances(TRON_KEY, tronAddress);
  return balances.map(toBalanceRow);
}

/**
 * Derive the wallet's Tron address, register it with the backend in the
 * background (best-effort — a failed register must never block balances), and
 * return the address + balance rows. Returns null when the chain is dormant
 * server-side or no mnemonic is available, so callers can simply skip Tron.
 */
export async function loadTronWallet(
  walletAddress?: string
): Promise<{ address: string; balances: WalletBalanceWithChain[] } | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const address = await getTronAddress(wallet);
  if (!address) return null;

  // Register without awaiting the balance path — the balances endpoint doesn't
  // require prior registration; registration only wires up reconcile tracking.
  registerNonEVMAddress(wallet, TRON_KEY, address).catch(() => {
    /* dormant chain or transient error — reconcile tracking is best-effort */
  });

  try {
    const balances = await getTronBalances(address);
    return { address, balances };
  } catch {
    // Chain unavailable (dormant backend) or upstream RPC error — surface the
    // address (receive still works) with no balance rows.
    return { address, balances: [] };
  }
}
