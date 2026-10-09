// utils/recentPayCheckouts.ts
//
// The payments she opened on this phone and has not paid yet — the Pay
// screen's "Waiting for you". The server only links a payment to her once she
// starts paying it, so an opened-but-unpaid one exists nowhere else.
//
// Only ids are trusted: each is checked with isPaymentId on the way in and on
// the way out (AsyncStorage is a value round-tripped through the device, see
// utils/paymentId.ts), and the Pay screen re-reads every one from the server
// before showing it. The merchant name and total kept beside it are only for
// the row while that read is in flight.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { isPaymentId } from '@/utils/paymentId';
import { MAX_TTL_MS } from '@/utils/postAuthRoute';
import { paymentExpiryMs } from '@/utils/payCheckout';

export const RECENT_PAY_CHECKOUTS_KEY = 'tsapay.recentCheckouts';
const MAX_ITEMS = 5;

export type RecentPayCheckout = { id: string; merchantName: string; customerTotal: number; until: number };

function parse(raw: string | null, now: number): RecentPayCheckout[] {
  if (!raw) return [];
  try {
    const list: unknown = JSON.parse(raw);
    if (!Array.isArray(list)) return [];
    return list.filter(
      (r): r is RecentPayCheckout =>
        !!r && isPaymentId(r.id) && typeof r.merchantName === 'string' &&
        typeof r.customerTotal === 'number' && typeof r.until === 'number' && r.until > now,
    );
  } catch {
    return [];
  }
}

async function read(now: number): Promise<RecentPayCheckout[]> {
  try {
    return parse(await AsyncStorage.getItem(RECENT_PAY_CHECKOUTS_KEY), now);
  } catch {
    return [];
  }
}

async function write(list: RecentPayCheckout[]): Promise<void> {
  try {
    await AsyncStorage.setItem(RECENT_PAY_CHECKOUTS_KEY, JSON.stringify(list));
  } catch {
    // Storage unavailable: the list is a convenience, never a reason to fail.
  }
}

/**
 * Remembers a payment she opened and can still pay, newest first, until it
 * expires — or, for one that never does (expiresAt null), for 7 days. Never throws.
 */
export async function rememberPayCheckout(
  c: { id: string; merchantName: string; customerTotal: number; expiresAt?: string | null },
  now: number = Date.now(),
): Promise<void> {
  if (!isPaymentId(c.id)) return;
  const expires = paymentExpiryMs(c.expiresAt);
  if (Number.isNaN(expires) || expires <= now) return;
  const entry: RecentPayCheckout = {
    id: c.id, merchantName: c.merchantName, customerTotal: c.customerTotal, until: Math.min(expires, now + MAX_TTL_MS),
  };
  const rest = (await read(now)).filter((r) => r.id !== c.id);
  await write([entry, ...rest].slice(0, MAX_ITEMS));
}

/** Drops payments that are paid, failed, expired or no longer hers to pay, in one read and one write. Never throws. */
export async function forgetPayCheckouts(ids: readonly string[], now: number = Date.now()): Promise<void> {
  if (ids.length === 0) return;
  const list = await read(now);
  const kept = list.filter((r) => !ids.includes(r.id));
  if (kept.length !== list.length) await write(kept);
}

/** Drops one payment (see forgetPayCheckouts). */
export function forgetPayCheckout(id: string, now: number = Date.now()): Promise<void> {
  return forgetPayCheckouts([id], now);
}

/** The remembered payments not yet past their expiry, newest first. */
export async function listPayCheckouts(now: number = Date.now()): Promise<RecentPayCheckout[]> {
  return read(now);
}
