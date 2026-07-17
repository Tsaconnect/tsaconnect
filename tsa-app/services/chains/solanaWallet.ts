// services/chains/solanaWallet.ts
// High-level Solana wallet operations the UI calls. The Solana address is
// derived from the SAME mnemonic the EVM wallet already uses — normally at the
// Phantom path m/44'/501'/0'/0', but seeds imported from Trust Wallet hold
// their funds one hop shorter at m/44'/501'/0', so the first load scans both
// paths' balances and sticks with the one that's funded. The resolved address
// is registered with the backend so the reconciler tracks it. Balances (SOL +
// all held SPL tokens) come from the dormant-safe /wallet/nonevm/* endpoints.
import { formatUnits } from 'ethers';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getActiveWallet, getMnemonic } from '../wallet';
import { deriveSolanaAddress, deriveSolanaPrivateKey, type SolanaDerivation } from './derivation';
import { getNonEVMBalances, registerNonEVMAddress, type NonEVMBalance } from './nonevmApi';
import { NONEVM_CHAINS } from '../../constants/chains';
import { getNetwork } from '../../hooks/useNetwork';
import type { WalletBalanceWithChain } from '../walletApi';

const SOLANA_KEY = 'solana';
const derivationPrefKey = (wallet: string) => `tsa-solana-derivation-${wallet}`;

type Resolved = { derivation: SolanaDerivation; address: string };

// resolvedCache memoizes only durable decisions (stored or freshly persisted)
// so a backend-down fallback keeps retrying the scan on later calls.
// phantomDefaults memoizes the pure standard-path derivation for those
// fallbacks (mnemonicToSeedSync is ~2048 PBKDF2 rounds per call).
const resolvedCache = new Map<string, Resolved>();
const phantomDefaults = new Map<string, Resolved>();
const inFlight = new Map<string, Promise<Resolved>>();
// Wallets whose OTHER path was already probed this session (see loadSolanaWallet).
const otherPathProbed = new Set<string>();

/** Test hook: forget all resolved derivations and probe throttles. */
export function clearSolanaCache(): void {
  resolvedCache.clear();
  phantomDefaults.clear();
  inFlight.clear();
  otherPathProbed.clear();
}

/** Drop a wallet's persisted path decision and caches (call on wallet removal). */
export async function forgetSolanaDerivation(wallet: string): Promise<void> {
  resolvedCache.delete(wallet);
  phantomDefaults.delete(wallet);
  otherPathProbed.delete(wallet);
  await AsyncStorage.removeItem(derivationPrefKey(wallet)).catch(() => {});
}

function hasFunds(balances: NonEVMBalance[]): boolean {
  return balances.some((b) => {
    try {
      return BigInt(b.raw || '0') > 0n;
    } catch {
      return false;
    }
  });
}

function phantomDefault(wallet: string, mnemonic: string): Resolved {
  const hit = phantomDefaults.get(wallet);
  if (hit) return hit;
  const out: Resolved = { derivation: 'phantom', address: deriveSolanaAddress(mnemonic) };
  phantomDefaults.set(wallet, out);
  return out;
}

/**
 * Decide which derivation path this wallet's Solana account lives at. Stored
 * preference wins; otherwise scan: standard (Phantom) path funded → phantom,
 * else Trust Wallet path funded → trustwallet, else phantom. The decision is
 * persisted per wallet — address and signing key must never disagree.
 * Concurrent callers share one in-flight resolution so they can't race to
 * different answers.
 */
function resolveDerivation(wallet: string, mnemonic: string): Promise<Resolved> {
  const hit = resolvedCache.get(wallet);
  if (hit) return Promise.resolve(hit);
  const pending = inFlight.get(wallet);
  if (pending) return pending;
  const p = doResolve(wallet, mnemonic).finally(() => inFlight.delete(wallet));
  inFlight.set(wallet, p);
  return p;
}

async function doResolve(wallet: string, mnemonic: string): Promise<Resolved> {
  let stored: string | null;
  try {
    stored = await AsyncStorage.getItem(derivationPrefKey(wallet));
  } catch {
    // Can't tell whether a decision already exists — don't run a scan that
    // could overwrite it. Fall back for this call; the next call retries.
    return phantomDefault(wallet, mnemonic);
  }
  if (stored === 'phantom' || stored === 'trustwallet') {
    const out: Resolved = { derivation: stored, address: deriveSolanaAddress(mnemonic, stored) };
    resolvedCache.set(wallet, out);
    return out;
  }

  // Only mainnet balances say where the user's real funds are — off mainnet
  // there is nothing to scan, so default to the standard path (unpersisted).
  if (getNetwork() !== 'mainnet') return phantomDefault(wallet, mnemonic);

  const standard = phantomDefault(wallet, mnemonic);
  let out = standard;
  try {
    if (!hasFunds(await getNonEVMBalances(SOLANA_KEY, standard.address))) {
      const legacy = deriveSolanaAddress(mnemonic, 'trustwallet');
      if (hasFunds(await getNonEVMBalances(SOLANA_KEY, legacy))) {
        out = { derivation: 'trustwallet', address: legacy };
      }
    }
    // This scan just checked both paths — no point probing again this session.
    otherPathProbed.add(wallet);
  } catch {
    // Backend unreachable/dormant — show the standard address for now, but
    // don't persist or cache so the next call retries the scan.
    return standard;
  }
  // Re-check the network: a mid-scan toggle means the balances above may mix
  // mainnet and devnet answers — treat the result as a throwaway fallback.
  if (getNetwork() !== 'mainnet') return out;
  resolvedCache.set(wallet, out);
  AsyncStorage.setItem(derivationPrefKey(wallet), out.derivation).catch(() => {});
  return out;
}

/**
 * Derive the Solana address for a wallet (defaults to the active wallet).
 * Returns null when there's no wallet or its mnemonic isn't available.
 */
export async function getSolanaAddress(walletAddress?: string): Promise<string | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const cached = resolvedCache.get(wallet);
  if (cached) return cached.address;
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  return (await resolveDerivation(wallet, mnemonic)).address;
}

export interface SolanaSigner {
  address: string;
  /** 0x-less 32-byte hex ed25519 seed pairing with `address`. */
  key: string;
}

/**
 * Resolve the wallet's Solana address and its matching ed25519 signing key in
 * ONE resolution, so a flow that verifies the address and then signs cannot
 * see the two disagree. Signing with the other path's key would fail
 * verification against the fee-payer pubkey the backend embedded in the
 * message. Returns null when the wallet has no mnemonic.
 */
export async function getSolanaSigner(walletAddress: string): Promise<SolanaSigner | null> {
  const mnemonic = await getMnemonic(walletAddress);
  if (!mnemonic) return null;
  const { derivation, address } = await resolveDerivation(walletAddress, mnemonic);
  return { address, key: deriveSolanaPrivateKey(mnemonic, derivation) };
}

/** The signing key alone — see getSolanaSigner for the pairing guarantee. */
export async function getSolanaSigningKey(walletAddress: string): Promise<string | null> {
  return (await getSolanaSigner(walletAddress))?.key ?? null;
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
 * Self-heal a stale path decision: when the resolved path holds nothing, probe
 * the OTHER path once per session and follow the funds if they're there (e.g.
 * a wallet locked to phantom while empty, funded on the Trust Wallet path
 * later). Only flips when the current path is empty, so nothing visible ever
 * disappears. Returns the new Resolved on a flip, null otherwise.
 */
async function followFundsToOtherPath(wallet: string): Promise<Resolved | null> {
  if (getNetwork() !== 'mainnet' || otherPathProbed.has(wallet)) return null;
  otherPathProbed.add(wallet);
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const current = await resolveDerivation(wallet, mnemonic);
  const other: SolanaDerivation = current.derivation === 'phantom' ? 'trustwallet' : 'phantom';
  const address = deriveSolanaAddress(mnemonic, other);
  try {
    if (!hasFunds(await getNonEVMBalances(SOLANA_KEY, address))) return null;
  } catch {
    otherPathProbed.delete(wallet); // transient failure — allow a later retry
    return null;
  }
  const out: Resolved = { derivation: other, address };
  resolvedCache.set(wallet, out);
  AsyncStorage.setItem(derivationPrefKey(wallet), other).catch(() => {});
  return out;
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
  let address = await getSolanaAddress(wallet);
  if (!address) return null;

  registerNonEVMAddress(wallet, SOLANA_KEY, address).catch(() => {
    /* dormant chain or transient error — reconcile tracking is best-effort */
  });

  try {
    let raw = await getNonEVMBalances(SOLANA_KEY, address);
    if (!hasFunds(raw)) {
      const healed = await followFundsToOtherPath(wallet);
      if (healed) {
        address = healed.address;
        raw = await getNonEVMBalances(SOLANA_KEY, address);
        registerNonEVMAddress(wallet, SOLANA_KEY, address).catch(() => {});
      }
    }
    return { address, balances: raw.map(toBalanceRow) };
  } catch {
    // Chain unavailable (dormant backend) or upstream RPC error — surface the
    // address (receive still works) with no balance rows.
    return { address, balances: [] };
  }
}
