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

// Trade-scoped broadcasts stashed by runP2PTx (`mode` = its gas action, or
// 'open'). Trades are reconciled server-side too, but the stash lets the trade
// screen retry the confirm right away and show "confirming" instead of
// re-offering an action that is already on its way.
export type TradeTxMode = 'open' | 'mark_paid' | 'release' | 'cancel_trade' | 'dispute';
export type EscrowMode = 'deposit' | 'withdraw' | TradeTxMode;

const TRADE_TX_MODES: readonly EscrowMode[] = ['open', 'mark_paid', 'release', 'cancel_trade', 'dispute'];

function isTradeTxMode(mode: EscrowMode): mode is TradeTxMode {
  return TRADE_TX_MODES.includes(mode);
}

export interface PendingEscrowConfirm {
  mode: EscrowMode;
  token: string;
  chainId: number;
  /** Base units, already scaled by the caller (decimals are not this layer's job). */
  amount: string;
  txHash: string;
  walletAddress: string;
  /** Trade modes only: the trade this broadcast acted on. */
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

/** A stashed trade tx: a trade mode, and always the trade it acted on. */
export type PendingTradeTx = PendingEscrowConfirm & { mode: TradeTxMode; tradeId: string };

export function isPendingTradeTx(r: PendingEscrowConfirm): r is PendingTradeTx {
  return isTradeTxMode(r.mode) && !!r.tradeId;
}

/** Every stashed trade tx for one trade (any TradeTxMode), oldest first. */
export async function listPendingForTrade(tradeId: string): Promise<PendingTradeTx[]> {
  const all = await readAll();
  return all.filter(isPendingTradeTx).filter((r) => r.tradeId === tradeId);
}
