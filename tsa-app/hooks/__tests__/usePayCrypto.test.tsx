/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Paying a crypto-settling TSA Pay merchant, from the payer's side: one swap
 * that delivers the merchant's token straight to their wallet. What these pin:
 *   - nothing is asked of the server until the payer authorizes;
 *   - the signed swap's hash is on the phone BEFORE the server hears it, so a
 *     kill or a lost reply never loses the one thing that lets the server
 *     follow a payment that has already left her wallet;
 *   - once the swap is sent the server confirms delivery — the app only waits;
 *   - an attempt that already exists is followed, never duplicated.
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
  return { __esModule: true, PayError, payCrypto: jest.fn(), getCryptoQuote: jest.fn() };
});
jest.mock('@/services/paycrestSellSwap', () => ({
  __esModule: true,
  recordSellSwap: jest.fn(),
  getOpenSellIntent: jest.fn(),
  requestSellIntentGasTopUp: jest.fn(),
  abandonSellIntent: jest.fn(),
}));
jest.mock('@/services/lifiFlow', () => ({
  __esModule: true,
  executeLiFiSwap: jest.fn(),
  SwapBroadcastUnconfirmedError: class extends Error {
    txHash?: string;
    constructor(mockHash?: string) { super("We couldn't confirm the swap was sent."); this.txHash = mockHash; }
  },
}));
jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true,
  useSignableWallet: () => ({ address: '0xPayer', ready: true, refresh: jest.fn() }),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as PayApi from '@/services/payApi';
import * as SellSwap from '@/services/paycrestSellSwap';
import * as LifiFlow from '@/services/lifiFlow';
import type { LiFiQuote, LiFiToken } from '@/services/lifi';
import type { SellIntent } from '@/services/paycrestSellSwap';
import { usePayCrypto, type UsePayCrypto } from '../usePayCrypto';

const { PayError } = PayApi;
const m = {
  payCrypto: jest.mocked(PayApi.payCrypto),
  getCryptoQuote: jest.mocked(PayApi.getCryptoQuote),
  recordSellSwap: jest.mocked(SellSwap.recordSellSwap),
  getOpenSellIntent: jest.mocked(SellSwap.getOpenSellIntent),
  requestSellIntentGasTopUp: jest.mocked(SellSwap.requestSellIntentGasTopUp),
  abandonSellIntent: jest.mocked(SellSwap.abandonSellIntent),
  executeLiFiSwap: jest.mocked(LifiFlow.executeLiFiSwap),
};

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const checkout = {
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'pending', amount: 5_000_000, customerTotal: 5_050_506,
  fee: 50_506, merchantNet: 5_000_000, currency: 'NGN', settlementMode: 'crypto', settleTokenSymbol: 'USDT',
  settleChainId: 56, settleTokenAddress: '0x55d398326f99059fF775485246999027B3197955', payable: true, youArePaying: false,
} as PayApi.PayCheckout;

const bnb = { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', decimals: 18 } as LiFiToken;
const BSC = 56;
const KEY = 'paycrest.sellSwap.pendingTx.i1';
const intent = { id: 'i1', status: 'swapping' } as SellIntent;
const quote = { fromAmount: '84000000000000000' } as LiFiQuote;

jest.setTimeout(30_000);

let hook: UsePayCrypto;
const Harness = () => {
  hook = usePayCrypto(checkout);
  return null;
};
let renderer: ReactTestRenderer;
const mount = () => act(() => { renderer = TestRenderer.create(<Harness />); });

/** Let promises settle, stepping the fake clock past any retry delay. */
const settle = () => act(async () => {
  for (let i = 0; i < 40; i++) {
    await new Promise((r) => setImmediate(r));
    jest.advanceTimersByTime(3_000);
  }
});

async function pay(token: LiFiToken = bnb, chainId = BSC) {
  act(() => hook.payWith(token, chainId));
  await act(async () => { hook.authorization.onAuthorized(); });
  await settle();
}

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
  m.payCrypto.mockResolvedValue({ intent, quote, gasTopUp: false });
  m.executeLiFiSwap.mockResolvedValue('0xswap');
  m.recordSellSwap.mockResolvedValue(undefined);
  m.abandonSellIntent.mockResolvedValue(undefined);
  m.getOpenSellIntent.mockResolvedValue(null);
});

afterEach(() => {
  act(() => renderer?.unmount());
  jest.useRealTimers();
});

describe('authorization', () => {
  it('payWith only opens the modal — nothing is sent, nothing asked of the server', async () => {
    await mount();
    act(() => hook.payWith(bnb, BSC));
    expect(hook.authorization.visible).toBe(true);
    expect(hook.authorization.title).toBe('Authorize payment');
    expect(m.payCrypto).not.toHaveBeenCalled();
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });

  it('cancelling sends nothing', async () => {
    await mount();
    act(() => hook.payWith(bnb, BSC));
    act(() => hook.authorization.onCancel());
    await settle();
    expect(hook.authorization.visible).toBe(false);
    expect(m.payCrypto).not.toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });
});

describe('paying', () => {
  it('starts the payment, signs the server’s quote, stores the hash, THEN reports it, and waits for the server', async () => {
    m.executeLiFiSwap.mockImplementation(async (p: any) => {
      expect(m.recordSellSwap).not.toHaveBeenCalled();
      await p.onSigned?.('0xswap');
      return '0xswap';
    });
    m.recordSellSwap.mockImplementation(async () => {
      expect(await AsyncStorage.getItem(KEY)).toBe('0xswap'); // on the phone before the server hears it
    });
    await mount();
    await pay();

    expect(m.payCrypto).toHaveBeenCalledWith(ID, { sourceChainId: BSC, sourceToken: bnb.address, sourceAddress: '0xPayer' });
    expect(m.executeLiFiSwap).toHaveBeenCalledWith(expect.objectContaining({
      quote, fromChainId: BSC, walletAddress: '0xPayer', fromAmount: '0.084', feeFeature: 'none',
    }));
    expect(m.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap');
    expect(hook.stage).toBe('waiting');
    expect(m.abandonSellIntent).not.toHaveBeenCalled();
  });

  it('asks for the gas top-up the server priced into the payment', async () => {
    m.payCrypto.mockResolvedValue({ intent, quote, gasTopUp: true });
    await mount();
    await pay();
    expect(m.requestSellIntentGasTopUp).toHaveBeenCalledWith('i1');
  });

  it('a swap that throws fails with its message and reports nothing', async () => {
    m.executeLiFiSwap.mockRejectedValue(new Error('User rejected the request'));
    await mount();
    await pay();

    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/User rejected the request/);
    expect(hook.error).toMatch(/nothing was sent/i);
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it('a refusal to start the payment keeps its machine code and never reaches the wallet', async () => {
    m.payCrypto.mockRejectedValue(new PayError('Verify your identity first', 403, 'kyc_verification_required'));
    await mount();
    await pay();

    expect(hook.stage).toBe('failed');
    expect(hook.error).toBe('Verify your identity first');
    expect(hook.errorCode).toBe('kyc_verification_required');
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
  });

  it('a chain the wallet cannot send from is refused before anything starts', async () => {
    await mount();
    await pay(bnb, 1151111081099710); // Solana
    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/isn't supported yet/i);
    expect(m.payCrypto).not.toHaveBeenCalled();
  });
});

describe('a swap that may have been broadcast', () => {
  it('reports the hash it does know and waits — the server decides, never a retry', async () => {
    m.executeLiFiSwap.mockRejectedValue(new (LifiFlow.SwapBroadcastUnconfirmedError as any)('0xmaybe'));
    await mount();
    await pay();

    expect(m.recordSellSwap).toHaveBeenCalledWith('i1', '0xmaybe');
    expect(hook.stage).toBe('waiting');
    expect(m.abandonSellIntent).not.toHaveBeenCalled();
  });

  it('with no hash to report, says so plainly and tells her not to pay twice', async () => {
    m.executeLiFiSwap.mockRejectedValue(new (LifiFlow.SwapBroadcastUnconfirmedError as any)());
    await mount();
    await pay();

    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/don't pay again/i);
    expect(m.recordSellSwap).not.toHaveBeenCalled();
  });
});

describe('the server not hearing the hash', () => {
  it('is retried, and succeeds without ever failing the payment', async () => {
    m.recordSellSwap.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(undefined);
    await mount();
    await pay();
    expect(m.recordSellSwap).toHaveBeenCalledTimes(2);
    expect(hook.stage).toBe('waiting');
  });

  it('after every retry keeps the hash on the phone and tells her not to pay again', async () => {
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await pay();

    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/don't pay again/i);
    expect(await AsyncStorage.getItem(KEY)).toBe('0xswap');
  });
});

describe('a payment that is already being paid (409 pay_in_progress)', () => {
  const inProgress = () => new PayError('You are already paying this', 409, 'pay_in_progress');

  it('reports a stored hash the server never got, then waits', async () => {
    m.payCrypto.mockRejectedValue(inProgress());
    m.getOpenSellIntent.mockResolvedValue(intent);
    await AsyncStorage.setItem(KEY, '0xstored');
    await mount();
    await pay();

    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(m.recordSellSwap).toHaveBeenCalledWith('i1', '0xstored');
    expect(hook.stage).toBe('waiting');
  });

  it('reports nothing when the server already has the hash', async () => {
    m.payCrypto.mockRejectedValue(inProgress());
    m.getOpenSellIntent.mockResolvedValue({ ...intent, status: 'swapped', swapTxHash: '0xserver' });
    await AsyncStorage.setItem(KEY, '0xserver');
    await mount();
    await pay();

    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });

  it('with no open sale just waits — the server settles it', async () => {
    m.payCrypto.mockRejectedValue(inProgress());
    await mount();
    await pay();
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });

  it('resume() alone — the screen re-opened on its own payment — does the same', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await AsyncStorage.setItem(KEY, '0xstored');
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.recordSellSwap).toHaveBeenCalledWith('i1', '0xstored');
  });
});

describe('estimate', () => {
  it('asks the server what the swap pulls and shows it in her token’s decimals', async () => {
    m.getCryptoQuote.mockResolvedValue({ fromAmount: '84000000000000000' });
    await mount();
    await expect(hook.estimate(bnb, BSC)).resolves.toBe('0.084 BNB');
    expect(m.getCryptoQuote).toHaveBeenCalledWith(ID, { sourceChainId: BSC, sourceToken: bnb.address, sourceAddress: '0xPayer' });
  });
});
