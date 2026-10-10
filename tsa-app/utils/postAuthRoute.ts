// utils/postAuthRoute.ts
//
// Where sign-in should land — ordinarily home, but a customer who tapped a
// merchant's pay link while signed out (app/pay.tsx's 'signin' state) should
// come straight back to it once they've authenticated, not be dumped on
// home and have to find the link again.
//
// A new customer takes the long way: she signs up, verifies her email, and may
// have to pass KYC before she can pay live — which can take days. So the
// payment is remembered until the payment itself expires (capped at 7 days),
// and replayed from every completion point: sign-in, cold start and unlock,
// finishing signup (app/verify, reached with ?from=signup) and finishing KYC
// (app/profile/kyc). A replay consumes it; the pay screen remembers it again
// when she is sent off to verify her identity, and forgets it once the
// payment is over.
//
// Only a payment ID is ever remembered, never a full route string: the app's
// own deep-link params are attacker-controlled (a pasted URL, a notification
// tap), so what gets replayed after sign-in is built entirely from our own
// '/pay?id=' shape, never from stored freeform text — see isPaymentId.
import AsyncStorage from '@react-native-async-storage/async-storage';
import type { Href } from 'expo-router';
import { isPaymentId } from '@/utils/paymentId';

export const PENDING_PAY_KEY = 'tsapay.pendingPayment';

/** How long a payment is remembered when its expiry isn't known. */
const DEFAULT_TTL_MS = 30 * 60 * 1000;
/** The longest a payment is ever remembered, whatever its expiry says. */
export const MAX_TTL_MS = 7 * 24 * 60 * 60 * 1000;

/** `until` is when to stop replaying it; a record from an older build has none. */
type Pending = { id: string; savedAt: number; until?: number };

/**
 * Remembers a payment to return to after sign-in, signup or KYC — until
 * `expiresAt` (the payment's own expiry, capped at 7 days), or 30 minutes when
 * that isn't known. Silently does nothing for anything that isn't a real payment id.
 *
 * Never throws: it runs just before navigating (Sign in, Create an account,
 * Verify identity), and a storage failure must cost only the return trip, never
 * the navigation itself.
 */
export async function rememberPendingPayment(
  id: string,
  opts: { expiresAt?: string; now?: number } = {},
): Promise<void> {
  if (!isPaymentId(id)) return;
  const now = opts.now ?? Date.now();
  const expires = opts.expiresAt ? Date.parse(opts.expiresAt) : NaN;
  const until = Number.isFinite(expires) ? Math.min(expires, now + MAX_TTL_MS) : now + DEFAULT_TTL_MS;
  const pending: Pending = { id, savedAt: now, until };
  try {
    await AsyncStorage.setItem(PENDING_PAY_KEY, JSON.stringify(pending));
  } catch {
    // Storage unavailable: she goes on without the way back.
  }
}

/**
 * The payment id in a stored record while it is still worth returning to, or
 * null. Throws on a record that isn't JSON — every caller catches that.
 */
function pendingIdFrom(raw: string, now: number): string | null {
  const pending: unknown = JSON.parse(raw);
  if (!pending || typeof pending !== 'object') return null;
  const { id, savedAt, until } = pending as Partial<Pending>;
  if (!isPaymentId(id)) return null;
  if (typeof savedAt !== 'number') return null;
  const deadline = typeof until === 'number' ? until : savedAt + DEFAULT_TTL_MS;
  return now < deadline ? id : null;
}

/** Whether a payment is waiting for her — without consuming it (KYC's "Continue to payment"). */
export async function hasPendingPayment(now: number = Date.now()): Promise<boolean> {
  try {
    const raw = await AsyncStorage.getItem(PENDING_PAY_KEY);
    return raw !== null && pendingIdFrom(raw, now) !== null;
  } catch {
    return false;
  }
}

/** Forgets the remembered payment if it is this one — it is over, nothing to return to. */
export async function forgetPendingPayment(id: string): Promise<void> {
  try {
    const raw = await AsyncStorage.getItem(PENDING_PAY_KEY);
    if (raw && (JSON.parse(raw) as Partial<Pending> | null)?.id === id) await AsyncStorage.removeItem(PENDING_PAY_KEY);
  } catch {
    // Unreadable: postAuthRoute already treats it as nothing.
  }
}

/**
 * Where to land right after authentication. Consumes the stored pending
 * payment (so a second call never replays it) and only honours it when it is
 * a valid payment id still inside its remembered window — otherwise home.
 *
 * Never throws: every launch and every sign-in waits on this, so a storage
 * failure or a mangled record lands on home rather than leaving the app stuck
 * on its entry screen.
 */
export async function postAuthRoute(now: number = Date.now()): Promise<string> {
  try {
    const raw = await AsyncStorage.getItem(PENDING_PAY_KEY);
    if (!raw) return '/home';
    // Consumed before it is read: a mangled record is gone too, not replayed forever.
    await AsyncStorage.removeItem(PENDING_PAY_KEY).catch(() => {});
    const id = pendingIdFrom(raw, now);
    return id ? `/pay?id=${id}` : '/home';
  } catch {
    return '/home';
  }
}

/** Every post-auth call site (login, cold-start index, lock unlock, finishing
 *  signup, finishing KYC) hands off through here instead of hardcoding
 *  '/home', so a pending payment always wins over the default landing screen. */
export async function goAfterAuth(router: { replace(href: Href): void }): Promise<void> {
  router.replace((await postAuthRoute()) as Href);
}
