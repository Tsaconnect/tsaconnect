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
const mockWallet: { address: string | null } = { address: '0xPayer' };
jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true,
  useSignableWallet: () => ({ address: mockWallet.address, ready: true, refresh: jest.fn() }),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as PayApi from '@/services/payApi';
import * as SellSwap from '@/services/paycrestSellSwap';
import * as LifiFlow from '@/services/lifiFlow';
import type { LiFiQuote, LiFiToken } from '@/services/lifi';
import type { SellIntent } from '@/services/paycrestSellSwap';
import { priceMovedMessage } from '@/utils/payCheckout';
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
const intent = { id: 'i1', status: 'swapping', payPaymentId: ID } as SellIntent;
/** A native-coin quote as the server builds it: the transaction sends exactly what the quote pulls. */
const quote = {
  fromAmount: '84000000000000000', transactionRequest: { value: `0x${BigInt('84000000000000000').toString(16)}` },
} as unknown as LiFiQuote;

jest.setTimeout(30_000);

/** A price no fresh quote can exceed — for tests that are not about the bound. */
const ANY_PRICE = { pay: '0.084 BNB', fromAmount: `1${'0'.repeat(40)}` };

let hook: UsePayCrypto;
const Harness = () => {
  hook = usePayCrypto(checkout);
  return null;
};
let renderer: ReactTestRenderer;
/** Mount, and let the hook's own look at the persisted hold finish (Pay is withheld until it has). */
const mount = () => act(async () => {
  renderer = TestRenderer.create(<Harness />);
  for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r));
});
const HOLD = `tsapay.pendingSwap.${ID}`;
/** What an app kill leaves behind: the hash, and the hold written the moment the swap was signed. */
const leaveHold = async (at = Date.now()) => {
  await AsyncStorage.setItem(KEY, '0xswap');
  await AsyncStorage.setItem(HOLD, JSON.stringify({ intentId: 'i1', at }));
};
const rejection = (status: number) => Object.assign(new Error('The server will not take a hash for this intent'), { status });

/** Let promises settle, stepping the fake clock past any retry delay. */
const settle = () => act(async () => {
  for (let i = 0; i < 40; i++) {
    await new Promise((r) => setImmediate(r));
    jest.advanceTimersByTime(3_000);
  }
});

async function pay(token: LiFiToken = bnb, chainId = BSC) {
  act(() => hook.payWith(token, chainId, ANY_PRICE));
  await act(async () => { hook.authorization.onAuthorized(); });
  await settle();
}

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
  mockWallet.address = '0xPayer';
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
    act(() => hook.payWith(bnb, BSC, ANY_PRICE));
    expect(hook.authorization.visible).toBe(true);
    expect(hook.authorization.title).toBe('Authorize payment of about 0.084 BNB');
    expect(m.payCrypto).not.toHaveBeenCalled();
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });

  it('cancelling sends nothing', async () => {
    await mount();
    act(() => hook.payWith(bnb, BSC, ANY_PRICE));
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

describe('follow-only resume and scoping', () => {
  it('a sale of hers that pays a DIFFERENT payment is never reported on', async () => {
    m.getOpenSellIntent.mockResolvedValue({ ...intent, payPaymentId: 'someone-elses' } as SellIntent);
    await AsyncStorage.setItem(KEY, '0xstored');
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });

  it('a sale with no payment id at all is not this payment’s either', async () => {
    m.getOpenSellIntent.mockResolvedValue({ id: 'i1', status: 'swapping' } as SellIntent);
    await AsyncStorage.setItem(KEY, '0xstored');
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.recordSellSwap).not.toHaveBeenCalled();
  });

  it('a delivered payment is done: nothing to report or resume, the server settles it', async () => {
    m.getOpenSellIntent.mockResolvedValue({ ...intent, status: 'delivered', swapTxHash: '0xswap' } as SellIntent);
    await AsyncStorage.setItem(KEY, '0xstale');
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });

  it('a failed sale is not reported on either', async () => {
    m.getOpenSellIntent.mockResolvedValue({ ...intent, status: 'failed' } as SellIntent);
    await AsyncStorage.setItem(KEY, '0xstale');
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });

  it('never signs or sends on its own — its only server write is reporting a hash already on her phone', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    await act(async () => { await hook.resume(); });
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(m.payCrypto).not.toHaveBeenCalled();
  });
});

describe('a payment that cannot safely be started over', () => {
  it('a hash the server never heard: not retryable, and Continue payment checks again without a new payment', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await pay();

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(true);

    m.recordSellSwap.mockResolvedValue(undefined);
    await act(async () => { hook.continuePayment(); });
    await settle();
    expect(m.recordSellSwap).toHaveBeenLastCalledWith('i1', '0xswap');
    expect(m.payCrypto).toHaveBeenCalledTimes(1);
    expect(hook.stage).toBe('waiting');
  });

  it('a broadcast that could not be confirmed is not retryable', async () => {
    m.executeLiFiSwap.mockRejectedValue(new (LifiFlow.SwapBroadcastUnconfirmedError as any)());
    await mount();
    await pay();
    expect(hook.retryable).toBe(false);
  });

  it('a swap refused before it left her wallet is retryable', async () => {
    m.executeLiFiSwap.mockRejectedValue(new Error('User rejected the request'));
    await mount();
    await pay();
    expect(hook.retryable).toBe(true);
    expect(hook.canContinue).toBe(false);
  });

  it('a refusal to start the payment is retryable', async () => {
    m.payCrypto.mockRejectedValue(new PayError('Verify your identity first', 403, 'kyc_verification_required'));
    await mount();
    await pay();
    expect(hook.retryable).toBe(true);
  });
});

describe("the server's uncertain-attempt answer (409 pay_attempt_uncertain)", () => {
  it('pauses the payment: honest copy, not retryable, nothing to continue, no swap signed', async () => {
    m.payCrypto.mockRejectedValue(new PayError('We could not tell whether an attempt exists', 409, 'pay_attempt_uncertain'));
    await mount();
    await pay();

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(false);
    expect(hook.error).toMatch(/paused/i);
    expect(hook.error).toMatch(/don't pay again/i);
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(m.getOpenSellIntent).not.toHaveBeenCalled();
  });

  it('is never auto-reset', async () => {
    m.payCrypto.mockRejectedValue(new PayError('x', 409, 'pay_attempt_uncertain'));
    await mount();
    await pay();
    await act(async () => { const p = hook.reset(); await settle(); await p; });
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
  });
});

describe("the server's own failure to start (500)", () => {
  it('is a plain retry: its words, retryable, nothing held', async () => {
    m.payCrypto.mockRejectedValue(new PayError("We couldn't start this payment — please try again.", 500));
    await mount();
    await pay();
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(true);
    expect(hook.error).toBe("We couldn't start this payment — please try again.");
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    await act(async () => { await hook.reset(); });
    expect(hook.stage).toBe('idle');
  });
});

describe('the route refusing this token right now (fail-closed 422 / 503)', () => {
  it.each([422, 503])('a %i on start says try another token or later, retryable, no unresolved state', async (status) => {
    m.payCrypto.mockRejectedValue(new PayError('any wording the server likes', status));
    await mount();
    await pay();

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(true);
    expect(hook.canContinue).toBe(false);
    expect(hook.error).toMatch(/another token/i);
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    await act(async () => { await hook.reset(); });
    expect(hook.stage).toBe('idle');
  });

  it.each([422, 503])('a %i on the price says the same, instead of a raw server sentence', async (status) => {
    m.getCryptoQuote.mockRejectedValue(new PayError('route floor below the amount owed', status));
    await mount();
    await expect(hook.estimate(bnb, BSC)).rejects.toThrow(/another token/i);
  });

  it('any other price failure keeps its own words', async () => {
    m.getCryptoQuote.mockRejectedValue(new PayError('Payments are not available right now.', 500));
    await mount();
    await expect(hook.estimate(bnb, BSC)).rejects.toThrow('Payments are not available right now.');
  });
});

describe('a delivery the server is still confirming (processing for up to 24 h)', () => {
  it('following a swapped sale of this payment just waits — no failure, no Pay, nothing signed or reported', async () => {
    m.getOpenSellIntent.mockResolvedValue({ ...intent, status: 'swapped', swapTxHash: '0xswap' } as SellIntent);
    await mount();
    await act(async () => { await hook.resume(); });
    expect(hook.stage).toBe('waiting');
    expect(hook.error).toBeUndefined();
    expect(hook.retryable).toBe(true);
    expect(hook.canContinue).toBe(false);
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
  });
});

describe('reset', () => {
  it('returns the payment to idle so Pay is offered again once the server frees it', async () => {
    await mount();
    await pay();
    expect(hook.stage).toBe('waiting');
    await act(async () => { await hook.reset(); });
    expect(hook.stage).toBe('idle');
    expect(hook.error).toBeUndefined();
    expect(hook.retryable).toBe(true);
  });

  it('does nothing while an attempt is in flight', async () => {
    let release!: (v: any) => void;
    m.payCrypto.mockReturnValue(new Promise((r) => { release = r; }));
    await mount();
    act(() => hook.payWith(bnb, BSC, ANY_PRICE));
    await act(async () => { hook.authorization.onAuthorized(); });
    expect(hook.stage).toBe('paying');

    await act(async () => { await hook.reset(); });
    expect(hook.stage).toBe('paying');

    await act(async () => { release({ intent, quote, gasTopUp: false }); });
    await settle();
    expect(hook.stage).toBe('waiting');
  });
});

describe('a swap that may have reached the merchant is never forgotten by a reset', () => {
  it('holds the "don\'t pay again" state when the server flip frees the payment and the hash still cannot be reported', async () => {
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await pay();
    expect(hook.retryable).toBe(false);
    m.recordSellSwap.mockClear();

    await act(async () => { const p = hook.reset(); await settle(); await p; });

    expect(m.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap'); // tried again first
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(true);
    expect(m.payCrypto).toHaveBeenCalledTimes(1);
    expect(await AsyncStorage.getItem(KEY)).toBe('0xswap'); // still on the phone
  });

  it('once the report lands the server has the swap: she waits on it, and Pay does not come back', async () => {
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await pay();
    m.recordSellSwap.mockResolvedValue(undefined);

    await act(async () => { const p = hook.reset(); await settle(); await p; });

    expect(hook.stage).toBe('waiting');
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it('a broadcast that could not be confirmed and has no hash to report is held too', async () => {
    m.executeLiFiSwap.mockRejectedValue(new (LifiFlow.SwapBroadcastUnconfirmedError as any)());
    await mount();
    await pay();
    await act(async () => { const p = hook.reset(); await settle(); await p; });
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
  });

  it('found on reopening (NOT_CONFIRMED from a resume) is held as well', async () => {
    m.getOpenSellIntent.mockResolvedValue(intent);
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await AsyncStorage.setItem(KEY, '0xstored');
    await mount();
    await act(async () => { const p = hook.resume(); await settle(); await p; });
    expect(hook.retryable).toBe(false);
    await act(async () => { const p = hook.reset(); await settle(); await p; });
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
  });

  it('a swap refused before it left her wallet holds nothing: reset frees Pay', async () => {
    m.executeLiFiSwap.mockRejectedValue(new Error('User rejected the request'));
    await mount();
    await pay();
    await act(async () => { await hook.reset(); });
    expect(hook.stage).toBe('idle');
  });
});

describe('Continue payment with nothing to continue', () => {
  it('says nothing was found instead of silently waiting', async () => {
    await mount();
    m.getOpenSellIntent.mockResolvedValue(null);
    await act(async () => { hook.continuePayment(); });
    await settle();
    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/couldn't find/i);
    expect(hook.retryable).toBe(false);
  });

  it('opening the screen with nothing to follow still just waits — that is the ordinary case', async () => {
    await mount();
    await act(async () => { await hook.resume(); });
    expect(hook.stage).toBe('waiting');
  });
});

describe('one attempt at a time', () => {
  it('a double biometric success starts one payment and signs one swap', async () => {
    await mount();
    act(() => hook.payWith(bnb, BSC, ANY_PRICE));
    await act(async () => {
      hook.authorization.onAuthorized();
      hook.authorization.onAuthorized();
    });
    await settle();
    expect(m.payCrypto).toHaveBeenCalledTimes(1);
    expect(m.executeLiFiSwap).toHaveBeenCalledTimes(1);
  });
});

describe('estimate', () => {
  it('asks the server what the swap pulls and shows it in her token’s decimals', async () => {
    m.getCryptoQuote.mockResolvedValue({ fromAmount: '84000000000000000' });
    await mount();
    await expect(hook.estimate(bnb, BSC)).resolves.toEqual({ pay: '0.084 BNB', fromAmount: '84000000000000000' });
    expect(m.getCryptoQuote).toHaveBeenCalledWith(ID, { sourceChainId: BSC, sourceToken: bnb.address, sourceAddress: '0xPayer' });
  });

  it('adds what the store receives when the server says, in the settle token’s name and decimals', async () => {
    m.getCryptoQuote.mockResolvedValue({
      fromAmount: '84000000000000000', destAmount: '33323336332433603252', destDecimals: 18,
    });
    await mount();
    await expect(hook.estimate(bnb, BSC)).resolves.toEqual({ pay: '0.084 BNB', fromAmount: '84000000000000000', receives: '33.32 USDT' });
  });

  it('leaves it out on an older server that sends no destination amount', async () => {
    m.getCryptoQuote.mockResolvedValue({ fromAmount: '84000000000000000' });
    await mount();
    expect((await hook.estimate(bnb, BSC)).receives).toBeUndefined();
  });

  it('with no signable wallet it says so, rather than returning nothing', async () => {
    mockWallet.address = null;
    await mount();
    await expect(hook.estimate(bnb, BSC)).rejects.toThrow(/wallet/i);
  });
});

describe('the price she was shown is the price she pays', () => {
  const priced = { pay: '0.084 BNB', fromAmount: '84000000000000000' };
  const withValue = (fromAmount: string) =>
    ({ fromAmount, transactionRequest: { value: `0x${BigInt(fromAmount).toString(16)}` } }) as unknown as LiFiQuote;

  it('the authorization says how much', async () => {
    await mount();
    act(() => hook.payWith(bnb, BSC, priced));
    expect(hook.authorization.title).toBe('Authorize payment of about 0.084 BNB');
  });

  it('a fresh quote past 2% is refused before anything is signed: retryable, in plain words, nothing held', async () => {
    m.payCrypto.mockResolvedValue({ intent, quote: withValue('99000000000000000'), gasTopUp: false });
    await mount();
    act(() => hook.payWith(bnb, BSC, priced));
    await act(async () => { hook.authorization.onAuthorized(); });
    await settle();

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(true);
    expect(hook.error).toMatch(/price moved/i);
    expect(m.executeLiFiSwap).not.toHaveBeenCalled();
    expect(m.abandonSellIntent).toHaveBeenCalledWith('i1');
    expect(m.recordSellSwap).not.toHaveBeenCalled();
  });

  it('a quote within the bound is signed, and the hold is recorded for the payment as it is signed', async () => {
    m.payCrypto.mockResolvedValue({ intent, quote: withValue('85000000000000000'), gasTopUp: false });
    m.executeLiFiSwap.mockImplementation(async (p: any) => { await p.onSigned('0xswap'); return '0xswap'; });
    await mount();
    act(() => hook.payWith(bnb, BSC, priced));
    await act(async () => { hook.authorization.onAuthorized(); });
    await settle();
    expect(m.executeLiFiSwap).toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });
});

describe('the same start-error reading as naira', () => {
  it('follows a 409 pay_in_progress; refuses a 422 with the shared copy; keeps the words and code of anything else', async () => {
    m.payCrypto.mockRejectedValue(new PayError('Verify first', 403, 'kyc_verification_required'));
    await mount();
    await pay();
    expect(hook.error).toBe('Verify first');
    expect(hook.errorCode).toBe('kyc_verification_required');
    expect(hook.retryable).toBe(true);
  });
});

describe('a hold that survives an app kill', () => {
  it('Pay is withheld until the persisted hold has been looked at', async () => {
    act(() => { renderer = TestRenderer.create(<Harness />); });
    expect(hook.retryable).toBe(false);
    await act(async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); });
    expect(hook.retryable).toBe(true);
  });

  it('records the hold when the report fails, so a kill leaves it behind', async () => {
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await pay();
    expect(JSON.parse((await AsyncStorage.getItem(HOLD))!)).toEqual({ intentId: 'i1', at: expect.any(Number) });
  });

  it('records it for a broadcast that could not be confirmed, even with no hash to report', async () => {
    m.executeLiFiSwap.mockRejectedValue(new (LifiFlow.SwapBroadcastUnconfirmedError as any)());
    await mount();
    await pay();
    expect(await AsyncStorage.getItem(HOLD)).not.toBeNull();
  });

  it('clears the hold and the hash once the server has heard', async () => {
    await mount();
    await pay();
    expect(hook.stage).toBe('waiting');
    expect(await AsyncStorage.getItem(HOLD)).toBeNull();
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it('after a kill, the server took the late hash: she waits on it, and the hold lifts', async () => {
    await leaveHold();
    m.getOpenSellIntent.mockResolvedValue(intent);
    await mount();
    expect(m.recordSellSwap).toHaveBeenCalledWith('i1', '0xswap');
    expect(hook.stage).toBe('waiting');
    expect(await AsyncStorage.getItem(HOLD)).toBeNull();
  });

  it('after a kill, the server already has the swap: nothing to report, hold lifts', async () => {
    await leaveHold();
    m.getOpenSellIntent.mockResolvedValue({ ...intent, status: 'swapped', swapTxHash: '0xswap' } as SellIntent);
    await mount();
    expect(m.recordSellSwap).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
    expect(await AsyncStorage.getItem(HOLD)).toBeNull();
  });

  it('after a kill and a reap (no open sale) the server refuses the late hash: held, honest terminal state, Pay never comes back', async () => {
    await leaveHold();
    m.recordSellSwap.mockRejectedValue(rejection(409));
    await mount();

    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(false); // no dead Continue button
    expect(hook.error).toMatch(/don't pay again/i);
    expect(hook.error).toContain(ID); // the reference for support
    expect(m.payCrypto).not.toHaveBeenCalled();
    expect(await AsyncStorage.getItem(HOLD)).not.toBeNull(); // still held
    expect(m.recordSellSwap).toHaveBeenCalledTimes(1); // a 4xx is final for the report loop: no 3 tries
  });

  it('a report that cannot reach the server is held with Continue available — it may yet land', async () => {
    await leaveHold();
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await settle();
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(true);
  });

  it('a hold with no hash left is held too — the swap may be out there', async () => {
    await AsyncStorage.setItem(HOLD, JSON.stringify({ intentId: 'i1', at: Date.now() }));
    await mount();
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(false);
    expect(hook.error).toContain(ID);
  });

  it('lifts after 24 hours: the server gives such a payment up by then', async () => {
    await leaveHold(Date.now() - 24 * 60 * 60_000 - 5_000);
    await mount();
    expect(hook.stage).toBe('idle');
    expect(hook.retryable).toBe(true);
    expect(await AsyncStorage.getItem(HOLD)).toBeNull();
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it('with no hold nothing is asked of the server on opening', async () => {
    await mount();
    expect(m.getOpenSellIntent).not.toHaveBeenCalled();
    expect(hook.stage).toBe('idle');
  });

  it('the reaped-then-released flip cannot free Pay: reset keeps the hold', async () => {
    await leaveHold();
    m.recordSellSwap.mockRejectedValue(rejection(404));
    await mount();
    await act(async () => { const p = hook.reset(); await settle(); await p; });
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
  });

  it('but once the hold has run out, reset frees it', async () => {
    await leaveHold();
    m.recordSellSwap.mockRejectedValue(rejection(404));
    await mount();
    await AsyncStorage.setItem(HOLD, JSON.stringify({ intentId: 'i1', at: Date.now() - 25 * 60 * 60_000 }));
    await act(async () => { const p = hook.reset(); await settle(); await p; });
    expect(hook.stage).toBe('idle');
    expect(await AsyncStorage.getItem(HOLD)).toBeNull();
  });
});

describe('Continue and reset never dead-end', () => {
  it('Continue after the intent was reaped ends in the terminal state, not in a silent nothing', async () => {
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await pay(); // hold recorded, hash stored, report failed
    m.recordSellSwap.mockRejectedValue(rejection(409));
    m.getOpenSellIntent.mockResolvedValue(null); // the reaper let go of the intent
    await act(async () => { hook.continuePayment(); });
    await settle();

    expect(hook.stage).toBe('failed');
    expect(hook.error).toContain(ID);
    expect(hook.canContinue).toBe(false);
    expect(hook.retryable).toBe(false);
  });

  it('Continue with nothing at all to continue offers no Continue button, and reset frees Pay', async () => {
    await mount();
    await act(async () => { hook.continuePayment(); });
    await settle();
    expect(hook.stage).toBe('failed');
    expect(hook.error).toMatch(/couldn't find/i);
    expect(hook.canContinue).toBe(false);

    await act(async () => { await hook.reset(); });
    expect(hook.stage).toBe('idle');
    expect(hook.retryable).toBe(true);
  });

  it('a server-paused payment is still never reset by us', async () => {
    m.payCrypto.mockRejectedValue(new PayError('x', 409, 'pay_attempt_uncertain'));
    await mount();
    await pay();
    await act(async () => { await hook.reset(); });
    expect(hook.stage).toBe('failed');
  });
});

describe('the reservation window in words, for a crypto payment', () => {
  it('a swap refused before it left her wallet says the payment frees up in about an hour — not ten minutes', async () => {
    m.executeLiFiSwap.mockRejectedValue(new Error('User rejected the request'));
    await mount();
    await pay();
    expect(hook.error).toContain('about an hour');
    expect(hook.error).not.toMatch(/ten minutes|10 minutes/i);
  });

  it('nothing to continue says about an hour', async () => {
    await mount();
    await act(async () => { hook.continuePayment(); });
    await settle();
    expect(hook.error).toContain('about an hour');
  });

  it('an unconfirmed swap says we will keep checking, and promises no time', async () => {
    m.recordSellSwap.mockRejectedValue(new Error('offline'));
    await mount();
    await pay();
    expect(hook.error).toMatch(/keep checking/i);
    expect(hook.error).not.toMatch(/minute|hour/i);
  });
});

describe('a call with no priced amount is refused outright', () => {
  it('nothing to authorize, nothing started', async () => {
    await mount();
    act(() => (hook.payWith as any)(bnb, BSC));
    expect(hook.authorization.visible).toBe(false);
    expect(m.payCrypto).not.toHaveBeenCalled();
  });
});

describe('storage that cannot be read holds — it does not say "nothing is out there"', () => {
  // Patch (and put back) the AsyncStorage mock's method: restoreAllMocks / mockRestore would reset its own implementation.
  let originalGet: typeof AsyncStorage.getItem | undefined;
  afterEach(() => { if (originalGet) { (AsyncStorage as any).getItem = originalGet; originalGet = undefined; } });
  const breakHoldReads = () => {
    originalGet = AsyncStorage.getItem;
    const real = originalGet.bind(AsyncStorage);
    (AsyncStorage as any).getItem = async (k: string) => {
      if (k.startsWith('tsapay.')) throw new Error('storage unavailable');
      return real(k);
    };
    return { mockRestore: () => { (AsyncStorage as any).getItem = originalGet; originalGet = undefined; } };
  };

  it('on opening: held at once, Pay withheld, with a way out — Continue payment checks again', async () => {
    breakHoldReads();
    await mount();
    expect(hook.stage).toBe('failed');
    expect(hook.retryable).toBe(false);
    expect(hook.canContinue).toBe(true);
    expect(hook.error).toMatch(/couldn't check/i);
    expect(hook.error).toMatch(/don't pay again|to be safe/i);
    expect(m.payCrypto).not.toHaveBeenCalled();
  });

  it('once storage answers and there is no hold, Continue payment frees Pay', async () => {
    const spy = breakHoldReads();
    await mount();
    spy.mockRestore();
    await act(async () => { hook.continuePayment(); });
    await settle();
    expect(hook.stage).toBe('idle');
    expect(hook.retryable).toBe(true);
  });

  it('and if there IS a hold once it can be read, it is honoured', async () => {
    const spy = breakHoldReads();
    await mount();
    spy.mockRestore();
    await leaveHold();
    m.getOpenSellIntent.mockResolvedValue(intent);
    await act(async () => { hook.continuePayment(); });
    await settle();
    expect(hook.stage).toBe('waiting'); // the server took the late hash
  });

  it('a reset (the server freed the payment) cannot free Pay while the hold cannot be read', async () => {
    await mount();
    breakHoldReads();
    await act(async () => { await hook.reset(); });
    expect(hook.stage).not.toBe('idle');
  });

  it('a Pay tap cannot start a payment while the hold cannot be read', async () => {
    await mount();
    breakHoldReads();
    await pay();
    expect(m.payCrypto).not.toHaveBeenCalled();
    expect(hook.retryable).toBe(false);
  });
});

describe('a rejected report is final only when the server means it', () => {
  it.each([401, 403])('a %i (an expired session, a permissions blip) is retried and stays recoverable — never the terminal state', async (status) => {
    await leaveHold();
    m.recordSellSwap.mockRejectedValue(Object.assign(new Error('auth'), { status }));
    await mount();
    await settle();
    expect(m.recordSellSwap).toHaveBeenCalledTimes(3);
    expect(hook.stage).toBe('failed');
    expect(hook.canContinue).toBe(true); // Continue payment can try again once she is signed in
    expect(hook.error).not.toContain(ID);
  });

  it.each([404, 409, 422])('a %i is the server’s final word', async (status) => {
    await leaveHold();
    m.recordSellSwap.mockRejectedValue(rejection(status));
    await mount();
    expect(m.recordSellSwap).toHaveBeenCalledTimes(1);
    expect(hook.canContinue).toBe(false);
    expect(hook.error).toContain(ID);
  });
});

describe('one attempt, and only one that is accepted', () => {
  it('a payment the server paused does not call the start route again', async () => {
    m.payCrypto.mockRejectedValue(new PayError('x', 409, 'pay_attempt_uncertain'));
    await mount();
    await pay();
    expect(m.payCrypto).toHaveBeenCalledTimes(1);
    await pay();
    expect(m.payCrypto).toHaveBeenCalledTimes(1);
    expect(hook.stage).toBe('failed');
  });

  it('re-reads the hold before it starts: one recorded since opening stops the start', async () => {
    await mount(); // nothing held at this point
    await leaveHold(); // ...and then a swap of hers is signed elsewhere
    m.getOpenSellIntent.mockResolvedValue(intent);
    await pay();
    expect(m.payCrypto).not.toHaveBeenCalled();
    expect(hook.stage).toBe('waiting');
  });
});

describe('a price the server moved is not "reserved" — the intent was closed', () => {
  it('shows the bare refusal, exactly, with no second "nothing was sent" and no reservation', async () => {
    const moved = {
      fromAmount: '99000000000000000', transactionRequest: { value: `0x${BigInt('99000000000000000').toString(16)}` },
    } as unknown as LiFiQuote;
    m.payCrypto.mockResolvedValue({ intent, quote: moved, gasTopUp: false });
    await mount();
    act(() => hook.payWith(bnb, BSC, { pay: '0.084 BNB', fromAmount: '84000000000000000' }));
    await act(async () => { hook.authorization.onAuthorized(); });
    await settle();
    expect(hook.error).toBe(priceMovedMessage('99000000000000000', '84000000000000000', 18, 'BNB'));
    expect(hook.error).not.toMatch(/hour/i);
  });
});
