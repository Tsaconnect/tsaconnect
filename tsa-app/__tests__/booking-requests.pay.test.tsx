/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Paying an approved booking request. An MCGP booking is paid from Spendable
 * MCGP, which the server holds with no wallet signature, so nothing may move
 * until the buyer passes the PIN/biometric prompt — as at cart checkout. A
 * stablecoin booking is signed in the wallet and needs no extra prompt.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
const mockToast = { show: jest.fn() };
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, props.title),
    Badge: () => null,
    Card: box,
    EmptyState: () => null,
    Spinner: () => null,
    InlineMessage: (props: any) => (props.message ? react.createElement(RNText, null, props.message) : null),
    useToast: () => mockToast,
  };
});
const mockAuthorization: { props: any } = { props: {} };
jest.mock('@/components/common/AuthorizationModal', () => ({
  __esModule: true,
  default: (props: any) => { mockAuthorization.props = props; return null; },
}));
jest.mock('@/contexts/CurrencyContext', () => ({
  __esModule: true,
  useCurrency: () => ({ formatPrice: (usd: number) => `$${usd}` }),
}));
jest.mock('@/hooks/useMerchantPricing', () => ({
  __esModule: true,
  parseAmount: (s: string) => Number(s),
  useMerchantPricing: () => ({ amount: () => '', ready: true, usdFor: () => undefined }),
}));
jest.mock('@/components/products/MerchantPricingFields', () => ({
  __esModule: true,
  LocalAmountInput: () => null,
  PricingCurrencySelect: () => null,
}));
const mockRows: { rows: any[] } = { rows: [] };
jest.mock('@/services/bookingRequestApi', () => ({
  __esModule: true,
  listBookingRequests: jest.fn(async () => ({ success: true, data: { requests: mockRows.rows } })),
  approveBookingRequest: jest.fn(),
  rejectBookingRequest: jest.fn(),
}));
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    router: { push: jest.fn() },
    useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]),
  };
});
jest.mock('@/services/serviceListingApi', () => ({
  __esModule: true,
  getFieldCatalog: jest.fn(async () => ({})),
}));
jest.mock('@/services/serviceCheckout', () => ({
  __esModule: true,
  payBookingRequestAndEscrow: jest.fn(async () => ({})),
  resumeOrderPayment: jest.fn(async () => ({})),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import BookingRequestsScreen from '@/app/(dashboard)/booking-requests';
import { payBookingRequestAndEscrow, resumeOrderPayment } from '@/services/serviceCheckout';

const pay = payBookingRequestAndEscrow as jest.Mock;
const resume = resumeOrderPayment as jest.Mock;
const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };

const row = (id: string, token: string) => ({
  serviceName: 'Haircut',
  request: { id, productId: 'p', buyerId: 'b', sellerId: 's', token, status: 'approved', quotedAmountUsd: 12, createdAt: '', updatedAt: '' },
});

async function render() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<BookingRequestsScreen />); });
  await flush();
  return tree;
}

const pressPay = (tree: ReactTestRenderer) =>
  act(async () => { tree.root.findAllByProps({ accessibilityLabel: 'button:Pay & confirm' })[0].props.onPress(); });

jest.setTimeout(30_000);

beforeEach(() => { pay.mockClear(); resume.mockClear(); mockAuthorization.props = {}; });

// A converted request whose order is still unpaid (a failed or abandoned payment).
const unpaidRow = (id: string, token: string) => ({
  serviceName: 'Haircut',
  orderStatus: 'pending_payment',
  request: { ...row(id, token).request, status: 'converted', orderId: `order-${id}` },
});

const finishButtons = (tree: ReactTestRenderer) =>
  tree.root.findAllByProps({ accessibilityLabel: 'button:Finish payment' });

it('an MCGP booking is not paid until the buyer authorizes it', async () => {
  mockRows.rows = [row('r1', 'MCGP')];
  const tree = await render();
  await pressPay(tree);
  expect(pay).not.toHaveBeenCalled();
  expect(mockAuthorization.props.visible).toBe(true);
  expect(mockAuthorization.props.description).toContain('Spendable MCGP');

  await act(async () => { mockAuthorization.props.onAuthorized(); });
  await flush();
  expect(pay).toHaveBeenCalledWith('r1', undefined, {});
});

it('a cancelled prompt pays nothing', async () => {
  mockRows.rows = [row('r2', 'mcgp')];
  const tree = await render();
  await pressPay(tree);
  await act(async () => { mockAuthorization.props.onCancel(); });
  await flush();
  expect(pay).not.toHaveBeenCalled();
  expect(mockAuthorization.props.visible).toBe(false);
});

it('a stablecoin booking goes straight to the wallet-signed escrow', async () => {
  mockRows.rows = [row('r3', 'USDC')];
  const tree = await render();
  await pressPay(tree);
  await flush();
  expect(pay).toHaveBeenCalledWith('r3', undefined, {});
  expect(mockAuthorization.props.visible).toBe(false);
});

it('an unpaid booking order is finished, not placed again', async () => {
  mockRows.rows = [unpaidRow('r4', 'USDC')];
  const tree = await render();
  await act(async () => { finishButtons(tree)[0].props.onPress(); });
  await flush();
  expect(resume).toHaveBeenCalledWith('order-r4', undefined, {});
  expect(pay).not.toHaveBeenCalled();
});

it('finishing an MCGP booking still asks the buyer to authorize', async () => {
  mockRows.rows = [unpaidRow('r5', 'MCGP')];
  const tree = await render();
  await act(async () => { finishButtons(tree)[0].props.onPress(); });
  expect(resume).not.toHaveBeenCalled();
  await act(async () => { mockAuthorization.props.onAuthorized(); });
  await flush();
  expect(resume).toHaveBeenCalledWith('order-r5', undefined, {});
});

it('a paid booking offers no payment', async () => {
  mockRows.rows = [{ ...unpaidRow('r6', 'USDC'), orderStatus: 'escrowed' }];
  const tree = await render();
  expect(finishButtons(tree)).toHaveLength(0);
});

it('a second try resumes the steps the first one signed', async () => {
  mockRows.rows = [unpaidRow('r7', 'USDC')];
  resume.mockImplementationOnce(async (_id: string, _phase: unknown, progress: any) => {
    progress.approve = { hash: '0xapprove', confirmed: true };
    throw new Error('wallet closed');
  });
  const tree = await render();
  await act(async () => { finishButtons(tree)[0].props.onPress(); });
  await flush();
  await act(async () => { finishButtons(tree)[0].props.onPress(); });
  await flush();
  expect(resume).toHaveBeenCalledTimes(2);
  expect(resume.mock.calls[1][2]).toEqual({ approve: { hash: '0xapprove', confirmed: true } });
});
