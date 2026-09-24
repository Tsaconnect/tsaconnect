import {
  directPriceMovedCopy, BROADCAST_UNCONFIRMED, KEEP_CHECKING, nothingToContinueCopy, heldNotSentCopy, NOT_CONFIRMED, PAYMENT_STILL_GOING, RESERVE_WINDOW,
  classifyStartError, estimateError, exceedsPriced, PRICE_TOLERANCE_BPS, priceMovedMessage, startFailure, unconfirmedCopy,
  errorCode, errorMessage, intentForPayment, isRouteRefused, PAY_ATTEMPT_UNCERTAIN, PAYMENT_PAUSED, CANT_PAY_WITH_TOKEN, nairaFromKobo, payAmountLabel, payScreenState, receiveAmountLabel,
  shortfallCopy, unsupportedNetworkMessage,
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

describe('intentForPayment', () => {
  const sale = { id: 'i1', status: 'swapping', payPaymentId: ID } as never;
  it('is the open sale only when it pays THIS payment', () => {
    expect(intentForPayment(sale, ID)).toBe(sale);
  });
  it('is none for an unrelated sale of hers, one with no payment, or no sale at all', () => {
    expect(intentForPayment({ id: 'i2', status: 'swapping', payPaymentId: 'other' } as never, ID)).toBeNull();
    expect(intentForPayment({ id: 'i3', status: 'swapping' } as never, ID)).toBeNull();
    expect(intentForPayment(null, ID)).toBeNull();
    expect(intentForPayment(undefined, ID)).toBeNull();
  });
});

describe('an attempt under way is never blocked by the expiry warning', () => {
  const nearlyOut = Date.parse('2026-09-23T12:26:00Z'); // 4 minutes left
  it('blocks a payer who has not started', () => {
    expect(payScreenState({ id: ID, authed: true, checkout: base, now: nearlyOut }).kind).toBe('blocked');
  });
  it('keeps the form up while money is moving, so progress and the shortfall prompt survive', () => {
    expect(payScreenState({ id: ID, authed: true, checkout: base, now: nearlyOut, attemptActive: true }).kind).toBe('payable');
  });
  it('still blocks on the server’s own word that it is not payable', () => {
    const s = payScreenState({
      id: ID, authed: true, checkout: { ...base, payable: false, notPayableWhy: 'Someone else is completing this payment' },
      now: nearlyOut, attemptActive: true,
    });
    expect(s.kind).toBe('blocked');
  });
});

describe('receiveAmountLabel', () => {
  it('shows what the merchant receives, cut (never rounded up) to the cent', () => {
    expect(receiveAmountLabel('33323336332433603252', 18, 'USDT')).toBe('33.32 USDT');
    expect(receiveAmountLabel('33999999', 6, 'USDC')).toBe('33.99 USDC');
    expect(receiveAmountLabel('50000000', 6, 'USDT')).toBe('50.00 USDT');
  });
  it('keeps small amounts readable', () => {
    expect(receiveAmountLabel('1234567890000000', 18, 'ETH')).toBe('0.001234 ETH');
  });
  it('is empty for a figure it cannot read', () => {
    expect(receiveAmountLabel('', 18, 'USDT')).toBe('');
    expect(receiveAmountLabel('1.5', 6, 'USDT')).toBe('');
  });
});

describe('shortfallCopy after her wallet USDC was already tried', () => {
  it('says her wallet is not enough, instead of repeating the question', () => {
    expect(shortfallCopy('0.10', true)).toBe(
      "Your USDC on Base isn't enough to cover the rest — about 0.10 USDC more is needed. Add USDC to your wallet, then tap Use my USDC again.",
    );
    expect(shortfallCopy(undefined, true)).toMatch(/isn't enough/);
  });
});

describe('what the server’s start-route refusals mean to the payer', () => {
  it('pay_attempt_uncertain is a machine code, and its copy says the payment is paused and not to pay again', () => {
    expect(PAY_ATTEMPT_UNCERTAIN).toBe('pay_attempt_uncertain');
    expect(PAYMENT_PAUSED).toMatch(/paused/i);
    expect(PAYMENT_PAUSED).toMatch(/don't pay again/i);
  });
  it('a 422 or 503 from the route is a refusal to price this token — by status, never by its text', () => {
    expect(isRouteRefused(Object.assign(new Error('anything at all'), { status: 422 }))).toBe(true);
    expect(isRouteRefused(Object.assign(new Error('x'), { status: 503 }))).toBe(true);
    expect(isRouteRefused(Object.assign(new Error('x'), { status: 500 }))).toBe(false);
    expect(isRouteRefused(Object.assign(new Error('x'), { status: 409 }))).toBe(false);
    expect(isRouteRefused(new Error('x'))).toBe(false);
    expect(isRouteRefused(null)).toBe(false);
  });
  it('its copy points at another token or later', () => {
    expect(CANT_PAY_WITH_TOKEN).toMatch(/another token/i);
  });
});

describe('the bound between the price she was shown and the amount signed', () => {
  it('tolerates 2% over the priced amount, and no more', () => {
    expect(PRICE_TOLERANCE_BPS).toBe(200);
    expect(exceedsPriced(1_000_000n, 1_000_000n)).toBe(false);
    expect(exceedsPriced(900_000n, 1_000_000n)).toBe(false); // cheaper is never a refusal
    expect(exceedsPriced(1_020_000n, 1_000_000n)).toBe(false); // exactly 2%
    expect(exceedsPriced(1_020_001n, 1_000_000n)).toBe(true);
  });
  it('is exact for 18-decimal amounts far past a float', () => {
    const priced = 84_000_000_000_000_000n;
    expect(exceedsPriced(priced + priced / 50n, priced)).toBe(false);
    expect(exceedsPriced(priced + priced / 50n + 1n, priced)).toBe(true);
  });
  it('the refusal names both amounts, says nothing was sent, and says what to do', () => {
    const m = priceMovedMessage('90720000000000000', '84000000000000000', 18, 'BNB');
    expect(m).toContain('0.09072 BNB');
    expect(m).toContain('0.084 BNB');
    expect(m).toMatch(/nothing was sent/i);
    expect(m).toMatch(/try again/i);
  });
});

describe('classifyStartError — one reading of a start route’s answer for both hooks', () => {
  const err = (status: number, code?: string) => Object.assign(new Error('server words'), { status, code });
  it.each([
    [err(409, 'pay_in_progress'), 'follow'],
    [err(409, 'pay_attempt_uncertain'), 'paused'],
    [err(422), 'refused'],
    [err(503), 'refused'],
    [err(500), 'other'],
    [err(403, 'kyc_verification_required'), 'other'],
    // No answer at all: the server may have opened the attempt before the reply was lost.
    [err(0, 'transport'), 'follow'],
    [new Error('offline'), 'other'],
  ])('%#: %s', (e, kind) => {
    expect(classifyStartError(e)).toBe(kind);
  });

  it('startFailure gives each kind its state: paused is held, refused is retryable, other keeps the words and code', () => {
    expect(startFailure(err(409, 'pay_attempt_uncertain'))).toMatchObject({ retryable: false, errorCode: 'pay_attempt_uncertain' });
    expect(startFailure(err(409, 'pay_attempt_uncertain')).error).toMatch(/paused/i);
    expect(startFailure(err(422))).toMatchObject({ retryable: true, error: expect.stringMatching(/another token/i) });
    expect(startFailure(err(403, 'kyc_verification_required'))).toEqual({
      error: 'server words', errorCode: 'kyc_verification_required', retryable: true,
    });
    expect(startFailure(Object.assign(new Error(''), { status: 500 })).error).toMatch(/try again/i);
  });

  it('estimateError maps a refusal to the same copy and passes anything else through', () => {
    expect(estimateError(err(422)).message).toMatch(/another token/i);
    const other = err(500);
    expect(estimateError(other)).toBe(other);
  });
});

describe('unconfirmedCopy', () => {
  it('is honest, says not to pay again, and carries the payment reference for support', () => {
    const c = unconfirmedCopy('pay-123');
    expect(c).toMatch(/couldn't confirm/i);
    expect(c).toMatch(/don't pay again/i);
    expect(c).toContain('pay-123');
  });
});

describe('how long a payment stays reserved — one constant per settlement mode', () => {
  it('naira is freed in about ten minutes, crypto (a delivery to the merchant) in about an hour', () => {
    expect(RESERVE_WINDOW).toEqual({ fiat: 'about 10 minutes', crypto: 'about an hour' });
  });

  it('a cancelled or refused attempt says its own mode’s window, and never the other’s', () => {
    expect(heldNotSentCopy('fiat', 'The swap did not go through.')).toContain(RESERVE_WINDOW.fiat);
    expect(heldNotSentCopy('fiat', 'The swap did not go through.')).not.toMatch(/hour/i);
    expect(heldNotSentCopy('crypto', 'The swap did not go through.')).toContain(RESERVE_WINDOW.crypto);
    expect(heldNotSentCopy('crypto', 'The swap did not go through.')).not.toMatch(/10 minutes|ten minutes/i);
    expect(heldNotSentCopy('crypto', 'The swap did not go through.')).toMatch(/nothing was sent/i);
  });

  it('a held refusal never tells her to try again now, and says "nothing was sent" once', () => {
    const said = heldNotSentCopy('crypto', "We couldn't check the price of this payment, so nothing was sent. Please try again.");
    expect(said).toBe("We couldn't check the price of this payment, so nothing was sent. This payment is held for about an hour; after that you can try again.");
    expect(said.match(/nothing was sent/gi)).toHaveLength(1);
  });

  it('"nothing to continue" says its own mode’s window too', () => {
    expect(nothingToContinueCopy('fiat')).toContain(RESERVE_WINDOW.fiat);
    expect(nothingToContinueCopy('crypto')).toContain(RESERVE_WINDOW.crypto);
    expect(nothingToContinueCopy('crypto')).not.toMatch(/10 minutes|ten minutes/i);
    expect(nothingToContinueCopy('crypto')).toMatch(/couldn't find/i);
  });

  it('a swap that is still being followed promises no time: "we\'ll keep checking", and never to pay again', () => {
    expect(KEEP_CHECKING).toMatch(/keep checking/i);
    for (const c of [PAYMENT_STILL_GOING, NOT_CONFIRMED, BROADCAST_UNCONFIRMED]) {
      expect(c).toContain(KEEP_CHECKING);
      expect(c).toMatch(/don't pay again/i);
      expect(c).not.toMatch(/\d|minute|hour/i);
    }
  });

  it('the terminal "couldn\'t confirm" state promises no time either — the 24 h hold lifts silently, not by a promise', () => {
    const c = unconfirmedCopy('pay-1');
    expect(c).not.toMatch(/\d+\s*(minute|hour|h\b)|an hour|ten minutes|24/i);
  });
});

describe('no time promise is scattered outside payCheckout.ts', () => {
  const fs = require('fs');
  const path = require('path');
  const root = path.resolve(__dirname, '../..');
  it.each([
    'hooks/usePayNaira.ts', 'hooks/usePayCrypto.ts', 'hooks/payAttempt.ts', 'hooks/usePayLive.ts',
    'components/pay/PayLivePanel.tsx', 'components/pay/PayNaira.tsx', 'components/pay/PayCrypto.tsx',
    'services/payHold.ts', 'services/sellSwapRun.ts', 'app/pay.tsx',
  ])('%s', (file) => {
    const src: string = fs.readFileSync(path.join(root, file), 'utf8');
    // Only user-facing string literals matter; comments may talk about the hold's 24 h.
    const strings = (src.match(/(['"`])(?:\\.|(?!\1).)*\1/g) ?? []).join('\n');
    expect(strings).not.toMatch(/ten minutes|10 minutes|about ten|an hour|few minutes/i);
  });
});

describe('directPriceMovedCopy — the order opened for more than she was shown', () => {
  const c = directPriceMovedCopy('35000000', '33510000', 6, 'USDC');
  it('names both amounts and says nothing was sent', () => {
    expect(c).toContain('35 USDC');
    expect(c).toContain('33.51 USDC');
    expect(c).toMatch(/nothing was sent/i);
  });
  it('says the payment is held for the naira window — an order exists, so she cannot just tap again — then she can retry', () => {
    expect(c).toContain(RESERVE_WINDOW.fiat);
    expect(c).toMatch(/after that you can try again/i);
    expect(c).not.toMatch(/hour/i);
  });
  it('is not the "check the new price and try again" text: trying again now would be refused', () => {
    expect(c).not.toMatch(/check the new price and try again/i);
  });
});
