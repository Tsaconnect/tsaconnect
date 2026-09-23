import { payScreenState } from '../payCheckout';
import type { PayCheckout } from '@/services/payApi';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const base: PayCheckout = { id: ID, merchantName: 'Sky Air', mode: 'test', status: 'pending', amount: 5_000_000,
  customerTotal: 5_050_506, fee: 50_506, merchantNet: 5_000_000, currency: 'NGN', description: '', expiresAt: '2026-09-23T12:30:00Z',
  settlementMode: 'fiat', payable: true, youArePaying: false };
const now = Date.parse('2026-09-23T12:00:00Z');

it('rejects a link whose id is not a payment id', () => {
  expect(payScreenState({ id: '../paycrest/orders', authed: true, now }).kind).toBe('invalid');
});
it('asks a signed-out customer to sign in first', () => {
  expect(payScreenState({ id: ID, authed: false, now }).kind).toBe('signin');
});
it('is payable with time to spare', () => {
  expect(payScreenState({ id: ID, authed: true, checkout: base, now }).kind).toBe('payable');
});
it('blocks with under five minutes left, allows exactly five', () => {
  expect(payScreenState({ id: ID, authed: true, checkout: base, now: Date.parse('2026-09-23T12:25:01Z') }).kind).toBe('blocked');
  expect(payScreenState({ id: ID, authed: true, checkout: base, now: Date.parse('2026-09-23T12:25:00Z') }).kind).toBe('payable');
});
it('blocks with the server reason when not payable', () => {
  const s = payScreenState({ id: ID, authed: true, checkout: { ...base, status: 'processing', payable: false, notPayableWhy: 'Someone else is completing this payment' }, now });
  expect(s).toEqual(expect.objectContaining({ kind: 'blocked', why: 'Someone else is completing this payment' }));
});
it('waits while its own payment is processing, and finishes on a terminal status', () => {
  expect(payScreenState({ id: ID, authed: true, checkout: { ...base, status: 'processing', payable: false, youArePaying: true }, now }).kind).toBe('waiting');
  for (const status of ['succeeded', 'failed', 'expired'] as const) {
    expect(payScreenState({ id: ID, authed: true, checkout: { ...base, status, payable: false }, now }).kind).toBe('done');
  }
});
