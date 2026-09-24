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
  getOrder: jest.fn(),
  claimPayment: jest.fn(),
}));
jest.mock('@/services/paycrestSellSwap', () => ({ __esModule: true, getOpenSellIntent: jest.fn() }));
jest.mock('@/services/paycrestDeposit', () => ({
  __esModule: true,
  checkFunding: jest.fn(),
  sendSponsoredPaycrestDeposit: jest.fn(),
  fmtTokenAmount: (n: number) => n.toLocaleString('en-US', { maximumFractionDigits: n >= 1 ? 2 : 6 }),
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
const mockWallet: { address: string | null } = { address: '0xPayer' };
jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true,
  useSignableWallet: () => ({ address: mockWallet.address, ready: true, refresh: jest.fn() }),
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
import { directPriceMovedCopy } from '@/utils/payCheckout';
import { usePayNaira, type UsePayNaira } from '../usePayNaira';

const { PayError } = PayApi;
const m = {
  payDirect: jest.mocked(PayApi.payDirect),
  payConverted: jest.mocked(PayApi.payConverted),
  getPayQuote: jest.mocked(PayApi.getPayQuote),
  getQuote: jest.mocked(PaycrestApi.getQuote),
  getOrder: jest.mocked(PaycrestApi.getOrder),
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

const usdcBase = {
  address: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', symbol: 'USDC', decimals: 6, coinKey: 'USDC', verificationStatus: 'verified',
} as LiFiToken;
const bnb = { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', decimals: 18 } as LiFiToken;
const BASE = 8453;
const BSC = 56;

const venue = { chain: 'base', token: 'USDC', rate: '1500', spreadPercent: 0.1, tokenAmount: 33.5 } as PaycrestVenue;
const quoteWith = (venues: PaycrestVenue[]) => ({ venues, best: venues[0] ?? null }) as PaycrestQuote;
const order = { id: 'o1', chain: 'base', token: 'USDC', status: 'awaiting_deposit', tokenAmount: '33.5' } as PaycrestOrder;
const intent = { id: 'i1', status: 'swapping', payPaymentId: ID } as SellIntent;

// The swapped-sell hook, replaced by a stub whose state the tests set.
const swapped = {
  stage: 'idle' as string,
  error: undefined as string | undefined,
  intent: undefined as SellIntent | undefined,
  missingUsdc: undefined as string | undefined,
  order: undefined as PaycrestOrder | undefined,
  retryable: true,
  start: jest.fn(),
  resume: jest.fn(),
  completeWithWallet: jest.fn(),
  cancel: jest.fn(),
  reset: jest.fn(),
};
/**
 * The pipeline is stubbed, and the hook reads the pipeline through EVENTS
 * (onStage / onStop / onShortfall). `replay` tells the hook what the real one
 * would have told it, from the `swapped` fields each test sets.
 */
let latestOpts: any;
const replay = (opts: any) => {
  const snap = { intent: swapped.intent, order: swapped.order };
  if (swapped.stage === 'failed') opts?.onStop?.({ message: swapped.error, retryable: swapped.retryable }, snap);
  else if (swapped.stage === 'shortfall') opts?.onShortfall?.(swapped.missingUsdc, snap);
  else if (swapped.stage !== 'idle') opts?.onStage?.(swapped.stage, snap);
};
/** Stubbed `start`: the intent is created for real (the hook's own creator), then the pipeline "runs". */
const pipelineStart = async (a: any) => { await createIntent!(a).catch(() => {}); replay(latestOpts); };
let createIntent!: NonNullable<Parameters<typeof Swapped.useSwappedSell>[0]>['createIntent'];

/** A price no fresh quote can exceed — for tests that are not about the bound. */
const ANY_PRICE = { pay: '33.5 USDC', fromAmount: `1${'0'.repeat(40)}` };

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
  act(() => hook.payWith(token, chainId, ANY_PRICE));
  await act(async () => { hook.authorization.onAuthorized(); });
  await flush();
}

beforeEach(() => {
  jest.clearAllMocks();
  Object.assign(swapped, {
    stage: 'idle', error: undefined, intent: undefined, missingUsdc: undefined, order: undefined, retryable: true,
  });
  mockWallet.address = '0xPayer';
  m.getOrder.mockResolvedValue(order);
  swapped.start.mockResolvedValue(undefined);
  swapped.resume.mockResolvedValue(undefined);
  swapped.completeWithWallet.mockResolvedValue(undefined);
  m.useSwappedSell.mockImplementation(((opts: any) => {
    createIntent = opts?.createIntent;
    latestOpts = opts;
    // A hook mounted while the (stubbed) pipeline is already somewhere has been told so.
    React.useEffect(() => { replay(opts); }, []); // eslint-disable-line react-hooks/exhaustive-deps
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
    act(() => hook.payWith(usdcBase, BASE, ANY_PRICE));
    expect(hook.authorization.visible).toBe(true);
    expect(hook.authorization.title).toBe('Authorize payment of about 33.5 USDC');
    expect(m.getQuote).not.toHaveBeenCalled();
    expect(m.payDirect).not.toHaveBeenCalled();
    expect(swapped.start).not.toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });

  it('cancelling asks for nothing', async () => {
    await mount();
    act(() => hook.payWith(usdcBase, BASE, ANY_PRICE));
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

  it('a send that fails with no message points at the payment order, not at trying again', async () => {
    m.send.mockRejectedValue('nope');
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.error).toMatch(/payment order/i);
    expect(hook.error).not.toMatch(/try again/i);
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
      pricedFromAmount: ANY_PRICE.fromAmount,
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

  it('"Use my USDC" asks for authorization first — nothing is opened or signed until she gives it', async () => {
    Object.assign(swapped, { stage: 'shortfall', intent, missingUsdc: '0.42' });
    await mount();
    act(() => hook.completeWithWallet());

    expect(hook.authorization.visible).toBe(true);
    expect(hook.authorization.title).toBe('Authorize paying about 0.42 USDC from your wallet');
    expect(swapped.completeWithWallet).not.toHaveBeenCalled();

    await act(async () => { hook.authorization.onAuthorized(); });
    expect(swapped.completeWithWallet).toHaveBeenCalledWith(intent);
  });

  it('cancelling that authorization leaves her USDC alone', async () => {
    Object.assign(swapped, { stage: 'shortfall', intent, missingUsdc: '0.42' });
    await mount();
    act(() => hook.completeWithWallet());
    act(() => hook.authorization.onCancel());
    await flush();
    expect(swapped.completeWithWallet).not.toHaveBeenCalled();
  });

  it('with no sale open it does not even ask', async () => {
    Object.assign(swapped, { stage: 'shortfall', intent: undefined });
    await mount();
    act(() => hook.completeWithWallet());
    expect(hook.authorization.visible).toBe(false);
    expect(swapped.completeWithWallet).not.toHaveBeenCalled();
  });

  it('a shortfall that comes back after her wallet was offered says her wallet was not enough', async () => {
    Object.assign(swapped, { stage: 'shortfall', intent, missingUsdc: '0.42' });
    swapped.completeWithWallet.mockImplementation(async () => { replay(latestOpts); }); // still short
    await mount();
    expect(hook.walletShort).toBe(false);
    act(() => hook.completeWithWallet());
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(hook.stage).toBe('shortfall'); // the stub never left it
    expect(hook.walletShort).toBe(true);
  });
});

describe('a payment that is already being paid (409 pay_in_progress)', () => {
  const inProgress = () => new PayError('You are already paying this', 409, 'pay_in_progress');

  it('a converted attempt FOLLOWS her open sale: nothing is signed or sent until she taps Continue payment', async () => {
    m.payConverted.mockRejectedValue(inProgress());
    m.getOpenSellIntent.mockResolvedValue(intent);
    // The swapped-sell stub runs the creator the way the real hook does: a
    // rejection ends its run.
    swapped.start.mockImplementation(pipelineStart);
    await mount();
    await pay(bnb, BSC);

    expect(hook.stage).toBe('continue');
    expect(hook.canContinue).toBe(true);
    expect(swapped.resume).not.toHaveBeenCalled();
    expect(m.send).not.toHaveBeenCalled();
  });

  it('with no open sale it is a direct order — the server settles it, so she waits', async () => {
    m.payConverted.mockRejectedValue(inProgress());
    m.getOpenSellIntent.mockResolvedValue(null);
    swapped.start.mockImplementation(pipelineStart);
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
    expect(hook.stage).toBe('continue');
    expect(swapped.resume).not.toHaveBeenCalled();
  });
});

describe('opening the screen on her own payment follows it and never signs', () => {
  it('a swap under way offers Continue payment, and calls nothing that moves money', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });

    expect(hook.stage).toBe('continue');
    expect(swapped.resume).not.toHaveBeenCalled();
    expect(swapped.start).not.toHaveBeenCalled();
    expect(swapped.completeWithWallet).not.toHaveBeenCalled();
    expect(m.send).not.toHaveBeenCalled();
    expect(m.payDirect).not.toHaveBeenCalled();
  });

  it('Continue payment asks for authorization, and only then resumes the sale — as the server has it NOW', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });

    act(() => hook.continuePayment());
    expect(hook.authorization.visible).toBe(true);
    expect(swapped.resume).not.toHaveBeenCalled();

    // Between opening the screen and confirming, the swap was recorded: resume
    // from that, not from the snapshot taken when the screen opened.
    const fresh = { ...intent, status: 'swapped', swapTxHash: '0xh' } as SellIntent;
    m.getOpenSellIntent.mockResolvedValue(fresh);
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(swapped.resume).toHaveBeenCalledWith(fresh, '0xPayer');
  });

  it('Continue payment with nothing of hers left to continue says so, and resumes nothing', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    act(() => hook.continuePayment());
    m.getOpenSellIntent.mockResolvedValue(null); // the server let go of it
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(swapped.resume).not.toHaveBeenCalled();
    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/couldn't find/i);
    expect(hook.retryable).toBe(false);
  });

  it('Continue payment when the sale is now another payment’s or already over resumes nothing', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    act(() => hook.continuePayment());
    m.getOpenSellIntent.mockResolvedValue({ ...intent, payPaymentId: 'other' } as SellIntent);
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(swapped.resume).not.toHaveBeenCalled();
  });

  it('cancelling that authorization resumes nothing', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    act(() => hook.continuePayment());
    act(() => hook.authorization.onCancel());
    await flush();
    expect(swapped.resume).not.toHaveBeenCalled();
    expect(hook.stage).toBe('continue');
  });

  it('an ordered sale whose payout still awaits its deposit needs her to send it — Continue payment', async () => {
    m.getOpenSellIntent.mockResolvedValue({ ...intent, status: 'ordered', paycrestOrderId: 'o1' } as SellIntent);
    m.getOrder.mockResolvedValue({ ...order, status: 'awaiting_deposit' } as PaycrestOrder);
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.getOrder).toHaveBeenCalledWith('o1');
    expect(hook.stage).toBe('continue');
    expect(m.send).not.toHaveBeenCalled();
  });

  it('an ordered sale whose deposit already left just waits', async () => {
    m.getOpenSellIntent.mockResolvedValue({ ...intent, status: 'ordered', paycrestOrderId: 'o1' } as SellIntent);
    m.getOrder.mockResolvedValue({ ...order, status: 'settling' } as PaycrestOrder);
    await mount();
    await act(async () => { await hook.resume(); });
    expect(hook.stage).toBe('waiting');
  });

  it('a delivered or failed sale has nothing for her to do — the server settles or frees the payment', async () => {
    for (const status of ['delivered', 'failed'] as const) {
      m.getOpenSellIntent.mockResolvedValue({ ...intent, status } as SellIntent);
      await mount();
      await act(async () => { await hook.resume(); });
      expect(hook.stage).toBe('waiting');
      act(() => renderer.unmount());
    }
  });

  it('an open sale that pays a DIFFERENT payment (or none) is not followed', async () => {
    m.getOpenSellIntent.mockResolvedValue({ ...intent, payPaymentId: 'someone-elses' } as SellIntent);
    await mount();
    await act(async () => { await hook.resume(); });
    expect(hook.stage).toBe('waiting');
    expect(hook.canContinue).toBe(false);
    act(() => hook.continuePayment());
    expect(hook.authorization.visible).toBe(false);
    expect(swapped.resume).not.toHaveBeenCalled();
  });

  it('with no signable wallet it follows nothing', async () => {
    mockWallet.address = null;
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.getOpenSellIntent).not.toHaveBeenCalled();
  });
});

describe('a payment that cannot safely be started over', () => {
  it('says so in payment words, is not retryable, and offers Continue payment for the sale it holds', async () => {
    Object.assign(swapped, { stage: 'failed', error: 'Tap Finish your sale to try again.', retryable: false, intent });
    await mount();
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.error).toMatch(/Continue payment/);
    expect(hook.error).toMatch(/don't pay again/i);
    expect(hook.error).not.toMatch(/Finish your sale/);
    expect(hook.canContinue).toBe(true);
    act(() => hook.continuePayment());
    expect(hook.authorization.visible).toBe(true);
    m.getOpenSellIntent.mockResolvedValue(intent);
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(swapped.resume).toHaveBeenCalledWith(intent, '0xPayer');
  });

  it('a plain failure keeps its own words and stays retryable', async () => {
    Object.assign(swapped, { stage: 'failed', error: 'User rejected the request' });
    await mount();
    expect(hook.retryable).toBe(true);
    expect(hook.error).toBe('User rejected the request');
    expect(hook.canContinue).toBe(false);
  });

  it('a direct order that opened but could not be sent is not retryable — the order screen finishes it', async () => {
    m.send.mockRejectedValue(new Error('Not enough ETH on base to pay gas'));
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.retryable).toBe(false);
  });
});

describe("the server's uncertain-attempt answer (409 pay_attempt_uncertain)", () => {
  const uncertain = () => new PayError('We could not tell whether an attempt exists', 409, 'pay_attempt_uncertain');

  it('a direct start is paused: honest copy, never retryable, nothing to continue, nothing further called', async () => {
    m.payDirect.mockRejectedValue(uncertain());
    await mount();
    await pay(usdcBase, BASE);

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(false);
    expect(hook.error).toMatch(/paused/i);
    expect(hook.error).toMatch(/don't pay again/i);
    expect(hook.errorCode).toBe('pay_attempt_uncertain');
    expect(m.send).not.toHaveBeenCalled();
    expect(m.getOpenSellIntent).not.toHaveBeenCalled(); // not followed either: the server could not prove one exists
  });

  it('a converted start is paused the same way, whatever words the swapped sell reports', async () => {
    m.payConverted.mockRejectedValue(uncertain());
    swapped.start.mockImplementation(pipelineStart);
    Object.assign(swapped, { stage: 'failed', error: 'We could not tell whether an attempt exists', retryable: true });
    await mount();
    await pay(bnb, BSC);

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.error).toMatch(/paused/i);
    expect(hook.canContinue).toBe(false);
  });

  it('is never auto-reset: the payment stays paused when the checkout flips back', async () => {
    m.payDirect.mockRejectedValue(uncertain());
    await mount();
    await pay(usdcBase, BASE);
    act(() => hook.reset());
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(swapped.reset).not.toHaveBeenCalled();
  });
});

describe("the server's own failure to start (500) is just a retry", () => {
  const ours = () => new PayError("We couldn't start this payment — please try again.", 500);

  it('a direct start: the server words, retryable, and nothing is held', async () => {
    m.payDirect.mockRejectedValue(ours());
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.stage).toBe('failed');
    expect(hook.error).toBe("We couldn't start this payment — please try again.");
    expect(hook.retryable).toBe(true);
    act(() => hook.reset());
    expect(hook.stage).toBe('idle');
  });

  it('a converted start likewise', async () => {
    m.payConverted.mockRejectedValue(ours());
    swapped.start.mockImplementation(pipelineStart);
    Object.assign(swapped, { stage: 'failed', error: "We couldn't start this payment — please try again.", retryable: true });
    await mount();
    await pay(bnb, BSC);
    expect(hook.retryable).toBe(true);
    expect(hook.error).toMatch(/please try again/);
  });
});

describe('reset', () => {
  it('does nothing while an attempt is in flight — it must not pull state out from under a payment', async () => {
    let release!: (o: PaycrestOrder) => void;
    m.payDirect.mockReturnValue(new Promise<PaycrestOrder>((r) => { release = r; }));
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.stage).toBe('paying');

    act(() => hook.reset());

    expect(hook.stage).toBe('paying');
    expect(swapped.reset).not.toHaveBeenCalled();
    await act(async () => { release(order); });
    await flush();
    expect(hook.stage).toBe('waiting');
  });

  it('after a swap that may be under way it still resets: her tokens end up in HER wallet, so freeing the payment strands nothing', async () => {
    Object.assign(swapped, { stage: 'failed', error: 'x', retryable: false, intent });
    await mount();
    expect(hook.retryable).toBe(false);
    swapped.reset.mockImplementation(() => { Object.assign(swapped, { stage: 'idle', error: undefined, retryable: true }); });
    act(() => hook.reset());
    await act(async () => { renderer.update(<Harness c={checkout} />); });
    expect(swapped.reset).toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });

  it('returns the payment to idle — hers and the swapped sell’s state both', async () => {
    m.send.mockRejectedValue(new Error('boom'));
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.stage).toBe('failed');

    act(() => hook.reset());

    expect(hook.stage).toBe('idle');
    expect(hook.error).toBeUndefined();
    expect(hook.order).toBeUndefined();
    expect(swapped.reset).toHaveBeenCalled();
  });
});

describe('one attempt at a time', () => {
  it('a double biometric success starts one payment, not two', async () => {
    await mount();
    act(() => hook.payWith(usdcBase, BASE, ANY_PRICE));
    await act(async () => {
      hook.authorization.onAuthorized();
      hook.authorization.onAuthorized();
    });
    await flush();
    expect(m.payDirect).toHaveBeenCalledTimes(1);
    expect(m.send).toHaveBeenCalledTimes(1);
  });
});

describe('only a verified coin of the right name is a direct route', () => {
  const spamUsdc = { address: '0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef', symbol: 'USDC', decimals: 6 } as LiFiToken;

  it('a lookalike USDC from her holdings is swapped, never sold as the real one', async () => {
    await mount();
    await pay(spamUsdc, BASE);
    expect(m.payDirect).not.toHaveBeenCalled();
    expect(swapped.start).toHaveBeenCalledWith(expect.objectContaining({ token: { ...spamUsdc, chainId: BASE } }));
  });

  it('a coin whose LiFi coin key is another coin is not the venue’s token either', async () => {
    await mount();
    await pay({ ...usdcBase, coinKey: 'USDT' } as LiFiToken, BASE);
    expect(m.payDirect).not.toHaveBeenCalled();
  });

  it('its estimate is the swap’s, not the venue’s', async () => {
    m.getPayQuote.mockResolvedValue({ fromAmount: '33835000', usdcTarget: '33835000' });
    await mount();
    await expect(hook.estimate(spamUsdc, BASE)).resolves.toEqual({ pay: '33.835 USDC', fromAmount: '33835000' });
    expect(m.getPayQuote).toHaveBeenCalled();
  });
});

describe('refusals keep their machine code', () => {
  it('a KYC refusal from starting the payment is exposed by code, not by words', async () => {
    m.payConverted.mockRejectedValue(new PayError('Verify your identity first', 403, 'kyc_verification_required'));
    swapped.start.mockImplementation(pipelineStart);
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
    await expect(hook.estimate(usdcBase, BASE)).resolves.toEqual({ pay: '33.51 USDC', fromAmount: '33510000' });
    expect(m.getPayQuote).not.toHaveBeenCalled();
  });

  it('a converted route asks the server what the swap pulls, in her token’s decimals', async () => {
    m.getPayQuote.mockResolvedValue({ fromAmount: '84000000000000000', usdcTarget: '33835000' });
    await mount();
    await expect(hook.estimate(bnb, BSC)).resolves.toEqual({ pay: '0.084 BNB', fromAmount: '84000000000000000' });
    expect(m.getPayQuote).toHaveBeenCalledWith(ID, { sourceChainId: BSC, sourceToken: bnb.address, sourceAddress: '0xPayer' });
  });

  it('with no signable wallet it says so, rather than returning nothing', async () => {
    mockWallet.address = null;
    await mount();
    await expect(hook.estimate(bnb, BSC)).rejects.toThrow(/wallet/i);
  });
});

describe('the price she was shown is the price she pays', () => {
  const priced = { pay: '33.51 USDC', fromAmount: '33510000' };
  const pricedBnb = { pay: '0.084 BNB', fromAmount: '84000000000000000' };

  it('the authorization says how much: "Authorize payment of about 33.51 USDC"', async () => {
    await mount();
    act(() => hook.payWith(usdcBase, BASE, priced));
    expect(hook.authorization.title).toBe('Authorize payment of about 33.51 USDC');
  });

  it('Continue payment has its own title', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    act(() => hook.continuePayment());
    expect(hook.authorization.title).toBe('Authorize continuing your payment');
  });

  it('a direct route whose fresh amount moved past 2% is refused BEFORE an order is opened — retryable', async () => {
    m.getQuote.mockResolvedValue(quoteWith([{ ...venue, tokenAmount: 34.5 } as PaycrestVenue]));
    await mount();
    act(() => hook.payWith(usdcBase, BASE, priced));
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();

    expect(m.payDirect).not.toHaveBeenCalled();
    expect(m.send).not.toHaveBeenCalled();
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(true);
    expect(hook.error).toMatch(/price moved/i);
    expect(hook.error).toContain('34.5 USDC');
    expect(hook.error).toContain('33.51 USDC');
  });

  it('a direct route within 2% goes ahead', async () => {
    m.getQuote.mockResolvedValue(quoteWith([{ ...venue, tokenAmount: 34.1 } as PaycrestVenue]));
    await mount();
    act(() => hook.payWith(usdcBase, BASE, priced));
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(m.payDirect).toHaveBeenCalled();
  });

  it('a swapped route hands the pipeline the priced amount to hold the fresh quote to', async () => {
    await mount();
    act(() => hook.payWith(bnb, BSC, pricedBnb));
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(swapped.start).toHaveBeenCalledWith(expect.objectContaining({ pricedFromAmount: '84000000000000000' }));
  });

  it('a call with no priced amount is refused outright — nothing to authorize, nothing started', async () => {
    await mount();
    act(() => (hook.payWith as any)(bnb, BSC));
    expect(hook.authorization.visible).toBe(false);
    expect(swapped.start).not.toHaveBeenCalled();
  });
});

describe('the route refusing this token — the same reading as crypto', () => {
  const refusal = (status: number) => new PayError('any words the server likes', status);

  it.each([422, 503])('a %i on a direct start says try another token, retryable', async (status) => {
    m.payDirect.mockRejectedValue(refusal(status));
    await mount();
    await pay(usdcBase, BASE);
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(true);
    expect(hook.error).toMatch(/another token/i);
  });

  it.each([422, 503])('a %i on a converted start likewise, whatever words the swapped sell reports', async (status) => {
    m.payConverted.mockRejectedValue(refusal(status));
    swapped.start.mockImplementation(pipelineStart);
    Object.assign(swapped, { stage: 'failed', error: 'any words the server likes', retryable: true });
    await mount();
    await pay(bnb, BSC);
    expect(hook.error).toMatch(/another token/i);
    expect(hook.retryable).toBe(true);
  });

  it.each([422, 503])('a %i when pricing gives the same copy, not a raw server sentence', async (status) => {
    m.getPayQuote.mockRejectedValue(refusal(status));
    await mount();
    await expect(hook.estimate(bnb, BSC)).rejects.toThrow(/another token/i);
  });

  it('a refused converted start does not leave the hook refused for the next attempt', async () => {
    m.payConverted.mockRejectedValueOnce(refusal(422));
    swapped.start.mockImplementation(pipelineStart);
    Object.assign(swapped, { stage: 'failed', error: 'x', retryable: true });
    await mount();
    await pay(bnb, BSC);
    act(() => hook.reset());
    Object.assign(swapped, { stage: 'failed', error: 'a different failure', retryable: true });
    m.payConverted.mockRejectedValue(new PayError('a different failure', 500));
    await pay(bnb, BSC);
    expect(hook.error).toBe('a different failure');
  });
});

describe('the reservation window in words, for a naira payment', () => {
  it('nothing to continue says about 10 minutes', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    act(() => hook.continuePayment());
    m.getOpenSellIntent.mockResolvedValue(null);
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(hook.error).toContain('about 10 minutes');
    expect(hook.error).not.toMatch(/hour/i);
  });

  it('a swap still going says we will keep checking, with no time promised', async () => {
    Object.assign(swapped, { stage: 'failed', error: 'x', retryable: false, intent });
    await mount();
    expect(hook.error).toMatch(/keep checking/i);
    expect(hook.error).not.toMatch(/\d|minute|hour/i);
  });
});

describe('the deposit is bound to the price she was shown — by the SERVER’s order, not our own quote', () => {
  const priced = { pay: '33.51 USDC', fromAmount: '33510000' };
  const authorize = async () => {
    act(() => hook.payWith(usdcBase, BASE, priced));
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
  };

  it('an order opened for more than 2% above the price is NOT funded: no deposit, no claim', async () => {
    m.payDirect.mockResolvedValue({ ...order, tokenAmount: '35.00' } as PaycrestOrder); // our own quote said 33.5 and passed
    await mount();
    await authorize();

    expect(m.payDirect).toHaveBeenCalledTimes(1);
    expect(m.send).not.toHaveBeenCalled();
    expect(m.claimPayment).not.toHaveBeenCalled();
  });

  it('and the state is honest: price moved, nothing sent, held for the window, Pay hidden, nothing to continue', async () => {
    m.payDirect.mockResolvedValue({ ...order, tokenAmount: '35.00' } as PaycrestOrder);
    await mount();
    await authorize();

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false); // the payment is linked to an order: a tap would only be refused
    expect(hook.canContinue).toBe(false);
    expect(hook.error).toBe(directPriceMovedCopy('35000000', '33510000', 6, 'USDC'));
    expect(hook.error).toContain('about 10 minutes');
  });

  it('once the server frees the payment, it can be reset and paid again', async () => {
    m.payDirect.mockResolvedValue({ ...order, tokenAmount: '35.00' } as PaycrestOrder);
    await mount();
    await authorize();
    act(() => hook.reset());
    expect(hook.stage).toBe('idle');
    expect(hook.retryable).toBe(true);
  });

  it('an order within 2% is funded as before', async () => {
    m.payDirect.mockResolvedValue({ ...order, tokenAmount: '34.10' } as PaycrestOrder);
    await mount();
    await authorize();
    expect(m.send).toHaveBeenCalledTimes(1);
    expect(hook.stage).toBe('waiting');
  });

  it('an order whose amount cannot be read is not funded either', async () => {
    m.payDirect.mockResolvedValue({ ...order, tokenAmount: undefined } as unknown as PaycrestOrder);
    await mount();
    await authorize();
    expect(m.send).not.toHaveBeenCalled();
    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/nothing was sent/i);
  });
});

describe('one attempt, and only one that is accepted', () => {
  it('a payment the server paused does not call the start route again', async () => {
    m.payDirect.mockRejectedValue(new PayError('x', 409, 'pay_attempt_uncertain'));
    await mount();
    await pay(usdcBase, BASE);
    expect(m.payDirect).toHaveBeenCalledTimes(1);
    await pay(usdcBase, BASE);
    expect(m.payDirect).toHaveBeenCalledTimes(1);
    expect(m.getQuote).toHaveBeenCalledTimes(1); // not even the rate lookup
    expect(hook.stage).toBe('failed');
  });
});

describe('a resume that finds nothing left to follow', () => {
  it('ends in a wait on the server — the attempt does not stay "paying" forever', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    act(() => hook.continuePayment());
    // The sale finished between the read and the resume: the pipeline has nothing to do and says "idle".
    swapped.resume.mockImplementation(async () => { latestOpts.onStage('idle', { intent }); });
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
    expect(hook.stage).toBe('waiting');
  });
});

describe('an empty price is no price', () => {
  it('payWith with a price that has no base-unit amount authorizes nothing', async () => {
    await mount();
    act(() => hook.payWith(usdcBase, BASE, { pay: '33.5 USDC', fromAmount: '' }));
    expect(hook.authorization.visible).toBe(false);
  });
});

describe('the order must be for what she chose', () => {
  const priced = { pay: '33.51 USDC', fromAmount: '33510000' };
  const authorize = async () => {
    act(() => hook.payWith(usdcBase, BASE, priced));
    await act(async () => { hook.authorization.onAuthorized(); });
    await flush();
  };

  it.each([
    ['another network', { chain: 'polygon' }],
    ['another token', { token: 'USDT' }],
  ])('an order on %s is not funded: no deposit, non-retryable, honest copy', async (_n, over) => {
    m.payDirect.mockResolvedValue({ ...order, tokenAmount: '33.5', ...over } as PaycrestOrder);
    await mount();
    await authorize();
    expect(m.send).not.toHaveBeenCalled();
    expect(m.claimPayment).not.toHaveBeenCalled();
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(false);
    expect(hook.error).toMatch(/nothing was sent/i);
    expect(hook.error).toContain('about 10 minutes');
  });

  it('the same asset in another letter case is fine — compared the way the venue match compares them', async () => {
    m.payDirect.mockResolvedValue({ ...order, tokenAmount: '33.5', chain: 'BASE', token: 'usdc' } as PaycrestOrder);
    await mount();
    await authorize();
    expect(m.send).toHaveBeenCalledTimes(1);
  });
});
