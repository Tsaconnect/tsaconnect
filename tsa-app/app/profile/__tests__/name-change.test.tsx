/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A verified user changing her name. What this pins: the explainer says what
 * the change means; a missing quote, money in flight or the yearly limit stops
 * it; nothing is paid without the account password AND PIN/biometric; the
 * paying wallet signs the server's challenge first and is the wallet sent,
 * debited and paid from; a lost answer is retried as the same request, a new
 * choice is a new one; a sent stablecoin fee is confirmed, never resent — polled
 * while the chain catches up, a refuted one a support case, a short or late one
 * refunded with its hash kept; an unsent transfer is recoverable after a
 * restart; "Verify your identity" opens the Didit session the server started;
 * every status reads as the server's message says; and she can cancel — told
 * what comes back — only before her first attempt, never mid-MCGP-debit.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn(), back: jest.fn(), canGoBack: () => true },
    useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]),
  };
});
const mockToast = { success: jest.fn(), error: jest.fn() };
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText, TextInput } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, {
      accessibilityLabel: `button:${props.title}`, onPress: props.onPress, disabled: !!props.disabled,
    }, props.title),
    Card: box,
    Icon: () => null,
    Spinner: () => null,
    ScreenHeader: (props: any) => react.createElement(RNText, null, props.title),
    StatusPill: (props: any) => react.createElement(RNText, null, `pill:${props.status}`),
    CopyableField: (props: any) => react.createElement(RNText, null, `${props.label}:${props.value}`),
    Input: (props: any) => react.createElement(TextInput, {
      accessibilityLabel: `input:${props.label}`, value: props.value, onChangeText: props.onChangeText,
    }),
    RadioRow: (props: any) => react.createElement(RNText, {
      accessibilityLabel: `radio:${props.label}`, onPress: props.onPress, disabled: !!props.disabled, selected: props.selected,
    }, `${props.label} ${props.sublabel ?? ''}`),
    SelectRow: (props: any) => react.createElement(RNText, { accessibilityLabel: `select:${props.label}`, onPress: props.onPress }, props.value ?? props.placeholder),
    useToast: () => mockToast,
  };
});
const mockAuthorization: { props: any } = { props: {} };
jest.mock('@/components/common/AuthorizationModal', () => ({
  __esModule: true,
  default: (props: any) => { mockAuthorization.props = props; return null; },
}));
const mockAssetPicker: { props: any } = { props: {} };
jest.mock('@/components/assets/AssetPicker', () => ({
  __esModule: true,
  default: (props: any) => { mockAssetPicker.props = props; return null; },
}));
const mockDidit: { props: any } = { props: null };
jest.mock('@/components/kyc/DiditSession', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    requestCameraPermission: jest.fn(async () => true),
    DiditWebView: (props: any) => { mockDidit.props = props; return react.createElement(RNText, null, `didit:${props.url}`); },
  };
});
const mockPolling: { args: any[] } = { args: [] };
jest.mock('@/hooks/useFocusPolling', () => ({ __esModule: true, useFocusPolling: (...a: any[]) => { mockPolling.args = a; } }));
jest.mock('@/utils/back', () => ({ __esModule: true, goBack: jest.fn() }));
jest.mock('@/services/wallet', () => ({ __esModule: true, getSignableWallet: jest.fn(), signMessage: jest.fn() }));
jest.mock('@/services/nameChangeApi', () => ({
  ...jest.requireActual('@/services/nameChangeApi'),
  getNameChange: jest.fn(),
  startNameChange: jest.fn(),
  startNameChangeVerification: jest.fn(),
  cancelNameChange: jest.fn(),
}));
jest.mock('@/services/nameChangePay', () => ({
  ...jest.requireActual('@/services/nameChangePay'),
  payNameChangeInStablecoin: jest.fn(),
  paymentPending: jest.fn(async () => null),
  confirmRecordedPayment: jest.fn(),
  forgetNameChangePayment: jest.fn(async () => {}),
  stablecoinShortfall: jest.fn(async () => null),
}));

import React from 'react';
import { Alert } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { TRANSPORT_ERROR } from '@/constants/api/config';
import { ApiError } from '@/services/apiRequest';
import {
  NameChangeError, cancelNameChange, getNameChange, startNameChange, startNameChangeVerification,
  type NameChange, type NameChangeOverview, type NameChangeQuote, type NameChangeStatus, type StablecoinOption,
  type StablecoinTransfer,
} from '@/services/nameChangeApi';
import {
  confirmRecordedPayment, forgetNameChangePayment, payNameChangeInStablecoin, paymentPending, stablecoinShortfall,
} from '@/services/nameChangePay';
import { getSignableWallet, signMessage } from '@/services/wallet';
import { requestCameraPermission } from '@/components/kyc/DiditSession';
import NameChangeScreen from '../name-change';

const WALLET = '0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A';
const OTHER = '0x52908400098527886E0F7030069857D2E4169EE7';
const USDC = '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913';
const HASH = `0x${'ab'.repeat(32)}`;

const usdc: StablecoinOption = {
  chain: 'base', chainId: 8453, token: 'USDC', tokenAddress: USDC, amount: '1.00', amountBaseUnits: '1000000', decimals: 6,
};
const usdt: StablecoinOption = {
  chain: 'bsc', chainId: 56, token: 'USDT', tokenAddress: '0x55d398326f99059fF775485246999027B3197955', amount: '1.00',
  amountBaseUnits: '1000000000000000000', decimals: 18,
};
const quote: NameChangeQuote = {
  feeUsd: '1.00',
  methods: [
    {
      method: 'mcgp_spendable', available: true, chain: 'sonic', token: 'MCGP', amount: '12.50',
      amountBaseUnits: '12500000000000000000', decimals: 18, balance: '100.00', balanceBaseUnits: '100000000000000000000',
    },
    { method: 'stablecoin', available: true, options: [usdc, usdt] },
  ],
};
const stableOnly: NameChangeQuote = { ...quote, methods: [quote.methods[1]] };
const limits = { maxPerYear: 2, completedInLastYear: 0, maxAttempts: 3, ttlDays: 7 };
const overview = (over: Partial<NameChangeOverview> = {}): NameChangeOverview =>
  ({ enabled: true, change: null, eligible: true, blockers: [], limits, quote, ...over });
const changeOf = (status: NameChangeStatus, over: Partial<NameChange> = {}): NameChange => ({
  id: 'nc1', status, feeUsd: '1.00', payMethod: 'mcgp_spendable', payToken: 'MCGP', payChain: 'sonic', payAmount: '12.50',
  payAmountBaseUnits: '12500000000000000000', payDecimals: 18, payFrom: WALLET, feeStatus: 'held', attempts: 0, maxAttempts: 3,
  expiresAt: '2026-10-05T12:00:00Z', oldName: 'Ada Obi', createdAt: '', updatedAt: '', ...over,
});
const transfer: StablecoinTransfer = {
  chain: 'base', chainId: 8453, token: 'USDC', tokenAddress: USDC, from: WALLET, to: OTHER,
  amount: '1.00', amountBaseUnits: '1000000', decimals: 6,
};
const stableChange = (status: NameChangeStatus, over: Partial<NameChange> = {}) => changeOf(status, {
  payMethod: 'stablecoin', payToken: 'USDC', payChain: 'base', payAmount: '1.00', payAmountBaseUnits: '1000000', payDecimals: 6,
  feeStatus: status === 'paying' ? 'none' : 'received', ...(status === 'paying' ? { transfer } : {}), ...over,
});
// An open change: the server quotes nothing while one is open.
const withChange = (c: NameChange, rest: Partial<NameChangeOverview> = {}) =>
  overview({ change: c, eligible: false, quote: null, ...rest });

const flush = () => act(async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); });
const byLabel = (tree: ReactTestRenderer, label: string) => {
  const found = tree.root.findAll((n) => n.props.accessibilityLabel === label);
  if (!found.length) throw new Error(`nothing labelled ${label}`);
  return found[0];
};
const has = (tree: ReactTestRenderer, label: string) => tree.root.findAll((n) => n.props.accessibilityLabel === label).length > 0;
const text = (tree: ReactTestRenderer) => JSON.stringify(tree.toJSON());
const press = (tree: ReactTestRenderer, label: string) => act(async () => { byLabel(tree, label).props.onPress(); });
const typePassword = (tree: ReactTestRenderer, pw = 'correct-horse') =>
  act(async () => { byLabel(tree, 'input:Account password').props.onChangeText(pw); });
const authorize = async () => { await act(async () => { mockAuthorization.props.onAuthorized(); }); await flush(); };

async function mount(first: NameChangeOverview) {
  jest.mocked(getNameChange).mockResolvedValue(first);
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<NameChangeScreen />); });
  await flush();
  return tree;
}

async function pickUsdc(tree: ReactTestRenderer) {
  await press(tree, 'select:Pay with');
  await act(async () => { mockAssetPicker.props.onSelect({ symbol: 'USDC', address: USDC, decimals: 6 }, 8453); });
}

beforeEach(() => {
  jest.clearAllMocks();
  mockDidit.props = null;
  mockPolling.args = [];
  jest.mocked(getSignableWallet).mockResolvedValue(WALLET);
  jest.mocked(paymentPending).mockResolvedValue(null);
  jest.mocked(stablecoinShortfall).mockResolvedValue(null);
});

describe('before a change', () => {
  it('explains what a name change means, reading the fee and MCGP figures as the strings they are', async () => {
    const tree = await mount(overview());
    const t = text(tree);
    expect(getNameChange).toHaveBeenCalledWith(WALLET);
    expect(t).toContain('exactly what your ID document shows');
    expect(t).toContain('The re-verification fee is $1.00.');
    expect(t).toContain('You stay verified meanwhile');
    expect(t).toContain('up to 3 verification attempts within 7 days');
    expect(t).toContain('up to 2 times a year');
    expect(t).toContain('12.5 MCGP · you have 100');
  });

  it('money in flight — an MCGP purchase still paying out — is listed, and there is nothing to pay', async () => {
    const tree = await mount(overview({
      eligible: false, blockers: [{ code: 'it_buy_in_flight', message: 'An MCGP purchase is still being paid out.' }],
    }));
    expect(text(tree)).toContain('Finish these first');
    expect(text(tree)).toContain('An MCGP purchase is still being paid out.');
    expect(has(tree, 'button:Pay $1.00')).toBe(false);
  });

  it('past the yearly limit, says when she can change it again', async () => {
    const tree = await mount(overview({ eligible: false, nextAllowedAt: '2027-03-01T12:00:00Z' }));
    expect(text(tree)).toContain('You can change your name again on');
    expect(text(tree)).toContain('2027');
    expect(has(tree, 'button:Pay $1.00')).toBe(false);
  });

  it('no quote (null) means no payment form, whatever else the overview says — and says the fee couldn\'t be worked out', async () => {
    const tree = await mount(overview({ quote: null }));
    expect(has(tree, 'button:Pay $1.00')).toBe(false);
    expect(has(tree, 'input:Account password')).toBe(false);
    expect(text(tree)).toContain('We couldn’t work out the fee right now');
    jest.mocked(getNameChange).mockResolvedValue(overview());
    await press(tree, 'button:Try again');
    await flush();
    expect(has(tree, 'button:Pay $1.00')).toBe(true);
  });

  it.each([
    ['disabled', 'Name changes aren’t available yet.'],
    ['not_verified', 'Your identity isn’t verified yet'],
  ] as const)('with no change, unavailableReason %s says why', async (reason, words) => {
    const tree = await mount(overview({ enabled: reason !== 'disabled', unavailableReason: reason, eligible: false, quote: null }));
    expect(text(tree)).toContain(words);
    expect(has(tree, 'button:Pay $1.00')).toBe(false);
  });

  it('a service switched off entirely (503 name_change_disabled) reads as unavailable', async () => {
    jest.mocked(getNameChange).mockRejectedValue(new NameChangeError('name changes aren\'t available right now', 503, 'name_change_disabled'));
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(<NameChangeScreen />); });
    await flush();
    expect(text(tree)).toContain('Name changes aren’t available yet.');
  });

  it('spendable MCGP short of the fee (in base units) cannot be chosen', async () => {
    const short: NameChangeQuote = {
      ...quote,
      methods: [{ ...quote.methods[0], balance: '12.49', balanceBaseUnits: '12490000000000000000' } as NameChangeQuote['methods'][number], quote.methods[1]],
    };
    const tree = await mount(overview({ quote: short }));
    expect(byLabel(tree, 'radio:Spendable MCGP').props.disabled).toBe(true);
    // Stablecoin becomes the default; a coin is still to be chosen.
    expect(text(tree)).toContain('Choose the coin you pay with.');
  });

  it('asks for the account password before PIN/biometric', async () => {
    const tree = await mount(overview());
    await press(tree, 'button:Pay $1.00');
    expect(text(tree)).toContain('Enter your account password to confirm.');
    expect(mockAuthorization.props.visible).toBeFalsy();
    expect(startNameChange).not.toHaveBeenCalled();
  });

  it('a money_in_flight refusal is shown, and the blockers the overview now lists stop the form', async () => {
    const tree = await mount(overview());
    jest.mocked(startNameChange).mockRejectedValue(new NameChangeError('finish what\'s in progress before changing your name', 409, 'money_in_flight',
      { data: { blockers: [{ code: 'it_buy_in_flight', message: 'An MCGP purchase is still being paid out.' }] } }));
    jest.mocked(getNameChange).mockResolvedValue(overview({
      eligible: false, blockers: [{ code: 'it_buy_in_flight', message: 'An MCGP purchase is still being paid out.' }],
    }));
    await typePassword(tree);
    await press(tree, 'button:Pay $1.00');
    await authorize();
    expect(text(tree)).toContain('finish what\'s in progress before changing your name');
    expect(text(tree)).toContain('An MCGP purchase is still being paid out.');
    expect(has(tree, 'button:Pay $1.00')).toBe(false);
  });
});

describe('paying the fee', () => {
  it('spendable MCGP: after password + PIN the server debits the account\'s proven wallet — this phone signs nothing', async () => {
    const tree = await mount(overview());
    await typePassword(tree);
    await press(tree, 'button:Pay $1.00');
    expect(startNameChange).not.toHaveBeenCalled();
    expect(mockAuthorization.props.visible).toBe(true);
    expect(mockAuthorization.props.title).toBe('Authorize paying the name change fee');

    jest.mocked(startNameChange).mockResolvedValue({ change: changeOf('paid') });
    jest.mocked(getNameChange).mockResolvedValue(withChange(changeOf('paid')));
    await authorize();
    expect(signMessage).not.toHaveBeenCalled();
    expect(startNameChange).toHaveBeenCalledWith({ payMethod: 'mcgp_spendable', password: 'correct-horse' }, expect.any(String), WALLET);
    expect(payNameChangeInStablecoin).not.toHaveBeenCalled();
    expect(text(tree)).toContain('button:Verify your identity');
  });

  it('a zero fee: only the password', async () => {
    const tree = await mount(overview({ quote: { feeUsd: '0.00', methods: [] } }));
    expect(text(tree)).toContain('There is no fee for this change.');
    await typePassword(tree);
    jest.mocked(startNameChange).mockResolvedValue({ change: changeOf('paid', { payMethod: 'free' }) });
    await press(tree, 'button:Continue');
    await authorize();
    expect(startNameChange).toHaveBeenCalledWith({ payMethod: 'free', password: 'correct-horse' }, expect.any(String), null);
  });

  it('a stablecoin: the coin she picks, the transfer the server returns, sent from her wallet', async () => {
    const tree = await mount(overview());
    await press(tree, 'radio:USDT or USDC');
    await press(tree, 'select:Pay with');
    // The picker offers only what the fee can be paid in, by contract.
    expect(mockAssetPicker.props.allow(8453, { symbol: 'USDC', address: USDC })).toBe(true);
    expect(mockAssetPicker.props.allow(8453, { symbol: 'USDC', address: OTHER })).toBe(false);
    expect(mockAssetPicker.props.allow(8453, { symbol: 'DAI' })).toBe(false);
    await act(async () => { mockAssetPicker.props.onSelect({ symbol: 'USDC', address: USDC, decimals: 6 }, 8453); });
    expect(text(tree)).toContain('1 USDC on Base');

    jest.mocked(startNameChange).mockResolvedValue({ change: stableChange('paying'), transfer });
    jest.mocked(payNameChangeInStablecoin).mockResolvedValue(stableChange('paid'));
    jest.mocked(getNameChange).mockResolvedValue(withChange(stableChange('paid')));
    await typePassword(tree);
    await press(tree, 'button:Pay $1.00');
    await authorize();

    expect(stablecoinShortfall).toHaveBeenCalledWith(usdc, WALLET);
    expect(startNameChange).toHaveBeenCalledWith({
      payMethod: 'stablecoin', chain: 'base', token: 'USDC', password: 'correct-horse',
    }, expect.any(String), WALLET);
    expect(payNameChangeInStablecoin).toHaveBeenCalledWith('nc1', transfer, usdc, WALLET);
    expect(text(tree)).toContain('button:Verify your identity');
  });

  it('a lost answer is retried as the same request (same key, same body); another choice is a new request', async () => {
    const tree = await mount(overview());
    await typePassword(tree);
    jest.mocked(startNameChange).mockRejectedValue(new ApiError('Could not reach the server.', 0, TRANSPORT_ERROR));
    await press(tree, 'button:Pay $1.00');
    await authorize();
    await press(tree, 'button:Pay $1.00');
    await authorize();
    const [first, second] = jest.mocked(startNameChange).mock.calls;
    expect(second).toEqual(first);

    // Switching to a stablecoin is a different request: a new key.
    await press(tree, 'radio:USDT or USDC');
    await pickUsdc(tree);
    await press(tree, 'button:Pay $1.00');
    await authorize();
    const third = jest.mocked(startNameChange).mock.calls[2];
    expect(third[1]).not.toBe(first[1]);
    expect(third[0]).toMatchObject({ payMethod: 'stablecoin', chain: 'base', token: 'USDC' });
  });

  it('a lost answer whose change then shows: starting over is a new request with a new key', async () => {
    const tree = await mount(overview());
    await typePassword(tree);
    jest.mocked(startNameChange).mockRejectedValueOnce(new ApiError('Could not reach the server.', 0, TRANSPORT_ERROR));
    // The request the answer was lost for did create a change (cancelled since): the re-read shows it.
    jest.mocked(getNameChange).mockResolvedValue(withChange(changeOf('cancelled', { feeStatus: 'reversed' }), { eligible: true, quote }));
    await press(tree, 'button:Pay $1.00');
    await authorize();
    expect(text(tree)).toContain('Name change cancelled');

    await press(tree, 'button:Change my name again');
    await typePassword(tree);
    jest.mocked(startNameChange).mockResolvedValue({ change: changeOf('paid', { id: 'nc2' }) });
    await press(tree, 'button:Pay $1.00');
    await authorize();
    const [first, second] = jest.mocked(startNameChange).mock.calls;
    // Same choice, but not the same request: the old key would only replay the first change's creation.
    expect(second[0]).toEqual(first[0]);
    expect(second[1]).not.toBe(first[1]);
  });

  it('a lost answer while an older change still shows is retried with the same key; a new change seen ends it', async () => {
    const old = changeOf('rejected', { id: 'old', attempts: 1 });
    const tree = await mount(overview({ change: old, eligible: true }));
    await press(tree, 'button:Change my name again');
    await typePassword(tree);
    jest.mocked(startNameChange).mockRejectedValue(new ApiError('Could not reach the server.', 0, TRANSPORT_ERROR));
    await press(tree, 'button:Pay $1.00');
    await authorize();
    await press(tree, 'button:Pay $1.00');
    await authorize();
    const [first, second] = jest.mocked(startNameChange).mock.calls;
    expect(second[1]).toBe(first[1]);

    // The re-read now shows a change made since (the lost request's): that request is over.
    jest.mocked(getNameChange).mockResolvedValue(overview({ change: changeOf('rejected', { id: 'nc2', attempts: 1 }), eligible: true }));
    await press(tree, 'button:Pay $1.00');
    await authorize();
    await press(tree, 'button:Pay $1.00');
    await authorize();
    const [, , third, fourth] = jest.mocked(startNameChange).mock.calls;
    expect(third[1]).toBe(first[1]);
    expect(fourth[1]).not.toBe(first[1]);
  });

  it('names this phone\'s wallet on the create, so the server holds the fee to the wallet that pays', async () => {
    const tree = await mount(overview());
    await typePassword(tree);
    jest.mocked(startNameChange).mockResolvedValue({ change: changeOf('paid') });
    await press(tree, 'button:Pay $1.00');
    await authorize();
    expect(startNameChange).toHaveBeenCalledWith(expect.anything(), expect.any(String), WALLET);
  });

  it.each([
    ['WALLET_KEY_UNAVAILABLE', 403, 'This wallet\'s key isn\'t on this device, so it can\'t be linked to your account.'],
    ['password_incorrect', 401, 'Incorrect password'],
    ['account_locked', 423, 'Your account is temporarily locked. Try again later.'],
  ])('an answered %s refusal is shown, and the next try is a fresh request', async (code, status, words) => {
    const tree = await mount(overview());
    await typePassword(tree);
    jest.mocked(startNameChange).mockRejectedValueOnce(new NameChangeError(words, status, code));
    await press(tree, 'button:Pay $1.00');
    await authorize();
    expect(text(tree)).toContain(words);

    jest.mocked(startNameChange).mockResolvedValue({ change: changeOf('paid') });
    await press(tree, 'button:Pay $1.00');
    await authorize();
    const [first, second] = jest.mocked(startNameChange).mock.calls;
    expect(second[1]).not.toBe(first[1]);
  });

  it('no wallet on this phone: nothing is created', async () => {
    jest.mocked(getSignableWallet).mockResolvedValue(null);
    const tree = await mount(overview());
    expect(text(tree)).toContain('Import your wallet on this phone');
    expect(byLabel(tree, 'button:Pay $1.00').props.disabled).toBe(true);
  });

  it('a wallet short of the fee creates nothing', async () => {
    const tree = await mount(overview({ quote: stableOnly }));
    await pickUsdc(tree);
    jest.mocked(stablecoinShortfall).mockResolvedValue('You have 0.2 USDC on Base — this payment needs 1.');
    await typePassword(tree);
    await press(tree, 'button:Pay $1.00');
    await authorize();
    expect(startNameChange).not.toHaveBeenCalled();
    expect(text(tree)).toContain('this payment needs 1.');
  });

  it('a send that failed can be retried with the same transfer, checked against the change\'s own price', async () => {
    const tree = await mount(overview({ quote: stableOnly }));
    await pickUsdc(tree);
    jest.mocked(startNameChange).mockResolvedValue({ change: stableChange('paying'), transfer });
    jest.mocked(payNameChangeInStablecoin).mockRejectedValueOnce(new Error('Not enough ETH on Base to pay gas.'));
    jest.mocked(getNameChange).mockResolvedValue(withChange(stableChange('paying')));
    await typePassword(tree);
    await press(tree, 'button:Pay $1.00');
    await authorize();
    expect(text(tree)).toContain('Not enough ETH on Base to pay gas.');
    expect(text(tree)).toContain('Send 1 USDC on Base to finish.');

    jest.mocked(payNameChangeInStablecoin).mockResolvedValue(stableChange('paid'));
    await press(tree, 'button:Pay now');
    await authorize();
    expect(startNameChange).toHaveBeenCalledTimes(1);
    expect(jest.mocked(payNameChangeInStablecoin).mock.calls[1]).toEqual(['nc1', transfer, usdc, WALLET]);
  });

  it('after a restart, an unsent transfer is recovered from the change itself — only from the proven wallet', async () => {
    let tree = await mount(withChange(stableChange('paying')));
    expect(has(tree, 'button:Pay now')).toBe(true);
    expect(has(tree, 'button:Cancel name change')).toBe(true);

    jest.mocked(getSignableWallet).mockResolvedValue(OTHER);
    tree = await mount(withChange(stableChange('paying')));
    expect(text(tree)).toContain('must be paid from the wallet linked to your account (0x19E7…ff2A)');
    expect(has(tree, 'button:Pay now')).toBe(false);
  });

  it('payment_pending_chain: "sent, confirming" — polled with its hash, never resent, no cancel', async () => {
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'sent', txHash: HASH });
    const tree = await mount(withChange(stableChange('paying')));
    expect(text(tree)).toContain('Payment sent — confirming');
    expect(has(tree, 'button:Cancel name change')).toBe(false);
    expect(has(tree, 'button:Pay now')).toBe(false);

    const [poll, , active] = mockPolling.args;
    expect(active).toBe(true);
    jest.mocked(confirmRecordedPayment).mockResolvedValue(null);
    await act(async () => { await poll(); });
    expect(confirmRecordedPayment).toHaveBeenCalledWith('nc1');

    jest.mocked(paymentPending).mockResolvedValue(null);
    jest.mocked(getNameChange).mockResolvedValue(withChange(stableChange('paid')));
    await press(tree, 'button:Check now');
    await flush();
    expect(text(tree)).toContain('button:Verify your identity');
    expect(payNameChangeInStablecoin).not.toHaveBeenCalled();
  });

  it('confirming is paced: every poll reads the change, the hash is asked about at most every 30 s', async () => {
    const now = jest.spyOn(Date, 'now').mockReturnValue(1_000_000);
    try {
      jest.mocked(paymentPending).mockResolvedValue({ kind: 'sent', txHash: HASH });
      jest.mocked(confirmRecordedPayment).mockResolvedValue(null);
      await mount(withChange(stableChange('paying')));
      expect(confirmRecordedPayment).toHaveBeenCalledTimes(1);
      const [poll] = mockPolling.args;
      const reads = jest.mocked(getNameChange).mock.calls.length;
      await act(async () => { await poll(); });
      await act(async () => { await poll(); });
      expect(getNameChange).toHaveBeenCalledTimes(reads + 2);
      expect(confirmRecordedPayment).toHaveBeenCalledTimes(1);
      now.mockReturnValue(1_000_000 + 30_000);
      await act(async () => { await poll(); });
      expect(confirmRecordedPayment).toHaveBeenCalledTimes(2);
    } finally {
      now.mockRestore();
    }
  });

  it('an answer of "no change it can pay" (no_open_change / bad_state) is terminal: a support case, polling stops', async () => {
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'unmatched', txHash: HASH });
    const tree = await mount(withChange(stableChange('paying')));
    expect(text(tree)).toContain('This payment couldn’t be used');
    expect(text(tree)).toContain('We couldn’t match this transaction');
    expect(text(tree)).toContain(`Transaction:${HASH}`);
    expect(mockPolling.args[2]).toBe(false);
    expect(confirmRecordedPayment).not.toHaveBeenCalled();
    expect(has(tree, 'button:Pay now')).toBe(false);
  });

  it('a closed change whose payment support now holds shows the hash, and neither confirms nor polls', async () => {
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'unmatched', txHash: HASH });
    const tree = await mount(withChange(stableChange('cancelled', { feeStatus: 'none' })));
    expect(text(tree)).toContain(`Transaction:${HASH}`);
    expect(mockPolling.args[2]).toBe(false);
    expect(confirmRecordedPayment).not.toHaveBeenCalled();
    expect(forgetNameChangePayment).not.toHaveBeenCalled();
  });

  it('cancelling beside a payment support holds never says nothing was charged', async () => {
    const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'refuted', txHash: HASH });
    const tree = await mount(withChange(stableChange('paying')));
    await press(tree, 'button:Cancel name change');
    const message = alert.mock.calls[0][1] as string;
    expect(message).not.toContain('Nothing has been charged');
    expect(message).toContain('isn’t returned by cancelling');
    alert.mockRestore();
  });

  it('payment_mismatch: a support case, with the transaction to quote', async () => {
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'refuted', txHash: HASH });
    const tree = await mount(withChange(stableChange('paying')));
    expect(text(tree)).toContain('This payment couldn’t be used');
    expect(text(tree)).toContain(`Transaction:${HASH}`);
    expect(text(tree)).toContain('support@');
    expect(has(tree, 'button:Pay now')).toBe(false);
  });

  it('paid short or late: the change comes back cancelled and refunding — its message shown, its hash kept', async () => {
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'sent', txHash: HASH });
    const tree = await mount(withChange(stableChange('cancelled', {
      feeStatus: 'refund_sending', message: 'Your payment was less than the fee, so it is being refunded. Start again to change your name.',
    }), { eligible: true, quote }));
    expect(text(tree)).toContain('Your payment was less than the fee, so it is being refunded.');
    expect(text(tree)).toContain(`Transaction:${HASH}`);
    expect(text(tree)).toContain('If the refund hasn’t reached your wallet');
    expect(forgetNameChangePayment).not.toHaveBeenCalled();
    expect(confirmRecordedPayment).not.toHaveBeenCalled();
    expect(has(tree, 'button:Change my name again')).toBe(true);
  });

  it('a change that closed before its sent payment was proven is still confirmed (to be refunded), and keeps polling', async () => {
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'sent', txHash: HASH });
    jest.mocked(confirmRecordedPayment).mockResolvedValue(null);
    const tree = await mount(withChange(stableChange('cancelled', {
      feeStatus: 'none', message: 'This name change was closed because its fee wasn\'t paid.',
    })));
    expect(confirmRecordedPayment).toHaveBeenCalledWith('nc1');
    expect(text(tree)).toContain('We’re still confirming it');
    expect(mockPolling.args[2]).toBe(true);
    expect(forgetNameChangePayment).not.toHaveBeenCalled();
  });

  it('a change that used the payment lets the hash go', async () => {
    jest.mocked(paymentPending).mockResolvedValue({ kind: 'sent', txHash: HASH });
    await mount(withChange(stableChange('verifying', { attempts: 1 })));
    expect(forgetNameChangePayment).toHaveBeenCalledWith('nc1');
  });

  it('MCGP still settling (paying): waits and polls — and offers no cancel', async () => {
    const tree = await mount(withChange(changeOf('paying', { feeStatus: 'none' })));
    expect(text(tree)).toContain('Confirming your payment');
    expect(has(tree, 'button:Cancel name change')).toBe(false);
    expect(mockPolling.args[2]).toBe(true);
  });
});

describe('verifying', () => {
  it('paid: "Verify your identity" opens the Didit session the server started, and re-reads the outcome', async () => {
    const tree = await mount(withChange(changeOf('paid')));
    jest.mocked(startNameChangeVerification).mockResolvedValue({
      url: 'https://verify.didit.me/session/abc', sessionId: 's1', resumed: false, attempts: 1, maxAttempts: 3,
    });
    await press(tree, 'button:Verify your identity');
    await flush();
    expect(requestCameraPermission).toHaveBeenCalled();
    expect(text(tree)).toContain('didit:https://verify.didit.me/session/abc');

    jest.mocked(getNameChange).mockResolvedValue(withChange(changeOf('verifying', { attempts: 1 })));
    await act(async () => { mockDidit.props.onFinished(); });
    await flush();
    expect(text(tree)).toContain('Checking your ID');
  });

  it('a session refusal (session_limit) is shown in the server\'s words', async () => {
    const tree = await mount(withChange(changeOf('paid')));
    jest.mocked(startNameChangeVerification).mockRejectedValue(
      new NameChangeError('you\'ve started too many verifications recently — try again later', 429, 'session_limit'));
    await press(tree, 'button:Verify your identity');
    await flush();
    expect(text(tree)).toContain('started too many verifications recently');
    expect(mockDidit.props).toBeNull();
  });

  it('declined: tries again while attempts are left', async () => {
    const tree = await mount(withChange(changeOf('declined', { attempts: 1 })));
    expect(text(tree)).toContain('You have 2 attempts left');
    expect(has(tree, 'button:Cancel name change')).toBe(false);
    jest.mocked(startNameChangeVerification).mockResolvedValue({
      url: 'https://verify.didit.me/session/2', sessionId: 's2', resumed: false, attempts: 2, maxAttempts: 3,
    });
    await press(tree, 'button:Try again');
    await flush();
    expect(text(tree)).toContain('didit:https://verify.didit.me/session/2');
  });

  it('declined with no attempts left offers no retry', async () => {
    const tree = await mount(withChange(changeOf('declined', { attempts: 3 })));
    expect(text(tree)).toContain('You have no attempts left.');
    expect(has(tree, 'button:Try again')).toBe(false);
  });
});

describe('outcomes', () => {
  it('held for review: the server\'s message, or both names', async () => {
    let tree = await mount(withChange(changeOf('held_for_review', { attempts: 1, newVerifiedName: 'Ada Uzochukwu' })));
    expect(text(tree)).toContain('Under review');
    expect(text(tree)).toContain('Your ID shows Ada Uzochukwu');
    tree = await mount(withChange(changeOf('held_for_review', { attempts: 1, message: 'We’re checking your new ID.' })));
    expect(text(tree)).toContain('We’re checking your new ID.');
  });

  it('approved shows the new name', async () => {
    const tree = await mount(withChange(changeOf('approved', { attempts: 1, newVerifiedName: 'Ada Obianuju Obi' })));
    expect(text(tree)).toContain('Your name is now Ada Obianuju Obi.');
  });

  it('rejected shows the message meant for her, and she may start again', async () => {
    const tree = await mount(overview({
      change: changeOf('rejected', { attempts: 1, message: 'Your new ID could not be matched to you.' }), eligible: true,
    }));
    expect(text(tree)).toContain('Your new ID could not be matched to you.');
    await press(tree, 'button:Change my name again');
    expect(text(tree)).toContain('button:Pay $1.00');
  });

  it('an ended change with nothing new quoted offers no restart', async () => {
    const tree = await mount(withChange(changeOf('lapsed', { attempts: 3 })));
    expect(text(tree)).toContain('This name change expired');
    expect(text(tree)).toContain('A new change needs a new fee.');
    expect(has(tree, 'button:Change my name again')).toBe(false);
  });

  it('a past change is still shown after name changes are switched off', async () => {
    const tree = await mount(withChange(changeOf('approved', { newVerifiedName: 'Ada Obi-Uzo' }), { enabled: false, unavailableReason: 'disabled' }));
    expect(text(tree)).toContain('Your name is now Ada Obi-Uzo.');
  });
});

describe('cancelling', () => {
  it('before the first attempt: says what comes back, then cancels', async () => {
    const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
    const tree = await mount(withChange(stableChange('paid')));
    await press(tree, 'button:Cancel name change');
    const [, message, buttons] = alert.mock.calls[0] as [string, string, any[]];
    expect(message).toContain('We send 1 USDC back to the wallet you paid from (0x19E7…ff2A), on Base.');

    jest.mocked(cancelNameChange).mockResolvedValue(stableChange('cancelled'));
    jest.mocked(getNameChange).mockResolvedValue(withChange(stableChange('cancelled', {
      feeStatus: 'refund_sending', message: 'You cancelled this name change. Your fee is being returned.',
    })));
    await act(async () => { buttons.find((b) => b.style === 'destructive').onPress(); });
    await flush();
    expect(cancelNameChange).toHaveBeenCalled();
    expect(mockToast.success).toHaveBeenCalledWith('Name change cancelled');
    expect(text(tree)).toContain('Your fee is being returned.');
    alert.mockRestore();
  });

  it('cancel_limit: dated from the refusal, and she may still verify', async () => {
    const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
    const tree = await mount(withChange(changeOf('paid')));
    expect(alert).not.toHaveBeenCalled();
    await press(tree, 'button:Cancel name change');
    expect(alert.mock.calls[0][1]).toContain('Your 12.5 MCGP is returned to your MCGP balance.');
    jest.mocked(cancelNameChange).mockRejectedValue(new NameChangeError('you\'ve already cancelled a name change recently', 409, 'cancel_limit',
      { data: { nextAllowedAt: '2026-10-20T09:00:00Z' } }));
    const buttons = alert.mock.calls[0][2] as any[];
    await act(async () => { buttons.find((b) => b.style === 'destructive').onPress(); });
    await flush();
    expect(text(tree)).toContain('can be cancelled for a refund only from');
    expect(text(tree)).toContain('button:Verify your identity');
    alert.mockRestore();
  });
});
