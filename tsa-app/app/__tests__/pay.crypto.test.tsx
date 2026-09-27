/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * SCREEN level for a crypto-settling merchant: the real PayCrypto / PayLivePanel
 * / usePayLive with only usePayCrypto steered. What the payer sees when
 *   - the server is still confirming a delivery (payment `processing`, its sale
 *     `swapped`): "waiting for confirmation", never Pay;
 *   - the server paused the attempt (pay_attempt_uncertain): the form and honest
 *     copy stay under the expiry warning, and no way to pay again;
 *   - the route refused this token right now: the payment is still payable and
 *     nothing unresolved is left behind, so the expiry warning applies as usual.
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
    Button: (props: any) => react.createElement(RNText, null, props.title),
    ErrorState: (props: any) => react.createElement(RNText, null, props.title),
    Spinner: () => null,
    Icon: () => null,
    PressableScale: box,
    AccentCard: box,
    BottomActionBar: box,
    Card: box,
    SummaryCard: box,
    SummaryRow: (props: any) => react.createElement(RNText, null, `${props.label}: ${props.value}`),
    useToast: () => ({ error: jest.fn() }),
  };
});
jest.mock('@/components/common/AuthorizationModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@/components/assets/AssetPicker', () => ({ __esModule: true, default: () => null }));
jest.mock('@/components/paycrest/SwappedSellForm', () => ({ __esModule: true, SWAPPED_STAGE_LABELS: {} }));
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
const mockHook: { value: any } = { value: null };
jest.mock('@/hooks/usePayCrypto', () => ({ __esModule: true, usePayCrypto: () => mockHook.value }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { getCheckout, type PayCheckout } from '@/services/payApi';
import { CANT_PAY_WITH_TOKEN, PAYMENT_PAUSED } from '@/utils/payCheckout';
import PayScreen from '../pay';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const checkoutWith = (over: Partial<PayCheckout> = {}, msLeft = 20 * 60_000) => ({
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'pending', amount: 5_000_000, customerTotal: 5_050_506, fee: 50_506,
  merchantNet: 5_000_000, currency: 'NGN', description: '', settlementMode: 'crypto', settleTokenSymbol: 'USDT',
  settleChainId: 56, settleTokenAddress: '0x55d398326f99059fF775485246999027B3197955', payable: true, youArePaying: false,
  expiresAt: new Date(Date.now() + msLeft).toISOString(), ...over,
}) as PayCheckout;

const hookState = (over: Record<string, unknown> = {}) => ({
  stage: 'idle', retryable: true, canContinue: false,
  payWith: jest.fn(), estimate: jest.fn(async () => ({ pay: '0.084 BNB' })),
  continuePayment: jest.fn(), resume: jest.fn(async () => {}), reset: jest.fn(async () => {}),
  authorization: { visible: false, title: '', onAuthorized: jest.fn(), onCancel: jest.fn() },
  ...over,
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
const tickPast = async (ms: number) => { await act(async () => { jest.advanceTimersByTime(ms); }); await flush(); };
const mount = async () => { await act(async () => { tree = TestRenderer.create(<PayScreen />); }); await flush(); };

beforeEach(() => { jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] }); });
afterEach(() => { act(() => tree?.unmount()); jest.useRealTimers(); });

it('a delivery still being confirmed shows "waiting for confirmation" once, with no way to pay again', async () => {
  mockHook.value = hookState({ stage: 'waiting' });
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({ status: 'processing', payable: false, youArePaying: true }));
  await mount();
  await tickPast(60_000); // and it keeps saying so as the polling goes on
  expect(text().match(/Waiting for confirmation/g)).toHaveLength(1);
  expect(text()).not.toContain('Choose a token');
  expect(text()).not.toContain('Pay ₦');
  expect(text()).not.toContain('Try again');
});

it('a paused attempt keeps its form and honest copy under the expiry warning, and offers no Pay or Continue', async () => {
  mockHook.value = hookState({ stage: 'paying' });
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({}, 5 * 60_000 + 2_000));
  await mount();
  mockHook.value = hookState({ stage: 'failed', retryable: false, canContinue: false, error: PAYMENT_PAUSED });
  await tickPast(10_000);

  expect(text()).toContain(PAYMENT_PAUSED);
  expect(text()).not.toContain('expires in under five minutes');
  expect(text()).not.toContain('Pay ₦');
  expect(text()).not.toContain('Continue payment');
});

it('a token the route refuses right now leaves the payment payable and nothing unresolved behind', async () => {
  mockHook.value = hookState({ stage: 'failed', retryable: true, error: CANT_PAY_WITH_TOKEN });
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({}, 5 * 60_000 + 2_000));
  await mount();
  expect(text()).toContain(CANT_PAY_WITH_TOKEN);
  expect(text()).toContain('Pay ₦50,505.06'); // still payable: pick another token and go
  expect(text()).toContain('Choose a token');

  // Nothing was started, so the expiry warning applies as it would to anyone.
  await tickPast(10_000);
  expect(text()).toContain('expires in under five minutes');
});
