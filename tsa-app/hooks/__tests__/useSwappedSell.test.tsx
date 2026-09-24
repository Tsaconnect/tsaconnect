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
jest.mock('@/services/lifiFlow', () => ({
  __esModule: true,
  executeLiFiSwap: jest.fn(),
  SwapBroadcastUnconfirmedError: class extends Error {
    txHash?: string;
    constructor(mockHash?: string) { super("We couldn't confirm the swap was sent."); this.txHash = mockHash; }
  },
}));
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
import { canCancelSale, useSwappedSell, type StartSellArgs } from '../useSwappedSell';

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
/** A second screen's copy of the hook: she backed out and came back. */
let other!: ReturnType<typeof useSwappedSell>;
function OtherProbe() {
  other = useSwappedSell();
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
  await act(async () => { create(<Probe />); create(<OtherProbe />); });
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

/** Let pending promises run without stepping the clock. */
const flush = () => act(async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r)); });

const intentCreated = () =>
  mocked.createSellIntent.mockResolvedValue({ intent: { id: 'i1', status: 'swapping' } as SellIntent, quote: q, gasTopUp: false });

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

it('frees the sale when the swap was refused before or at the node', async () => {
  intentCreated();
  mocked.executeLiFiSwap.mockRejectedValue(new Error('Swap broadcast failed.'));
  await run(hook.start(args));
  expect(hook.stage).toBe('failed');
  expect(hook.error).toMatch(/Swap broadcast failed/);
  expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  expect(mocked.recordSellSwap).not.toHaveBeenCalled();
});

describe('source-chain gas: asked for exactly when the server priced it', () => {
  const ercArgs: StartSellArgs = {
    ...args, token: { address: '0x55d398326f99059fF775485246999027B3197955', symbol: 'USDT', decimals: 18 } as LiFiToken,
    amount: '5',
  };
  // The quote's gasCostWei is the swap's gas ALONE — the threshold the app
  // used to judge by, and the one the server stopped using (it counts the
  // approve as well). Fixtures where the two disagree are the whole point.
  const created = (gasTopUp: boolean) => mocked.createSellIntent.mockResolvedValue({
    intent: { id: 'i1', status: 'swapping' } as SellIntent, quote: { gasCostWei: '1000' } as LiFiQuote, gasTopUp,
  });
  beforeEach(() => {
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'FAILED' });
  });

  it('asks for the top-up the server priced, even when her balance covers the swap alone', async () => {
    created(true);
    // 1,500 wei: more than the swap's 1,000, less than swap + approve. The old
    // local check read this as "funded" and skipped gas the sale was budgeted for.
    mocked.nativeBalance.mockResolvedValue(1_500n);
    mocked.requestSellIntentGasTopUp.mockResolvedValue({ txHash: '0xgas', skipped: false });
    await run(hook.start(ercArgs));
    expect(mocked.requestSellIntentGasTopUp).toHaveBeenCalledWith('i1');
    expect(mocked.executeLiFiSwap).toHaveBeenCalled();
    // The top-up was asked for BEFORE the swap was signed.
    expect(mocked.requestSellIntentGasTopUp.mock.invocationCallOrder[0])
      .toBeLessThan(mocked.executeLiFiSwap.mock.invocationCallOrder[0]);
  });

  it('asks for none when the server priced none, however short her balance looks', async () => {
    created(false);
    mocked.nativeBalance.mockResolvedValue(0n);
    await run(hook.start(ercArgs));
    expect(mocked.requestSellIntentGasTopUp).not.toHaveBeenCalled();
    expect(mocked.executeLiFiSwap).toHaveBeenCalled();
  });

  it('never reads her source-chain balance to decide', async () => {
    created(true);
    mocked.requestSellIntentGasTopUp.mockResolvedValue({ skipped: true });
    await run(hook.start(ercArgs));
    expect(mocked.nativeBalance).not.toHaveBeenCalledWith('bsc', expect.anything());
  });

  it('carries on silently when a top-up fails, and names the gas if the swap then fails', async () => {
    created(true);
    mocked.requestSellIntentGasTopUp.mockRejectedValue(new Error('daily cap'));
    mocked.executeLiFiSwap.mockRejectedValue(new Error('insufficient funds for gas'));
    await run(hook.start(ercArgs));
    expect(mocked.executeLiFiSwap).toHaveBeenCalled();
    expect(hook.error).toMatch(/BNB/);
    expect(hook.error).toMatch(/network fee/i);
    expect(hook.error).not.toMatch(/daily cap/);
  });
});

describe('cancelling a stuck sale', () => {
  const at = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString();

  it('is offered only once a pre-payout sale has sat 10 minutes', () => {
    expect(canCancelSale({ id: 'i1', status: 'swapped', updatedAt: at(11) } as SellIntent)).toBe(true);
    expect(canCancelSale({ id: 'i1', status: 'swapping', updatedAt: at(10) } as SellIntent)).toBe(true);
    // Still possibly in flight.
    expect(canCancelSale({ id: 'i1', status: 'swapped', updatedAt: at(9) } as SellIntent)).toBe(false);
    // The payout is being or has been opened: the server refuses, so it is never offered.
    expect(canCancelSale({ id: 'i1', status: 'ordering', updatedAt: at(60) } as SellIntent)).toBe(false);
    expect(canCancelSale({ id: 'i1', status: 'ordered', updatedAt: at(60) } as SellIntent)).toBe(false);
    // An unknown time never qualifies.
    expect(canCancelSale({ id: 'i1', status: 'swapped' } as SellIntent)).toBe(false);
  });

  it('closes the sale and drops its stored hash', async () => {
    await AsyncStorage.setItem(KEY, '0xswap');
    await act(async () => { await hook.cancel({ id: 'i1', status: 'swapped' } as SellIntent); });
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it("won't cancel a sale this app is still driving", async () => {
    intentCreated();
    let release!: (hash: string) => void;
    mocked.executeLiFiSwap.mockImplementation(() => new Promise<string>((r) => { release = r; }));
    mocked.getSwapDelivery.mockResolvedValue({ status: 'FAILED' });
    const first = hook.start(args);
    await flush();
    await expect(other.cancel({ id: 'i1', status: 'swapping' } as SellIntent)).rejects.toThrow(/still going through/i);
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    release('0xswap');
    await run(first);
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

it('closes a sale with no swap to follow when no run of it is going, and says only what it knows', async () => {
  await run(hook.resume({ id: 'i1', status: 'swapping' } as SellIntent));
  expect(mocked.executeLiFiSwap).not.toHaveBeenCalled();
  expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  expect(hook.error).toMatch(/couldn't find a swap/i);
  expect(hook.error).not.toMatch(/never started|nothing was sold/i);
});

describe('one sale, one run — across every copy of the hook', () => {
  it("won't close a sale another screen is still swapping (she backed out and tapped Finish your sale)", async () => {
    intentCreated();
    let release!: (hash: string) => void;
    mocked.executeLiFiSwap.mockImplementation(() => new Promise<string>((r) => { release = r; }));
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockResolvedValue(order);
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');

    const first = hook.start(args); // the screen she left: stuck waiting on the approval
    await flush();
    expect(mocked.executeLiFiSwap).toHaveBeenCalled();

    // The new screen sees the open sale, with no hash anywhere yet.
    await run(other.resume({ id: 'i1', status: 'swapping' } as SellIntent));
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    expect(other.error).not.toMatch(/never started|nothing was sold/i);
    expect(other.error).toMatch(/still going through/i);

    // The first run carries on and finishes the sale it owns.
    release('0xswap');
    await run(first);
    expect(mocked.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap');
    expect(hook.stage).toBe('done');
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
  });

  it("won't open a second sale while another screen's is still running", async () => {
    intentCreated();
    let release!: (hash: string) => void;
    mocked.executeLiFiSwap.mockImplementation(() => new Promise<string>((r) => { release = r; }));
    mocked.getSwapDelivery.mockResolvedValue({ status: 'FAILED' });

    const first = hook.start(args);
    await flush();
    await run(other.start(args));
    expect(mocked.createSellIntent).toHaveBeenCalledTimes(1);
    expect(other.error).toMatch(/still going through/i);

    release('0xswap');
    await run(first);
  });

  it('frees the sale for resume once the run that held it ends', async () => {
    intentCreated();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.recordSellSwap.mockRejectedValue(new Error('offline'));
    await run(hook.start(args));
    expect(hook.error).toMatch(/finish your sale/i);

    mocked.recordSellSwap.mockResolvedValue(undefined);
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockResolvedValue(order);
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
    await run(other.resume({ id: 'i1', status: 'swapping' } as SellIntent));
    expect(other.stage).toBe('done');
  });
});

describe('a swap whose broadcast is unconfirmed', () => {
  beforeEach(() => { intentCreated(); });

  it('keeps the signed hash and the sale when the reply was lost, so Finish your sale can follow it', async () => {
    mocked.executeLiFiSwap.mockImplementation(async (p) => {
      await p.onSigned?.('0xsigned');
      // Persisted before the broadcast call, not after it returns.
      expect(await AsyncStorage.getItem(KEY)).toBe('0xsigned');
      throw new LifiFlow.SwapBroadcastUnconfirmedError('0xsigned');
    });
    await run(hook.start(args));
    expect(await AsyncStorage.getItem(KEY)).toBe('0xsigned');
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    expect(hook.error).toMatch(/finish your sale/i);
    expect(hook.error).not.toMatch(/nothing was sold/i);

    // Later: the resume follows that hash rather than closing the sale.
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockResolvedValue(order);
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
    await run(hook.resume({ id: 'i1', status: 'swapping' } as SellIntent));
    expect(mocked.recordSellSwap).toHaveBeenCalledWith('i1', '0xsigned');
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    expect(hook.stage).toBe('done');
  });

  it('drops the signed hash and frees the sale when the server refused the swap outright', async () => {
    mocked.executeLiFiSwap.mockImplementation(async (p) => {
      await p.onSigned?.('0xsigned');
      throw new Error('insufficient funds for gas');
    });
    await run(hook.start(args));
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  });
});

describe('the payout failing to open says nothing about her USDC it cannot back', () => {
  beforeEach(() => {
    intentCreated();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
  });
  const USDC_CLAIM = /USDC is in your wallet/i;

  it('a plain network error', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE' });
    mocked.openSellOrder.mockRejectedValue(new Error('Network request failed'));
    await run(hook.start(args));
    expect(hook.stage).toBe('failed');
    expect(hook.error).not.toMatch(USDC_CLAIM);
    expect(hook.error).toMatch(/finish your sale/i);
  });

  it('a 409 naming a refund in words the app does not parse', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE' });
    mocked.openSellOrder.mockRejectedValue(new PaycrestError('the bridge refunded this swap', 409));
    await run(hook.start(args));
    expect(hook.error).not.toMatch(USDC_CLAIM);
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
  });

  it("the server's REFUNDED verdict", async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE' });
    mocked.openSellOrder.mockRejectedValue(new PaycrestError(
      "your swap didn't complete (DONE/REFUNDED) — nothing was sold; check your wallet", 409));
    await run(hook.start(args));
    expect(hook.error).not.toMatch(USDC_CLAIM);
  });

  it('"still swapping" that never clears, without a confirmed delivery', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE' });
    mocked.openSellOrder.mockRejectedValue(new PaycrestError('Still swapping — try again in a moment', 409));
    await run(hook.start(args));
    expect(hook.error).not.toMatch(USDC_CLAIM);
    expect(hook.error).toMatch(/finish your sale/i);
  });

  it('keeps the reassurance when LiFi itself confirmed the USDC landed', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockRejectedValue(new Error('Network request failed'));
    await run(hook.start(args));
    expect(hook.error).toMatch(USDC_CLAIM);
  });
});

describe("the server's verdict, read from its 409 when the poll carried no substatus", () => {
  beforeEach(() => {
    intentCreated();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE' });
  });

  it('(DONE/PARTIAL): check the wallet, contact support', async () => {
    mocked.openSellOrder.mockRejectedValue(new PaycrestError(
      "your swap didn't complete (DONE/PARTIAL) — nothing was sold; check your wallet", 409));
    await run(hook.start(args));
    expect(hook.error).toMatch(/different token/i);
    expect(hook.error).toMatch(/support/i);
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  });

  it('(FAILED): her tokens are still in her wallet', async () => {
    mocked.openSellOrder.mockRejectedValue(new PaycrestError(
      "your swap didn't complete (FAILED) — nothing was sold; check your wallet", 409));
    await run(hook.start(args));
    expect(hook.error).toMatch(/didn't go through/i);
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  });

  it('(FAILED/<qualifier>) reads the same way', async () => {
    mocked.openSellOrder.mockRejectedValue(new PaycrestError(
      "your swap didn't complete (FAILED/NOT_PROCESSABLE_REFUND_NEEDED) — nothing was sold; check your wallet", 409));
    await run(hook.start(args));
    expect(hook.error).toMatch(/didn't go through/i);
  });
});

// The server names each outcome with a code (tsa-api-go sellIntentError). The
// code decides, not the copy: these messages say nothing the old parser could
// read, so only the code can be what routes them. The wording tests above
// remain as the fallback for a server that sends no code.
describe("the server's verdict, read from its code", () => {
  beforeEach(() => {
    intentCreated();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE' });
  });

  it.each([
    ['swap_failed', /didn't go through/i],
    ['swap_refunded', /on their way back/i],
    ['swap_partial', /different token/i],
  ])('%s ends the sale with its own sentence', async (code, sentence) => {
    mocked.openSellOrder.mockRejectedValue(new PaycrestError('Reworded copy', 409, code));
    await run(hook.start(args));
    expect(hook.error).toMatch(sentence);
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  });

  it.each(['swap_pending', 'payout_opening'])('%s is a wait, not a failure', async (code) => {
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
    mocked.openSellOrder
      .mockRejectedValueOnce(new PaycrestError('Reworded copy', 409, code))
      .mockResolvedValueOnce(order);
    await run(hook.start(args));
    expect(mocked.openSellOrder).toHaveBeenCalledTimes(2);
    expect(hook.stage).toBe('done');
  });

  it('a code outranks the words: text that reads as a refund is not one when the code says wait', async () => {
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
    mocked.openSellOrder
      .mockRejectedValueOnce(new PaycrestError('(DONE/REFUNDED) — try again in a moment', 409, 'swap_pending'))
      .mockResolvedValueOnce(order);
    await run(hook.start(args));
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    expect(hook.stage).toBe('done');
  });

  it('an unknown code is neither a wait nor a verdict — the words are not consulted', async () => {
    mocked.openSellOrder.mockRejectedValue(new PaycrestError(
      "your swap didn't complete (DONE/REFUNDED) — nothing was sold; check your wallet", 409, 'swap_mismatch'));
    await run(hook.start(args));
    expect(mocked.openSellOrder).toHaveBeenCalledTimes(1);
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    expect(hook.stage).toBe('failed');
  });
});

// ── TSA Pay: the same pipeline, started by a payment instead of a sale ──────

let payOpts: Parameters<typeof useSwappedSell>[0];
let payHook!: ReturnType<typeof useSwappedSell>;
function PayProbe() {
  payHook = useSwappedSell(payOpts);
  return null;
}
const payArgs = {
  sourceChainId: 56,
  token: args.token,
  walletAddress: '0xUser',
  sourceAddress: '0xUser',
};
/** A payment signs what the server's quote pulls, so its quote must say so. */
const qp = { fromAmount: '84000000000000000' } as LiFiQuote;
const payIntent = { id: 'i1', status: 'swapping' } as SellIntent;
const mountPay = () => act(async () => { create(<PayProbe />); });

describe('an injected intent creator', () => {
  beforeEach(() => {
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockResolvedValue(order);
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  });

  it('starts the sale through it instead of createSellIntent, and runs the same pipeline', async () => {
    const createIntent = jest.fn().mockResolvedValue({ intent: payIntent, quote: qp, gasTopUp: false });
    payOpts = { createIntent };
    await mountPay();

    await run(payHook.start(payArgs));

    expect(createIntent).toHaveBeenCalledWith(payArgs);
    expect(mocked.createSellIntent).not.toHaveBeenCalled();
    expect(mocked.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap');
    expect(mocked.openSellOrder).toHaveBeenCalledWith('i1');
    expect(payHook.stage).toBe('done');
    expect(payHook.intent?.id).toBe('i1');
  });

  it('always calls the creator of the latest render, not the first', async () => {
    const stale = jest.fn().mockRejectedValue(new Error('stale creator'));
    const fresh = jest.fn().mockResolvedValue({ intent: payIntent, quote: qp });
    payOpts = { createIntent: stale };
    await mountPay();
    payOpts = { createIntent: fresh };
    await act(async () => { create(<PayProbe />); });

    await run(payHook.start(payArgs));

    expect(stale).not.toHaveBeenCalled();
    expect(fresh).toHaveBeenCalled();
  });

  it('signs for the amount the server quoted, not one typed here', async () => {
    const quoted = { fromAmount: '84000000000000000' } as LiFiQuote;
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: quoted }) };
    await mountPay();

    await run(payHook.start(payArgs));

    expect(mocked.executeLiFiSwap).toHaveBeenCalledWith(expect.objectContaining({ fromAmount: '0.084', feeFeature: 'none' }));
  });

  it("asks for the gas top-up the creator's answer says was priced", async () => {
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp, gasTopUp: true }) };
    await mountPay();

    await run(payHook.start(payArgs));

    expect(mocked.requestSellIntentGasTopUp).toHaveBeenCalledWith('i1');
  });

  it('a creator that throws fails the run with its message and starts nothing', async () => {
    payOpts = { createIntent: jest.fn().mockRejectedValue(new Error('You need to verify your identity')) };
    await mountPay();

    await run(payHook.start(payArgs));

    expect(payHook.stage).toBe('failed');
    expect(payHook.error).toBe('You need to verify your identity');
    expect(mocked.executeLiFiSwap).not.toHaveBeenCalled();
  });
});

describe('a payment whose swap landed short', () => {
  const shortfall = () => new PaycrestError('The swap landed short of the payout', 409, 'pay_shortfall', '0.42');

  beforeEach(async () => {
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp }) };
    await mountPay();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  });

  it('stops at a shortfall stage with the figure, keeping the sale open', async () => {
    mocked.openSellOrder.mockRejectedValue(shortfall());

    await run(payHook.start(payArgs));

    expect(payHook.stage).toBe('shortfall');
    expect(payHook.missingUsdc).toBe('0.42');
    expect(payHook.intent?.id).toBe('i1');
    expect(payHook.error).toBeUndefined();
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    expect(mocked.sendSponsoredPaycrestDeposit).not.toHaveBeenCalled();
    expect(await AsyncStorage.getItem(KEY)).toBe('0xswap'); // the swap is still followable
  });

  it('completeWithWallet opens the order against her wallet USDC, then sends and finishes', async () => {
    mocked.openSellOrder.mockRejectedValueOnce(shortfall());
    await run(payHook.start(payArgs));
    // A resumed run may not know her wallet: the order's own is the USDC's owner.
    const owned = { ...order, walletAddress: '0xUser' } as PaycrestOrder;
    mocked.openSellOrder.mockResolvedValue(owned);

    await run(payHook.completeWithWallet(payHook.intent!));

    expect(mocked.openSellOrder).toHaveBeenLastCalledWith('i1', { useWalletBalance: true });
    expect(mocked.sendSponsoredPaycrestDeposit).toHaveBeenCalledWith(owned, '0xUser', expect.anything());
    expect(mocked.claimPayment).toHaveBeenCalledWith('o1');
    expect(payHook.stage).toBe('done');
    expect(payHook.missingUsdc).toBeUndefined();
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it('a wallet that cannot cover it either stays at shortfall with the new figure', async () => {
    mocked.openSellOrder.mockRejectedValueOnce(shortfall());
    await run(payHook.start(payArgs));
    mocked.openSellOrder.mockRejectedValue(new PaycrestError('still short', 409, 'pay_shortfall', '0.10'));

    await run(payHook.completeWithWallet(payHook.intent!));

    expect(payHook.stage).toBe('shortfall');
    expect(payHook.missingUsdc).toBe('0.10');
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
  });

  it('any other refusal to open it fails with the server words and her USDC reassurance', async () => {
    mocked.openSellOrder.mockRejectedValueOnce(shortfall());
    await run(payHook.start(payArgs));
    mocked.openSellOrder.mockRejectedValue(new Error('Paycrest is unavailable'));

    await run(payHook.completeWithWallet(payHook.intent!));

    expect(payHook.stage).toBe('failed');
    expect(payHook.error).toMatch(/Paycrest is unavailable/);
    expect(payHook.error).toMatch(/USDC is in your wallet/i);
  });
});

describe('the rate moving while the payout opens', () => {
  const moved = () => new PaycrestError('The rate moved', 409, 'rate_moved');

  beforeEach(async () => {
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp }) };
    await mountPay();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  });

  it('retries once and carries on', async () => {
    mocked.openSellOrder.mockRejectedValueOnce(moved()).mockResolvedValueOnce(order);

    await run(payHook.start(payArgs));

    expect(mocked.openSellOrder).toHaveBeenCalledTimes(2);
    expect(payHook.stage).toBe('done');
  });

  it('fails with the message when it moves again, closing nothing', async () => {
    mocked.openSellOrder.mockRejectedValue(moved());

    await run(payHook.start(payArgs));

    expect(mocked.openSellOrder).toHaveBeenCalledTimes(2);
    expect(payHook.stage).toBe('failed');
    expect(payHook.error).toMatch(/The rate moved/);
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
  });
});

describe('resuming a payment sale', () => {
  it('a failed intent has nothing to follow — back to idle, nothing called', async () => {
    payOpts = undefined;
    await mountPay();

    await run(payHook.resume({ id: 'i1', status: 'failed' } as SellIntent, '0xUser'));

    expect(payHook.stage).toBe('idle');
    expect(mocked.openSellOrder).not.toHaveBeenCalled();
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
  });

  it('a swapped intent that then hits a shortfall resumes into the shortfall stage', async () => {
    payOpts = undefined;
    await mountPay();
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockRejectedValue(new PaycrestError('short', 409, 'pay_shortfall', '0.42'));

    await run(payHook.resume({ id: 'i1', status: 'swapped', swapTxHash: '0xserver' } as SellIntent, '0xUser'));

    expect(payHook.stage).toBe('shortfall');
    expect(payHook.missingUsdc).toBe('0.42');
    expect(payHook.intent?.id).toBe('i1');
  });
});

// ── review fixes ────────────────────────────────────────────────────────────

describe('an ordinary sale signs what she typed, whatever the quote carries', () => {
  it('passes her typed amount even when toLiFiQuote populated quote.fromAmount', async () => {
    mocked.createSellIntent.mockResolvedValue({
      intent: { id: 'i1', status: 'swapping' } as SellIntent,
      quote: { fromAmount: '999000000000000000' } as LiFiQuote,
      gasTopUp: false,
    });
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockResolvedValue(order);
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');

    await run(hook.start(args));

    expect(mocked.executeLiFiSwap).toHaveBeenCalledWith(expect.objectContaining({ fromAmount: '0.016' }));
  });
});

describe('reset', () => {
  it('returns a finished or stopped run to idle, clearing what it held', async () => {
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp }) };
    await mountPay();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockRejectedValue(new PaycrestError('short', 409, 'pay_shortfall', '0.42'));
    await run(payHook.start(payArgs));
    expect(payHook.stage).toBe('shortfall');

    act(() => payHook.reset());

    expect(payHook.stage).toBe('idle');
    expect(payHook.missingUsdc).toBeUndefined();
    expect(payHook.intent).toBeUndefined();
    expect(payHook.error).toBeUndefined();
    expect(payHook.retryable).toBe(true);
  });
});

describe('which stops are safe to start over from (retryable)', () => {
  beforeEach(async () => {
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp }) };
    await mountPay();
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
  });

  it('a swap still on its way is not', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'PENDING' });
    await run(payHook.start(payArgs));
    expect(payHook.stage).toBe('failed');
    expect(payHook.retryable).toBe(false);
  });

  it('a hash the server never heard is not', async () => {
    mocked.recordSellSwap.mockRejectedValue(new Error('offline'));
    await run(payHook.start(payArgs));
    expect(payHook.retryable).toBe(false);
  });

  it('a payout that would not open is not', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockRejectedValue(new Error('Paycrest is unavailable'));
    await run(payHook.start(payArgs));
    expect(payHook.retryable).toBe(false);
  });

  it('a swap refused at the node — nothing left her wallet — is', async () => {
    mocked.executeLiFiSwap.mockRejectedValue(new Error('User rejected'));
    await run(payHook.start(payArgs));
    expect(payHook.stage).toBe('failed');
    expect(payHook.retryable).toBe(true);
  });

  it('a swap that ended short and was closed is', async () => {
    mocked.getSwapDelivery.mockResolvedValue({ status: 'FAILED' });
    await run(payHook.start(payArgs));
    expect(payHook.retryable).toBe(true);
  });
});

describe('a delivered payment has nothing left to resume', () => {
  it('resume of a delivered intent is a no-op back to idle', async () => {
    payOpts = undefined;
    await mountPay();
    await run(payHook.resume({ id: 'i1', status: 'delivered' } as SellIntent, '0xUser'));
    expect(payHook.stage).toBe('idle');
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
    expect(mocked.openSellOrder).not.toHaveBeenCalled();
  });
});

describe('the priced amount bound, through the pipeline', () => {
  beforeEach(async () => {
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.openSellOrder.mockResolvedValue(order);
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  });
  const native = { ...payArgs, pricedFromAmount: '84000000000000000' };
  const quoteOf = (fromAmount: string) =>
    ({ fromAmount, transactionRequest: { value: `0x${BigInt(fromAmount).toString(16)}` } }) as unknown as LiFiQuote;

  it('a fresh quote past the tolerance is refused before signing: retryable, closed, in plain words', async () => {
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: quoteOf('99000000000000000') }) };
    await mountPay();

    await run(payHook.start(native));

    expect(mocked.executeLiFiSwap).not.toHaveBeenCalled();
    expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
    expect(payHook.stage).toBe('failed');
    expect(payHook.retryable).toBe(true);
    expect(payHook.error).toMatch(/price moved/i);
  });

  it('a quote within it is signed, and the amount signed is the quote’s', async () => {
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: quoteOf('85000000000000000') }) };
    await mountPay();
    await run(payHook.start(native));
    expect(mocked.executeLiFiSwap).toHaveBeenCalledWith(expect.objectContaining({ fromAmount: '0.085' }));
  });

  it('an ordinary sale supplies no priced amount and is bounded by nothing — unchanged', async () => {
    mocked.createSellIntent.mockResolvedValue({
      intent: { id: 'i1', status: 'swapping' } as SellIntent, quote: quoteOf('999000000000000000'), gasTopUp: false,
    });
    await run(hook.start(args));
    expect(mocked.executeLiFiSwap).toHaveBeenCalledWith(expect.objectContaining({ fromAmount: '0.016' }));
    expect(mocked.abandonSellIntent).not.toHaveBeenCalled();
  });
});

describe('events — what a payment reads instead of six state slices', () => {
  const events = () => ({ onStage: jest.fn(), onStop: jest.fn(), onShortfall: jest.fn() });
  beforeEach(() => {
    mocked.executeLiFiSwap.mockResolvedValue('0xswap');
    mocked.getSwapDelivery.mockResolvedValue({ status: 'DONE', substatus: 'COMPLETED' });
    mocked.sendSponsoredPaycrestDeposit.mockResolvedValue('0xdeposit');
  });

  it('emits each stage of a run in order, with the intent and, once it exists, the order', async () => {
    const ev = events();
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp }), ...ev };
    mocked.openSellOrder.mockResolvedValue(order);
    await mountPay();

    await run(payHook.start(payArgs));

    expect(ev.onStage.mock.calls.map((c) => c[0])).toEqual(['swapping', 'waiting', 'ordering', 'sending', 'done']);
    expect(ev.onStage.mock.calls[1][1]).toEqual({ intent: payIntent, order: undefined });
    expect(ev.onStage.mock.calls[4][1]).toEqual({ intent: payIntent, order });
    expect(ev.onStop).not.toHaveBeenCalled();
    expect(ev.onShortfall).not.toHaveBeenCalled();
  });

  it('emits a stop with its words, whether it is safe to start over, and the intent it holds', async () => {
    const ev = events();
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp }), ...ev };
    mocked.getSwapDelivery.mockResolvedValue({ status: 'PENDING' });
    await mountPay();

    await run(payHook.start(payArgs));

    expect(ev.onStop).toHaveBeenCalledTimes(1);
    expect(ev.onStop).toHaveBeenCalledWith({ message: expect.stringMatching(/still on its way/i), retryable: false }, { intent: payIntent, order: undefined });
  });

  it('a generic failure is a retryable stop with its message', async () => {
    const ev = events();
    payOpts = { createIntent: jest.fn().mockRejectedValue(new Error('You need to verify your identity')), ...ev };
    await mountPay();
    await run(payHook.start(payArgs));
    expect(ev.onStop).toHaveBeenCalledWith({ message: 'You need to verify your identity', retryable: true }, { intent: undefined, order: undefined });
  });

  it('emits a shortfall with the figure — and no stop', async () => {
    const ev = events();
    payOpts = { createIntent: jest.fn().mockResolvedValue({ intent: payIntent, quote: qp }), ...ev };
    mocked.openSellOrder.mockRejectedValue(new PaycrestError('short', 409, 'pay_shortfall', '0.42'));
    await mountPay();
    await run(payHook.start(payArgs));
    expect(ev.onShortfall).toHaveBeenCalledWith('0.42', { intent: payIntent, order: undefined });
    expect(ev.onStop).not.toHaveBeenCalled();
  });

  it('a sale with no events (the ordinary sell) behaves exactly as before — and still returns its own state', async () => {
    mocked.createSellIntent.mockResolvedValue({ intent: { id: 'i1', status: 'swapping' } as SellIntent, quote: q, gasTopUp: false });
    mocked.openSellOrder.mockResolvedValue(order);
    await run(hook.start(args));
    expect(hook.stage).toBe('done');
    expect(hook.order?.id).toBe('o1');
    expect(hook.depositTx).toBe('0xdeposit');
  });
});
