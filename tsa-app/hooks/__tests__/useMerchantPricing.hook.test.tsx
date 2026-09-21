/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The failure modes these guard are silent price corruption: converting at a
 * 1:1 fallback before rates load, and an unrelated edit re-rounding a stored
 * USD price through the current rate.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

const USD = { code: 'USD', symbol: '$', name: 'US Dollar' };
const NGN = { code: 'NGN', symbol: '₦', name: 'Nigerian Naira' };
const UGX = { code: 'UGX', symbol: 'USh', name: 'Ugandan Shilling' };

const currencyState: {
  loading: boolean;
  allRates: Record<string, { midRate: number }>;
  supportedCurrencies: typeof USD[];
} = { loading: true, allRates: {}, supportedCurrencies: [USD] };

jest.mock('@/contexts/CurrencyContext', () => ({
  __esModule: true,
  useCurrency: () => ({ ...currencyState, currency: USD }),
}));

jest.mock('@/AuthContext/AuthContext', () => ({
  __esModule: true,
  useAuth: () => ({ currentUser: { country: 'NG' } }),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { BASE_FEE_KEY, useMerchantPricing, type MerchantPricing } from '../useMerchantPricing';

let pricing: MerchantPricing;
const Harness = () => {
  pricing = useMerchantPricing();
  return null;
};

let renderer: ReactTestRenderer;
const mount = () => act(() => { renderer = TestRenderer.create(<Harness />); });
const rerender = () => act(() => { renderer.update(<Harness />); });

const loadRates = (rates: Record<string, number>) => {
  currencyState.loading = false;
  currencyState.allRates = Object.fromEntries(Object.entries(rates).map(([k, midRate]) => [k, { midRate }]));
  currencyState.supportedCurrencies = [USD, NGN, UGX].filter((c) => c.code === 'USD' || c.code in rates);
  rerender();
};

beforeEach(() => {
  currencyState.loading = true;
  currencyState.allRates = {};
  currencyState.supportedCurrencies = [USD];
});

afterEach(() => act(() => renderer.unmount()));

describe('useMerchantPricing', () => {
  it('is not ready while rates load, then commits the KYC currency rather than the provisional USD', () => {
    mount();
    expect(pricing.ready).toBe(false);

    loadRates({ NGN: 1500 });
    expect(pricing.ready).toBe(true);
    expect(pricing.selectedCurrencyCode).toBe('NGN');
  });

  it('converts a typed local price to USD at the real rate', () => {
    mount();
    loadRates({ NGN: 1500 });
    act(() => pricing.setAmount('price', '75000'));
    expect(pricing.usdPayload()).toEqual({ price: 50 });
  });

  it('only offers currencies it has a rate for', () => {
    mount();
    loadRates({ NGN: 1500 });
    expect(pricing.currencyOptions.map((o) => o.id)).toEqual(['USD', 'NGN']);
  });

  it('holds an existing product prefill until the rate is known', () => {
    mount();
    act(() => pricing.loadSavedAmounts({ price: 35, shippingSameCity: 2 }));
    // No rate yet: showing "35" in a naira field is how $35 became $0.02.
    expect(pricing.amount('price')).toBe('');

    loadRates({ NGN: 1500 });
    expect(pricing.amount('price')).toBe('52500');
    expect(pricing.amount('shippingSameCity')).toBe('3000');
  });

  it('submits untouched stored amounts verbatim, even after a currency switch', () => {
    mount();
    act(() => pricing.loadSavedAmounts({ price: 35, shippingSameCity: 0, shippingInternational: 9.99 }));
    loadRates({ NGN: 1523.37, UGX: 3701.13 });

    act(() => pricing.switchCurrency('UGX'));
    expect(pricing.selectedCurrencyCode).toBe('UGX');
    expect(pricing.usdPayload()).toEqual({ price: 35, shippingInternational: 9.99 });
  });

  it('re-converts only the fields the merchant edited', () => {
    mount();
    act(() => pricing.loadSavedAmounts({ price: 35, shippingSameCity: 2 }));
    loadRates({ NGN: 1500 });

    act(() => pricing.setAmount('price', '60000'));
    expect(pricing.usdPayload()).toEqual({ price: 40, shippingSameCity: 2 });
  });

  it('fills zone rates from the base fee and marks them edited', () => {
    mount();
    act(() => pricing.loadSavedAmounts({ price: 35, shippingSameCity: 2 }));
    loadRates({ NGN: 1500 });

    act(() => pricing.applyPackagePreset(3));
    expect(pricing.amount(BASE_FEE_KEY)).toBe('4500');
    expect(['shippingSameCity', 'shippingSameState', 'shippingSameCountry', 'shippingInternational'].map(pricing.amount))
      .toEqual(['4500', '6750', '11250', '22500']);
    expect(pricing.usdPayload()).toEqual({
      price: 35,
      shippingSameCity: 3,
      shippingSameState: 4.5,
      shippingSameCountry: 7.5,
      shippingInternational: 15,
    });
  });

  it('falls back to USD when the rate fetch fails outright', () => {
    mount();
    currencyState.loading = false;
    rerender();
    expect(pricing.selectedCurrencyCode).toBe('USD');
    expect(pricing.ready).toBe(true);
  });

  it('keys beyond the product fields: merged loads, per-key USD, switch converts them all', () => {
    mount();
    act(() => pricing.loadSavedAmounts({ accessFeeUsd: 5 }));
    act(() => pricing.loadSavedAmounts({ 'menu:a': 2, 'menu:b': 0 }));
    loadRates({ NGN: 1500, UGX: 3700 });

    expect(pricing.amount('accessFeeUsd')).toBe('7500');
    expect(pricing.amount('menu:a')).toBe('3000');
    expect(pricing.amount('menu:b')).toBe('');

    act(() => pricing.setAmount('menu:new', '4500'));
    act(() => pricing.switchCurrency('UGX'));
    expect(pricing.amount('menu:new')).toBe('11100');
    expect(pricing.usdFor('menu:new')).toBe(3);
    expect(pricing.usdFor('menu:a')).toBe(2);
    expect(pricing.usdFor('menu:b')).toBeUndefined();
  });
});
