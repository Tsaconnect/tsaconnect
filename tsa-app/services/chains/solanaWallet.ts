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
import { deriveSolanaAddresses, deriveSolanaPrivateKey, type SolanaDerivation } from './derivation';
import { getNonEVMBalances, registerNonEVMAddress, type NonEVMBalance } from './nonevmApi';
import { nonEvmUsdPrice } from './nonevmPrice';
import { NONEVM_CHAINS } from '../../constants/chains';
import { getNetwork } from '../../hooks/useNetwork';
import type { WalletBalanceWithChain } from '../walletApi';

const SOLANA_KEY = 'solana';
const derivationPrefKey = (wallet: string) => `tsa-solana-derivation-${wallet}`;

/**
 * `settled` is false for a stand-in answer (pref unreadable, scan failed, or
 * the network changed mid-scan). A stand-in must never be registered with the
 * backend or signed with: the backend keeps ONE Solana address per wallet and
 * delivers stablecoin buys to it, so registering a guess would route a user's
 * funds to the path the app is about to stop showing.
 */
type Resolved = { derivation: SolanaDerivation; address: string; settled: boolean };

const otherPathOf = (d: SolanaDerivation): SolanaDerivation => (d === 'phantom' ? 'trustwallet' : 'phantom');

// Public addresses only, one seed derivation per wallet per session.
const addressCache = new Map<string, Record<SolanaDerivation, string>>();
// Only persisted decisions, so a stand-in answer keeps retrying the scan.
const resolvedCache = new Map<string, Resolved>();
const inFlight = new Map<string, Promise<Resolved>>();
// The other-path probe runs once per session; its finding is kept so every
// reload in that session reports the same notice.
const otherPathFindings = new Map<string, SolanaOtherPath | null>();
// Bumped by forgetSolanaDerivation so a scan that outlives its wallet can't
// persist a decision for it afterwards.
const epochs = new Map<string, number>();
const epochOf = (wallet: string) => epochs.get(wallet) ?? 0;

/** Test hook: forget all resolved derivations and probe findings. */
export function clearSolanaCache(): void {
  addressCache.clear();
  resolvedCache.clear();
  inFlight.clear();
  otherPathFindings.clear();
  epochs.clear();
}

/** Drop a wallet's persisted path decision and caches (call on wallet removal). */
export async function forgetSolanaDerivation(wallet: string): Promise<void> {
  epochs.set(wallet, epochOf(wallet) + 1);
  addressCache.delete(wallet);
  resolvedCache.delete(wallet);
  inFlight.delete(wallet);
  otherPathFindings.delete(wallet);
  await AsyncStorage.removeItem(derivationPrefKey(wallet)).catch(() => {});
}

// Stablecoins stay counted when the price feed is down. Their symbol is safe to
// trust: the backend names only mints in its registry and abbreviates the rest.
const TRUSTED_UNPRICED = new Set(['USDC', 'USDT']);

/**
 * Whether a path holds anything worth following. Spam SPL airdrops are common
 * on Solana and carry no price, so counting them would let an airdrop on one
 * path outrank real funds on the other.
 */
function hasFunds(balances: NonEVMBalance[]): boolean {
  return balances.some((b) => {
    let raw: bigint;
    try {
      raw = BigInt(b.raw || '0');
    } catch {
      return false;
    }
    if (raw <= 0n) return false;
    return b.isNative || (b.usdPrice ?? 0) > 0 || TRUSTED_UNPRICED.has(b.symbol);
  });
}

function addressesFor(wallet: string, mnemonic: string): Record<SolanaDerivation, string> {
  const hit = addressCache.get(wallet);
  if (hit) return hit;
  const both = deriveSolanaAddresses(mnemonic);
  addressCache.set(wallet, both);
  return both;
}

function persist(wallet: string, out: Resolved): void {
  resolvedCache.set(wallet, out);
  AsyncStorage.setItem(derivationPrefKey(wallet), out.derivation).catch(() => {});
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
  const p = doResolve(wallet, mnemonic).finally(() => {
    if (inFlight.get(wallet) === p) inFlight.delete(wallet);
  });
  inFlight.set(wallet, p);
  return p;
}

async function doResolve(wallet: string, mnemonic: string): Promise<Resolved> {
  const epoch = epochOf(wallet);
  const addresses = addressesFor(wallet, mnemonic);
  const standIn: Resolved = { derivation: 'phantom', address: addresses.phantom, settled: false };

  let stored: string | null;
  try {
    stored = await AsyncStorage.getItem(derivationPrefKey(wallet));
  } catch {
    // Can't tell whether a decision already exists — don't run a scan that
    // could overwrite it. The next call retries.
    return standIn;
  }
  if (stored === 'phantom' || stored === 'trustwallet') {
    const out: Resolved = { derivation: stored, address: addresses[stored], settled: true };
    if (epoch === epochOf(wallet)) resolvedCache.set(wallet, out);
    return out;
  }

  // Only mainnet balances say where the user's real funds are. Off mainnet the
  // standard path is the answer (settled, so devnet sends can register), but it
  // isn't persisted: the first mainnet load still gets to scan.
  if (getNetwork() !== 'mainnet') return { ...standIn, settled: true };

  let out: Resolved = { ...standIn, settled: true };
  try {
    if (!hasFunds(await getNonEVMBalances(SOLANA_KEY, addresses.phantom))) {
      if (hasFunds(await getNonEVMBalances(SOLANA_KEY, addresses.trustwallet))) {
        out = { derivation: 'trustwallet', address: addresses.trustwallet, settled: true };
      }
    }
  } catch {
    return standIn;
  }
  // A mid-scan network toggle may have mixed mainnet and devnet answers, and a
  // removed wallet must not get its decision written back.
  if (getNetwork() !== 'mainnet' || epoch !== epochOf(wallet)) return { ...out, settled: false };
  persist(wallet, out);
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
 * message. Returns null when the wallet has no mnemonic; throws when the path
 * isn't settled yet, rather than signing (and registering) a guess.
 */
export async function getSolanaSigner(walletAddress: string): Promise<SolanaSigner | null> {
  const mnemonic = await getMnemonic(walletAddress);
  if (!mnemonic) return null;
  const { derivation, address, settled } = await resolveDerivation(walletAddress, mnemonic);
  if (!settled) {
    throw new Error("Couldn't confirm your Solana address. Check your connection and try again.");
  }
  return { address, key: deriveSolanaPrivateKey(mnemonic, derivation) };
}


function toBalanceRow(b: NonEVMBalance): WalletBalanceWithChain {
  const human = formatUnits(b.raw || '0', b.decimals);
  const usdPrice = nonEvmUsdPrice(b);
  const usdValue = typeof b.usdValue === 'number' ? b.usdValue : parseFloat(human) * usdPrice;
  return {
    symbol: b.symbol,
    name: b.symbol,
    balance: human,
    usdPrice,
    usdValue: usdValue.toFixed(2),
    contractAddress: b.tokenAddress || '',
    decimals: b.decimals,
    listed: b.listed === true ? true : undefined,
    chainKey: SOLANA_KEY,
    chainName: NONEVM_CHAINS.solana.shortName,
  };
}

/** Fetch Solana balances (SOL + held SPL tokens) as display rows. */
export async function getSolanaBalances(solAddress: string): Promise<WalletBalanceWithChain[]> {
  const balances = await getNonEVMBalances(SOLANA_KEY, solAddress);
  return balances.map(toBalanceRow);
}

/** Funds found on the path the wallet is NOT using, for the UI to offer a switch. */
export interface SolanaOtherPath {
  /** The EVM wallet id this finding belongs to. */
  wallet: string;
  derivation: SolanaDerivation;
  address: string;
  balances: WalletBalanceWithChain[];
}

/**
 * Check the path the wallet isn't using, once per session on mainnet. Returns
 * its address and balances when it holds funds, null otherwise. A transient
 * failure isn't remembered, so a later load retries.
 */
async function probeOtherPath(
  wallet: string,
  mnemonic: string,
  current: Resolved,
): Promise<{ derivation: SolanaDerivation; address: string; raw: NonEVMBalance[] } | null> {
  if (getNetwork() !== 'mainnet' || otherPathFindings.has(wallet)) return null;
  const derivation = otherPathOf(current.derivation);
  const address = addressesFor(wallet, mnemonic)[derivation];
  let raw: NonEVMBalance[];
  try {
    raw = await getNonEVMBalances(SOLANA_KEY, address);
  } catch {
    return null;
  }
  // A toggle to testnet mid-probe means this may be devnet's answer: acting on
  // it could move the wallet off its funded path, and remembering an empty one
  // would silence the probe for the rest of the session.
  if (getNetwork() !== 'mainnet') return null;
  if (!hasFunds(raw)) {
    otherPathFindings.set(wallet, null);
    return null;
  }
  return { derivation, address, raw };
}

/**
 * Move the wallet onto the other derivation path: re-register the address with
 * the backend, then persist it and forget the session's probe so the path it
 * leaves is checked again on the next load. Registration goes first so a
 * failure leaves the app and the backend both on the old path — otherwise the
 * app would show the new address while buys still deliver to the old one.
 */
export async function switchSolanaDerivation(wallet: string, derivation: SolanaDerivation): Promise<string | null> {
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const address = addressesFor(wallet, mnemonic)[derivation];
  await registerNonEVMAddress(wallet, SOLANA_KEY, address);
  persist(wallet, { derivation, address, settled: true });
  otherPathFindings.delete(wallet);
  return address;
}

/**
 * Derive the wallet's Solana address, register it with the backend in the
 * background (best-effort), and return the address + balance rows. When the
 * resolved path is empty but the other path holds funds, the wallet follows
 * the funds. When BOTH hold funds, the other path comes back as `otherPath`
 * so the UI can offer a switch instead of silently hiding it. Returns null
 * when no mnemonic is available, so callers can simply skip Solana.
 */
export async function loadSolanaWallet(
  walletAddress?: string
): Promise<{ address: string; balances: WalletBalanceWithChain[]; otherPath: SolanaOtherPath | null } | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const resolved = await resolveDerivation(wallet, mnemonic);
  const address = resolved.address;

  if (resolved.settled) {
    registerNonEVMAddress(wallet, SOLANA_KEY, address).catch(() => {
      /* dormant chain or transient error — reconcile tracking is best-effort */
    });
  }

  let raw: NonEVMBalance[];
  try {
    raw = await getNonEVMBalances(SOLANA_KEY, address);
  } catch {
    // Chain unavailable (dormant backend) or upstream RPC error — surface the
    // address (receive still works) with no balance rows.
    return { address, balances: [], otherPath: null };
  }

  // Probing on top of a stand-in answer would compare against a guess.
  const probe = resolved.settled ? await probeOtherPath(wallet, mnemonic, resolved) : null;
  if (probe && !hasFunds(raw)) {
    // Nothing visible disappears: the current path is empty, the other isn't.
    persist(wallet, { derivation: probe.derivation, address: probe.address, settled: true });
    otherPathFindings.set(wallet, null);
    registerNonEVMAddress(wallet, SOLANA_KEY, probe.address).catch(() => {});
    return { address: probe.address, balances: probe.raw.map(toBalanceRow), otherPath: null };
  }
  if (probe) {
    otherPathFindings.set(wallet, {
      wallet,
      derivation: probe.derivation,
      address: probe.address,
      balances: probe.raw.map(toBalanceRow),
    });
  }
  return { address, balances: raw.map(toBalanceRow), otherPath: otherPathFindings.get(wallet) ?? null };
}
