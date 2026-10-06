/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The buy screen with the company wallet and Flutterwave behind the router:
 * venues come from /ramp/quote even while Paycrest's naira book is empty, fee
 * lines are shown, an IT order goes to the Instant Transfer order screen, and
 * a purchase already waiting for payment offers View and Cancel.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('expo-router', () => ({
  __esModule: true,
  router: { replace: jest.fn(), push: jest.fn(), back: jest.fn() },
  useLocalSearchParams: () => ({}),
}));
jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ colors: new Proxy({}, { get: () => '#000000' }), spacing: new Proxy({}, { get: () => 8 }) }),
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
      react.createElement(View, { accessibilityLabel: `amount:${props.label}`, onChangeText: props.onChangeText }, label(props.label)),
    Input: (props: any) =>
      react.createElement(View, { accessibilityLabel: `input:${props.label}`, onChangeText: props.onChangeText }),
    PickerSheet: (props: any) =>
      props.visible
        ? react.createElement(View, { accessibilityLabel: `sheet:${props.title}`, onSelect: props.onSelect, items: props.items })
        : null,
  };
});
// The picker, reachable: Flutterwave's venue is Polygon, so a test picks USDC
// on Polygon the way she would.
jest.mock('@/components/assets/AssetPicker', () => {
  const react = jest.requireActual('react');
  const { View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (props: any) =>
      props.visible ? react.createElement(View, { accessibilityLabel: 'asset-picker', onSelect: props.onSelect }) : null,
  };
});
jest.mock('@/components/wallet/TokenAvatar', () => ({ __esModule: true, default: () => null }));
jest.mock('@/hooks/useSignableWallet', () => ({ __esModule: true, useSignableWallet: () => ({ address: '0xUser', ready: true }) }));
jest.mock('@/hooks/useCountdown', () => ({ __esModule: true, useCountdown: () => ({ label: '10:00', expired: false, secs: 600 }) }));
jest.mock('@/hooks/useRampCurrency', () => {
  const value = { code: 'NGN', currency: { symbol: '₦' }, setCode: () => {}, options: [{ id: 'NGN', label: 'NGN' }] };
  return { __esModule: true, useRampCurrency: () => value, rampCurrencyLabel: () => 'Naira' };
});
jest.mock('@/hooks/useScreenTitle', () => ({ __esModule: true, useScreenTitle: () => {} }));
jest.mock('@/services/paycrestApi', () => ({
  ...jest.requireActual('@/services/paycrestApi'),
  getQuote: jest.fn(),
  getPayoutInstitutions: jest.fn(async () => [{ code: '058', name: 'GTBank' }]),
  createBuy: jest.fn(),
}));
jest.mock('@/services/rampApi', () => ({
  ...jest.requireActual('@/services/rampApi'),
  getRampQuote: jest.fn(),
  createRampBuy: jest.fn(),
  cancelRampOrder: jest.fn(),
  claimRampPayment: jest.fn(async () => ({})),
}));
jest.mock('@/services/lifi', () => ({
  ...jest.requireActual('@/services/lifi'),
  getLiFiTokens: jest.fn(async () => [{ symbol: 'USDC', address: '0xusdc', chainId: 8453, name: 'USD Coin', decimals: 6, logoURI: '' }]),
}));
jest.mock('@/services/chains/solanaWallet', () => ({ __esModule: true, getSolanaAddress: jest.fn(async () => undefined) }));
jest.mock('@/components/paycrest/DepositProgress', () => ({ __esModule: true, DepositProgress: () => null }));
jest.mock('@/components/paycrest/SwappedSellForm', () => ({
  __esModule: true, SWAPPED_STAGE_LABELS: {}, SwappedSellForm: () => null, SwappedSellStatus: () => null,
}));
jest.mock('@/hooks/useSwappedSell', () => ({
  __esModule: true,
  useSwappedSell: () => ({ stage: 'idle', start: jest.fn(), resume: jest.fn(), cancel: jest.fn(async () => {}) }),
  canCancelSale: () => false,
}));
jest.mock('@/services/ngBanks', () => ({ __esModule: true, resolveAccountName: jest.fn(async () => ({ ok: true, accountName: 'ADA OBI' })) }));
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

const walletVenue = {
  handle: 'dHJlYXN1cnl8YmFzZXxVU0RD', chain: 'base', token: 'USDC', rate: '1382.60', effectiveRate: '1382.60',
  spreadPercent: 2.56, tokenAmount: 0.072328, kind: 'direct', fees: [],
};
const fwVenue = {
  handle: 'Zmx1dHRlcndhdmV8cG9seWdvbnxVU0RD', chain: 'polygon', token: 'USDC', rate: '1692.57', effectiveRate: '1745.61',
  spreadPercent: 5.78, tokenAmount: 94.5375, kind: 'direct',
  fees: [
    { kind: 'conversion', label: 'Flutterwave conversion fee (1.5%)', provider: 'Flutterwave', token: '1.46216', ngn: '2474.80' },
    { kind: 'network', label: 'Network fee', provider: 'Flutterwave', token: '1.5', ngn: '2538.85' },
  ],
};
const rampQuote = (amount: string, venues: any[]) =>
  ({ currency: 'NGN', amount, side: 'buy', marketRate: 1348, venues, best: venues[0], quotedAt: '' });
const order = {
  id: 'o1', direction: 'buy', status: 'pending', reference: 'R1', currency: 'NGN', amount: '100',
  chain: 'base', token: 'USDC', tokenAmount: '0.072328', walletAddress: '0xuser', createdAt: '2026-10-05T10:00:00Z',
} as PaycrestOrder;

async function pickPolygonUSDC(tree: ReactTestRenderer) {
  await act(async () => { byLabel(tree, 'row:You want').props.onPress(); });
  await act(async () => {
    await byLabel(tree, 'asset-picker').props.onSelect(
      { symbol: 'USDC', address: '0xpolyusdc', chainId: 137, name: 'USD Coin', decimals: 6, logoURI: '' }, 137);
  });
  await flush();
}

async function quoteAndConfirm(amount: string, expectRate: string, polygon = false): Promise<ReactTestRenderer> {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<BuyScreen />); });
  await flush();
  if (polygon) await pickPolygonUSDC(tree);
  await act(async () => { byLabel(tree, 'amount:You pay').props.onChangeText(amount); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(text(tree)).toContain(expectRate);
  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  return tree;
}

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
  await AsyncStorage.setItem('paycrest.refundAccount.v1.NGN', JSON.stringify({ code: '058', account: '0123456789' }));
  // Paycrest's naira book has been empty since 2026-09-30.
  api.getQuote.mockRejectedValue(new PaycrestApi.PaycrestError("This currency can't be traded in that direction right now.", 409));
});
afterEach(() => { jest.useRealTimers(); });

// §2.3 #3: ₦100 is enterable, quoted by the company wallet, and opens its order screen.
it('a ₦100 company-wallet buy opens the Instant Transfer order screen', async () => {
  ramp.getRampQuote.mockResolvedValue(rampQuote('100', [walletVenue]) as any);
  ramp.createRampBuy.mockResolvedValueOnce({ ...order, id: 't1', provider: 'treasury' } as any);
  await quoteAndConfirm('100', '1,382.60');
  expect(ramp.createRampBuy).toHaveBeenCalledWith(expect.objectContaining({
    amount: '100', quotedRate: '1382.60', handle: walletVenue.handle, chain: 'base', token: 'USDC',
  }));
  expect(router.replace).toHaveBeenCalledWith('/wallet/it/intent/t1');
});

// §14: the display rate, each fee tagged, and the order held to the all-in rate.
it('a Flutterwave venue shows its rate and fee lines and holds the order to the all-in rate', async () => {
  ramp.getRampQuote.mockResolvedValue(rampQuote('165025', [fwVenue]) as any);
  ramp.createRampBuy.mockResolvedValueOnce({ ...order, id: 'f1', provider: 'flutterwave' } as any);
  const tree = await quoteAndConfirm('165025', '1,692.57', true);
  const out = text(tree);
  expect(out).toContain('Flutterwave conversion fee (1.5%)');
  expect(out).toContain('Network fee');
  expect(out).toContain('Flutterwave');
  expect(ramp.createRampBuy).toHaveBeenCalledWith(expect.objectContaining({
    quotedRate: '1745.61', handle: fwVenue.handle, chain: 'polygon', token: 'USDC',
  }));
  expect(router.replace).toHaveBeenCalledWith('/wallet/it/intent/f1');
});

// Spec §15: a company-wallet converted venue opens through the router with the target.
it('opens a company-wallet converted venue through the router with the target', async () => {
  const cw = {
    chain: 'base', token: 'USDC', rate: '1382.57', effectiveRate: '1389.52', spreadPercent: 3, tokenAmount: 7.196772,
    kind: 'converted', handle: 'h-cw', viaChain: 'base', viaToken: 'USDC', requestedChain: 'optimism', requestedToken: 'USDT',
    requestedChainId: 10, requestedTokenAddress: '0xusdt', requestedTokenDecimals: 6, estimatedAmount: '7190000',
    promisedAmount: '7196772', etaSeconds: 60,
    fees: [{ kind: 'conversion', label: 'Conversion (LiFi)', provider: 'LiFi', token: '0.036165', ngn: '50' }],
  };
  ramp.getRampQuote.mockResolvedValue(rampQuote('10000', [cw]) as any);
  ramp.createRampBuy.mockResolvedValueOnce({ id: 'i7', provider: 'treasury' } as any);
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<BuyScreen />); });
  await flush();
  await act(async () => { byLabel(tree, 'row:You want').props.onPress(); });
  await act(async () => {
    await byLabel(tree, 'asset-picker').props.onSelect(
      { symbol: 'USDT', address: '0xusdt', chainId: 10, name: 'Tether', decimals: 6, logoURI: '' }, 10);
  });
  await flush();
  await act(async () => { byLabel(tree, 'amount:You pay').props.onChangeText('10000'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(ramp.getRampQuote).toHaveBeenCalledWith('buy', '10000', 'NGN', expect.objectContaining({ chainId: 10 }));
  expect(text(tree)).toContain('Conversion (LiFi)');
  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  expect(ramp.createRampBuy).toHaveBeenCalledWith(expect.objectContaining({
    handle: 'h-cw', quotedRate: '1389.52', requestedChainId: 10, requestedTokenAddress: '0xusdt', requestedToken: 'USDT',
  }));
  expect(api.createBuy).not.toHaveBeenCalled();
  expect(router.replace).toHaveBeenCalledWith(expect.stringContaining('i7'));
});

it('a Paycrest order still shows its own pay screen', async () => {
  ramp.getRampQuote.mockResolvedValue(rampQuote('100', [walletVenue]) as any);
  ramp.createRampBuy.mockResolvedValueOnce({ ...order, payAmount: '100' } as any);
  const tree = await quoteAndConfirm('100', '1,382.60');
  expect(router.replace).not.toHaveBeenCalled();
  expect(text(tree)).toContain('Transfer exactly');
});

it('a purchase already waiting for payment offers View and Cancel', async () => {
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  ramp.getRampQuote.mockResolvedValue(rampQuote('100', [walletVenue]) as any);
  ramp.createRampBuy.mockRejectedValueOnce(new RampApi.RampError(
    'You have a purchase waiting for payment.', 409, 'ramp_order_open',
    { success: false, code: 'ramp_order_open', data: { orderId: 'i9' } }));
  ramp.cancelRampOrder.mockResolvedValueOnce({ ...order, id: 'i9', provider: 'treasury' } as any);
  await quoteAndConfirm('100', '1,382.60');
  const [title, , buttons] = alert.mock.calls[0] as [string, string, { text: string; onPress?: () => void }[]];
  expect(title).toBe('You have a purchase waiting for payment');
  const press = (l: string) => buttons.find((b) => b.text === l)!.onPress!();
  press('View');
  expect(router.push).toHaveBeenCalledWith('/wallet/it/intent/i9');
  await act(async () => { press('Cancel it'); });
  await flush();
  expect(ramp.cancelRampOrder).toHaveBeenCalledWith('i9');
  expect(alert).toHaveBeenCalledWith('Purchase cancelled', expect.any(String));
  alert.mockRestore();
});

// Owner 2026-10-04 #4: a routed Flutterwave buy needs the IT agreement.
it('a missing IT agreement sends her to the hub to accept it', async () => {
  ramp.getRampQuote.mockResolvedValue(rampQuote('165025', [fwVenue]) as any);
  ramp.createRampBuy.mockRejectedValueOnce(new RampApi.RampError(
    'Please review and accept the trading agreement to continue', 403, 'it_agreement_required',
    { success: false, code: 'it_agreement_required' }));
  await quoteAndConfirm('165025', '1,692.57', true);
  expect(router.replace).toHaveBeenCalledWith('/wallet/it');
});
