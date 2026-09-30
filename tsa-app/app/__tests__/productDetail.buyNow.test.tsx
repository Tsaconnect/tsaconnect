/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Buy now puts the buyer's pick in the cart and opens checkout. It used to ADD
 * to the cart on every tap, so each tap left one more in the cart. It must set
 * the line (replace) and ignore taps while the first one is still in flight.
 */
const mockPush = jest.fn();
jest.mock('expo-router', () => ({
  __esModule: true,
  Stack: { Screen: () => null },
  useRouter: () => ({ push: mockPush, replace: jest.fn() }),
  useLocalSearchParams: () => ({
    productId: 'p1',
    productData: JSON.stringify({
      id: 'p1', name: 'Chargeable handfan', price: 0.37, stock: 1, type: 'Product',
      images: [], createdAt: '2026-09-28T00:00:00Z', views: 12, sales: 0,
    }),
  }),
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
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('@/AuthContext/AuthContext', () => ({ __esModule: true, useAuth: () => ({ token: 't' }) }));
jest.mock('@/contexts/CurrencyContext', () => ({ __esModule: true, useCurrency: () => ({ currency: 'USD', rate: 1 }) }));
jest.mock('@/utils/formatPrice', () => ({ __esModule: true, formatPrice: (n: number) => `$${n.toFixed(2)}` }));
jest.mock('@/utils/back', () => ({ __esModule: true, goBack: jest.fn() }));
jest.mock('@/components/services/api', () => ({
  __esModule: true,
  default: { setToken: jest.fn(), getProductById: jest.fn() },
}));
const mockAddToCart = jest.fn();
jest.mock('@/components/services/cart', () => ({
  __esModule: true,
  cartService: {
    setToken: jest.fn(),
    getCartItemCount: jest.fn(async () => 0),
    getCartSummary: jest.fn(async () => ({ success: false, message: 'Cart not found' })),
    subscribe: jest.fn(() => () => {}),
    addToCart: (...args: unknown[]) => mockAddToCart(...args),
  },
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  const pressable = (props: any) =>
    react.createElement(View, { onPress: props.onPress, onClick: props.onPress }, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) =>
      react.createElement(RNText, { onPress: props.disabled || props.loading ? undefined : props.onPress }, props.title),
    AccentCard: box, Badge: () => null, Avatar: () => null, BottomActionBar: box, Card: box, Chip: box, Divider: () => null,
    EmptyState: () => null, Icon: () => null, Input: () => null, PressableScale: pressable, Spinner: () => null,
    useToast: () => ({ success: jest.fn(), error: jest.fn(), info: jest.fn() }),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import ProductDetailsScreen from '../(detail)/product/[productId]';

const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };
const buyNow = (tree: ReactTestRenderer) =>
  tree.root.findAll((n) => /^Buy now/.test(String(n.props.children)))[0];

describe('product detail Buy now', () => {
  let tree: ReactTestRenderer;
  beforeEach(() => {
    mockPush.mockClear();
    mockAddToCart.mockReset();
  });
  afterEach(() => { act(() => tree?.unmount()); });

  it('sets the cart line, once, however many times it is tapped', async () => {
    let finish: (v: unknown) => void = () => {};
    mockAddToCart.mockReturnValue(new Promise((r) => { finish = r; }));

    await act(async () => { tree = TestRenderer.create(<ProductDetailsScreen />); });
    await flush();
    await act(async () => { buyNow(tree).props.onPress?.(); });
    await act(async () => { buyNow(tree).props.onPress?.(); });

    expect(mockAddToCart).toHaveBeenCalledTimes(1);
    expect(mockAddToCart).toHaveBeenCalledWith(expect.objectContaining({ productId: 'p1', quantity: 1, replace: true }));

    await act(async () => finish({ success: true }));
    await flush();
    expect(mockPush).toHaveBeenCalledWith('/checkout');
  });
});
