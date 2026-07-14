// services/chains/suiWallet.ts
// High-level Sui wallet operations the UI calls. The Sui address is derived from
// the SAME mnemonic the EVM wallet already uses (SLIP-0010 ed25519 path
// m/44'/784'/0'/0'/0', so an exported seed reproduces the same address in a Sui
// wallet), then registered with the backend so the reconciler tracks it.
// Balances (SUI + all held coin types) come from the dormant-safe
// /wallet/nonevm/* endpoints.
import { formatUnits } from 'ethers';
import { getActiveWallet, getMnemonic } from '../wallet';
import { deriveSuiAddress } from './derivation';
import { getNonEVMBalances, registerNonEVMAddress, type NonEVMBalance } from './nonevmApi';
import { NONEVM_CHAINS } from '../../constants/chains';
import type { WalletBalanceWithChain } from '../walletApi';

const SUI_KEY = 'sui';

// Derived Sui address is a pure function of the mnemonic, so cache per EVM
// wallet address to avoid re-deriving (and re-reading SecureStore) on refresh.
const addressCache = new Map<string, string>();

/**
 * Derive the Sui address for a wallet (defaults to the active wallet).
 * Returns null when there's no wallet or its mnemonic isn't available.
 */
export async function getSuiAddress(walletAddress?: string): Promise<string | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const cached = addressCache.get(wallet);
  if (cached) return cached;
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const suiAddress = deriveSuiAddress(mnemonic);
  addressCache.set(wallet, suiAddress);
  return suiAddress;
}

// Fallback pricing when the backend didn't attach a price: stablecoins pin to
// $1; everything else shows no USD rather than a wrong value.
function fallbackUsdPrice(symbol: string): number {
  return symbol === 'USDT' || symbol === 'USDC' ? 1 : 0;
}

function toBalanceRow(b: NonEVMBalance): WalletBalanceWithChain {
  const human = formatUnits(b.raw || '0', b.decimals);
  const usdPrice = typeof b.usdPrice === 'number' ? b.usdPrice : fallbackUsdPrice(b.symbol);
  const usdValue = typeof b.usdValue === 'number' ? b.usdValue : parseFloat(human) * usdPrice;
  return {
    symbol: b.symbol,
    name: b.symbol,
    balance: human,
    usdPrice,
    usdValue: usdValue.toFixed(2),
    contractAddress: b.tokenAddress || '',
    decimals: b.decimals,
    chainKey: SUI_KEY,
    chainName: NONEVM_CHAINS.sui.shortName,
  };
}

/** Fetch Sui balances (SUI + held coin types) as display rows. */
export async function getSuiBalances(suiAddress: string): Promise<WalletBalanceWithChain[]> {
  const balances = await getNonEVMBalances(SUI_KEY, suiAddress);
  return balances.map(toBalanceRow);
}

/**
 * Derive the wallet's Sui address, register it with the backend in the
 * background (best-effort), and return the address + balance rows. Returns null
 * when the chain is dormant server-side or no mnemonic is available, so callers
 * can simply skip Sui.
 */
export async function loadSuiWallet(
  walletAddress?: string
): Promise<{ address: string; balances: WalletBalanceWithChain[] } | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const address = await getSuiAddress(wallet);
  if (!address) return null;

  registerNonEVMAddress(wallet, SUI_KEY, address).catch(() => {
    /* dormant chain or transient error — reconcile tracking is best-effort */
  });

  try {
    const balances = await getSuiBalances(address);
    return { address, balances };
  } catch {
    // Chain unavailable (dormant backend) or upstream RPC error — surface the
    // address (receive still works) with no balance rows.
    return { address, balances: [] };
  }
}
