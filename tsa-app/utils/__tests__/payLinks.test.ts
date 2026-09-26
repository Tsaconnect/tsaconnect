/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

// utils/__tests__/payLinks.test.ts
//
// The pure pieces of a payment link: the amount she types becomes whole kobo
// (or nothing), a cancelled link reads cancelled, and the headline counts only
// what is still waiting and what was paid today.
//
// stableSellQuote (formatKobo) reaches walletApi's AsyncStorage import, hence the mock.
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import type { PayLink } from '@/services/payMerchantApi';
import {
  koboFromNairaInput, payLinkCounts, payLinkReceives, payLinkShareMessage, payLinkStatus, payLinkTimeRow,
} from '../payLinks';

it('reads the amount field as whole kobo', () => {
  expect(koboFromNairaInput('5000')).toBe(500_000);
  expect(koboFromNairaInput('1234.5')).toBe(123_450);
  expect(koboFromNairaInput('0.01')).toBe(1);
  expect(koboFromNairaInput('')).toBeNull();
  expect(koboFromNairaInput('0')).toBeNull();
  expect(koboFromNairaInput('.')).toBeNull();
  expect(koboFromNairaInput('abc')).toBeNull();
});

it('refuses what it would have to guess: a comma, a third decimal, an exponent', () => {
  expect(koboFromNairaInput('5000,50')).toBeNull();
  expect(koboFromNairaInput('5,000')).toBeNull();
  expect(koboFromNairaInput('12.345')).toBeNull();
  expect(koboFromNairaInput('1e3')).toBeNull();
  expect(koboFromNairaInput('5000.')).toBeNull();
});

it('names one time per state, and shows what she receives only while it can come', () => {
  const base = { expiresAt: 'E', succeededAt: 'S' };
  expect(payLinkTimeRow({ ...base, status: 'pending' })).toEqual({ label: 'Open until', at: 'E' });
  expect(payLinkTimeRow({ ...base, status: 'expired' })).toEqual({ label: 'Expired', at: 'E' });
  expect(payLinkTimeRow({ ...base, status: 'succeeded' })).toEqual({ label: 'Paid', at: 'S' });
  expect(payLinkTimeRow({ ...base, status: 'processing' })).toBeNull();
  expect(payLinkTimeRow({ ...base, status: 'failed' })).toBeNull();
  expect(payLinkTimeRow({ ...base, status: 'expired', cancelled: true })).toBeNull();

  expect(payLinkReceives({ status: 'pending' })).toBe(true);
  expect(payLinkReceives({ status: 'succeeded' })).toBe(true);
  expect(payLinkReceives({ status: 'expired' })).toBe(false);
  expect(payLinkReceives({ status: 'expired', cancelled: true })).toBe(false);
  expect(payLinkReceives({ status: 'failed' })).toBe(false);
});

it('a cancelled link reads cancelled; any other keeps its status', () => {
  expect(payLinkStatus({ status: 'expired', cancelled: true })).toBe('cancelled');
  expect(payLinkStatus({ status: 'expired' })).toBe('expired');
  expect(payLinkStatus({ status: 'succeeded' })).toBe('succeeded');
});

it('shares the web app\'s words, quoting what the customer will be charged', () => {
  expect(payLinkShareMessage('Sky Air', { customerTotal: 151_500, description: 'a haircut', checkoutUrl: 'https://x/pay?id=1' }))
    .toBe('Sky Air is requesting ₦1,515.00 for a haircut. Pay here: https://x/pay?id=1');
});

it('counts links still waiting and links paid today', () => {
  const now = new Date('2026-09-26T15:00:00');
  const l = (over: Partial<PayLink>) => ({ status: 'pending', ...over }) as PayLink;
  expect(payLinkCounts([
    l({}),
    l({ status: 'pending', cancelled: false }),
    l({ status: 'succeeded', succeededAt: new Date('2026-09-26T09:00:00').toISOString() }),
    l({ status: 'succeeded', succeededAt: new Date('2026-09-25T09:00:00').toISOString() }),
    l({ status: 'expired', cancelled: true }),
  ], now)).toEqual({ awaiting: 2, paidToday: 1 });
});
