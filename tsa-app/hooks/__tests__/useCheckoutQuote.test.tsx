/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The quote is the number a buyer confirms. These guard the two ways it could
 * show the wrong one: an older request landing after a newer one, and a quote
 * for the previously selected token lingering after a switch.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

jest.mock('@/contexts/CurrencyContext', () => ({
  __esModule: true,
  useCurrency: () => ({
    currency: { code: 'NGN', symbol: '₦' },
    formatPrice: (usd: number) => `₦${(usd * 1500).toFixed(2)}`,
  }),
}));

jest.mock('@/services/orderApi', () => ({ __esModule: true, quoteCart: jest.fn() }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { quoteCart } from '@/services/orderApi';
import { listingAmount, useCheckoutQuote, useQuoteFormat } from '../useCheckoutQuote';

const mockQuote = quoteCart as jest.Mock;

const quoteFor = (token: string, total: string, decimals = 6, usdPerToken = 1) => ({
  token,
  decimals,
  usdPerToken,
  lines: [],
  totals: { productAmount: total, shippingAmount: '0', platformFee: '0', gasFee: '0', total },
});

type Result = ReturnType<typeof useCheckoutQuote>;
let result: Result;
const Harness = (props: Parameters<typeof useCheckoutQuote>[0]) => {
  result = useCheckoutQuote(props);
  return null;
};

const base = { city: 'Ikeja', state: 'Lagos', country: 'Nigeria', enabled: true, cartKey: 'p1:1' };
const flush = () => act(async () => { await Promise.resolve(); });

let renderer: ReactTestRenderer;
afterEach(() => {
  act(() => renderer?.unmount());
  mockQuote.mockReset();
});

it('drops a slower, older response so the latest token wins', async () => {
  let resolveSlow!: (v: unknown) => void;
  mockQuote
    .mockImplementationOnce(() => new Promise((r) => { resolveSlow = r; }))
    .mockResolvedValueOnce({ success: true, data: quoteFor('USDT', '2000000') });

  act(() => { renderer = TestRenderer.create(<Harness {...base} token="USDC" />); });
  act(() => { renderer.update(<Harness {...base} token="USDT" />); });
  await flush();
  expect(result.quote?.token).toBe('USDT');

  await act(async () => { resolveSlow({ success: true, data: quoteFor('USDC', '1000000') }); });
  expect(result.quote?.token).toBe('USDT');
  expect(result.quote?.totals.total).toBe('2000000');
});

it('never shows a quote priced in a different token than the one selected', async () => {
  mockQuote.mockResolvedValue({ success: true, data: quoteFor('USDC', '1000000') });
  act(() => { renderer = TestRenderer.create(<Harness {...base} token="USDC" />); });
  await flush();
  expect(result.quote?.token).toBe('USDC');

  mockQuote.mockImplementation(() => new Promise(() => {}));
  act(() => { renderer.update(<Harness {...base} token="MCGP" />); });
  expect(result.quote).toBeNull();
  expect(result.loading).toBe(true);
});

it('does not price a cart with no address', async () => {
  act(() => { renderer = TestRenderer.create(<Harness {...base} enabled={false} token="USDC" />); });
  await flush();
  expect(mockQuote).not.toHaveBeenCalled();
  expect(result.loading).toBe(false);
});

it('surfaces a failed quote as an error, not a stale total', async () => {
  mockQuote.mockResolvedValue({ success: false, message: 'Insufficient stock for Mic' });
  act(() => { renderer = TestRenderer.create(<Harness {...base} token="USDC" />); });
  await flush();
  expect(result.quote).toBeNull();
  expect(result.error).toBe('Insufficient stock for Mic');
  expect(result.loading).toBe(false);
});

describe('useQuoteFormat', () => {
  let fmt: ReturnType<typeof useQuoteFormat>;
  const FmtHarness = ({ q }: { q: any }) => {
    fmt = useQuoteFormat(q);
    return null;
  };

  it('shows the token amount and the app-currency equivalent', () => {
    act(() => { renderer = TestRenderer.create(<FmtHarness q={quoteFor('USDC', '160000')} />); });
    expect(fmt.token('160000')).toBe('0.16 USDC');
    expect(fmt.approx('160000')).toBe('≈ ₦240.00');
  });

  it('values MCGP through its USD price, not 1:1', () => {
    // 2 MCGP at $0.25 = $0.50 → ₦750 at the mocked 1500 rate.
    act(() => { renderer = TestRenderer.create(<FmtHarness q={quoteFor('MCGP', '2000000000000000000', 18, 0.25)} />); });
    expect(fmt.token('2000000000000000000')).toBe('2.00 MCGP');
    expect(fmt.approx('2000000000000000000')).toBe('≈ ₦750.00');
  });
});

it('shows the buyer the listing price: escrowed amount plus the seller-paid fee', () => {
  // $20 listing: 19.607844 escrowed for the seller + 0.392156 fee.
  expect(listingAmount({ productAmount: '19607844', platformFee: '392156' })).toBe('20000000');
  // 18-decimal MCGP amounts stay exact past Number precision.
  expect(listingAmount({ productAmount: '5000000000000000001', platformFee: '0' })).toBe('5000000000000000001');
});
