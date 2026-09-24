/**
 * The one state machine behind a live TSA Pay attempt (naira and crypto). Pure:
 * every transition the hooks rely on is a row here — including the guards that
 * keep a payment safe (an attempt in flight is never begun twice or reset, a
 * server-paused payment is never reset, a "don't pay again" failure never
 * offers Pay, unresolved attempts keep the form up).
 */
import type { SellIntent } from '@/services/paycrestSellSwap';
import type { PaycrestOrder } from '@/services/paycrestApi';
import { canReset, isUnresolved, payAttemptView, reducePayAttempt, type PayAttempt, type PayAttemptEvent } from '../payAttempt';

const intent = { id: 'i1', status: 'swapping' } as SellIntent;
const order = { id: 'o1' } as PaycrestOrder;
const IDLE: PayAttempt = { kind: 'idle' };
const paying: PayAttempt = { kind: 'paying', swapStage: 'swapping', walletTried: false };
const failed = (over: Partial<Extract<PayAttempt, { kind: 'failed' }>> = {}): PayAttempt => ({
  kind: 'failed', error: 'x', retryable: true, paused: false, held: false, noContinue: false, ...over,
});

describe('reducePayAttempt — transitions', () => {
  const rows: [string, PayAttempt, PayAttemptEvent, Partial<PayAttempt>][] = [
    ['begin from idle', IDLE, { type: 'begin' }, { kind: 'paying' }],
    ['begin after a retryable failure', failed(), { type: 'begin' }, { kind: 'paying' }],
    ['begin after a wait', { kind: 'waiting' }, { type: 'begin' }, { kind: 'paying' }],
    ['progress from paying keeps the wallet flag', { ...paying, walletTried: true }, { type: 'progress', swapStage: 'ordering' }, { kind: 'paying', swapStage: 'ordering', walletTried: true }],
    ['progress from idle (a resumed sale) is paying', IDLE, { type: 'progress', swapStage: 'waiting' }, { kind: 'paying', swapStage: 'waiting' }],
    ['progress from a continue prompt is paying', { kind: 'continue', intent }, { type: 'progress', swapStage: 'waiting' }, { kind: 'paying' }],
    ['the pipeline finishing is a wait on the server', paying, { type: 'waiting', swapStage: 'done', order }, { kind: 'waiting', swapStage: 'done', order }],
    ['a stop is a failure with its words and its retry flag', paying, { type: 'stop', message: 'no', retryable: false, intent }, { kind: 'failed', error: 'no', retryable: false, noContinue: false }],
    ['a stop with no intent has nothing to continue', paying, { type: 'stop', message: 'no', retryable: false }, { kind: 'failed', retryable: false, noContinue: true }],
    ['a shortfall keeps the intent and the figure', paying, { type: 'shortfall', missingUsdc: '0.42', intent }, { kind: 'shortfall', missingUsdc: '0.42', walletTried: false }],
    ['a shortfall after her wallet was offered remembers it', { ...paying, walletTried: true }, { type: 'shortfall', missingUsdc: '0.10', intent }, { kind: 'shortfall', walletTried: true }],
    ['offering her wallet resumes paying and remembers it', { kind: 'shortfall', intent, missingUsdc: '0.42', walletTried: false }, { type: 'walletOffered' }, { kind: 'paying', walletTried: true }],
    ['a payment under way that needs her', IDLE, { type: 'continue', intent }, { kind: 'continue' }],
    ['a failure with its code', paying, { type: 'fail', error: 'kyc', errorCode: 'kyc_verification_required', retryable: true }, { kind: 'failed', code: 'kyc_verification_required', retryable: true }],
    ['a server-paused payment', paying, { type: 'fail', error: 'paused', retryable: false, paused: true }, { kind: 'failed', paused: true, retryable: false }],
    ['reset from idle', IDLE, { type: 'reset' }, { kind: 'idle' }],
    ['reset a wait', { kind: 'waiting' }, { type: 'reset' }, { kind: 'idle' }],
    ['reset a retryable failure', failed(), { type: 'reset' }, { kind: 'idle' }],
    ['reset a "don\'t pay again" failure (the hook decides whether to ask)', failed({ retryable: false }), { type: 'reset' }, { kind: 'idle' }],
  ];
  it.each(rows)('%s', (_name, from, event, expected) => {
    expect(reducePayAttempt(from, event)).toMatchObject(expected);
  });

  it('an attempt in flight is never begun twice and never reset', () => {
    expect(reducePayAttempt(paying, { type: 'begin' })).toBe(paying);
    expect(reducePayAttempt(paying, { type: 'reset' })).toBe(paying);
  });

  it('a payment the server paused is never reset, and no later stop can rewrite its words', () => {
    const paused = failed({ retryable: false, paused: true, error: 'paused' });
    expect(reducePayAttempt(paused, { type: 'reset' })).toBe(paused);
    expect(reducePayAttempt(paused, { type: 'stop', message: 'other', retryable: true })).toBe(paused);
    expect(reducePayAttempt(paused, { type: 'progress', swapStage: 'waiting' })).toBe(paused);
    expect(reducePayAttempt(paused, { type: 'begin' })).toBe(paused);
  });

  it('nor can a later fail — even a paused one — overwrite it or downgrade it to a retryable failure', () => {
    const paused = failed({ retryable: false, paused: true, error: 'paused' });
    expect(reducePayAttempt(paused, { type: 'fail', error: 'later', retryable: true })).toBe(paused);
    expect(reducePayAttempt(paused, { type: 'fail', error: 'again', retryable: false, paused: true })).toBe(paused);
  });

  it('a held failure (a swap that may have reached the merchant) is never downgraded to one that offers Pay', () => {
    const held = failed({ retryable: false, held: true, error: 'held' });
    expect(reducePayAttempt(held, { type: 'fail', error: 'later', retryable: true })).toBe(held);
    expect(reducePayAttempt(held, { type: 'stop', message: 'later', retryable: true })).toBe(held);
  });

  it('but a held failure can be replaced by another one that is no less safe, and lifted by the hold lifting', () => {
    const held = failed({ retryable: false, held: true, error: 'held' });
    expect(reducePayAttempt(held, { type: 'fail', error: 'terminal', retryable: false, noContinue: true, held: true }))
      .toMatchObject({ kind: 'failed', error: 'terminal', held: true, retryable: false });
    expect(reducePayAttempt(held, { type: 'waiting' })).toMatchObject({ kind: 'waiting' });
    expect(reducePayAttempt(held, { type: 'reset' })).toMatchObject({ kind: 'idle' });
  });

  it('a stop after a wait is still a failure — the pipeline said so', () => {
    expect(reducePayAttempt({ kind: 'waiting' }, { type: 'stop', message: 'x', retryable: true })).toMatchObject({ kind: 'failed' });
  });
});

describe('payAttemptView — what the payer and the screen read', () => {
  it('idle', () => {
    expect(payAttemptView(IDLE)).toEqual({
      stage: 'idle', retryable: true, canContinue: false, walletShort: false, swapStage: 'idle',
    });
  });

  it('paying shows the pipeline stage and the direct order', () => {
    expect(payAttemptView({ kind: 'paying', swapStage: 'waiting', walletTried: false, order })).toMatchObject({
      stage: 'paying', swapStage: 'waiting', order, retryable: true, canContinue: false,
    });
  });

  it('a continue prompt can be continued', () => {
    expect(payAttemptView({ kind: 'continue', intent })).toMatchObject({ stage: 'continue', canContinue: true });
  });

  it('a shortfall says how far, and whether her wallet was already offered', () => {
    expect(payAttemptView({ kind: 'shortfall', intent, missingUsdc: '0.42', walletTried: false })).toMatchObject({
      stage: 'shortfall', missingUsdc: '0.42', walletShort: false, swapStage: 'shortfall',
    });
    expect(payAttemptView({ kind: 'shortfall', intent, walletTried: true }).walletShort).toBe(true);
  });

  it.each([
    ['retryable', failed(), true, false],
    ['"don\'t pay again" with an intent to continue', failed({ retryable: false, intent }), false, true],
    ['"don\'t pay again" with nothing to continue', failed({ retryable: false, noContinue: true }), false, false],
    ['paused by the server', failed({ retryable: false, paused: true }), false, false],
  ])('failed, %s', (_n, a, retryable, canContinue) => {
    expect(payAttemptView(a)).toMatchObject({ stage: 'failed', retryable, canContinue });
  });

  it('a failure carries its words, code and order', () => {
    expect(payAttemptView(failed({ error: 'kyc', code: 'kyc_verification_required', order }))).toMatchObject({
      error: 'kyc', errorCode: 'kyc_verification_required', order,
    });
  });
});

describe('isUnresolved — does the form stay up under the expiry warning', () => {
  it.each([
    ['idle', 'idle', true, false],
    ['paying', 'paying', true, true],
    ['continue', 'continue', true, true],
    ['shortfall', 'shortfall', true, true],
    ['waiting', 'waiting', true, true],
    ['a retryable failure (nothing under way)', 'failed', true, false],
    ['a "don\'t pay again" failure', 'failed', false, true],
  ] as const)('%s', (_n, stage, retryable, expected) => {
    expect(isUnresolved(stage, retryable)).toBe(expected);
  });
});

describe('canReset — the guard the async crypto reset shares with the reducer', () => {
  it.each([
    [IDLE, true],
    [{ kind: 'waiting' } as PayAttempt, true],
    [failed(), true],
    [failed({ retryable: false }), true],
    [paying, false],
    [failed({ retryable: false, paused: true }), false],
  ])('%#', (a, expected) => {
    expect(canReset(a)).toBe(expected);
  });
});
