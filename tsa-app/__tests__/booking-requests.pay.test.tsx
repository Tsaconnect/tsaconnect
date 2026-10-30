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
jest.mock('@/services/serviceCheckout', () => ({
  __esModule: true,
  payBookingRequestAndEscrow: jest.fn(async () => ({})),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import BookingRequestsScreen from '@/app/(dashboard)/booking-requests';
import { payBookingRequestAndEscrow } from '@/services/serviceCheckout';

const pay = payBookingRequestAndEscrow as jest.Mock;
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

beforeEach(() => { pay.mockClear(); mockAuthorization.props = {}; });

it('an MCGP booking is not paid until the buyer authorizes it', async () => {
  mockRows.rows = [row('r1', 'MCGP')];
  const tree = await render();
  await pressPay(tree);
  expect(pay).not.toHaveBeenCalled();
  expect(mockAuthorization.props.visible).toBe(true);
  expect(mockAuthorization.props.description).toContain('Spendable MCGP');

  await act(async () => { mockAuthorization.props.onAuthorized(); });
  await flush();
  expect(pay).toHaveBeenCalledWith('r1');
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
  expect(pay).toHaveBeenCalledWith('r3');
  expect(mockAuthorization.props.visible).toBe(false);
});
