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
import {
  clearHold, clearPayHold, depositHoldKey, HOLD_MAX_MS, holdActive, payHoldKey, readHold, readPayHold, writeHold, writePayHold,
} from '../payHold';

beforeEach(async () => { await AsyncStorage.clear(); });

it('is keyed by the payment', () => {
  expect(payHoldKey('pay-1')).toBe('tsapay.pendingSwap.pay-1');
});

// The storage is shared with a sale's deposit hold (keyed by the Paycrest
// order); a pay hold already on a device must read exactly as before.
describe('one hold store, two keys', () => {
  it('a pay hold recorded before the generalisation still reads, and still under its old key', async () => {
    await AsyncStorage.setItem('tsapay.pendingSwap.pay-1', JSON.stringify({ intentId: 'i1', at: 1000 }));
    expect(await readPayHold('pay-1')).toEqual({ intentId: 'i1', at: 1000 });
    await writePayHold('pay-2', 'i2', 2000);
    expect(JSON.parse((await AsyncStorage.getItem('tsapay.pendingSwap.pay-2'))!)).toEqual({ intentId: 'i2', at: 2000 });
    await clearPayHold('pay-1');
    expect(await AsyncStorage.getItem('tsapay.pendingSwap.pay-1')).toBeNull();
  });

  it("a sale's deposit hold is keyed by its order and never collides with a payment's", async () => {
    expect(depositHoldKey('o1')).toBe('paycrest.sellDeposit.o1');
    await writeHold(depositHoldKey('o1'), 'o1', 1000);
    expect(await readHold(depositHoldKey('o1'))).toEqual({ intentId: 'o1', at: 1000 });
    expect(await readPayHold('o1')).toBeNull();
    await clearHold(depositHoldKey('o1'));
    expect(await readHold(depositHoldKey('o1'))).toBeNull();
  });

  it('the same rules hold for either key: first time kept, unreadable throws', async () => {
    await writeHold(depositHoldKey('o1'), 'o1', 1000);
    await writeHold(depositHoldKey('o1'), 'o1', 9000);
    expect((await readHold(depositHoldKey('o1')))?.at).toBe(1000);
    await AsyncStorage.setItem(depositHoldKey('o2'), 'not json');
    await expect(readHold(depositHoldKey('o2'))).rejects.toThrow(/unreadable/i);
  });
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

it('a value that is PRESENT but corrupt or foreign throws: the key exists, so something was recorded — hold, do not assume none', async () => {
  await AsyncStorage.setItem(payHoldKey('pay-1'), 'not json');
  await expect(readPayHold('pay-1')).rejects.toThrow(/unreadable/i);
  await AsyncStorage.setItem(payHoldKey('pay-1'), JSON.stringify({ intentId: 5 }));
  await expect(readPayHold('pay-1')).rejects.toThrow(/unreadable/i);
});

it('holds for 24 hours from the signature, then lifts', () => {
  expect(HOLD_MAX_MS).toBe(24 * 60 * 60_000);
  const hold = { intentId: 'i1', at: 1_000 };
  expect(holdActive(hold, 1_000 + HOLD_MAX_MS)).toBe(true);
  expect(holdActive(hold, 1_000 + HOLD_MAX_MS + 1)).toBe(false);
  expect(holdActive(null, 5)).toBe(false);
});

describe('storage that fails is not "no hold"', () => {
  // Patch the AsyncStorage mock's methods and put them back (jest's restoreAllMocks/mockRestore would also
  // reset the mock's own implementations, breaking every later test).
  const undo: Array<() => void> = [];
  const patch = (name: 'getItem' | 'setItem', impl: (...a: any[]) => Promise<any>) => {
    const original = AsyncStorage[name];
    (AsyncStorage as any)[name] = impl;
    undo.push(() => { (AsyncStorage as any)[name] = original; });
  };
  afterEach(() => { undo.splice(0).forEach((u) => u()); });

  it('a read that fails throws — the caller must hold, not assume nothing is out there', async () => {
    patch('getItem', async () => { throw new Error('disk full'); });
    await expect(readPayHold('pay-1')).rejects.toThrow('disk full');
  });

  it('a write that fails throws — a hold that cannot be recorded must stop the swap', async () => {
    patch('setItem', async () => { throw new Error('disk full'); });
    await expect(writePayHold('pay-1', 'i1')).rejects.toThrow('disk full');
  });

  it('an absent value is still just no hold', async () => {
    await expect(readPayHold('pay-1')).resolves.toBeNull();
  });
});
