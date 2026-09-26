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
  koboFromNairaInput, nairaFieldText, openLinkAmountRule, openLinkNotTakingWhy, openLinkRangeFrom, openLinkReceived,
  openLinkShareMessage, openLinkStatus, openPaymentPurpose, payLinkCounts, payLinkReceives, payLinkShareMessage, payLinkStatus, payLinkTimeRow,
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

it('says what a reusable link takes, a 0 bound being the merchant\'s own', () => {
  expect(openLinkAmountRule({ minAmount: 0, maxAmount: 0 })).toBe('Any amount');
  expect(openLinkAmountRule({ minAmount: 100_000, maxAmount: 0 })).toBe('Any amount from ₦1,000.00');
  expect(openLinkAmountRule({ minAmount: 0, maxAmount: 500_000 })).toBe('Any amount up to ₦5,000.00');
  expect(openLinkAmountRule({ minAmount: 100_000, maxAmount: 500_000 })).toBe('Any amount from ₦1,000.00 to ₦5,000.00');
  // With the merchant's bounds on the link, a 0 reads as that bound.
  expect(openLinkAmountRule({ minAmount: 0, maxAmount: 500_000, merchantMinAmount: 10_000, merchantMaxAmount: 9_000_000 }))
    .toBe('Any amount from ₦100.00 to ₦5,000.00');
  expect(openLinkAmountRule({ minAmount: 0, maxAmount: 0, merchantMinAmount: 10_000, merchantMaxAmount: 9_000_000 }))
    .toBe('Any amount from ₦100.00 to ₦90,000.00');
});

it('a reusable link is active, paused by her, or not taking payments for a reason she is told', () => {
  expect(openLinkStatus({ active: true, takingPayments: true })).toBe('active');
  expect(openLinkStatus({ active: false, takingPayments: false, notTakingReason: 'paused' })).toBe('paused');
  expect(openLinkStatus({ active: true, takingPayments: false, notTakingReason: 'suspended' })).toBe('not_taking');
  // An older server sends no takingPayments: her switch decides.
  expect(openLinkStatus({ active: false } as never)).toBe('paused');
  expect(openLinkNotTakingWhy({ active: true, takingPayments: true })).toBeUndefined();
  expect(openLinkNotTakingWhy({ active: true, takingPayments: false, notTakingReason: 'no_payout' }))
    .toBe("Set where you're paid in Payment Settings before customers can pay.");
  expect(openLinkNotTakingWhy({ active: true, takingPayments: false, notTakingReason: 'range_outside_limits' }))
    .toBe('Your amount range no longer fits your limits — edit the link.');
  expect(openLinkReceived({ receivedTotal: 980_000, paidCount: 1 })).toBe('You received ₦9,800.00 from 1 payment');
});

it('reads a reusable link\'s range: blank is 0, bad text and a low above a high are refused', () => {
  expect(openLinkRangeFrom('', '')).toEqual({ range: { minAmount: 0, maxAmount: 0 } });
  expect(openLinkRangeFrom('1000', '5000.50')).toEqual({ range: { minAmount: 100_000, maxAmount: 500_050 } });
  expect(openLinkRangeFrom('1,000', '')).toEqual({ errors: { min: expect.stringContaining('Enter naira') } });
  expect(openLinkRangeFrom('5000', '1000')).toEqual({ errors: { max: "The highest amount can't be less than the lowest." } });
});

it('puts kobo back into an amount field, and a 0 bound as blank', () => {
  expect(nairaFieldText(150_050)).toBe('1500.50');
  expect(nairaFieldText(12_345_600)).toBe('123456.00');
  expect(nairaFieldText(0)).toBe('');
});

it('shares a reusable link with the web app\'s words', () => {
  expect(openLinkShareMessage('Sky Air', { label: 'Gate 3', url: 'https://x/pay/l/sky' }))
    .toBe('Pay Sky Air — Gate 3: https://x/pay/l/sky');
});

it('says what an open payment was for: the link\'s name, or the customer\'s note when she wrote one', () => {
  expect(openPaymentPurpose({ description: '', linkLabel: 'Gate 1' }, 'Shop')).toEqual({ title: 'Gate 1', note: false });
  expect(openPaymentPurpose({ description: '  ' }, 'Shop')).toEqual({ title: 'Shop', note: false });
  expect(openPaymentPurpose({ description: 'Two plates', linkLabel: 'Gate 1' }, 'Shop')).toEqual({ title: 'Two plates', note: true });
});
