/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The swapped sell, from her side: one tap, and the hook does the rest — the
 * swap, the wait, the payout order, the deposit. What these pin:
 *   - the swap hash is on the phone BEFORE the server hears it, so a kill
 *     between the two loses nothing;
 *   - each way a swap can end short gets its own honest sentence;
 *   - a killed sale picks up where it stopped, without swapping twice.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/services/paycrestSellSwap', () => ({
  __esModule: true,
  createSellIntent: jest.fn(),
  recordSellSwap: jest.fn(),
  openSellOrder: jest.fn(),
  requestSellIntentGasTopUp: jest.fn(),
  abandonSellIntent: jest.fn(),
  getOpenSellIntent: jest.fn(),
  getSellSwapQuote: jest.fn(),
}));
jest.mock('@/services/lifiFlow', () => ({ __esModule: true, executeLiFiSwap: jest.fn() }));
jest.mock('@/services/lifi', () => ({
  ...jest.requireActual('@/services/lifi'),
  getSwapDelivery: jest.fn(),
}));
jest.mock('@/services/paycrestDeposit', () => ({ __esModule: true, sendSponsoredPaycrestDeposit: jest.fn() }));
jest.mock('@/services/paycrestApi', () => ({
  ...jest.requireActual('@/services/paycrestApi'),
  claimPayment: jest.fn(),
  getOrder: jest.fn(),
}));
jest.mock('@/services/wallet', () => ({ __esModule: true, nativeBalance: jest.fn() }));

import React from 'react';
import { act, create } from 'react-test-renderer';
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as SellSwap from '@/services/paycrestSellSwap';
import * as LifiFlow from '@/services/lifiFlow';
import * as Lifi from '@/services/lifi';
import * as Deposit from '@/services/paycrestDeposit';
import * as PaycrestApi from '@/services/paycrestApi';
import * as Wallet from '@/services/wallet';
import { PaycrestError, type PaycrestOrder } from '@/services/paycrestApi';
import type { LiFiQuote, LiFiToken } from '@/services/lifi';
import type { SellIntent } from '@/services/paycrestSellSwap';
import { useSwappedSell, type StartSellArgs } from '../useSwappedSell';

const mocked = {
  ...jest.mocked(SellSwap),
  ...jest.mocked(LifiFlow),
  getSwapDelivery: jest.mocked(Lifi.getSwapDelivery),
  ...jest.mocked(Deposit),
  claimPayment: jest.mocked(PaycrestApi.claimPayment),
  getOrder: jest.mocked(PaycrestApi.getOrder),
  ...jest.mocked(Wallet),
};

jest.setTimeout(30_000);

let hook!: ReturnType<typeof useSwappedSell>;
function Probe() {
  hook = useSwappedSell();
  return null;
}

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
  mocked.nativeBalance.mockResolvedValue(0n);
  mocked.recordSellSwap.mockResolvedValue(undefined);
  mocked.abandonSellIntent.mockResolvedValue(undefined);
  mocked.claimPayment.mockResolvedValue({} as PaycrestOrder);
  await act(async () => { create(<Probe />); });
});

afterEach(() => { jest.useRealTimers(); });

const q = {} as LiFiQuote;
const order = { id: 'o1', status: 'awaiting_deposit', chain: 'base', token: 'USDC' } as PaycrestOrder;
const args: StartSellArgs = {
  sourceChainId: 56,
  token: { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', decimals: 18 } as LiFiToken,
  amount: '0.016', walletAddress: '0xUser', sourceAddress: '0xUser', bank: { code: '058', account: '0123456789' },
};
const KEY = 'paycrest.sellSwap.pendingTx.i1';

/** Drive the hook to its end: flush promises, step the 15 s poll clock, repeat. */
async function run(p: Promise<void>) {
  let settled = false;
  const done = p.finally(() => { settled = true; });
  await act(async () => {
    for (let i = 0; i < 200 && !settled; i++) {
      await new Promise((r) => setImmediate(r));
      jest.advanceTimersByTime(15_000);
    }
    await done;
  });
}

const intentCreated = () =>
  mocked.createSellIntent.mockResolvedValue({ intent: { id: 'i1', status: 'swapping' } as SellIntent, quote: q });

it('runs intent → swap → wait → order → send, persisting the hash before reporting it', async () => {
  intentCreated();
  mocked.executeLiFiSwap.mockImplementation(async () => {
    expect(mocked.recordSellSwap).not.toHaveBeenCalled();
    return '0xswap';
  });
  // The hash must already be on the phone when the server is told about it.
  mocked.recordSellSwap.mockImplementation(async () => {
    expect(await AsyncStorage.getItem(KEY)).toBe('0xswap');
  });
  mocked.getSwapDelivery
    .mockResolvedValueOnce({ status: 'PENDING' })
    .mockResolvedValueOnce({ status: 'DONE', substatus: 'COMPLETED' });
  mocked.openSellOrder.mockResolvedValue(order);
  mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');

  await run(hook.start(args));

  expect(mocked.createSellIntent).toHaveBeenCalledWith(expect.objectContaining({
    sourceChainId: 56, sourceToken: args.token.address, sourceAmount: '16000000000000000',
    sourceAddress: '0xUser', bankCode: '058', accountNumber: '0123456789',
  }));
  expect(mocked.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap');
  expect(mocked.openSellOrder).toHaveBeenCalledWith('i1');
  expect(mocked.sendSponsoredPaycrestDeposit).toHaveBeenCalledWith(order, '0xUser', expect.anything());
  expect(mocked.claimPayment).toHaveBeenCalledWith('o1');
  expect(await AsyncStorage.getItem(KEY)).toBeNull(); // cleared on success
  expect(hook.stage).toBe('done');
  expect(hook.order?.id).toBe('o1');
  expect(hook.depositTx).toBe('0xdeposit');
});

it('persists the hash even when reporting it fails, so the sale can be finished later', async () => {
  intentCreated();
  mocked.executeLiFiSwap.mockResolvedValue('0xswap');
  mocked.recordSellSwap.mockRejectedValue(new Error('offline'));
  await run(hook.start(args));
  expect(await AsyncStorage.getItem(KEY)).toBe('0xswap');
  expect(hook.stage).toBe('failed');
  expect(hook.error).toMatch(/finish your sale/i);
  expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
});

describe('each way a swap can end short says so honestly', () => {
  beforeEach(() => {
    intentCreated();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
  });

  it('FAILED: her tokens never left', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'FAILED' });
    await run(hook.start(args));
    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/still in your wallet/i);
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
    expect(mocked.openSellOrder).not.toHaveBeenCalled();
  });

  it('REFUNDED: her tokens are on their way back', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'REFUNDED' });
    await run(hook.start(args));
    expect(hook.error).toMatch(/refunded/i);
    expect(hook.error).toMatch(/on their way back/i);
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
    expect(mocked.openSellOrder).not.toHaveBeenCalled();
  });

  it('PARTIAL: she got something, not what was quoted — check the wallet, contact support', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'PARTIAL' });
    await run(hook.start(args));
    expect(hook.error).toMatch(/check your wallet/i);
    expect(hook.error).toMatch(/support/i);
    expect(hook.error).not.toMatch(/didn't go through|still in your wallet/i);
    expect(mocked.openSellOrder).not.toHaveBeenCalled();
  });

  it('never collapses the three into one sentence', async () => {
    const seen: string[] = [];
    for (const d of [{ status: 'FAILED' }, { status: 'DONE', substatus: 'REFUNDED' }, { status: 'DONE', substatus: 'PARTIAL' }] as const) {
      mocked.getSwapDelivery.mockResolvedValue(d);
      await run(hook.start(args));
      seen.push(hook.error ?? '');
    }
    expect(new Set(seen).size).toBe(3);
  });

  it("reads the server's own verdict when the status feed carried no substatus", async () => {
    // DONE with no qualifier: the server re-checks and names the outcome.
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE' });
    mocked.openSellOrder.mockRejectedValue(new PaycrestError(
      "your swap didn't complete (DONE/REFUNDED) — nothing was sold; check your wallet", 409));
    await run(hook.start(args));
    expect(hook.error).toMatch(/on their way back/i);
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  });
});

it('waits out "still swapping" from the server rather than failing the sale', async () => {
  intentCreated();
  mocked.executeLiFiSwap.mockResolvedValue('0xswap');
  mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
  mocked.openSellOrder
    .mockRejectedValueOnce(new PaycrestError('Still swapping — try again in a moment', 409))
    .mockResolvedValueOnce(order);
  mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  await run(hook.start(args));
  expect(mocked.openSellOrder).toHaveBeenCalledTimes(2);
  expect(hook.stage).toBe('done');
});

it('never abandons a sale once the swap has landed, even when the send fails', async () => {
  intentCreated();
  mocked.executeLiFiSwap.mockResolvedValue('0xswap');
  mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
  mocked.openSellOrder.mockResolvedValue(order);
  mocked.sendSponsoredPaycrestDeposit.mockRejectedValue(new Error('relay down'));
  await run(hook.start(args));
  expect(hook.stage).toBe('failed');
  expect(hook.error).toMatch(/relay down/);
  // The order stays in hand so the screen can offer the send again.
  expect(hook.order?.id).toBe('o1');
  expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
});

it('frees the sale when the swap could not be sent at all', async () => {
  intentCreated();
  mocked.executeLiFiSwap.mockRejectedValue(new Error('Swap broadcast failed.'));
  await run(hook.start(args));
  expect(hook.stage).toBe('failed');
  expect(hook.error).toMatch(/Swap broadcast failed/);
  expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  expect(mocked.recordSellSwap).not.toHaveBeenCalled();
});

describe('source-chain gas', () => {
  const ercArgs: StartSellArgs = {
    ...args, token: { address: '0x55d398326f99059fF775485246999027B3197955', symbol: 'USDT', decimals: 18 } as LiFiToken,
    amount: '5',
  };
  beforeEach(() => {
    mocked.createSellIntent.mockResolvedValue({
      intent: { id: 'i1', status: 'swapping' } as SellIntent, quote: { gasCostWei: '1000' } as LiFiQuote,
    });
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'FAILED' });
  });

  it('asks for a top-up when her gas coin is short of the route', async () => {
    mocked.nativeBalance.mockResolvedValue(10n);
    mocked.requestSellIntentGasTopUp.mockResolvedValue({ txHash: '0xgas', skipped: false });
    await run(hook.start(ercArgs));
    expect(mocked.nativeBalance).toHaveBeenCalledWith('bsc', '0xUser');
    expect(mocked.requestSellIntentGasTopUp).toHaveBeenCalledWith('i1');
    expect(mocked.executeLiFiSwap).toHaveBeenCalled();
  });

  it('treats an unreadable balance as unknown and just tries the swap', async () => {
    mocked.nativeBalance.mockResolvedValue(null);
    await run(hook.start(ercArgs));
    expect(mocked.requestSellIntentGasTopUp).not.toHaveBeenCalled();
    expect(mocked.executeLiFiSwap).toHaveBeenCalled();
  });

  it('carries on silently when a top-up fails, and names the gas if the swap then fails', async () => {
    mocked.nativeBalance.mockResolvedValue(10n);
    mocked.requestSellIntentGasTopUp.mockRejectedValue(new Error('daily cap'));
    mocked.executeLiFiSwap.mockRejectedValue(new Error('insufficient funds for gas'));
    await run(hook.start(ercArgs));
    expect(mocked.executeLiFiSwap).toHaveBeenCalled();
    expect(hook.error).toMatch(/BNB/);
    expect(hook.error).toMatch(/network fee/i);
    expect(hook.error).not.toMatch(/daily cap/);
  });
});

it('resumes a killed sale from its stored hash', async () => {
  await AsyncStorage.setItem(KEY, '0xswap');
  mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
  mocked.openSellOrder.mockResolvedValue(order);
  mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  await run(hook.resume({ id: 'i1', status: 'swapping' } as SellIntent));
  expect(mocked.executeLiFiSwap).not.toHaveBeenCalled();
  expect(mocked.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap');
  expect(mocked.openSellOrder).toHaveBeenCalledWith('i1');
  expect(hook.stage).toBe('done');
});

it('resumes a recorded sale from the server copy of the hash', async () => {
  mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
  mocked.openSellOrder.mockResolvedValue(order);
  mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  await run(hook.resume({ id: 'i1', status: 'swapped', swapTxHash: '0xserver' } as SellIntent));
  expect(mocked.getSwapDelivery).toHaveBeenCalledWith('0xserver');
  expect(mocked.recordSellSwap).not.toHaveBeenCalled();
  expect(hook.stage).toBe('done');
});

it('resumes an ordered sale by sending if the order still awaits a deposit', async () => {
  mocked.getOrder.mockResolvedValue(order);
  mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  await run(hook.resume({ id: 'i1', status: 'ordered', paycrestOrderId: 'o1' } as SellIntent));
  expect(mocked.getOrder).toHaveBeenCalledWith('o1');
  expect(mocked.sendSponsoredPaycrestDeposit).toHaveBeenCalled();
  expect(hook.stage).toBe('done');
});

it('does not send twice when a resumed order is already paid', async () => {
  mocked.getOrder.mockResolvedValue({ ...order, status: 'settling' } as PaycrestOrder);
  await run(hook.resume({ id: 'i1', status: 'ordered', paycrestOrderId: 'o1' } as SellIntent));
  expect(mocked.sendSponsoredPaycrestDeposit).not.toHaveBeenCalled();
  expect(hook.stage).toBe('done');
});

it('closes a sale whose swap never started, since nothing can be recovered', async () => {
  await run(hook.resume({ id: 'i1', status: 'swapping' } as SellIntent));
  expect(mocked.executeLiFiSwap).not.toHaveBeenCalled();
  expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  expect(hook.error).toMatch(/nothing was sold/i);
});
