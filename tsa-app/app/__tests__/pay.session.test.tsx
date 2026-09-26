/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * SCREEN level: whether the payer is signed in is decided by the server's
 * answer, not the AuthContext flag, which can drift from the stored token.
 *   - flag says signed out, server serves the checkout -> the payment shows;
 *   - flag says signed in, server answers 401 -> "Sign in to pay", not an error;
 *   - any other failure is still an error with a retry.
 * Also pins the summary as Amount / Fee / Total, and that a signed-out payer —
 * maybe a new customer — can sign in or create an account, and either way the
 * payment is remembered until it expires, not a flat half hour.
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
  }),
}));
const mockAuth = { isAuthenticated: true, isHydrated: true };
jest.mock('@/AuthContext/AuthContext', () => ({ __esModule: true, useAuth: () => mockAuth }));
jest.mock('@/constants/navigation', () => ({ __esModule: true, useDefaultScreenOptions: () => ({}) }));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, props.title),
    ErrorState: (props: any) => react.createElement(RNText, null, props.title),
    Spinner: () => null,
    Icon: () => null,
    AccentCard: box,
    BottomActionBar: box,
    Card: box,
    SummaryCard: box,
    SummaryRow: (props: any) => react.createElement(RNText, null, `${props.label}: ${props.value}`),
    useToast: () => ({ error: jest.fn() }),
  };
});
jest.mock('@/components/common/AuthorizationModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@/hooks/usePayTest', () => ({
  __esModule: true,
  usePayTest: () => ({ request: jest.fn(), authorization: {}, submitting: false }),
}));
jest.mock('@/utils/postAuthRoute', () => ({
  __esModule: true, rememberPendingPayment: jest.fn(async () => {}), forgetPendingPayment: jest.fn(async () => {}),
}));
jest.mock('@/services/payApi', () => ({
  __esModule: true,
  getCheckout: jest.fn(),
  getPublicPayment: jest.fn(async () => ({ expiresAt: '2026-10-03T12:00:00Z' })),
  returnUrlFor: () => null,
}));
jest.mock('@/components/pay/PayNaira', () => ({ __esModule: true, PayNaira: () => null }));
jest.mock('@/components/pay/PayCrypto', () => ({ __esModule: true, PayCrypto: () => null }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import { getCheckout, type PayCheckout } from '@/services/payApi';
import { forgetPendingPayment, rememberPendingPayment } from '@/utils/postAuthRoute';
import PayScreen from '../pay';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const testCheckout = () => ({
  id: ID, merchantName: 'Test Merchant', mode: 'test', status: 'pending', amount: 100_000, customerTotal: 101_011,
  fee: 1_011, merchantNet: 100_000, currency: 'NGN', description: '', settlementMode: 'fiat', payable: true,
  youArePaying: false, expiresAt: new Date(Date.now() + 20 * 60_000).toISOString(),
}) as unknown as PayCheckout;
const httpError = (status: number, message: string) => Object.assign(new Error(message), { status });

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
let tree: ReactTestRenderer;
const text = () => textNodes(tree.toJSON()).join(' ');
const mount = async () => {
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => { mockAuth.isAuthenticated = true; jest.mocked(getCheckout).mockReset(); });
afterEach(() => { act(() => tree?.unmount()); });

it('shows the payment when the server serves it, even if the context flag says signed out', async () => {
  mockAuth.isAuthenticated = false;
  jest.mocked(getCheckout).mockResolvedValue(testCheckout());
  await mount();
  expect(getCheckout).toHaveBeenCalledWith(ID);
  expect(text()).not.toContain('Sign in to pay');
  expect(text()).toContain('Approve');
});

it('shows "Sign in to pay" on a 401, even if the context flag says signed in', async () => {
  jest.mocked(getCheckout).mockRejectedValue(httpError(401, 'Please authenticate'));
  await mount();
  expect(text()).toContain('Sign in to pay');
  expect(text()).not.toContain('Could not load this payment');
});

it('still shows a load error for failures other than 401', async () => {
  jest.mocked(getCheckout).mockRejectedValue(httpError(500, 'Request failed'));
  await mount();
  expect(text()).toContain('Could not load this payment');
  expect(text()).not.toContain('Sign in to pay');
});

it('summarises the charge as Amount, then the payer\'s Fee, then Total', async () => {
  jest.mocked(getCheckout).mockResolvedValue(testCheckout());
  await mount();
  const t = text();
  expect(t).toContain('Amount: ₦1,000.00');
  expect(t).toContain('Fee: ₦10.11');
  expect(t).toContain('Total: ₦1,010.11');
  expect(t.indexOf('Amount:')).toBeLessThan(t.indexOf('Fee:'));
  expect(t.indexOf('Fee:')).toBeLessThan(t.indexOf('Total:'));
});

it('omits the Fee row when the merchant absorbs the whole fee', async () => {
  jest.mocked(getCheckout).mockResolvedValue({ ...testCheckout(), customerTotal: 100_000 } as PayCheckout);
  await mount();
  expect(text()).not.toContain('Fee:');
  expect(text()).toContain('Total: ₦1,000.00');
});

it.each([['Sign in', '/login'], ['Create an account', '/signup']] as const)(
  'signed out, "%s" remembers the payment until it expires, then goes to %s',
  async (title, to) => {
    jest.mocked(getCheckout).mockRejectedValue(httpError(401, 'Please authenticate'));
    await mount();
    const button = tree.root.findAll((n) => n.props.accessibilityLabel === `button:${title}`)[0];
    await act(async () => { await button.props.onPress(); });
    expect(rememberPendingPayment).toHaveBeenCalledWith(ID, { expiresAt: '2026-10-03T12:00:00Z' });
    expect(router.replace).toHaveBeenCalledWith(to);
  },
);

it('a finished payment is forgotten: nothing to come back to after sign-in or KYC', async () => {
  jest.mocked(getCheckout).mockResolvedValue({ ...testCheckout(), status: 'succeeded' } as PayCheckout);
  await mount();
  expect(forgetPendingPayment).toHaveBeenCalledWith(ID);
});
