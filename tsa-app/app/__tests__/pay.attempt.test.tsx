/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The pay screen's expiry warning ("under five minutes left — ask for a new
 * link") must not tear the live form down while an attempt of hers is moving
 * money: a bridge swap can outlast it, and the progress and shortfall prompts
 * live in that form. It still blocks a payer who has not started.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('expo-router', () => ({
  __esModule: true,
  Stack: { Screen: () => null },
  router: { replace: jest.fn() },
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
// The live components are stubs: this test is about what the SCREEN does with
// the "attempt active" signal they send it.
const mockLive: { active: boolean } = { active: false };
jest.mock('@/components/pay/PayNaira', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    PayNaira: (props: any) => {
      react.useEffect(() => { props.onActive(mockLive.active); }, []);
      return react.createElement(RNText, null, 'LIVE FORM');
    },
  };
});
jest.mock('@/components/pay/PayCrypto', () => ({ __esModule: true, PayCrypto: () => null }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { getCheckout, type PayCheckout } from '@/services/payApi';
import PayScreen from '../pay';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
/** Five minutes and two seconds left: the warning trips a few ticks after the form is up. */
const justOverFive = () => ({
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'pending', amount: 5_000_000, customerTotal: 5_050_506, fee: 50_506,
  merchantNet: 5_000_000, currency: 'NGN', description: '', settlementMode: 'fiat', payable: true, youArePaying: false,
  expiresAt: new Date(Date.now() + 5 * 60_000 + 2_000).toISOString(),
}) as PayCheckout;

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
let tree: ReactTestRenderer;
const text = () => textNodes(tree.toJSON()).join(' ');
async function mount() {
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); });
}
/** The screen re-reads the clock every five seconds. */
const tickPast = async (ms: number) => { await act(async () => { jest.advanceTimersByTime(ms); }); };

beforeEach(() => { jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] }); });
afterEach(() => { act(() => tree?.unmount()); jest.useRealTimers(); });

it('blocks a payer who has not started once under five minutes remain', async () => {
  mockLive.active = false;
  jest.mocked(getCheckout).mockResolvedValue(justOverFive());
  await mount();
  expect(text()).toContain('LIVE FORM');
  await tickPast(10_000);
  expect(text()).toContain('expires in under five minutes');
  expect(text()).not.toContain('LIVE FORM');
});

it('keeps the live form up past that warning once an attempt is moving money', async () => {
  mockLive.active = true;
  jest.mocked(getCheckout).mockResolvedValue(justOverFive());
  await mount();
  await tickPast(10_000);
  expect(text()).toContain('LIVE FORM');
  expect(text()).not.toContain('expires in under five minutes');
});
