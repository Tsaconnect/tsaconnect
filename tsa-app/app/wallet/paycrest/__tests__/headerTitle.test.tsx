/* eslint-disable import/first -- jest hoists mocks above imports */
import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';

// Every title a screen sets, in order — the last one is what the header shows.
let mockTitles: string[] = [];

const mockCurrentRampCurrency = {
  code: 'NGN',
  currency: { code: 'NGN', symbol: '₦', name: 'Nigerian Naira' },
  setCode: jest.fn(),
  options: [
    { id: 'NGN', label: 'NGN' },
    { id: 'UGX', label: 'UGX' },
  ],
};

jest.mock('expo-router', () => ({
  __esModule: true,
  router: { push: jest.fn(), replace: jest.fn(), back: jest.fn() },
  useLocalSearchParams: () => ({}),
  Stack: {
    Screen: (props: { options?: { title?: string } }) => {
      if (props.options?.title) mockTitles.push(props.options.title);
      return null;
    },
  },
}));

jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    SafeAreaView: View,
    useSafeAreaInsets: () => ({ top: 0, bottom: 0, left: 0, right: 0 }),
  };
});

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000' }),
    spacing: { xs: 4, sm: 8, md: 12, lg: 16, xl: 24 },
    radius: { sm: 4 },
  }),
}));

jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true,
  useSignableWallet: () => ({
    address: '0x1111111111111111111111111111111111111111',
    ready: true,
  }),
}));

jest.mock('@/hooks/useCountdown', () => ({
  __esModule: true,
  useCountdown: () => ({ label: '10:00', expired: false, secs: 600 }),
}));

jest.mock('@/hooks/useRampCurrency', () => ({
  __esModule: true,
  ...jest.requireActual('@/hooks/useRampCurrency'),
  useRampCurrency: () => mockCurrentRampCurrency,
}));

jest.mock('@/services/paycrestApi', () => ({
  __esModule: true,
  getPayoutInstitutions: jest.fn(async () => []),
  getQuote: jest.fn(async () => null),
  createSell: jest.fn(),
  cancelOrder: jest.fn(),
  claimPayment: jest.fn(),
}));

jest.mock('@/services/paycrestDeposit', () => ({
  __esModule: true,
  checkFunding: jest.fn(() => ({ ok: true })),
  sendSponsoredPaycrestDeposit: jest.fn(),
  balanceOf: jest.fn(async () => 0),
}));

jest.mock('@/services/gasSponsorship', () => ({
  __esModule: true,
  getSponsoredRoutes: jest.fn(async () => []),
  getSponsorshipOffer: jest.fn(async () => null),
  isGasSponsored: jest.fn(() => false),
}));

jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(async () => []),
  normalizeWalletBalances: jest.fn(() => []),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  const label = (text: any) => react.createElement(RNText, null, text);
  return {
    __esModule: true,
    Text: (props: any) => label(props.children),
    Button: (props: any) => label(props.title),
    AmountInput: (props: any) => label(props.label),
    Input: (props: any) => label(props.label),
    Card: box,
    SummaryCard: box,
    SummaryRow: (props: any) => label(`${props.label}: ${props.value}`),
    SelectRow: (props: any) => label(`${props.label}: ${props.value}`),
    PickerSheet: () => null,
    Spinner: () => null,
    CopyableField: () => null,
  };
});

jest.mock('@/components/swap/TokenPickerModal', () => ({
  __esModule: true,
  default: () => null,
}));

import PaycrestSellScreen from '../sell';
import PaycrestBuyScreen from '../buy';

const selectRampCurrency = (code: string) => {
  mockCurrentRampCurrency.code = code;
  mockCurrentRampCurrency.currency = { code, symbol: code, name: code };
};

async function lastTitle(Screen: React.ComponentType): Promise<string | undefined> {
  let renderer: ReactTestRenderer | undefined;
  await act(async () => {
    renderer = TestRenderer.create(<Screen />);
  });
  await act(async () => {
    renderer?.unmount();
  });
  return mockTitles[mockTitles.length - 1];
}

describe('Paycrest header title follows the ramp currency', () => {
  beforeEach(() => {
    mockTitles = [];
    selectRampCurrency('NGN');
  });

  it('sell screen says "Sell for Naira" for NGN', async () => {
    expect(await lastTitle(PaycrestSellScreen)).toBe('Sell for Naira');
  });

  it('sell screen says "Sell for UGX" for UGX', async () => {
    selectRampCurrency('UGX');
    expect(await lastTitle(PaycrestSellScreen)).toBe('Sell for UGX');
  });

  it('buy screen says "Buy with Naira" for NGN', async () => {
    expect(await lastTitle(PaycrestBuyScreen)).toBe('Buy with Naira');
  });

  it('buy screen says "Buy with KES" for KES', async () => {
    selectRampCurrency('KES');
    expect(await lastTitle(PaycrestBuyScreen)).toBe('Buy with KES');
  });
});
