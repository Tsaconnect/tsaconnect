// services/chains/solanaWallet.ts
// High-level Solana wallet operations the UI calls. The Solana address is
// derived from the SAME mnemonic the EVM wallet already uses (Phantom path
// m/44'/501'/0'/0', so an exported seed reproduces the same address in Phantom),
// then registered with the backend so the reconciler tracks it. Balances (SOL +
// all held SPL tokens) come from the dormant-safe /wallet/nonevm/* endpoints.
import { formatUnits } from 'ethers';
import { getActiveWallet, getMnemonic } from '../wallet';
import { deriveSolanaAddress } from './derivation';
import { getNonEVMBalances, registerNonEVMAddress, type NonEVMBalance } from './nonevmApi';
import { NONEVM_CHAINS } from '../../constants/chains';
import type { WalletBalanceWithChain } from '../walletApi';

const SOLANA_KEY = 'solana';

// Derived Solana address is a pure function of the mnemonic, so cache per EVM
// wallet address to avoid re-deriving (and re-reading SecureStore) on refresh.
const addressCache = new Map<string, string>();

/**
 * Derive the Solana address for a wallet (defaults to the active wallet).
 * Returns null when there's no wallet or its mnemonic isn't available.
 */
export async function getSolanaAddress(walletAddress?: string): Promise<string | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const cached = addressCache.get(wallet);
  if (cached) return cached;
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const solAddress = deriveSolanaAddress(mnemonic);
  addressCache.set(wallet, solAddress);
  return solAddress;
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
    chainKey: SOLANA_KEY,
    chainName: NONEVM_CHAINS.solana.shortName,
  };
}

/** Fetch Solana balances (SOL + held SPL tokens) as display rows. */
export async function getSolanaBalances(solAddress: string): Promise<WalletBalanceWithChain[]> {
  const balances = await getNonEVMBalances(SOLANA_KEY, solAddress);
  return balances.map(toBalanceRow);
}

/**
 * Derive the wallet's Solana address, register it with the backend in the
 * background (best-effort), and return the address + balance rows. Returns null
 * when the chain is dormant server-side or no mnemonic is available, so callers
 * can simply skip Solana.
 */
export async function loadSolanaWallet(
  walletAddress?: string
): Promise<{ address: string; balances: WalletBalanceWithChain[] } | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const address = await getSolanaAddress(wallet);
  if (!address) return null;

  registerNonEVMAddress(wallet, SOLANA_KEY, address).catch(() => {
    /* dormant chain or transient error — reconcile tracking is best-effort */
  });

  try {
    const balances = await getSolanaBalances(address);
    return { address, balances };
  } catch {
    // Chain unavailable (dormant backend) or upstream RPC error — surface the
    // address (receive still works) with no balance rows.
    return { address, balances: [] };
  }
}
