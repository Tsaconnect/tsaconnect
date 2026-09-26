/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * SCREEN level, with the real PayNaira / PayLivePanel / usePayLive and only the
 * payment hook itself steered: an attempt that has gone wrong — even into
 * "don't pay again" — must not lose its form and safety copy to the expiry
 * warning. Otherwise the payer is told "ask for a new link" while her payment
 * may still be under way, and pays a second time.
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
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}` }, props.title),
    ErrorState: (props: any) => react.createElement(RNText, null, props.title),
    Spinner: () => null,
    // Each step as `label:state`, so a test can read where the stepper says things are.
    Stepper: (props: any) =>
      react.createElement(View, null, props.steps.map((st: any) => react.createElement(RNText, { key: st.key }, `${st.label}:${st.state}`))),
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
jest.mock('@/components/disputes/DisputeSection', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    DisputeSection: (p: any) => react.createElement(RNText, null, `report:${p.subjectType}:${p.subjectId}:${p.disputable}`),
  };
});
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
jest.mock('@/components/pay/PayCrypto', () => ({ __esModule: true, PayCrypto: () => null }));
const mockHook: { value: any } = { value: null };
jest.mock('@/hooks/usePayNaira', () => ({ __esModule: true, usePayNaira: () => mockHook.value }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { getCheckout, type PayCheckout } from '@/services/payApi';
import PayScreen from '../pay';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
/** Five minutes and two seconds left: the warning trips on the second tick. */
const justOverFive = () => ({
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'pending', amount: 5_000_000, customerTotal: 5_050_506, fee: 50_506,
  merchantNet: 5_000_000, currency: 'NGN', description: '', settlementMode: 'fiat', payable: true, youArePaying: false,
  expiresAt: new Date(Date.now() + 5 * 60_000 + 2_000).toISOString(),
}) as PayCheckout;

const hookState = (over: Record<string, unknown> = {}) => ({
  stage: 'idle', retryable: true, canContinue: false, walletShort: false,
  payWith: jest.fn(), estimate: jest.fn(async () => ({ pay: '0.084 BNB' })),
  completeWithWallet: jest.fn(), continuePayment: jest.fn(), resume: jest.fn(async () => {}), reset: jest.fn(),
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
/** The screen re-reads the clock every five seconds; re-render to pick up a new hook state. */
const tickPast = async (ms: number) => {
  await act(async () => { jest.advanceTimersByTime(ms); });
  await flush();
};

beforeEach(() => { jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] }); });
afterEach(() => { act(() => tree?.unmount()); jest.useRealTimers(); });

it('an attempt that goes active and then fails "don\'t pay again" near expiry keeps its form and safety copy', async () => {
  mockHook.value = hookState({ stage: 'paying' });
  jest.mocked(getCheckout).mockResolvedValue(justOverFive());
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(text()).toContain('Choose a token'); // the live form is up

  // The swap goes wrong while the warning window opens.
  mockHook.value = hookState({
    stage: 'failed', retryable: false, canContinue: true, error: "Your payment isn't finished yet — don't pay again.",
  });
  await tickPast(10_000);

  expect(text()).not.toContain('expires in under five minutes');
  expect(text()).toContain("don't pay again");
  expect(text()).toContain('Continue payment');
  expect(text()).not.toContain('Pay ₦'); // and no Pay button to pay twice with
});

it('so does an attempt that is only waiting on the server', async () => {
  mockHook.value = hookState({ stage: 'paying' });
  jest.mocked(getCheckout).mockResolvedValue(justOverFive());
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  mockHook.value = hookState({ stage: 'waiting' });
  await tickPast(10_000);
  expect(text()).not.toContain('expires in under five minutes');
});

it('a payer who never started is still told the link is about to expire', async () => {
  mockHook.value = hookState({ stage: 'idle' });
  jest.mocked(getCheckout).mockResolvedValue(justOverFive());
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  await tickPast(10_000);
  expect(text()).toContain('expires in under five minutes');
});

describe('a finished payment, opened again (from her history)', () => {
  const done = (over: Partial<PayCheckout>) => ({ ...justOverFive(), youArePaying: false, payable: false, ...over }) as PayCheckout;
  const open = async (c: PayCheckout) => {
    mockHook.value = hookState();
    jest.mocked(getCheckout).mockResolvedValue(c);
    await act(async () => { tree = TestRenderer.create(<PayScreen />); });
    await flush();
  };

  it('a failed one says why, never "Paid to", and can be reported', async () => {
    await open(done({
      status: 'failed', orderId: 'o1', orderStatus: 'unmatched',
      failureReason: "the deposit couldn't be matched — support is reviewing it",
    }));
    expect(text()).toContain('Payment failed');
    expect(text()).toContain("The deposit couldn't be matched — support is reviewing it");
    expect(text()).toContain('To: Sky Air');
    expect(text()).not.toContain('Paid to');
    expect(text()).toContain('report:paycrest_order:o1:true');
  });

  it('a paid one says Paid to, and can still be reported ("it never arrived")', async () => {
    await open(done({ status: 'succeeded', orderId: 'o1', orderStatus: 'settled' }));
    expect(text()).toContain('Paid to: Sky Air');
    expect(text()).toContain('report:paycrest_order:o1:true');
  });

  it('without its order (not her payment, or a test payment) there is nothing to report', async () => {
    await open(done({ status: 'succeeded' }));
    expect(text()).not.toContain('report:');
    act(() => tree.unmount());
    await open(done({ status: 'succeeded', mode: 'test', orderId: 'o1', orderStatus: 'settled' }));
    expect(text()).not.toContain('report:');
  });
});
