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

it('frees the sale when the swap was refused before or at the node', async () => {
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

it('closes a sale with no swap to follow when no run of it is going, and says only what it knows', async () => {
  await run(hook.resume({ id: 'i1', status: 'swapping' } as SellIntent));
  expect(mocked.executeLiFiSwap).not.toHaveBeenCalled();
  expect(mocked.abandonSellIntent).toHaveBeenCalledWith('i1');
  expect(hook.error).toMatch(/couldn't find a swap/i);
  expect(hook.error).not.toMatch(/never started|nothing was sold/i);
});

/** Let pending promises run without stepping the clock. */
const flush = () => act(async () => { for (let i = 0; i < 20; i++) await new Promise((r) => setImmediate(r)); });

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
