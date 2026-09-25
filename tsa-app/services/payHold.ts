// services/payHold.ts
//
// The persisted "don't pay again" hold: one AsyncStorage record saying "money
// for this may be out there, and the server may not know of it yet". Two kinds
// share this one implementation and differ only in their key:
//
// - A TSA Pay payment's hold, `tsapay.pendingSwap.<paymentId>` (payHoldKey).
//   A crypto payment's swap delivers straight to the MERCHANT, so once it may
//   have been broadcast, paying again pays them twice. It is written the moment
//   the swap is signed (before it is broadcast), beside the swap's hash. A naira
//   payment's direct deposit that she pays gas for herself uses the same record,
//   written right before its broadcast (usePayNaira): `intentId` is then the
//   Paycrest order's id.
// - A sale's deposit hold, `paycrest.sellDeposit.<orderId>` (depositHoldKey).
//   A sale has no payment id, so its self-paid deposit is held under the
//   Paycrest order itself (sell screen, useSwappedSell); `intentId` is that
//   same order id.
//
// Either way, Paycrest reports a deposit only some time after it lands, so
// without the record a resend could pay an order twice. services/depositGuard
// writes it around the send and reads it before any resend.
//
// It lifts when what it covers is terminal (the server's own answer) or
// HOLD_MAX_MS after it was written — the server gives a payment up after 24 h of
// a swap it cannot resolve, and a Paycrest order has long closed by then, so
// holding longer would protect nothing.
import AsyncStorage from '@react-native-async-storage/async-storage';

export const HOLD_MAX_MS = 24 * 60 * 60_000;

/** `intentId`: what may be out there — the swap's sell intent, or a direct deposit's Paycrest order. */
export type PayHold = { intentId: string; at: number };

export const payHoldKey = (paymentId: string) => `tsapay.pendingSwap.${paymentId}`;

/** A sale's deposit hold: a sale has no payment id, so its Paycrest order keys it. */
export const depositHoldKey = (orderId: string) => `paycrest.sellDeposit.${orderId}`;

/**
 * The hold stored under `key`, or null when there is none.
 * THROWS when storage cannot be read, or holds a value that cannot be: "I could not look" is not "there is
 * nothing" — the caller holds rather than offering to pay again.
 */
export async function readHold(key: string): Promise<PayHold | null> {
  const raw = await AsyncStorage.getItem(key);
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
 * Record a hold under `key`. An existing one keeps its original time: the 24 h
 * runs from the first signature. THROWS when it cannot be recorded: money must
 * not be broadcast without the safeguard that survives an app kill.
 */
export async function writeHold(key: string, intentId: string, at: number = Date.now()): Promise<void> {
  if (await readHold(key)) return;
  await AsyncStorage.setItem(key, JSON.stringify({ intentId, at } satisfies PayHold));
}

export const clearHold = (key: string): Promise<void> => AsyncStorage.removeItem(key).catch(() => {});

/** A TSA Pay payment's hold, or null (see readHold — THROWS when it cannot be read). */
export const readPayHold = (paymentId: string): Promise<PayHold | null> => readHold(payHoldKey(paymentId));

/** Record a TSA Pay payment's hold (see writeHold — THROWS when it cannot be recorded). */
export const writePayHold = (paymentId: string, intentId: string, at?: number): Promise<void> =>
  writeHold(payHoldKey(paymentId), intentId, at);

export const clearPayHold = (paymentId: string): Promise<void> => clearHold(payHoldKey(paymentId));

export const holdActive = (hold: PayHold | null, now: number = Date.now()): boolean =>
  !!hold && now - hold.at <= HOLD_MAX_MS;
