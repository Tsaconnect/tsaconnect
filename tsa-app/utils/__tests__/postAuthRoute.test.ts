jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
/* eslint-disable import/first */
import AsyncStorage from '@react-native-async-storage/async-storage';
import {
  forgetPendingPayment, goAfterAuth, hasPendingPayment, postAuthRoute, rememberPendingPayment,
} from '../postAuthRoute';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const T0 = Date.parse('2026-09-23T12:00:00Z');

beforeEach(() => AsyncStorage.clear());

it('goes home when nothing is pending', async () => {
  expect(await postAuthRoute(T0)).toBe('/home');
});

it('returns to the pending payment once, then forgets it', async () => {
  await rememberPendingPayment(ID, { now: T0 });
  expect(await postAuthRoute(T0 + 60_000)).toBe(`/pay?id=${ID}`);
  expect(await postAuthRoute(T0 + 60_000)).toBe('/home');
});

it('with no known expiry, forgets a pending payment after 30 minutes', async () => {
  await rememberPendingPayment(ID, { now: T0 });
  expect(await postAuthRoute(T0 + 31 * 60_000)).toBe('/home');
});

it('never stores or follows something that is not a payment id', async () => {
  await rememberPendingPayment('../wallet/send?to=0xdead', { now: T0 });
  expect(await postAuthRoute(T0)).toBe('/home');
  await AsyncStorage.setItem('tsapay.pendingPayment', JSON.stringify({ id: '../x', savedAt: T0 }));
  expect(await postAuthRoute(T0)).toBe('/home');
});

it('login, index and lock hand off through goAfterAuth', async () => {
  await rememberPendingPayment(ID);
  const router = { replace: jest.fn() };
  await goAfterAuth(router);
  expect(router.replace).toHaveBeenCalledWith(`/pay?id=${ID}`);
});

it('lands on home, never throws, when the stored record is unreadable or mangled', async () => {
  for (const raw of ['null', '42', '"a string"', '{not json', JSON.stringify({ id: ID, savedAt: 'soon' })]) {
    await AsyncStorage.setItem('tsapay.pendingPayment', raw);
    expect(await postAuthRoute(T0)).toBe('/home');
  }
  // Once only: getItem is the storage mock's own jest.fn, and restoring a spy on it would strip its implementation.
  jest.spyOn(AsyncStorage, 'getItem').mockRejectedValueOnce(new Error('storage unavailable'));
  expect(await postAuthRoute(T0)).toBe('/home');
});

const DAY = 24 * 60 * 60_000;

it('remembers a payment until its own expiry — through signup, email and KYC', async () => {
  await rememberPendingPayment(ID, { expiresAt: new Date(T0 + 3 * DAY).toISOString(), now: T0 });
  expect(await hasPendingPayment(T0 + 2 * DAY)).toBe(true);
  expect(await postAuthRoute(T0 + 2 * DAY)).toBe(`/pay?id=${ID}`);

  await rememberPendingPayment(ID, { expiresAt: new Date(T0 + 3 * DAY).toISOString(), now: T0 });
  expect(await postAuthRoute(T0 + 3 * DAY)).toBe('/home');
});

it('never remembers one longer than 7 days, whatever its expiry says', async () => {
  await rememberPendingPayment(ID, { expiresAt: new Date(T0 + 30 * DAY).toISOString(), now: T0 });
  expect(await hasPendingPayment(T0 + 7 * DAY - 1)).toBe(true);
  expect(await hasPendingPayment(T0 + 7 * DAY)).toBe(false);
});

it('a payment that never expires is remembered for the full 7 days', async () => {
  await rememberPendingPayment(ID, { expiresAt: null, now: T0 });
  expect(await hasPendingPayment(T0 + 6 * DAY)).toBe(true);
  expect(await hasPendingPayment(T0 + 7 * DAY - 1)).toBe(true);
  expect(await hasPendingPayment(T0 + 7 * DAY)).toBe(false);
});

it('an expiry that does not read as a time is unknown: 30 minutes', async () => {
  await rememberPendingPayment(ID, { expiresAt: 'soon', now: T0 });
  expect(await hasPendingPayment(T0 + 29 * 60_000)).toBe(true);
  expect(await hasPendingPayment(T0 + 31 * 60_000)).toBe(false);
});

it('an older record with no window still gets its 30 minutes', async () => {
  await AsyncStorage.setItem('tsapay.pendingPayment', JSON.stringify({ id: ID, savedAt: T0 }));
  expect(await hasPendingPayment(T0 + 29 * 60_000)).toBe(true);
  expect(await postAuthRoute(T0 + 31 * 60_000)).toBe('/home');
});

it('peeking does not consume it; replaying does', async () => {
  await rememberPendingPayment(ID, { now: T0 });
  expect(await hasPendingPayment(T0)).toBe(true);
  expect(await hasPendingPayment(T0)).toBe(true);
  expect(await postAuthRoute(T0)).toBe(`/pay?id=${ID}`);
  expect(await hasPendingPayment(T0)).toBe(false);
});

it('forgets only the payment that is over', async () => {
  const OTHER = '4a2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
  await rememberPendingPayment(ID, { now: T0 });
  await forgetPendingPayment(OTHER);
  expect(await hasPendingPayment(T0)).toBe(true);
  await forgetPendingPayment(ID);
  expect(await hasPendingPayment(T0)).toBe(false);
});

it('never throws when storage refuses to save: whatever navigates next still does', async () => {
  jest.spyOn(AsyncStorage, 'setItem').mockRejectedValueOnce(new Error('storage full'));
  await expect(rememberPendingPayment(ID, { now: T0 })).resolves.toBeUndefined();
  expect(await postAuthRoute(T0)).toBe('/home');
});

it('touches nothing when nothing is stored', async () => {
  const remove = jest.spyOn(AsyncStorage, 'removeItem');
  remove.mockClear();
  expect(await postAuthRoute(T0)).toBe('/home');
  expect(remove).not.toHaveBeenCalled();
});
