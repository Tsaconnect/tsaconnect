jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
/* eslint-disable import/first */
import AsyncStorage from '@react-native-async-storage/async-storage';
import { goAfterAuth, postAuthRoute, rememberPendingPayment } from '../postAuthRoute';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const T0 = Date.parse('2026-09-23T12:00:00Z');

beforeEach(() => AsyncStorage.clear());

it('goes home when nothing is pending', async () => {
  expect(await postAuthRoute(T0)).toBe('/home');
});

it('returns to the pending payment once, then forgets it', async () => {
  await rememberPendingPayment(ID, T0);
  expect(await postAuthRoute(T0 + 60_000)).toBe(`/pay?id=${ID}`);
  expect(await postAuthRoute(T0 + 60_000)).toBe('/home');
});

it('forgets a pending payment after 30 minutes', async () => {
  await rememberPendingPayment(ID, T0);
  expect(await postAuthRoute(T0 + 31 * 60_000)).toBe('/home');
});

it('never stores or follows something that is not a payment id', async () => {
  await rememberPendingPayment('../wallet/send?to=0xdead', T0);
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
