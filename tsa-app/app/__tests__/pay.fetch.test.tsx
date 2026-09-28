/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The pay screen's checkout reads. The 5 s poll can overlap: a slow OLD answer
 * landing after a newer one must not put the screen back in time. And a payment
 * that has reached a terminal status has no use for its "don't pay again" hold
 * any more, so the screen lets go of it. A payment link says what it is for and,
 * when the merchant named one, who it was requested for.
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
    Button: (props: any) => react.createElement(RNText, { onPress: props.onPress }, props.title),
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
jest.mock('@/utils/verifyThenReturn', () => ({ __esModule: true, verifyThenReturnToPayment: jest.fn(async () => {}) }));
jest.mock('@/services/payApi', () => ({ __esModule: true, getCheckout: jest.fn(), getPublicPayment: jest.fn(async () => ({ expiresAt: '2026-10-03T12:00:00Z' })), returnUrlFor: () => null }));
jest.mock('@/services/payHold', () => ({ __esModule: true, clearPayHold: jest.fn(async () => {}) }));
jest.mock('@/components/pay/PayNaira', () => ({ __esModule: true, PayNaira: () => null }));
jest.mock('@/components/pay/PayCrypto', () => ({ __esModule: true, PayCrypto: () => null }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { getCheckout, type PayCheckout } from '@/services/payApi';
import { clearPayHold } from '@/services/payHold';
import { verifyThenReturnToPayment } from '@/utils/verifyThenReturn';
import PayScreen from '../pay';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const checkoutWith = (over: Partial<PayCheckout>) => ({
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'processing', amount: 5_000_000, customerTotal: 5_050_506, fee: 50_506,
  merchantNet: 5_000_000, currency: 'NGN', description: '', settlementMode: 'fiat', payable: false, youArePaying: true,
  expiresAt: new Date(Date.now() + 30 * 60_000).toISOString(), ...over,
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
const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };
const tick = async (ms: number) => { await act(async () => { jest.advanceTimersByTime(ms); }); await flush(); };

beforeEach(() => { jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] }); jest.clearAllMocks(); });
afterEach(() => { act(() => tree?.unmount()); jest.useRealTimers(); });

it('a slow older read cannot overwrite a newer one', async () => {
  // A test payment: the screen draws its own "waiting" line only for those (a
  // live one's is the panel's), and that line is how this test reads the state.
  let slow!: (c: PayCheckout) => void;
  jest.mocked(getCheckout)
    .mockResolvedValueOnce(checkoutWith({ mode: 'test' }))                       // initial: processing, ours
    .mockReturnValueOnce(new Promise((r) => { slow = r; }))                      // poll 1: slow
    .mockResolvedValueOnce(checkoutWith({ mode: 'test', status: 'succeeded' })); // poll 2: fast, and final
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(text()).toContain('Waiting for confirmation');

  await tick(5_000); // poll 1 starts and hangs
  await tick(5_000); // poll 2 lands: the payment succeeded
  expect(text()).toContain('Payment complete');

  await act(async () => { slow(checkoutWith({ mode: 'test' })); }); // the old answer finally arrives
  await flush();
  expect(text()).toContain('Payment complete');
  expect(text()).not.toContain('Waiting for confirmation');
});

it.each(['succeeded', 'failed', 'expired'] as const)('a %s payment lets go of its persisted hold', async (status) => {
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({ status, youArePaying: false }));
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(clearPayHold).toHaveBeenCalledWith(ID);
});

it('a payment still under way does not', async () => {
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({}));
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(clearPayHold).not.toHaveBeenCalled();
});

it('a payment link says what it is for and who it was requested for', async () => {
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({
    status: 'pending', payable: true, youArePaying: false, description: 'Two bags of rice', customerName: 'Ada Obi',
  }));
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(text()).toContain('For: Two bags of rice');
  expect(text()).toContain('Requested for: Ada Obi');
});

it('without a customer name there is no "Requested for"', async () => {
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({ status: 'pending', payable: true, youArePaying: false }));
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(text()).not.toContain('Requested for');
});

it('through a reusable link, what it is for is the link\'s name, and the description is the payer\'s note', async () => {
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({
    status: 'pending', payable: true, youArePaying: false, source: 'open', linkLabel: 'Mama Ngozi Store — Shop 12',
    description: 'Two plates of rice',
  }));
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(text()).toContain('For: Mama Ngozi Store — Shop 12');
  expect(text()).toContain("Customer's note: Two plates of rice");
  expect(text()).not.toContain('For: Two plates of rice');
});

it('a reusable-link payment with no note shows only what it is for', async () => {
  jest.mocked(getCheckout).mockResolvedValue(checkoutWith({
    status: 'pending', payable: true, youArePaying: false, source: 'open', linkLabel: 'Shop 12', description: '',
  }));
  await act(async () => { tree = TestRenderer.create(<PayScreen />); });
  await flush();
  expect(text()).toContain('For: Shop 12');
  expect(text()).not.toContain("Customer's note");
});

describe('a payment she cannot pay yet', () => {
  const blocked = (over: Partial<PayCheckout>) => checkoutWith({ status: 'pending', payable: false, youArePaying: false, ...over });

  it('asks an unverified payer to verify, and sends her there with the way back', async () => {
    jest.mocked(getCheckout).mockResolvedValue(blocked({
      notPayableWhy: 'Verify your identity to pay this', notPayableCode: 'kyc_verification_required',
    }));
    await act(async () => { tree = TestRenderer.create(<PayScreen />); });
    await flush();
    expect(text()).toContain('Verify your identity to pay');
    expect(text()).toContain('bring you back to this payment');
    await act(async () => { tree.root.findAllByProps({ children: 'Verify identity' })[0].props.onPress(); });
    expect(verifyThenReturnToPayment).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ id: ID }));
  });

  it('points a verified payer with no legal name at support, not at verification', async () => {
    jest.mocked(getCheckout).mockResolvedValue(blocked({
      notPayableWhy: "We couldn't confirm the legal name on your verified identity. Contact support to pay this.",
      notPayableCode: 'kyc_legal_name_missing',
    }));
    await act(async () => { tree = TestRenderer.create(<PayScreen />); });
    await flush();
    expect(text()).toContain('Contact support to pay');
    expect(text()).toContain('Email support');
    expect(text()).not.toContain('Verify identity');
  });

  it('shows any other reason as it is, with nothing to tap', async () => {
    jest.mocked(getCheckout).mockResolvedValue(blocked({ status: 'processing', notPayableWhy: 'Someone else is completing this payment' }));
    await act(async () => { tree = TestRenderer.create(<PayScreen />); });
    await flush();
    expect(text()).toContain('Someone else is completing this payment');
    expect(text()).not.toContain('Verify identity');
  });
});
