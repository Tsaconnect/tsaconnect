/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * usePayNaira on top of the REAL useSwappedSell (the other test file replaces it
 * with a stub, so how its stages, stops and hand-offs are read is only tested
 * there against fakes). Only the network and the wallet are mocked here.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/services/payApi', () => ({
  __esModule: true,
  payDirect: jest.fn(), payConverted: jest.fn(), getPayQuote: jest.fn(),
}));
jest.mock('@/services/paycrestApi', () => ({
  ...jest.requireActual('@/services/paycrestApi'),
  getQuote: jest.fn(), claimPayment: jest.fn(), getOrder: jest.fn(),
}));
jest.mock('@/services/paycrestSellSwap', () => ({
  __esModule: true,
  createSellIntent: jest.fn(), recordSellSwap: jest.fn(), openSellOrder: jest.fn(), requestSellIntentGasTopUp: jest.fn(),
  abandonSellIntent: jest.fn(), getOpenSellIntent: jest.fn(),
}));
jest.mock('@/services/lifiFlow', () => ({
  __esModule: true,
  executeLiFiSwap: jest.fn(),
  SwapBroadcastUnconfirmedError: class extends Error {},
}));
jest.mock('@/services/lifi', () => ({ ...jest.requireActual('@/services/lifi'), getSwapDelivery: jest.fn() }));
jest.mock('@/services/paycrestDeposit', () => ({
  __esModule: true,
  checkFunding: jest.fn(), sendSponsoredPaycrestDeposit: jest.fn(), fundingMessage: jest.fn(() => null),
  nativeBalanceFor: jest.fn(() => 0.5), fmtTokenAmount: (n: number) => String(n),
}));
jest.mock('@/services/walletApi', () => ({
  __esModule: true, getWalletBalances: jest.fn(async () => ({ success: true, data: [] })), normalizeWalletBalances: (d: unknown) => d,
}));
jest.mock('@/services/gasSponsorship', () => ({
  __esModule: true, getSponsoredRoutes: jest.fn(async () => []), isGasSponsored: () => false,
}));
jest.mock('@/services/wallet', () => ({ __esModule: true, nativeBalance: jest.fn(async () => 0n) }));
jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true, useSignableWallet: () => ({ address: '0xPayer', ready: true, refresh: jest.fn() }),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as PayApi from '@/services/payApi';
import * as PaycrestApi from '@/services/paycrestApi';
import * as SellSwap from '@/services/paycrestSellSwap';
import * as LifiFlow from '@/services/lifiFlow';
import * as Lifi from '@/services/lifi';
import * as Deposit from '@/services/paycrestDeposit';
import { PaycrestError, type PaycrestOrder, type PaycrestVenue } from '@/services/paycrestApi';
import type { LiFiQuote, LiFiToken } from '@/services/lifi';
import type { SellIntent } from '@/services/paycrestSellSwap';
import type { PayCheckout } from '@/services/payApi';
import { usePayNaira, type UsePayNaira } from '../usePayNaira';

const m = {
  payConverted: jest.mocked(PayApi.payConverted),
  getQuote: jest.mocked(PaycrestApi.getQuote),
  claimPayment: jest.mocked(PaycrestApi.claimPayment),
  openSellOrder: jest.mocked(SellSwap.openSellOrder),
  recordSellSwap: jest.mocked(SellSwap.recordSellSwap),
  abandon: jest.mocked(SellSwap.abandonSellIntent),
  exec: jest.mocked(LifiFlow.executeLiFiSwap),
  delivery: jest.mocked(Lifi.getSwapDelivery),
  send: jest.mocked(Deposit.sendSponsoredPaycrestDeposit),
};

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const checkout = { id: ID, mode: 'live', settlementMode: 'fiat', merchantNet: 5_000_000 } as PayCheckout;
const bnb = { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', decimals: 18 } as LiFiToken;
const order = { id: 'o1', chain: 'base', token: 'USDC', status: 'awaiting_deposit', walletAddress: '0xPayer' } as PaycrestOrder;
const intent = { id: 'i1', status: 'swapping', payPaymentId: ID } as SellIntent;
const quoteOf = (fromAmount: string) =>
  ({ fromAmount, transactionRequest: { value: `0x${BigInt(fromAmount).toString(16)}` } }) as unknown as LiFiQuote;
const priced = { pay: '0.084 BNB', fromAmount: '84000000000000000' };

jest.setTimeout(30_000);

let hook: UsePayNaira;
const Harness = () => { hook = usePayNaira(checkout); return null; };
let renderer: ReactTestRenderer;

/** Let promises settle, stepping the 15 s swap-status poll clock as it goes. */
const settle = () => act(async () => {
  for (let i = 0; i < 200; i++) {
    await new Promise((r) => setImmediate(r));
    jest.advanceTimersByTime(15_000);
  }
});
async function pay() {
  act(() => hook.payWith(bnb, 56, priced));
  await act(async () => { hook.authorization.onAuthorized(); });
  await settle();
}

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  m.getQuote.mockResolvedValue({ venues: [{ chain: 'base', token: 'USDC', tokenAmount: 33.5 } as PaycrestVenue], best: null } as any);
  m.payConverted.mockResolvedValue({ intent, quote: quoteOf('84000000000000000'), gasTopUp: false });
  m.exec.mockResolvedValue('0xswap');
  m.recordSellSwap.mockResolvedValue(undefined);
  m.abandon.mockResolvedValue(undefined);
  m.delivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
  m.openSellOrder.mockResolvedValue(order);
  m.send.mockResolvedValue('0xdeposit');
  m.claimPayment.mockResolvedValue(order);
  await act(async () => { renderer = TestRenderer.create(<Harness />); });
});
afterEach(() => { act(() => renderer.unmount()); jest.useRealTimers(); });

it('a swapped payment runs the real pipeline start to finish and hands over to the server', async () => {
  await pay();
  expect(m.payConverted).toHaveBeenCalledWith(ID, { sourceChainId: 56, sourceToken: bnb.address, sourceAddress: '0xPayer' });
  expect(m.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap');
  expect(m.openSellOrder).toHaveBeenCalledWith('i1');
  expect(m.send).toHaveBeenCalledWith(order, '0xPayer', expect.anything());
  expect(hook.stage).toBe('waiting'); // the pipeline's 'done' read as the payment's wait
  expect(hook.swapStage).toBe('done');
  expect(hook.retryable).toBe(true);
});

it('a swap that will not confirm stops as "don\'t pay again": payment words, Continue offered, Pay withheld', async () => {
  m.delivery.mockResolvedValue({ status: 'PENDING' });
  await pay();
  expect(hook.stage).toBe('failed');
  expect(hook.retryable).toBe(false);
  expect(hook.error).toMatch(/don't pay again/i);
  expect(hook.error).not.toMatch(/Finish your sale/);
  expect(hook.canContinue).toBe(true);
});

it('a shortfall stops at its own stage, and "Use my USDC" (authorized) finishes it through the real pipeline', async () => {
  m.openSellOrder.mockRejectedValueOnce(new PaycrestError('short', 409, 'pay_shortfall', '0.42'));
  await pay();
  expect(hook.stage).toBe('shortfall');
  expect(hook.missingUsdc).toBe('0.42');
  expect(m.abandon).not.toHaveBeenCalled();

  act(() => hook.completeWithWallet());
  expect(hook.authorization.title).toBe('Authorize paying about 0.42 USDC from your wallet');
  await act(async () => { hook.authorization.onAuthorized(); });
  await settle();

  expect(m.openSellOrder).toHaveBeenLastCalledWith('i1', { useWalletBalance: true });
  expect(m.send).toHaveBeenCalledTimes(1);
  expect(hook.stage).toBe('waiting');
});

it('a fresh quote past the priced amount is refused before signing: retryable, closed, in payment words', async () => {
  m.payConverted.mockResolvedValue({ intent, quote: quoteOf('99000000000000000'), gasTopUp: false });
  await pay();
  expect(m.exec).not.toHaveBeenCalled();
  expect(m.abandon).toHaveBeenCalledWith('i1');
  expect(hook.stage).toBe('failed');
  expect(hook.retryable).toBe(true);
  expect(hook.error).toMatch(/price moved/i);
});

it('the sale is handed back from the payment to the pipeline and forth without leaving a stale stage', async () => {
  m.getQuote.mockResolvedValue({ venues: [], best: null } as any);
  const stages: string[] = [];
  m.recordSellSwap.mockImplementation(async () => { stages.push(hook.stage); });
  await pay();
  expect(stages).toEqual(['paying']); // mid-run the payment reads as paying, not idle or failed
  expect(hook.stage).toBe('waiting');
});
