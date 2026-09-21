/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The picker is wired to PickerSheet by prop name; a wrong name (`open` for
 * `visible`) leaves the sheet permanently closed with nothing else failing.
 * This reads the props PickerSheet actually receives.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/components/ui', () => {
  const seen: { selectRow?: any; pickerSheet?: any } = {};
  return {
    __esModule: true,
    __seen: seen,
    SelectRow: (props: any) => { seen.selectRow = props; return null; },
    PickerSheet: (props: any) => { seen.pickerSheet = props; return null; },
    Input: () => null,
    PressableScale: () => null,
    Text: () => null,
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { PricingCurrencySelect } from '../MerchantPricingFields';
import type { MerchantPricing } from '@/hooks/useMerchantPricing';

const seen = (jest.requireMock('@/components/ui') as any).__seen;

const pricing = {
  ready: true,
  selectedCurrencyCode: 'NGN',
  selectedCurrency: { code: 'NGN', symbol: '₦', name: 'Nigerian Naira' },
  currencyOptions: [{ id: 'USD', label: 'USD' }, { id: 'NGN', label: 'NGN' }],
  switchCurrency: jest.fn(),
} as unknown as MerchantPricing;

it('opens the currency sheet and switches on selection', () => {
  let renderer!: ReactTestRenderer;
  act(() => { renderer = TestRenderer.create(<PricingCurrencySelect pricing={pricing} />); });

  expect(seen.pickerSheet.visible).toBe(false);
  expect(seen.pickerSheet.selectedId).toBe('NGN');

  act(() => seen.selectRow.onPress());
  expect(seen.pickerSheet.visible).toBe(true);

  act(() => seen.pickerSheet.onSelect({ id: 'USD', label: 'USD' }));
  expect(pricing.switchCurrency).toHaveBeenCalledWith('USD');
  expect(seen.pickerSheet.visible).toBe(false);

  act(() => renderer.unmount());
});
