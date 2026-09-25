// services/payHold.ts
//
// The persisted "don't pay again" hold for a TSA Pay payment.
//
// A crypto payment's swap delivers straight to the MERCHANT, so once it may have
// been broadcast, paying again pays them twice. What keeps that from happening
// after the app is killed is this record — `tsapay.pendingSwap.<paymentId>` —
// written the moment the swap is signed (before it is broadcast), beside the
// swap's hash. It says: a swap for this payment may be out there, and the
// server may not know of it.
//
// A naira payment's direct deposit that she pays gas for herself uses the same
// record, written right before its broadcast (usePayNaira): Paycrest reports a
// deposit only some time after it lands, so without it a resend could pay the
// order twice. `intentId` is then the Paycrest order's id.
//
// It lifts only when the payment is terminal (the server's own answer) or
// HOLD_MAX_MS after the signature — the server gives a payment up after 24 h of
// a swap it cannot resolve, so holding longer would protect nothing.
import AsyncStorage from '@react-native-async-storage/async-storage';

export const HOLD_MAX_MS = 24 * 60 * 60_000;

/** `intentId`: what may be out there — the swap's sell intent, or a direct deposit's Paycrest order. */
export type PayHold = { intentId: string; at: number };

export const payHoldKey = (paymentId: string) => `tsapay.pendingSwap.${paymentId}`;

/**
 * The hold for a payment, or null when there is none.
 * THROWS when storage cannot be read, or holds a value that cannot be: "I could not look" is not "there is
 * nothing" — the caller holds rather than offering Pay again.
 */
export async function readPayHold(paymentId: string): Promise<PayHold | null> {
  const raw = await AsyncStorage.getItem(payHoldKey(paymentId));
  if (!raw) return null;
  // The key exists, so something was recorded: a value that cannot be read is
  // not "no hold" — throw, so the caller holds.
  let v: { intentId?: unknown; at?: unknown } | null;
  try {
    v = JSON.parse(raw);
  } catch {
    throw new Error('The recorded hold is unreadable');
  }
  if (typeof v?.intentId !== 'string' || typeof v?.at !== 'number') throw new Error('The recorded hold is unreadable');
  return { intentId: v.intentId, at: v.at };
}

/**
 * Record a hold. An existing one keeps its original time: the 24 h runs from
 * the signature. THROWS when it cannot be recorded: a swap must not be
 * broadcast without the safeguard that survives an app kill.
 */
export async function writePayHold(paymentId: string, intentId: string, at: number = Date.now()): Promise<void> {
  if (await readPayHold(paymentId)) return;
  await AsyncStorage.setItem(payHoldKey(paymentId), JSON.stringify({ intentId, at } satisfies PayHold));
}

export const clearPayHold = (paymentId: string): Promise<void> =>
  AsyncStorage.removeItem(payHoldKey(paymentId)).catch(() => {});

export const holdActive = (hold: PayHold | null, now: number = Date.now()): boolean =>
  !!hold && now - hold.at <= HOLD_MAX_MS;
