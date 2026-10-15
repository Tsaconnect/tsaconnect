// services/chains/nonEvmAddresses.ts
// One place that turns a wallet's mnemonic into its Tron, BTC, Sui and Solana
// receive addresses. Seed derivation is 2048 rounds of pure-JS PBKDF2 and
// blocked the JS thread for seconds per chain on every cold start, holding up
// the whole wallet screen; this derives them all from ONE seed, once per
// wallet, and persists the result.
//
// Only public addresses are stored — never key material. The entry is keyed by
// the EVM wallet address, which is itself derived from the same mnemonic, so an
// entry can never belong to a different seed.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getMnemonic } from '../wallet';
import { deriveNonEvmAddresses, type NonEvmAddresses } from './derivation';

const storageKey = (wallet: string) => `tsa-nonevm-addresses-v1-${wallet}`;

const memory = new Map<string, NonEvmAddresses>();
const inFlight = new Map<string, Promise<NonEvmAddresses | null>>();
// Bumped by forgetNonEvmAddresses so a load that outlives its wallet can't
// write the removed wallet back.
const epochs = new Map<string, number>();
const epochOf = (wallet: string) => epochs.get(wallet) ?? 0;

function isComplete(v: any): v is NonEvmAddresses {
  return (
    !!v &&
    typeof v.tron === 'string' &&
    typeof v.sui === 'string' &&
    typeof v.btc?.mainnet === 'string' &&
    typeof v.btc?.testnet === 'string' &&
    typeof v.solana?.phantom === 'string' &&
    typeof v.solana?.trustwallet === 'string'
  );
}

/**
 * The wallet's non-EVM receive addresses, or null when it has no mnemonic
 * (a private-key import). Concurrent callers share one load.
 */
export function getNonEvmAddresses(wallet: string): Promise<NonEvmAddresses | null> {
  const hit = memory.get(wallet);
  if (hit) return Promise.resolve(hit);
  const pending = inFlight.get(wallet);
  if (pending) return pending;
  const p = load(wallet).finally(() => {
    if (inFlight.get(wallet) === p) inFlight.delete(wallet);
  });
  inFlight.set(wallet, p);
  return p;
}

async function load(wallet: string): Promise<NonEvmAddresses | null> {
  const epoch = epochOf(wallet);
  try {
    const raw = await AsyncStorage.getItem(storageKey(wallet));
    const stored = raw ? JSON.parse(raw) : null;
    if (isComplete(stored)) {
      if (epoch === epochOf(wallet)) memory.set(wallet, stored);
      return stored;
    }
  } catch {
    // Unreadable or corrupt entry — derive afresh and overwrite it.
  }

  const mnemonic = await getMnemonic(wallet);
  if (!mnemonic) return null;
  const derived = deriveNonEvmAddresses(mnemonic);
  if (epoch === epochOf(wallet)) {
    memory.set(wallet, derived);
    AsyncStorage.setItem(storageKey(wallet), JSON.stringify(derived)).catch(() => {});
  }
  return derived;
}

/** Drop a wallet's stored addresses (call on wallet removal). */
export async function forgetNonEvmAddresses(wallet: string): Promise<void> {
  epochs.set(wallet, epochOf(wallet) + 1);
  memory.delete(wallet);
  inFlight.delete(wallet);
  await AsyncStorage.removeItem(storageKey(wallet)).catch(() => {});
}

/** Test hook: forget every in-memory entry. */
export function clearNonEvmAddressCache(): void {
  memory.clear();
  inFlight.clear();
  epochs.clear();
}
