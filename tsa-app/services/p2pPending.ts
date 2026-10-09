// services/p2pPending.ts
//
// Safe, non-overwriting recovery store for P2P escrow ops whose on-chain tx
// broadcast but whose server-side confirm POST failed.
//
// There is NO server-side deposit reconciler (only trades are reconciled), so
// this AsyncStorage stash is the ONLY safety net for a dropped deposit/withdraw
// confirm — it must never lose a record. Two design rules follow from that
// (see the spec's "Review fixes" §4):
//
//   1. The store is a LIST, never a singleton. Funding ad #2 must not orphan
//      ad #1's stranded confirm: every pending record is kept until it is
//      explicitly removed (after a successful retried confirm) or discarded.
//   2. Records are keyed by `txHash` for dedupe — the same broadcast stashed
//      twice (e.g. a double-tapped retry) collapses to one entry.
//
// This module is framework-free (no React) so it can back both the
// deposit-in-create-ad path and the withdraw-in-reclaim-screen path. The
// recovery banner keys off the RECORD's own {token, chainId, amount, txHash},
// NOT the live form selection, so a stranded confirm is always visible.
//
// All ops are case-insensitive on walletAddress — a record stashed for
// "0xAbC…" must be found whether the caller passes the checksummed or the
// lowercased form.

import AsyncStorage from '@react-native-async-storage/async-storage';

// 'open' is a trade open (P2P runP2PTx stash): trades are reconciled
// server-side too, but the stash lets the trade screen record it right away.
export type EscrowMode = 'deposit' | 'withdraw' | 'open';

export interface PendingEscrowConfirm {
  mode: EscrowMode;
  token: string;
  chainId: number;
  /** Base units, already scaled by the caller (decimals are not this layer's job). */
  amount: string;
  txHash: string;
  walletAddress: string;
  /** mode 'open' only: the trade this broadcast opened. */
  tradeId?: string;
  /** Epoch milliseconds the record was stashed. */
  createdAt: number;
}

// Single AsyncStorage key holding the entire array of pending records across
// all modes/wallets/chains. Versioned so a future shape change can migrate
// cleanly without colliding with stale entries.
const STORAGE_KEY = 'p2p.pendingEscrowConfirms.v1';

function sameWallet(a: string, b: string): boolean {
  return (a || '').toLowerCase() === (b || '').toLowerCase();
}

/**
 * Read the full pending array. Returns [] on a missing key or any
 * parse/shape error — a corrupt blob must never throw into a recovery banner
 * (failing closed to "nothing pending" is safer than crashing the screen).
 */
async function readAll(): Promise<PendingEscrowConfirm[]> {
  try {
    const raw = await AsyncStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? (parsed as PendingEscrowConfirm[]) : [];
  } catch {
    return [];
  }
}

async function writeAll(records: PendingEscrowConfirm[]): Promise<void> {
  if (records.length === 0) {
    // Keep storage tidy: an empty list is indistinguishable from "no key".
    await AsyncStorage.removeItem(STORAGE_KEY);
    return;
  }
  await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(records));
}

/**
 * List pending records for a given mode + wallet (case-insensitive on the
 * wallet). Other modes/wallets are filtered out so a deposit recovery banner
 * never surfaces a withdraw record (or another wallet's).
 */
export async function listPending(
  mode: EscrowMode,
  walletAddress: string,
): Promise<PendingEscrowConfirm[]> {
  const all = await readAll();
  return all.filter((r) => r.mode === mode && sameWallet(r.walletAddress, walletAddress));
}

/**
 * Append a pending record. Deduped by `txHash`: stashing a record whose
 * txHash already exists replaces that entry rather than adding a duplicate,
 * so a double-tapped retry can't produce two banners for one broadcast.
 */
export async function addPending(rec: PendingEscrowConfirm): Promise<void> {
  const all = await readAll();
  const next = all.filter((r) => r.txHash !== rec.txHash);
  next.push(rec);
  await writeAll(next);
}

/**
 * Remove the record with the given txHash (called after a successful retried
 * confirm, or on explicit discard). No-op if absent.
 */
export async function removePending(txHash: string): Promise<void> {
  const all = await readAll();
  const next = all.filter((r) => r.txHash !== txHash);
  if (next.length !== all.length) {
    await writeAll(next);
  }
}

/** The stashed open broadcast for a trade, if its confirm was lost. */
export async function findPendingOpen(tradeId: string): Promise<PendingEscrowConfirm | null> {
  const all = await readAll();
  return all.find((r) => r.mode === 'open' && r.tradeId === tradeId) ?? null;
}
