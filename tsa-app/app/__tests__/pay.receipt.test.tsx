/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The checkout shows the payment's code under the merchant; once a live payment
 * has succeeded, the done summary is her receipt: code, when she paid, a QR of
 * the page that verifies it, Share receipt, and Print / Save PDF (opens the page
 * — no native print module).
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('expo-router', () => ({
  __esModule: true,
  Stack: { Screen: () => null },
  router: { replace: jest.fn(), push: jest.fn() },
  useLocalSearchParams: () => ({ id: '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f' }),
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
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('@/AuthContext/AuthContext', () => ({
  __esModule: true,
  useAuth: () => ({ isAuthenticated: true, isHydrated: true }),
}));
jest.mock('@/constants/navigation', () => ({ __esModule: true, useDefaultScreenOptions: () => ({}) }));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, { testID: `button:${props.title}`, onPress: props.onPress }, props.title),
    ErrorState: (props: any) => react.createElement(RNText, null, props.title),
    Spinner: () => null,
    Icon: () => null,
    AccentCard: box,
    BottomActionBar: box,
    Card: box,
    SummaryCard: box,
    SummaryRow: (props: any) => react.createElement(RNText, null, `${props.label}: ${props.value}`),
    useToast: () => ({ error: jest.fn(), success: jest.fn() }),
  };
});
jest.mock('react-native-qrcode-svg', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return { __esModule: true, default: (props: any) => react.createElement(Text, null, `qr:${props.value}`) };
});
jest.mock('expo-clipboard', () => ({ __esModule: true, setStringAsync: jest.fn(async () => true) }));
jest.mock('@/components/common/AuthorizationModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@/hooks/usePayTest', () => ({
  __esModule: true,
  usePayTest: () => ({ request: jest.fn(), authorization: {}, submitting: false }),
}));
jest.mock('@/utils/postAuthRoute', () => ({
  __esModule: true, rememberPendingPayment: jest.fn(async () => {}), forgetPendingPayment: jest.fn(async () => {}),
}));
jest.mock('@/utils/verifyThenReturn', () => ({ __esModule: true, verifyThenReturnToPayment: jest.fn(async () => {}) }));
jest.mock('@/services/payApi', () => ({ __esModule: true, getCheckout: jest.fn(), getPublicPayment: jest.fn(async () => ({ expiresAt: '2026-10-03T12:00:00Z' })), returnUrlFor: () => null }));
jest.mock('@/services/payHold', () => ({ __esModule: true, clearPayHold: jest.fn(async () => {}) }));
jest.mock('@/components/pay/PayNaira', () => ({ __esModule: true, PayNaira: () => null }));
jest.mock('@/components/pay/PayCrypto', () => ({ __esModule: true, PayCrypto: () => null }));

import React from 'react';
import { Linking, Share } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { getCheckout, type PayCheckout } from '@/services/payApi';
import PayScreen from '../pay';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const RECEIPT = 'https://tsaconnectworld.com/r/TSA-7K2QF-M9XWD';
const checkoutWith = (over: Partial<PayCheckout>) => ({
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'pending', amount: 5_000_000, customerTotal: 5_050_506, fee: 50_506,
  merchantNet: 5_000_000, currency: 'NGN', description: '', settlementMode: 'fiat', payable: true, youArePaying: false,
  expiresAt: new Date(Date.now() + 30 * 60_000).toISOString(), ...over,
}) as PayCheckout;
const paidLive = checkoutWith({
  status: 'succeeded', payable: false, payCode: 'TSA-7K2QF-M9XWD', succeededAt: '2026-10-08T09:05:00Z', receiptUrl: RECEIPT,
});

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
let tree: ReactTestRenderer;
const text = () => textNodes(tree.toJSON()).join(' ');
const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };
const button = (title: string) => tree.root.findAll((n) => n.props.testID === `button:${title}` && typeof (n as { type?: unknown }).type === 'string')[0];
const show = async (c: PayCheckout) => {
  jest.mocked(getCheckout).mockResolvedValue(c);
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
};

beforeEach(() => { jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] }); jest.clearAllMocks(); });
afterEach(() => { act(() => tree?.unmount()); jest.useRealTimers(); });

it('shows the code under the merchant while she pays', async () => {
  await show(checkoutWith({ payCode: 'TSA-7K2QF-M9XWD' }));
  expect(text()).toContain('Paying: Sky Air');
  expect(text()).toContain('Code: TSA-7K2QF-M9XWD');
});

// Review Focus 4.
it('a payment without a code shows no Code row', async () => {
  await show(checkoutWith({}));
  expect(text()).not.toContain('Code:');
});

it('a succeeded live payment is her receipt: code, when, a QR of the verify page', async () => {
  await show(paidLive);
  expect(text()).toContain('Payment complete');
  expect(text()).toContain('Code: TSA-7K2QF-M9XWD');
  expect(text()).toContain('Paid on:');
  expect(text()).toContain(`qr:${RECEIPT}`);
  expect(text()).toContain('Share receipt');
  expect(text()).toContain('Print / Save PDF');
  expect(text()).not.toContain('Copy link');
});

it('Share receipt sends the verify link', async () => {
  const share = jest.spyOn(Share, 'share').mockResolvedValue({ action: 'sharedAction' });
  await show(paidLive);
  await act(async () => { await button('Share receipt').props.onPress(); });
  expect(share.mock.calls[0][0].message).toContain(RECEIPT);
  expect(share.mock.calls[0][0].message).toContain('TSA-7K2QF-M9XWD');
});

it('Print / Save PDF opens the receipt page', async () => {
  const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
  await show(paidLive);
  await act(async () => { button('Print / Save PDF').props.onPress(); });
  expect(open).toHaveBeenCalledWith(RECEIPT);
});

it('a test payment has no public receipt to share', async () => {
  await show(checkoutWith({ mode: 'test', status: 'succeeded', payable: false, payCode: 'TSA-7K2QF-M9XWD' }));
  expect(text()).toContain('Code: TSA-7K2QF-M9XWD');
  expect(text()).not.toContain('Share receipt');
});

// A tampered response must not send her anywhere but our own receipt page.
it.each([
  ['another host', 'https://evil.example/r/TSA-7K2QF-M9XWD'],
  ['http', 'http://tsaconnectworld.com/r/TSA-7K2QF-M9XWD'],
  ['another path', 'https://tsaconnectworld.com/login'],
  ['a lookalike host', 'https://tsaconnectworld.com.evil.example/r/TSA-7K2QF-M9XWD'],
  ['a fragment that fakes the host', 'https://evil.com#@tsaconnectworld.com/r/TSA-7K2QF-M9XWD'],
  ['a backslash that fakes the host', 'https://evil.com\\@tsaconnectworld.com/r/TSA-7K2QF-M9XWD'],
  ['credentials in the url', 'https://tsaconnectworld.com@evil.example/r/TSA-7K2QF-M9XWD'],
])('a receiptUrl on %s shows no receipt block', async (_name, url) => {
  await show({ ...paidLive, receiptUrl: url });
  expect(text()).toContain('Payment complete');
  expect(text()).not.toContain('Share receipt');
  expect(text()).not.toContain('Print / Save PDF');
});

it('a receipt on the www. host is ours too', async () => {
  const www = 'https://www.tsaconnectworld.com/r/TSA-7K2QF-M9XWD';
  await show({ ...paidLive, receiptUrl: www });
  expect(text()).toContain(`qr:${www}`);
  expect(text()).toContain('Share receipt');
  expect(text()).toContain('Print / Save PDF');
});

it('a test payment shows no receipt block even with a valid receiptUrl', async () => {
  await show({ ...paidLive, mode: 'test' });
  expect(text()).not.toContain('qr:');
  expect(text()).not.toContain('Share receipt');
  expect(text()).not.toContain('Print / Save PDF');
});
