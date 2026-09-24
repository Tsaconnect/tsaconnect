// services/payHold.ts
//
// The persisted "don't pay again" hold for a crypto-settling TSA Pay payment.
//
// A crypto payment's swap delivers straight to the MERCHANT, so once it may have
// been broadcast, paying again pays them twice. What keeps that from happening
// after the app is killed is this record — `tsapay.pendingSwap.<paymentId>` —
// written the moment the swap is signed (before it is broadcast), beside the
// swap's hash. It says: a swap for this payment may be out there, and the
// server may not know of it.
//
// It lifts only when the payment is terminal (the server's own answer) or
// HOLD_MAX_MS after the signature — the server gives a payment up after 24 h of
// a swap it cannot resolve, so holding longer would protect nothing.
import AsyncStorage from '@react-native-async-storage/async-storage';

export const HOLD_MAX_MS = 24 * 60 * 60_000;

export type PayHold = { intentId: string; at: number };

export const payHoldKey = (paymentId: string) => `tsapay.pendingSwap.${paymentId}`;

/** The hold for a payment, or null — never throws: a corrupt value is no hold. */
export async function readPayHold(paymentId: string): Promise<PayHold | null> {
  try {
    const raw = await AsyncStorage.getItem(payHoldKey(paymentId));
    if (!raw) return null;
    const v = JSON.parse(raw);
    return typeof v?.intentId === 'string' && typeof v?.at === 'number' ? { intentId: v.intentId, at: v.at } : null;
  } catch {
    return null;
  }
}

/** Record a hold. An existing one keeps its original time: the 24 h runs from the signature. */
export async function writePayHold(paymentId: string, intentId: string, at: number = Date.now()): Promise<void> {
  if (await readPayHold(paymentId)) return;
  await AsyncStorage.setItem(payHoldKey(paymentId), JSON.stringify({ intentId, at } satisfies PayHold))
    .catch((e) => console.warn('[pay] could not persist the hold', e));
}

export const clearPayHold = (paymentId: string): Promise<void> =>
  AsyncStorage.removeItem(payHoldKey(paymentId)).catch(() => {});

export const holdActive = (hold: PayHold | null, now: number = Date.now()): boolean =>
  !!hold && now - hold.at <= HOLD_MAX_MS;
