/* eslint-disable import/first -- jest hoists mocks above imports */
import React from 'react';
import TestRenderer, { act } from 'react-test-renderer';

const mockScreenOptionsState = {
  sellTitles: [] as string[],
  buyTitles: [] as string[],
};

let mockCurrentParams: Record<string, string> = {};
let mockCurrentRampCurrency = {
  code: 'NGN',
  currency: { code: 'NGN', symbol: '₦', name: 'Nigerian Naira' },
  setCode: jest.fn((next: string) => {
    mockCurrentRampCurrency.code = next;
    mockCurrentRampCurrency.currency = { code: next, symbol: next, name: next };
  }),
  options: [
    { id: 'NGN', label: 'NGN' },
    { id: 'UGX', label: 'UGX' },
  ],
};

jest.mock('expo-router', () => ({
  __esModule: true,
  router: { push: jest.fn(), replace: jest.fn(), back: jest.fn() },
  useLocalSearchParams: () => mockCurrentParams,
  Stack: {
    Screen: (props: { options?: { title?: string } }) => {
      if (props.options?.title) {
        if (props.options.title.startsWith('Sell for')) {
          mockScreenOptionsState.sellTitles.push(props.options.title);
        } else if (props.options.title.startsWith('Buy with')) {
          mockScreenOptionsState.buyTitles.push(props.options.title);
        }
      }
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

describe('Paycrest header title dynamic currency display', () => {
  beforeEach(() => {
    mockScreenOptionsState.sellTitles = [];
    mockScreenOptionsState.buyTitles = [];
    mockCurrentParams = {};
    mockCurrentRampCurrency = {
      code: 'NGN',
      currency: { code: 'NGN', symbol: '₦', name: 'Nigerian Naira' },
      setCode: jest.fn((next: string) => {
        mockCurrentRampCurrency.code = next;
        mockCurrentRampCurrency.currency = { code: next, symbol: next, name: next };
      }),
      options: [
        { id: 'NGN', label: 'NGN' },
        { id: 'UGX', label: 'UGX' },
      ],
    };
  });

  it('renders "Sell for Naira" when currency is NGN', async () => {
    let renderer: any;
    await act(async () => {
      renderer = TestRenderer.create(<PaycrestSellScreen />);
    });
    expect(mockScreenOptionsState.sellTitles).toContain('Sell for Naira');
    await act(async () => {
      renderer.unmount();
    });
  });

  it('renders "Sell for UGX" when currency is UGX', async () => {
    mockCurrentRampCurrency.code = 'UGX';
    mockCurrentRampCurrency.currency = { code: 'UGX', symbol: 'USh', name: 'Ugandan Shilling' };
    currentParamsMock();

    let renderer: any;
    await act(async () => {
      renderer = TestRenderer.create(<PaycrestSellScreen />);
    });
    expect(mockScreenOptionsState.sellTitles).toContain('Sell for UGX');
    await act(async () => {
      renderer.unmount();
    });
  });

  it('renders "Buy with Naira" when currency is NGN', async () => {
    let renderer: any;
    await act(async () => {
      renderer = TestRenderer.create(<PaycrestBuyScreen />);
    });
    expect(mockScreenOptionsState.buyTitles).toContain('Buy with Naira');
    await act(async () => {
      renderer.unmount();
    });
  });

  it('renders "Buy with UGX" when currency is UGX', async () => {
    mockCurrentRampCurrency.code = 'UGX';
    mockCurrentRampCurrency.currency = { code: 'UGX', symbol: 'USh', name: 'Ugandan Shilling' };
    currentParamsMock();

    let renderer: any;
    await act(async () => {
      renderer = TestRenderer.create(<PaycrestBuyScreen />);
    });
    expect(mockScreenOptionsState.buyTitles).toContain('Buy with UGX');
    await act(async () => {
      renderer.unmount();
    });
  });
});

function currentParamsMock() {
  mockCurrentParams = { currency: 'UGX' };
}
