/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Buy screen routing and price_moved: a direct venue opens through the ramp
 * router, and a refusal for a moved rate shows the new rate and opens nothing;
 * confirming again holds the order to the rate she now sees. A converted venue
 * stays on /paycrest/buy.
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
  createBuy: jest.fn(),
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
jest.mock('@/services/lifi', () => ({
  ...jest.requireActual('@/services/lifi'),
  getLiFiTokens: jest.fn(async () => [
    { symbol: 'USDC', address: '0xusdc', chainId: 8453, name: 'USD Coin', decimals: 6, logoURI: '' },
  ]),
}));
jest.mock('@/services/chains/solanaWallet', () => ({ __esModule: true, getSolanaAddress: jest.fn(async () => undefined) }));
jest.mock('@/components/wallet/TokenAvatar', () => ({ __esModule: true, default: () => null }));
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
import AsyncStorage from '@react-native-async-storage/async-storage';
import BuyScreen from '../buy';
import * as PaycrestApi from '@/services/paycrestApi';
import * as RampApi from '@/services/rampApi';
import type { PaycrestOrder } from '@/services/paycrestApi';

const ramp = jest.mocked(RampApi);
const api = jest.mocked(PaycrestApi);

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
  id: 'o1', direction: 'buy', status: 'pending', reference: 'R1', currency: 'NGN', amount: '1000',
  chain: 'base', token: 'USDC', tokenAmount: '0.71', walletAddress: '0xuser',
  createdAt: '2026-09-25T10:00:00Z',
} as PaycrestOrder;

const direct = (rate: string, tokenAmount: number) => ({ chain: 'base', token: 'USDC', rate, tokenAmount, kind: 'direct' });

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
  // A remembered refund account, so the form is complete once an amount is in.
  await AsyncStorage.setItem('paycrest.refundAccount.v1.NGN', JSON.stringify({ code: '058', account: '0123456789' }));
});
afterEach(() => { jest.useRealTimers(); });

it('a direct buy opens through the ramp router at the confirmed rate; a moved rate re-confirms at the new one', async () => {
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  api.getQuote.mockResolvedValue({ venues: [direct('1397.59', 0.71)], best: direct('1397.59', 0.71) } as any);
  const moved = { data: { side: 'buy', venues: [direct('1405.00', 0.7)], best: direct('1405.00', 0.7) } };
  ramp.createRampBuy
    .mockRejectedValueOnce(new RampApi.RampError('The price changed', 409, 'price_moved', moved))
    .mockResolvedValueOnce(order);

  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<BuyScreen />); });
  await flush();
  await act(async () => { byLabel(tree, 'amount:You pay').props.onChangeText('1000'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(text(tree)).toContain('1397.59');

  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  expect(ramp.createRampBuy).toHaveBeenCalledTimes(1);
  const first = ramp.createRampBuy.mock.calls[0][0] as unknown as Record<string, unknown>;
  expect(first).toMatchObject({ quotedRate: '1397.59', chain: 'base', token: 'USDC', refundBankCode: '058' });
  expect(first).not.toHaveProperty('requestedChainId');
  expect(first).not.toHaveProperty('requestedTokenAddress');
  expect(alert).toHaveBeenCalledWith('The rate changed', expect.any(String));
  expect(text(tree)).toContain('1405.00');
  expect(text(tree)).not.toContain('1397.59');

  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  expect(ramp.createRampBuy).toHaveBeenCalledTimes(2);
  expect(ramp.createRampBuy.mock.calls[1][0]).toMatchObject({ quotedRate: '1405.00' });
  expect(api.createBuy).not.toHaveBeenCalled();
  alert.mockRestore();
});

it('a converted venue confirms through /paycrest/buy, never the ramp router', async () => {
  // Nothing fills USDC on Base directly; Paycrest settles USDT on Polygon and
  // a LiFi leg delivers the USDC she asked for.
  const converted = {
    chain: 'polygon', token: 'USDT', rate: '1410.00', spreadPercent: 1, tokenAmount: 0.7, kind: 'converted',
    viaChain: 'polygon', viaToken: 'USDT', requestedChain: 'base', requestedToken: 'USDC',
    requestedChainId: 8453, requestedTokenAddress: '0xusdc', markupBps: 120, promisedAmount: '700000', etaSeconds: 60,
  };
  api.getQuote.mockResolvedValue({ venues: [converted], best: converted } as any);
  api.createBuy.mockResolvedValueOnce({ ...order, routeKind: 'converted' } as PaycrestOrder);

  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<BuyScreen />); });
  await flush();
  await act(async () => { byLabel(tree, 'amount:You pay').props.onChangeText('1000'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(text(tree)).toContain('Settles as USDT');

  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  expect(api.createBuy).toHaveBeenCalledTimes(1);
  expect(api.createBuy.mock.calls[0][0]).toMatchObject({
    chain: 'polygon', token: 'USDT', requestedChainId: 8453, requestedTokenAddress: '0xusdc', quotedMarkupBps: 120,
  });
  expect(ramp.createRampBuy).not.toHaveBeenCalled();
});
