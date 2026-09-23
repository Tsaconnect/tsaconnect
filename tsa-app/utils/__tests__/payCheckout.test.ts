import {
  errorCode, errorMessage, nairaFromKobo, payAmountLabel, payScreenState, shortfallCopy, unsupportedNetworkMessage,
} from '../payCheckout';
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

describe('nairaFromKobo', () => {
  it('writes an integer kobo amount as the decimal naira string the rate endpoints take', () => {
    expect(nairaFromKobo(5_000_000)).toBe('50000.00');
    expect(nairaFromKobo(4_950_000)).toBe('49500.00');
    expect(nairaFromKobo(100)).toBe('1.00');
    expect(nairaFromKobo(5)).toBe('0.05');
    expect(nairaFromKobo(0)).toBe('0.00');
  });
  it('refuses anything that is not a non-negative whole number of kobo', () => {
    expect(() => nairaFromKobo(1.5)).toThrow(RangeError);
    expect(() => nairaFromKobo(-1)).toThrow(RangeError);
    expect(() => nairaFromKobo(NaN)).toThrow(RangeError);
  });
});

describe('payAmountLabel', () => {
  it('shows what the payer sends, in the token’s own decimals', () => {
    expect(payAmountLabel('84000000000000000', 18, 'BNB')).toBe('0.084 BNB');
    expect(payAmountLabel('33835000', 6, 'USDC')).toBe('33.835 USDC');
    expect(payAmountLabel('50000000', 6, 'USDT')).toBe('50 USDT');
  });
  it('never understates the amount: a sliver past the last shown digit rounds up', () => {
    expect(payAmountLabel('1', 18, 'BNB')).toBe('0.000001 BNB');
    expect(payAmountLabel('1000000000000001', 18, 'ETH')).toBe('0.001001 ETH');
    expect(payAmountLabel('999999999999999999', 18, 'ETH')).toBe('1 ETH');
  });
  it('is empty for a figure it cannot read, rather than a wrong one', () => {
    expect(payAmountLabel('', 18, 'BNB')).toBe('');
    expect(payAmountLabel('1.5', 18, 'BNB')).toBe('');
    expect(payAmountLabel('-3', 18, 'BNB')).toBe('');
  });
});

describe('shortfallCopy', () => {
  it('says how far short in USDC and what tapping the button does', () => {
    expect(shortfallCopy('0.42')).toBe(
      'The rate moved and your swap landed about 0.42 USDC short. Pay the rest from the USDC in your wallet?',
    );
  });
  it('still asks the question when the server sent no figure', () => {
    expect(shortfallCopy(undefined)).toBe(
      'The rate moved and your swap landed short. Pay the rest from the USDC in your wallet?',
    );
  });
});

describe('error helpers shared by the live payment hooks', () => {
  it('errorCode reads a server machine code off any error, and nothing else', () => {
    expect(errorCode(Object.assign(new Error('x'), { code: 'pay_in_progress' }))).toBe('pay_in_progress');
    expect(errorCode(new Error('x'))).toBeUndefined();
    expect(errorCode(Object.assign(new Error('x'), { code: 7 }))).toBeUndefined();
    expect(errorCode('boom')).toBeUndefined();
    expect(errorCode(null)).toBeUndefined();
  });
  it('errorMessage shows the error’s own words, else the fallback', () => {
    expect(errorMessage(new Error('Verify your identity first'), 'fallback')).toBe('Verify your identity first');
    expect(errorMessage(new Error(''), 'fallback')).toBe('fallback');
    expect(errorMessage('boom', 'fallback')).toBe('fallback');
  });
  it('unsupportedNetworkMessage names the coin', () => {
    expect(unsupportedNetworkMessage('SOL')).toBe(
      "Paying with SOL from that network isn't supported yet. Pick a coin on another network.",
    );
  });
});
