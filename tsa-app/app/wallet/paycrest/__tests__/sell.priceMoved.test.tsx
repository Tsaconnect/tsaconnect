/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Sell screen, price_moved: a refusal for a moved rate shows the new rate and
 * opens nothing; confirming again holds the order to the rate she now sees.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

jest.mock('expo-router', () => ({
  __esModule: true,
  router: { replace: jest.fn(), back: jest.fn() },
  useLocalSearchParams: () => ({}),
}));

jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  const label = (text: any) => react.createElement(RNText, null, text);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) =>
      react.createElement(View, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress, disabled: props.disabled },
        label(props.title)),
    Spinner: () => null,
    Card: box,
    SummaryCard: box,
    SummaryRow: (props: any) => label(`${props.label}: ${props.value}`),
    CopyableField: (props: any) => label(props.value),
    SelectRow: (props: any) =>
      react.createElement(View, { accessibilityLabel: `row:${props.label}`, onPress: props.onPress },
        label(`${props.label}: ${props.value ?? props.placeholder ?? ''}`)),
    AmountInput: (props: any) =>
      react.createElement(View, { accessibilityLabel: `amount:${props.label}`, onChangeText: props.onChangeText },
        label(props.label)),
    Input: (props: any) =>
      react.createElement(View, { accessibilityLabel: `input:${props.label}`, onChangeText: props.onChangeText }),
    PickerSheet: (props: any) =>
      props.visible
        ? react.createElement(View, { accessibilityLabel: `sheet:${props.title}`, onSelect: props.onSelect, items: props.items })
        : null,
  };
});

jest.mock('@/components/paycrest/DepositProgress', () => ({ __esModule: true, DepositProgress: () => null }));
jest.mock('@/components/paycrest/SwappedSellForm', () => ({
  __esModule: true, SWAPPED_STAGE_LABELS: {}, SwappedSellForm: () => null, SwappedSellStatus: () => null,
}));
jest.mock('@/components/assets/AssetPicker', () => ({ __esModule: true, default: () => null }));

jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true,
  useSignableWallet: () => ({ address: '0xUser', ready: true }),
}));
jest.mock('@/hooks/useCountdown', () => ({
  __esModule: true,
  useCountdown: () => ({ label: '10:00', expired: false, secs: 600 }),
}));
jest.mock('@/hooks/useRampCurrency', () => {
  const value = {
    code: 'NGN', currency: { symbol: '₦' }, setCode: () => {}, options: [{ id: 'NGN', label: 'NGN' }],
  };
  return { __esModule: true, useRampCurrency: () => value, rampCurrencyLabel: () => 'Naira' };
});
jest.mock('@/hooks/useScreenTitle', () => ({ __esModule: true, useScreenTitle: () => {} }));
jest.mock('@/hooks/useSwappedSell', () => {
  const state = {
    value: {
      stage: 'idle', error: undefined, order: undefined, depositTx: undefined,
      start: jest.fn(), resume: jest.fn(), cancel: jest.fn(async () => {}),
    } as any,
  };
  return { __esModule: true, __state: state, useSwappedSell: () => state.value, canCancelSale: () => false };
});

jest.mock('@/services/ngBanks', () => ({
  __esModule: true,
  resolveAccountName: jest.fn(async () => ({ ok: true, accountName: 'ADA OBI' })),
}));
jest.mock('@/services/paycrestApi', () => ({
  ...jest.requireActual('@/services/paycrestApi'),
  getQuote: jest.fn(),
  getPayoutInstitutions: jest.fn(async () => [{ code: '058', name: 'GTBank' }]),
  createSell: jest.fn(),
  cancelOrder: jest.fn(),
  claimPayment: jest.fn(async () => ({})),
  getOrder: jest.fn(),
}));
jest.mock('@/services/rampApi', () => ({
  ...jest.requireActual('@/services/rampApi'),
  getRampQuote: jest.fn(),
  createRampSell: jest.fn(),
  createRampBuy: jest.fn(),
  getRampOrder: jest.fn(),
  cancelRampOrder: jest.fn(),
  claimRampPayment: jest.fn(async () => ({})),
  listRampOrders: jest.fn(async () => []),
}));
jest.mock('@/services/paycrestSellSwap', () => ({
  __esModule: true,
  getSwappedSellState: jest.fn(async () => ({ enabled: false, intent: null })),
}));
// The real deposit module, bar the send: the guard (services/depositGuard) and
// the outcome-unknown error it keys on are what run.
jest.mock('@/services/paycrestDeposit', () => ({
  ...jest.requireActual('@/services/paycrestDeposit'),
  __esModule: true,
  sendSponsoredPaycrestDeposit: jest.fn(),
}));
jest.mock('@/services/gasSponsorship', () => ({
  __esModule: true,
  getSponsoredRoutes: jest.fn(async () => []),
  getSponsorshipOffer: jest.fn(async () => ({ mode: 'none' })),
  isGasSponsored: () => false,
  SPONSORSHIP_IN_PROGRESS: 'sponsorship_in_progress',
}));
jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(async () => ({ success: false })),
  normalizeWalletBalances: () => [],
  getAuthHeaders: jest.fn(async () => ({})),
}));
jest.mock('@/services/wallet', () => ({ __esModule: true }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { Alert } from 'react-native';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import SellScreen from '../sell';
import * as RampApi from '@/services/rampApi';
import type { PaycrestOrder } from '@/services/paycrestApi';

const ramp = jest.mocked(RampApi);

jest.setTimeout(30_000);

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
const text = (tree: ReactTestRenderer) => textNodes(tree.toJSON()).join(' ');
const byLabel = (tree: ReactTestRenderer, l: string) => {
  const hit = tree.root.findAll((n) => typeof (n as { type?: unknown }).type === 'string' && n.props.accessibilityLabel === l)[0];
  if (!hit) throw new Error(`nothing labelled "${l}"`);
  return hit;
};
const flush = async () => {
  for (let i = 0; i < 8; i += 1) await act(async () => { await new Promise((r) => setImmediate(r)); });
};

const order = {
  id: 'o1', direction: 'sell', status: 'awaiting_deposit', reference: 'R1', currency: 'NGN', amount: '50000',
  chain: 'base', token: 'USDC', tokenAmount: '36.42', walletAddress: '0xuser',
  depositAddress: '0x2222222222222222222222222222222222222222', createdAt: '2026-09-25T10:00:00Z',
} as PaycrestOrder;

const venue = (handle: string, rate: string, tokenAmount: number) =>
  ({ handle, chain: 'base', token: 'USDC', rate, tokenAmount });

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
});
afterEach(() => { jest.useRealTimers(); });

it('a moved rate shows the new one, opens nothing, and the next confirm is held to the new rate', async () => {
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  ramp.getRampQuote.mockResolvedValue({ venues: [venue('h1', '1353.25', 0.59)], best: venue('h1', '1353.25', 0.59) } as any);
  const moved = { data: { side: 'sell', venues: [venue('h2', '1340.00', 0.6)], best: venue('h2', '1340.00', 0.6) } };
  ramp.createRampSell
    .mockRejectedValueOnce(new RampApi.RampError('The price changed', 409, 'price_moved', moved))
    .mockResolvedValueOnce(order);

  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<SellScreen />); });
  await flush();
  await act(async () => { byLabel(tree, 'amount:You receive').props.onChangeText('800'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  await act(async () => { byLabel(tree, 'row:Pay into').props.onPress(); });
  await act(async () => { byLabel(tree, 'sheet:Choose a destination').props.onSelect({ id: '058' }); });
  await act(async () => { byLabel(tree, 'input:Account number').props.onChangeText('0123456789'); });
  await flush();

  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  expect(ramp.createRampSell).toHaveBeenCalledTimes(1);
  expect(ramp.createRampSell.mock.calls[0][0]).toMatchObject({ quotedRate: '1353.25', handle: 'h1' });
  expect(alert).toHaveBeenCalledWith('The rate changed', expect.any(String));
  expect(router.replace).not.toHaveBeenCalled();
  expect(text(tree)).toContain('1340.00');
  expect(text(tree)).not.toContain('1353.25');
  expect(text(tree)).not.toContain(order.depositAddress); // no order screen

  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  expect(ramp.createRampSell).toHaveBeenCalledTimes(2);
  expect(ramp.createRampSell.mock.calls[1][0]).toMatchObject({ quotedRate: '1340.00', handle: 'h2' });
  alert.mockRestore();
});
