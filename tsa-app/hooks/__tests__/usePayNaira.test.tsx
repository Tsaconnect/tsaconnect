/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Paying a naira-settling TSA Pay merchant, from the payer's side. What these
 * pin:
 *   - nothing is asked of the server until the payer authorizes;
 *   - direct vs converted is decided by the ramp's own quote and selectVenue,
 *     never by anything typed here — a route Paycrest fills directly is never
 *     swapped;
 *   - a converted payment is the swapped-sell pipeline (useSwappedSell) driven
 *     by the payment's intent, not a second copy of it;
 *   - a 409 pay_in_progress resumes the attempt that exists rather than
 *     starting another.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/services/payApi', () => {
  class PayError extends Error {
    status: number;
    code?: string;
    constructor(message: string, status: number, code?: string) {
      super(message);
      this.name = 'PayError';
      this.status = status;
      this.code = code;
    }
  }
  return { __esModule: true, PayError, payDirect: jest.fn(), payConverted: jest.fn(), getPayQuote: jest.fn() };
});
jest.mock('@/services/paycrestApi', () => ({
  ...jest.requireActual('@/services/paycrestApi'),
  getQuote: jest.fn(),
  claimPayment: jest.fn(),
}));
jest.mock('@/services/paycrestSellSwap', () => ({ __esModule: true, getOpenSellIntent: jest.fn() }));
jest.mock('@/services/paycrestDeposit', () => ({
  __esModule: true,
  checkFunding: jest.fn(),
  sendSponsoredPaycrestDeposit: jest.fn(),
  fundingMessage: jest.fn(() => 'You have 1 USDC on Base — this payment needs 33.5.'),
  nativeBalanceFor: jest.fn(() => 0.5),
}));
jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(),
  normalizeWalletBalances: jest.fn((d: unknown) => d),
}));
jest.mock('@/services/gasSponsorship', () => ({
  __esModule: true,
  getSponsoredRoutes: jest.fn(),
  isGasSponsored: jest.fn(() => false),
}));
jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true,
  useSignableWallet: () => ({ address: '0xPayer', ready: true, refresh: jest.fn() }),
}));
jest.mock('@/hooks/useSwappedSell', () => ({ __esModule: true, useSwappedSell: jest.fn() }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as PayApi from '@/services/payApi';
import * as PaycrestApi from '@/services/paycrestApi';
import * as SellSwap from '@/services/paycrestSellSwap';
import * as Deposit from '@/services/paycrestDeposit';
import * as WalletApi from '@/services/walletApi';
import * as Sponsorship from '@/services/gasSponsorship';
import * as Swapped from '@/hooks/useSwappedSell';
import type { LiFiToken } from '@/services/lifi';
import { PaycrestError, type PaycrestOrder, type PaycrestQuote, type PaycrestVenue } from '@/services/paycrestApi';
import type { SellIntent } from '@/services/paycrestSellSwap';
import { usePayNaira, type UsePayNaira } from '../usePayNaira';

const { PayError } = PayApi;
const m = {
  payDirect: jest.mocked(PayApi.payDirect),
  payConverted: jest.mocked(PayApi.payConverted),
  getPayQuote: jest.mocked(PayApi.getPayQuote),
  getQuote: jest.mocked(PaycrestApi.getQuote),
  claimPayment: jest.mocked(PaycrestApi.claimPayment),
  getOpenSellIntent: jest.mocked(SellSwap.getOpenSellIntent),
  checkFunding: jest.mocked(Deposit.checkFunding),
  send: jest.mocked(Deposit.sendSponsoredPaycrestDeposit),
  getWalletBalances: jest.mocked(WalletApi.getWalletBalances),
  getSponsoredRoutes: jest.mocked(Sponsorship.getSponsoredRoutes),
  useSwappedSell: jest.mocked(Swapped.useSwappedSell),
};

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const checkout = {
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'pending', amount: 5_000_000, customerTotal: 5_050_506,
  fee: 50_506, merchantNet: 5_000_000, currency: 'NGN', settlementMode: 'fiat', payable: true, youArePaying: false,
} as PayApi.PayCheckout;

const usdcBase = { address: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', symbol: 'USDC', decimals: 6 } as LiFiToken;
const bnb = { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', decimals: 18 } as LiFiToken;
const BASE = 8453;
const BSC = 56;

const venue = { chain: 'base', token: 'USDC', rate: '1500', spreadPercent: 0.1, tokenAmount: 33.5 } as PaycrestVenue;
const quoteWith = (venues: PaycrestVenue[]) => ({ venues, best: venues[0] ?? null }) as PaycrestQuote;
const order = { id: 'o1', chain: 'base', token: 'USDC', status: 'awaiting_deposit' } as PaycrestOrder;
const intent = { id: 'i1', status: 'swapping' } as SellIntent;

// The swapped-sell hook, replaced by a stub whose state the tests set.
const swapped = {
  stage: 'idle' as string,
  error: undefined as string | undefined,
  intent: undefined as SellIntent | undefined,
  missingUsdc: undefined as string | undefined,
  order: undefined as PaycrestOrder | undefined,
  start: jest.fn(),
  resume: jest.fn(),
  completeWithWallet: jest.fn(),
  cancel: jest.fn(),
};
let createIntent!: NonNullable<Parameters<typeof Swapped.useSwappedSell>[0]>['createIntent'];

let hook: UsePayNaira;
const Harness = ({ c }: { c: PayApi.PayCheckout }) => {
  hook = usePayNaira(c);
  return null;
};
let renderer: ReactTestRenderer;
const mount = (c = checkout) => act(() => { renderer = TestRenderer.create(<Harness c={c} />); });
const flush = () => act(async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); });

/** Tap a token, then authorize. */
async function pay(token: LiFiToken, chainId: number) {
  act(() => hook.payWith(token, chainId));
  await act(async () => { hook.authorization.onAuthorized(); });
  await flush();
}

beforeEach(() => {
  jest.clearAllMocks();
  Object.assign(swapped, {
    stage: 'idle', error: undefined, intent: undefined, missingUsdc: undefined, order: undefined,
  });
  swapped.start.mockResolvedValue(undefined);
  swapped.resume.mockResolvedValue(undefined);
  swapped.completeWithWallet.mockResolvedValue(undefined);
  m.useSwappedSell.mockImplementation(((opts: any) => {
    createIntent = opts?.createIntent;
    return { ...swapped };
  }) as any);
  m.getQuote.mockResolvedValue(quoteWith([venue]));
  m.getWalletBalances.mockResolvedValue({ success: true, data: [] } as any);
  m.getSponsoredRoutes.mockResolvedValue([]);
  m.checkFunding.mockReturnValue({ ok: true });
  m.payDirect.mockResolvedValue(order);
  m.send.mockResolvedValue('0xdeposit');
  m.claimPayment.mockResolvedValue(order);
  m.getOpenSellIntent.mockResolvedValue(null);
});

afterEach(() => act(() => renderer?.unmount()));

describe('authorization', () => {
  it('payWith only opens the modal — nothing is asked of the server yet', async () => {
    await mount();
    act(() => hook.payWith(usdcBase, BASE));
    expect(hook.authorization.visible).toBe(true);
    expect(hook.authorization.title).toBe('Authorize payment');
    expect(m.getQuote).not.toHaveBeenCalled();
    expect(m.payDirect).not.toHaveBeenCalled();
    expect(swapped.start).not.toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });

  it('cancelling asks for nothing', async () => {
    await mount();
    act(() => hook.payWith(usdcBase, BASE));
    act(() => hook.authorization.onCancel());
    await flush();
    expect(hook.authorization.visible).toBe(false);
    expect(m.getQuote).not.toHaveBeenCalled();
    expect(m.payDirect).not.toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });
});

describe('a token Paycrest fills directly', () => {
  it('sizes the route off what the merchant is owed, funds the order, claims it, and waits', async () => {
    await mount();
    await pay(usdcBase, BASE);

    expect(m.getQuote).toHaveBeenCalledWith('sell', '50000.00');
    expect(m.checkFunding).toHaveBeenCalledWith(venue, expect.anything(), { gasSponsored: false });
    expect(m.payDirect).toHaveBeenCalledWith(ID, { chainKey: 'base', token: 'USDC' });
    expect(m.send).toHaveBeenCalledWith(order, '0xPayer', 0.5);
    expect(m.claimPayment).toHaveBeenCalledWith('o1');
    expect(swapped.start).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
    expect(hook.order?.id).toBe('o1');
  });

  it('funds it in order: checked, then opened, then sent, then claimed', async () => {
    const calls: string[] = [];
    m.checkFunding.mockImplementation(() => { calls.push('funding'); return { ok: true }; });
    m.payDirect.mockImplementation(async () => { calls.push('payDirect'); return order; });
    m.send.mockImplementation(async () => { calls.push('send'); return '0xdeposit'; });
    m.claimPayment.mockImplementation(async () => { calls.push('claim'); return order; });
    await mount();
    await pay(usdcBase, BASE);
    expect(calls).toEqual(['funding', 'payDirect', 'send', 'claim']);
  });

  it('refuses with the funding message and never opens an order when she cannot fund it', async () => {
    m.checkFunding.mockReturnValue({ ok: false, reason: 'short-token', token: 'USDC', chain: 'base', have: 1, need: 33.5 });
    await mount();
    await pay(usdcBase, BASE);

    expect(hook.stage).toBe('failed');
    expect(hook.error).toBe('You have 1 USDC on Base — this payment needs 33.5.');
    expect(m.payDirect).not.toHaveBeenCalled();
    expect(m.send).not.toHaveBeenCalled();
  });

  it('a failed send keeps the order so she can send it by hand from the order screen', async () => {
    m.send.mockRejectedValue(new Error('Not enough ETH on base to pay gas'));
    await mount();
    await pay(usdcBase, BASE);

    expect(hook.stage).toBe('failed');
    expect(hook.error).toBe('Not enough ETH on base to pay gas');
    expect(hook.order?.id).toBe('o1');
    expect(m.claimPayment).not.toHaveBeenCalled();
  });

  it('a lost claim never fails a payment whose deposit already left', async () => {
    m.claimPayment.mockRejectedValue(new Error('offline'));
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.stage).toBe('waiting');
  });
});

describe('any other token', () => {
  it('goes through the swapped-sell pipeline with the payment as its intent', async () => {
    m.getQuote.mockResolvedValue(quoteWith([venue])); // USDC on Base only — BNB has no direct route
    await mount();
    await pay(bnb, BSC);

    expect(m.payDirect).not.toHaveBeenCalled();
    expect(m.checkFunding).not.toHaveBeenCalled();
    expect(swapped.start).toHaveBeenCalledWith({
      sourceChainId: BSC,
      token: { ...bnb, chainId: BSC },
      walletAddress: '0xPayer',
      sourceAddress: '0xPayer',
    });
  });

  it('the creator it hands the pipeline starts the PAYMENT, from where her tokens are', async () => {
    const created = { intent, quote: {}, gasTopUp: true };
    m.payConverted.mockResolvedValue(created as any);
    await mount();

    await expect(createIntent!({
      sourceChainId: BSC, token: bnb, walletAddress: '0xPayer', sourceAddress: '0xPayer',
    })).resolves.toBe(created);

    expect(m.payConverted).toHaveBeenCalledWith(ID, {
      sourceChainId: BSC, sourceToken: bnb.address, sourceAddress: '0xPayer',
    });
  });

  it('a converted route Paycrest lists is not a direct one — the token is still swapped', async () => {
    m.getQuote.mockResolvedValue(quoteWith([
      { ...venue, kind: 'converted', requestedChainId: BSC, requestedTokenAddress: bnb.address } as PaycrestVenue,
    ]));
    await mount();
    await pay(bnb, BSC);
    expect(swapped.start).toHaveBeenCalled();
    expect(m.payDirect).not.toHaveBeenCalled();
  });

  it('a chain the wallet cannot send from is refused up front, before any quote', async () => {
    await mount();
    await pay(bnb, 1151111081099710); // Solana
    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/isn't supported yet/i);
    expect(m.getQuote).not.toHaveBeenCalled();
    expect(swapped.start).not.toHaveBeenCalled();
  });
});

describe('the swapped-sell stages, as a payment', () => {
  it.each([
    ['swapping', 'paying'], ['waiting', 'paying'], ['ordering', 'paying'], ['sending', 'paying'],
    ['shortfall', 'shortfall'], ['done', 'waiting'], ['failed', 'failed'], ['idle', 'idle'],
  ])('%s → %s', async (from, to) => {
    swapped.stage = from;
    swapped.error = from === 'failed' ? 'The swap did not go through' : undefined;
    await mount();
    expect(hook.stage).toBe(to);
    if (from === 'failed') expect(hook.error).toBe('The swap did not go through');
  });

  it('exposes the shortfall figure and the swap stage for the screen', async () => {
    Object.assign(swapped, { stage: 'shortfall', missingUsdc: '0.42' });
    await mount();
    expect(hook.missingUsdc).toBe('0.42');
    expect(hook.swapStage).toBe('shortfall');
  });

  it('completeWithWallet finishes the sale that is open at its shortfall', async () => {
    Object.assign(swapped, { stage: 'shortfall', intent, missingUsdc: '0.42' });
    await mount();
    await act(async () => { await hook.completeWithWallet(); });
    expect(swapped.completeWithWallet).toHaveBeenCalledWith(intent);
  });

  it('completeWithWallet with no sale open does nothing', async () => {
    Object.assign(swapped, { stage: 'shortfall', intent: undefined });
    await mount();
    await act(async () => { await hook.completeWithWallet(); });
    expect(swapped.completeWithWallet).not.toHaveBeenCalled();
  });
});

describe('a payment that is already being paid (409 pay_in_progress)', () => {
  const inProgress = () => new PayError('You are already paying this', 409, 'pay_in_progress');

  it('a converted attempt resumes her open sale instead of starting another', async () => {
    m.payConverted.mockRejectedValue(inProgress());
    m.getOpenSellIntent.mockResolvedValue(intent);
    // The swapped-sell stub runs the creator the way the real hook does: a
    // rejection ends its run.
    swapped.start.mockImplementation(async (a: any) => { await createIntent!(a).catch(() => {}); });
    await mount();
    await pay(bnb, BSC);

    expect(m.getOpenSellIntent).toHaveBeenCalled();
    expect(swapped.resume).toHaveBeenCalledWith(intent, '0xPayer');
  });

  it('with no open sale it is a direct order — the server settles it, so she waits', async () => {
    m.payConverted.mockRejectedValue(inProgress());
    m.getOpenSellIntent.mockResolvedValue(null);
    swapped.start.mockImplementation(async (a: any) => { await createIntent!(a).catch(() => {}); });
    await mount();
    await pay(bnb, BSC);

    expect(swapped.resume).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });

  it('a direct attempt takes the same road', async () => {
    m.payDirect.mockRejectedValue(inProgress());
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await pay(usdcBase, BASE);

    expect(m.send).not.toHaveBeenCalled();
    expect(swapped.resume).toHaveBeenCalledWith(intent, '0xPayer');
  });

  it('resume() alone — the screen re-opened on its own payment — follows the same road', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    expect(swapped.resume).toHaveBeenCalledWith(intent, '0xPayer');
  });
});

describe('refusals keep their machine code', () => {
  it('a KYC refusal from starting the payment is exposed by code, not by words', async () => {
    m.payConverted.mockRejectedValue(new PayError('Verify your identity first', 403, 'kyc_verification_required'));
    swapped.start.mockImplementation(async (a: any) => { await createIntent!(a).catch(() => {}); });
    // The real pipeline ends its run at 'failed' with the refusal's words.
    Object.assign(swapped, { stage: 'failed', error: 'Verify your identity first' });
    await mount();
    await pay(bnb, BSC);
    expect(hook.stage).toBe('failed');
    expect(hook.error).toBe('Verify your identity first');
    expect(hook.errorCode).toBe('kyc_verification_required');
  });

  it('a KYC refusal on a direct payment likewise', async () => {
    m.payDirect.mockRejectedValue(new PayError('Verify your identity first', 403, 'kyc_verification_required'));
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.stage).toBe('failed');
    expect(hook.error).toBe('Verify your identity first');
    expect(hook.errorCode).toBe('kyc_verification_required');
  });

  it('a quote that cannot be fetched fails with the server words', async () => {
    m.getQuote.mockRejectedValue(new PaycrestError('Naira transfers are not available right now.', 503));
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.stage).toBe('failed');
    expect(hook.error).toBe('Naira transfers are not available right now.');
  });
});

describe('estimate — what she will pay, before she taps Pay', () => {
  it('a direct route shows the venue amount, rounded up', async () => {
    m.getQuote.mockResolvedValue(quoteWith([{ ...venue, tokenAmount: 33.501 } as PaycrestVenue]));
    await mount();
    await expect(hook.estimate(usdcBase, BASE)).resolves.toBe('33.51 USDC');
    expect(m.getPayQuote).not.toHaveBeenCalled();
  });

  it('a converted route asks the server what the swap pulls, in her token’s decimals', async () => {
    m.getPayQuote.mockResolvedValue({ fromAmount: '84000000000000000', usdcTarget: '33835000' });
    await mount();
    await expect(hook.estimate(bnb, BSC)).resolves.toBe('0.084 BNB');
    expect(m.getPayQuote).toHaveBeenCalledWith(ID, { sourceChainId: BSC, sourceToken: bnb.address, sourceAddress: '0xPayer' });
  });
});

