// services/chains/tronWallet.ts
// High-level Tron wallet operations the UI calls. The Tron address is derived
// from the SAME mnemonic the EVM wallet already uses (standard path m/44'/195',
// so an exported seed reproduces the same address in TronLink), then registered
// with the backend so the reconciler tracks it. Balances come from the
// dormant-safe /wallet/nonevm/* endpoints.
import { getActiveWallet } from '../wallet';
import { getNonEvmAddresses } from './nonEvmAddresses';
import { getNonEVMBalances, registerNonEVMAddress } from './nonevmApi';
import { toNonEvmBalanceRow } from './nonevmRows';
import type { WalletBalanceWithChain } from '../walletApi';

const TRON_KEY = 'tron';

/**
 * Derive the Tron address for a wallet (defaults to the active wallet). Returns
 * null when there's no wallet or its mnemonic isn't available (e.g. a
 * watch-only or key-only legacy wallet).
 */
export async function getTronAddress(walletAddress?: string): Promise<string | null> {
  const wallet = walletAddress || (await getActiveWallet());
  if (!wallet) return null;
  return (await getNonEvmAddresses(wallet))?.tron ?? null;
}

/** Fetch Tron balances (TRX + configured TRC-20s) as display rows. */
export async function getTronBalances(tronAddress: string): Promise<WalletBalanceWithChain[]> {
  const balances = await getNonEVMBalances(TRON_KEY, tronAddress);
  return balances.map((b) => toNonEvmBalanceRow(b, TRON_KEY));
}

/**
 * Derive the wallet's Tron address, register it with the backend in the
 * background (best-effort — a failed register must never block balances), and
 * return the address + balance rows. Returns null when the chain is dormant
 * server-side or no mnemonic is available, so callers can simply skip Tron.
 */
export async function loadTronWallet(
  walletAddress?: string
): Promise<{ address: string; balances: WalletBalanceWithChain[]; failed?: boolean } | null> {
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
    return { address, balances: [], failed: true };
  }
}
