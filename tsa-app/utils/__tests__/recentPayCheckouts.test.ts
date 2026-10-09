/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * "Waiting for you" on this phone: payments she opened and has not paid, kept
 * until they expire, newest first, at most five — and nothing that isn't a
 * payment id ever comes back out.
 */
jest.mock('@react-native-async-storage/async-storage', () => {
  const store = new Map<string, string>();
  return {
    __esModule: true,
    default: {
      getItem: jest.fn(async (k: string) => store.get(k) ?? null),
      setItem: jest.fn(async (k: string, v: string) => { store.set(k, v); }),
      clear: () => store.clear(),
    },
  };
});

import AsyncStorage from '@react-native-async-storage/async-storage';
import {
  forgetPayCheckout, forgetPayCheckouts, listPayCheckouts, rememberPayCheckout, RECENT_PAY_CHECKOUTS_KEY,
} from '../recentPayCheckouts';

const NOW = Date.parse('2026-10-09T10:00:00Z');
const id = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`;
const pay = (n: number, expiresAt: string | null = '2026-10-16T10:00:00Z') => ({ id: id(n), merchantName: `M${n}`, customerTotal: n * 100, expiresAt });

beforeEach(() => (AsyncStorage as any).clear());

it('keeps the newest first, once each, at most five', async () => {
  for (const n of [1, 2, 3, 4, 5, 6]) await rememberPayCheckout(pay(n), NOW);
  await rememberPayCheckout(pay(3), NOW);
  expect((await listPayCheckouts(NOW)).map((r) => r.merchantName)).toEqual(['M3', 'M6', 'M5', 'M4', 'M2']);
});

it('drops one once it expires, and never keeps one past seven days', async () => {
  await rememberPayCheckout(pay(1, '2026-10-09T11:00:00Z'), NOW);
  await rememberPayCheckout(pay(2, '2027-01-01T00:00:00Z'), NOW);
  expect(await listPayCheckouts(NOW + 2 * 3600_000)).toHaveLength(1);
  expect(await listPayCheckouts(NOW + 8 * 24 * 3600_000)).toHaveLength(0);
});

it('keeps a payment that never expires for seven days', async () => {
  await rememberPayCheckout(pay(1, null), NOW);
  expect((await listPayCheckouts(NOW + 7 * 24 * 3600_000 - 1)).map((r) => r.id)).toEqual([id(1)]);
  expect(await listPayCheckouts(NOW + 7 * 24 * 3600_000)).toHaveLength(0);
});

it('ignores a payment already expired or whose expiry is unknown, and a bad id', async () => {
  await rememberPayCheckout(pay(1, '2026-10-01T00:00:00Z'), NOW);
  await rememberPayCheckout({ ...pay(2), expiresAt: undefined }, NOW);
  await rememberPayCheckout({ ...pay(3), id: '../wallet' }, NOW);
  expect(await listPayCheckouts(NOW)).toEqual([]);
});

it('forgets one', async () => {
  await rememberPayCheckout(pay(1), NOW);
  await rememberPayCheckout(pay(2), NOW);
  await forgetPayCheckout(id(1), NOW);
  expect((await listPayCheckouts(NOW)).map((r) => r.id)).toEqual([id(2)]);
});

it('forgets several in one write', async () => {
  for (const n of [1, 2, 3]) await rememberPayCheckout(pay(n), NOW);
  jest.mocked(AsyncStorage.setItem).mockClear();
  await forgetPayCheckouts([id(1), id(3)], NOW);
  expect(AsyncStorage.setItem).toHaveBeenCalledTimes(1);
  expect((await listPayCheckouts(NOW)).map((r) => r.id)).toEqual([id(2)]);
});

it('reads a tampered or corrupt store as nothing waiting', async () => {
  await AsyncStorage.setItem(RECENT_PAY_CHECKOUTS_KEY, JSON.stringify([{ id: 'javascript:alert(1)', merchantName: 'x', customerTotal: 1, until: NOW + 1 }]));
  expect(await listPayCheckouts(NOW)).toEqual([]);
  await AsyncStorage.setItem(RECENT_PAY_CHECKOUTS_KEY, '{not json');
  expect(await listPayCheckouts(NOW)).toEqual([]);
});
