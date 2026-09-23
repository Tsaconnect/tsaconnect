// utils/postAuthRoute.ts
//
// Where sign-in should land — ordinarily home, but a customer who tapped a
// merchant's pay link while signed out (app/pay.tsx's 'signin' state) should
// come straight back to it once they've authenticated, not be dumped on
// home and have to find the link again.
//
// Only a payment ID is ever remembered, never a full route string: the app's
// own deep-link params are attacker-controlled (a pasted URL, a notification
// tap), so what gets replayed after sign-in is built entirely from our own
// '/pay?id=' shape, never from stored freeform text — see isPaymentId.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { isPaymentId } from '@/services/payApi';

export const PENDING_PAY_KEY = 'tsapay.pendingPayment';

const TTL_MS = 30 * 60 * 1000;

type Pending = { id: string; savedAt: number };

/** Remembers a payment to return to after sign-in. Silently does nothing for
 *  anything that isn't a real payment id. */
export async function rememberPendingPayment(id: string, now: number = Date.now()): Promise<void> {
  if (!isPaymentId(id)) return;
  const pending: Pending = { id, savedAt: now };
  await AsyncStorage.setItem(PENDING_PAY_KEY, JSON.stringify(pending));
}

/**
 * Where to land right after authentication. Consumes the stored pending
 * payment (so a second call never replays it) and only honours it when it is
 * a valid payment id saved less than 30 minutes ago — otherwise home.
 */
export async function postAuthRoute(now: number = Date.now()): Promise<string> {
  const raw = await AsyncStorage.getItem(PENDING_PAY_KEY);
  if (!raw) return '/home';
  await AsyncStorage.removeItem(PENDING_PAY_KEY);

  let pending: Pending;
  try {
    pending = JSON.parse(raw);
  } catch {
    return '/home';
  }
  if (!isPaymentId(pending.id)) return '/home';
  if (now - pending.savedAt >= TTL_MS) return '/home';
  return `/pay?id=${pending.id}`;
}

/** Every post-auth call site (login, cold-start index, lock unlock) hands off
 *  through here instead of hardcoding '/home', so a pending payment always
 *  wins over the default landing screen. */
export async function goAfterAuth(router: { replace(href: string): void }): Promise<void> {
  router.replace(await postAuthRoute());
}
