/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Checkout's unified "Pay with" selector (owner decision 2026-09-29): one row
 * — direct Sonic USDC/USDT, Spendable MCGP, or a swap — instead of three chips
 * plus a separate "pay with another token" link. This exercises the screen's
 * own wiring (checkoutPaySelection is unit-tested on its own): the row shows
 * the chain name, "Change" opens the picker, and the summary switches between
 * a direct payment and a swap quote.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null), removeItem: jest.fn(async () => null) },
}));

jest.mock('expo-router', () => ({
  __esModule: true,
  router: { push: jest.fn(), back: jest.fn(), replace: jest.fn() },
}));

jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});

jest.mock('@/hooks/useNetwork', () => ({
  __esModule: true,
  getNetwork: () => 'mainnet',
  useNetwork: () => ({ network: 'mainnet', switchNetwork: jest.fn() }),
}));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/contexts/CurrencyContext', () => ({
  __esModule: true,
  useCurrency: () => ({ currency: { code: 'USD' }, rate: 1, formatPrice: (usd: number) => `$${usd.toFixed(2)}` }),
}));

jest.mock('@/AuthContext/AuthContext', () => ({
  __esModule: true,
  useAuth: () => ({
    currentUser: { verificationStatus: 'verified', country: 'NG', state: 'Lagos', city: 'Ikeja', name: 'Ada', phoneNumber: '+234' },
    token: 'tok',
  }),
}));

jest.mock('@/hooks/useKycVerification', () => ({
  __esModule: true,
  useKycVerification: () => ({ isKycVerified: true, requireKycVerified: () => true }),
}));

jest.mock('@/services/wallet', () => ({
  __esModule: true,
  getActiveWallet: jest.fn(async () => '0xWallet0000000000000000000000000000000000'),
}));

const mockBalances: { USDC: number | null; USDT: number | null; MCGP: number | null; S: number | null } = {
  USDC: 0, USDT: 0, MCGP: 0, S: 5,
};
jest.mock('@/hooks/useCheckoutBalances', () => ({
  __esModule: true,
  useCheckoutBalances: () => ({ balances: mockBalances, address: '0xWallet0000000000000000000000000000000000', refresh: jest.fn() }),
}));

let mockHoldings: any[] = [];
jest.mock('@/hooks/useWalletHoldings', () => ({
  __esModule: true,
  useWalletHoldings: () => ({ holdings: mockHoldings, loading: false }),
}));

const mockAddress = {
  id: 'addr1', label: 'Home', recipientName: 'Ada Obi', phone: '+2348012345678', street: '1 Ade St',
  city: 'Ikeja', state: 'Lagos', country: 'Nigeria', isDefault: true, createdAt: '', updatedAt: '',
};
jest.mock('@/hooks/useDeliveryAddresses', () => ({
  __esModule: true,
  useDeliveryAddresses: () => ({
    addresses: [mockAddress], selected: mockAddress, select: jest.fn(), loading: false, error: null, reload: jest.fn(),
  }),
}));

const mockCartItem = {
  _id: 'i1',
  product: { _id: 'p1', id: 'p1', name: 'Widget', images: [] },
  seller: { _id: 's1', name: 'Seller' },
  quantity: 1,
  price: 10,
  addedAt: '',
  updatedAt: '',
};
const mockCartData = {
  cart: { items: [mockCartItem] },
  itemsBySeller: [{ sellerId: 's1', sellerName: 'Seller', items: [mockCartItem] }],
  summary: { totalQuantity: 1, totalItems: 1, subtotal: 10, shipping: 0, gasFee: 0 },
  feeConfig: { buyerCashbackPercent: 2 },
};
jest.mock('@/components/services/cart', () => ({
  __esModule: true,
  cartService: {
    setToken: jest.fn(),
    initializeToken: jest.fn(async () => {}),
    getCartSummary: jest.fn(async () => ({ success: true, data: mockCartData })),
    notifyChange: jest.fn(),
  },
}));

jest.mock('@/services/serviceCheckout', () => ({
  __esModule: true,
  payOrder: jest.fn(),
  OrderNotPayableError: class OrderNotPayableError extends Error {},
  MARKETPLACE_NETWORK: 'mainnet',
}));

// Quotes for each order token, set per test. quoteCart is the only network
// call useCheckoutQuote makes; everything else in that hook (quoteUnits,
// useQuoteFormat, the CartQuote shape) is real.
const mockQuotes: Record<string, any> = {};
jest.mock('@/services/orderApi', () => {
  const actual = jest.requireActual('@/services/orderApi');
  return {
    ...actual,
    __esModule: true,
    quoteCart: jest.fn(async (token: string) => (mockQuotes[token] ? { success: true, data: mockQuotes[token] } : { success: false, message: 'no quote' })),
    createOrders: jest.fn(async () => ({ success: true, data: { orders: [], checkoutGroupId: 'g1' } })),
  };
});

// The funding run stays idle throughout — this test covers the review screen,
// not a payment in flight. useFundingPrice answers with a quote whenever a
// swap pick is present, mirroring what the real hook does once priced.
const mockFundingQuote = {
  groupId: 'g1', orders: 1, token: 'USDC', tokenAddress: '0xusdc', chainId: 146,
  orderTotal: '160000', walletBalance: '0', shortfall: '160000', toAmount: '160000',
  fromAmount: '500000000000000000', fromAmountUsd: 0.2, lifiCostUsd: 0.01, gasUsd: 0.005,
  costBps: 50, gasBps: 25, budgetBps: 200, etaSeconds: 30, tool: 'test', sourceGasTopUp: false, destGasTopUp: false,
};
jest.mock('@/hooks/useCheckoutFunding', () => ({
  __esModule: true,
  useCheckoutFundingRun: () => ({
    state: { stage: 'idle' },
    runner: { resume: jest.fn(async () => {}), pay: jest.fn(), retry: jest.fn(), discard: jest.fn(async () => {}) },
  }),
  useFundingPrice: jest.fn((opts: any) => (
    opts.pick
      ? { price: { pick: opts.pick, quote: mockFundingQuote, at: Date.now() }, pricing: false, stale: false, reprice: jest.fn() }
      : { price: null, pricing: false, stale: false, reprice: jest.fn() }
  )),
}));

jest.mock('@/components/checkout/ConfirmPaymentSheet', () => ({ __esModule: true, ConfirmPaymentSheet: () => null }));
jest.mock('@/components/checkout/DeliveryAddressSheet', () => ({ __esModule: true, DeliveryAddressSheet: () => null }));
jest.mock('@/components/checkout/PaymentProgress', () => ({ __esModule: true, PaymentProgress: () => null }));
jest.mock('@/components/checkout/QuoteBreakdown', () => ({ __esModule: true, QuoteBreakdown: () => null, TokenAmount: () => null }));
jest.mock('@/components/common/AuthorizationModal', () => ({ __esModule: true, default: () => null }));
jest.mock('@/components/wallet/TokenAvatar', () => ({ __esModule: true, default: () => null }));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, props, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, null, `[${props.title}${props.disabled ? ':disabled' : ''}]`),
    Card: box,
    Divider: () => null,
    BottomActionBar: box,
    EmptyState: () => null,
    ErrorState: () => null,
    Icon: () => null,
    IconButton: (props: any) => react.createElement(View, props),
    PressableScale: box,
    Spinner: (props: any) => react.createElement(RNText, null, props.label ?? 'spinner'),
    Sheet: (props: any) => (props.visible ? react.createElement(View, null, props.children, props.footer) : null),
    AccentCard: box,
    Stepper: () => null,
  };
});

// A stand-in for the real AssetPicker (unit-tested on its own): exposes the
// extraSections options and a fake swap row, so this test can drive checkout's
// own selection wiring without re-running AssetPicker's catalogue machinery.
jest.mock('@/components/assets/AssetPicker', () => {
  const react = jest.requireActual('react');
  const { View, Text, TouchableOpacity } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (props: any) => {
      if (!props.visible) return null;
      const rows = (props.extraSections ?? []).flatMap((s: any) => s.options);
      return react.createElement(
        View,
        { testID: 'asset-picker' },
        ...rows.map((o: any) =>
          react.createElement(
            TouchableOpacity,
            { key: o.key, testID: `pick-${o.key}`, onPress: o.onSelect },
            react.createElement(Text, null, o.label ?? o.symbol),
          )),
        react.createElement(
          TouchableOpacity,
          {
            testID: 'pick-swap-bnb',
            onPress: () => props.onSelect({ address: '0xbnb', symbol: 'BNB', name: 'BNB', decimals: 18, logoURI: '', chainId: 56 }, 56),
          },
          react.createElement(Text, null, 'BNB (swap)'),
        ),
      );
    },
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import CheckoutScreen from '@/app/(detail)/checkout/index';

const usdcQuote = (total: string) => ({
  token: 'USDC', decimals: 6, usdPerToken: 1,
  lines: [{ productId: 'p1', name: 'Widget', sellerId: 's1', sellerName: 'Seller', quantity: 1, kind: 'product', shippingZone: 'local', productAmount: '150000', shippingAmount: '0', platformFee: '10000', gasFee: '0', total }],
  totals: { productAmount: '150000', shippingAmount: '0', platformFee: '10000', gasFee: '0', total },
});
const mcgpQuote = (total: string) => ({
  token: 'MCGP', decimals: 18, usdPerToken: 0.02,
  lines: [{ productId: 'p1', name: 'Widget', sellerId: 's1', sellerName: 'Seller', quantity: 1, kind: 'product', shippingZone: 'local', productAmount: total, shippingAmount: '0', platformFee: '0', gasFee: '0', total }],
  totals: { productAmount: total, shippingAmount: '0', platformFee: '0', gasFee: '0', total },
});

const holding = (over: any) => ({
  chainId: 56, address: '0xbnb', native: false, trusted: true, symbol: 'BNB', name: 'BNB', decimals: 18, balance: 1, usdValue: 600, ...over,
});

function text(tree: ReactTestRenderer): string {
  const out: string[] = [];
  const walk = (node: any) => {
    if (node == null) return;
    if (typeof node === 'string') { out.push(node); return; }
    if (Array.isArray(node)) { node.forEach(walk); return; }
    (node.children ?? []).forEach(walk);
  };
  walk(tree.toJSON());
  return out.join(' ');
}
const flush = async () => { for (let i = 0; i < 10; i += 1) await act(async () => { await Promise.resolve(); }); };
const press = (tree: ReactTestRenderer, testID: string) => act(async () => {
  tree.root.findAllByProps({ testID })[0].props.onPress();
});
const pressLabelPrefix = (tree: ReactTestRenderer, start: string) => {
  const hit = tree.root.findAll(n => typeof n.props.accessibilityLabel === 'string'
    && n.props.accessibilityLabel.startsWith(start) && typeof n.props.onPress === 'function')[0];
  if (!hit) throw new Error(`no pressable starting "${start}"`);
  return act(async () => { hit.props.onPress(); });
};

beforeEach(() => {
  jest.clearAllMocks();
  mockBalances.USDC = 0; mockBalances.USDT = 0; mockBalances.MCGP = 0; mockBalances.S = 5;
  mockHoldings = [];
  delete mockQuotes.USDC; delete mockQuotes.MCGP; delete mockQuotes.USDT;
});

async function render() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(React.createElement(CheckoutScreen)); });
  await flush();
  return tree;
}

it('shows one selector row that names the chain, defaulting to a sufficient swap holding when USDC and MCGP are both short', async () => {
  mockQuotes.USDC = usdcQuote('160000'); // 0.16 USDC
  mockQuotes.MCGP = mcgpQuote('10000000000000000000'); // 10 MCGP
  mockBalances.USDC = 0.02;
  mockBalances.MCGP = 1;
  mockHoldings = [holding({ balance: 1, usdValue: 600 })]; // far more than 0.16 * 1.02

  const tree = await render();
  const t = text(tree);
  expect(t).toContain('BNB');
  expect(t).toContain('BNB Smart Chain');
  // Only ONE "Pay with" selector row — no leftover chip list.
  expect(t).not.toContain('Bal ');
});

it('switches the summary between a direct payment and a swap quote as the selection changes', async () => {
  mockQuotes.USDC = usdcQuote('160000');
  mockQuotes.MCGP = mcgpQuote('10000000000000000000');
  mockBalances.USDC = 5; // covers the 0.16 USDC total — defaults to direct USDC
  mockBalances.MCGP = 0;

  const tree = await render();
  expect(text(tree)).toContain('USDC');
  expect(text(tree)).not.toContain("You'll send about");
  // The direct-pay cashback note shows for a direct selection.
  expect(text(tree)).toContain('cashback');

  // Change → pick the swap row.
  await pressLabelPrefix(tree, 'Pay with USDC');
  await press(tree, 'pick-swap-bnb');
  await flush();
  expect(text(tree)).toContain('BNB Smart Chain');
  expect(text(tree)).toContain("You'll send about");
  expect(text(tree)).not.toContain('cashback');

  // Change → back to Pay directly with USDC.
  await pressLabelPrefix(tree, 'Pay with BNB');
  await press(tree, 'pick-usdc');
  await flush();
  expect(text(tree)).not.toContain("You'll send about");
  expect(text(tree)).toContain('cashback');
});

it('names the chain in the shortfall message when nothing covers the total', async () => {
  mockQuotes.USDC = usdcQuote('160000'); // 0.16 USDC
  mockQuotes.MCGP = mcgpQuote('10000000000000000000');
  mockBalances.USDC = 0.02;
  mockBalances.MCGP = 0;
  mockHoldings = []; // nothing to swap either — falls back to direct USDC, short

  const tree = await render();
  const t = text(tree);
  expect(t).toContain('Not enough USDC on Sonic');
  expect(t).toContain('0.16 USDC');
  expect(t).toContain('0.02 USDC');
});
