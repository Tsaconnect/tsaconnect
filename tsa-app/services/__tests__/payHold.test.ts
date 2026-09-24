/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * The persisted "don't pay again" hold for a crypto TSA Pay payment: a swap
 * that may have reached the merchant, unknown to the server. It outlives an app
 * kill, and lifts only when the payment is terminal or 24 h have passed.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import AsyncStorage from '@react-native-async-storage/async-storage';
import { clearPayHold, HOLD_MAX_MS, holdActive, payHoldKey, readPayHold, writePayHold } from '../payHold';

beforeEach(async () => { await AsyncStorage.clear(); });

it('is keyed by the payment', () => {
  expect(payHoldKey('pay-1')).toBe('tsapay.pendingSwap.pay-1');
});

it('writes and reads back {intentId, at}', async () => {
  await writePayHold('pay-1', 'i1', 1000);
  expect(await readPayHold('pay-1')).toEqual({ intentId: 'i1', at: 1000 });
  expect(await readPayHold('pay-2')).toBeNull();
});

it('a second write for the same payment keeps the ORIGINAL time — the 24 h runs from the signature', async () => {
  await writePayHold('pay-1', 'i1', 1000);
  await writePayHold('pay-1', 'i1', 9000);
  expect((await readPayHold('pay-1'))?.at).toBe(1000);
});

it('clears', async () => {
  await writePayHold('pay-1', 'i1', 1000);
  await clearPayHold('pay-1');
  expect(await readPayHold('pay-1')).toBeNull();
});

it('reads a corrupt or foreign value as no hold, never throwing', async () => {
  await AsyncStorage.setItem(payHoldKey('pay-1'), 'not json');
  expect(await readPayHold('pay-1')).toBeNull();
  await AsyncStorage.setItem(payHoldKey('pay-1'), JSON.stringify({ intentId: 5 }));
  expect(await readPayHold('pay-1')).toBeNull();
});

it('holds for 24 hours from the signature, then lifts', () => {
  expect(HOLD_MAX_MS).toBe(24 * 60 * 60_000);
  const hold = { intentId: 'i1', at: 1_000 };
  expect(holdActive(hold, 1_000 + HOLD_MAX_MS)).toBe(true);
  expect(holdActive(hold, 1_000 + HOLD_MAX_MS + 1)).toBe(false);
  expect(holdActive(null, 5)).toBe(false);
});
