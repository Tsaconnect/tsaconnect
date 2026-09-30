/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Buy now puts the buyer's pick in the cart and opens checkout. It used to ADD
 * to the cart on every tap, so each tap left one more in the cart. It must set
 * the line (replace) and ignore taps while the first one is still in flight.
 * Because replace overwrites the line, the choices open prefilled from it.
 */
const mockPush = jest.fn();
jest.mock('expo-router', () => ({
  __esModule: true,
  Stack: { Screen: () => null },
  useRouter: () => ({ push: mockPush, replace: jest.fn() }),
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  useFocusEffect: (cb: () => void) => require('react').useEffect(() => cb(), [cb]),
  useLocalSearchParams: () => ({
    productId: 'p1',
    productData: JSON.stringify({
      id: 'p1', name: 'Chargeable handfan', price: 0.37, stock: 3, type: 'Product',
      attributes: [{ name: 'Colour', value: 'White, Blue' }],
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
jest.mock('@/contexts/CartContext', () => ({ __esModule: true, useCartCount: () => ({ cartCount: 0 }) }));
jest.mock('@/contexts/CurrencyContext', () => ({ __esModule: true, useCurrency: () => ({ currency: 'USD', rate: 1 }) }));
jest.mock('@/utils/formatPrice', () => ({ __esModule: true, formatPrice: (n: number) => `$${n.toFixed(2)}` }));
jest.mock('@/utils/back', () => ({ __esModule: true, goBack: jest.fn() }));
jest.mock('@/components/services/api', () => ({
  __esModule: true,
  default: { setToken: jest.fn(), getProductById: jest.fn() },
}));
const mockAddToCart = jest.fn();
const mockGetCartSummary = jest.fn();
jest.mock('@/components/services/cart', () => ({
  __esModule: true,
  cartService: {
    setToken: jest.fn(),
    getCartSummary: (...args: unknown[]) => mockGetCartSummary(...args),
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

const pressable = (tree: ReactTestRenderer, label: RegExp) =>
  tree.root.findAll((n) => label.test(String(n.props.children)) && typeof n.props.onPress === 'function')[0];
const mount = async () => {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<ProductDetailsScreen />); });
  await flush();
  return tree;
};

describe('product detail cart actions', () => {
  let tree: ReactTestRenderer | undefined;
  beforeEach(() => {
    mockPush.mockClear();
    mockAddToCart.mockReset();
    mockGetCartSummary.mockReset();
    mockGetCartSummary.mockResolvedValue({ success: false, message: 'Cart not found' });
  });
  afterEach(() => { act(() => tree?.unmount()); tree = undefined; });

  it('Buy now sets the cart line, once, however many times it is tapped', async () => {
    let finish: (v: unknown) => void = () => {};
    mockAddToCart.mockReturnValue(new Promise((r) => { finish = r; }));

    tree = await mount();
    const buy = pressable(tree, /^Buy now$/);
    // Both taps land before any re-render, so only the in-flight ref can stop the second.
    await act(async () => { buy.props.onPress(); buy.props.onPress(); });

    expect(mockAddToCart).toHaveBeenCalledTimes(1);
    expect(mockAddToCart).toHaveBeenCalledWith(expect.objectContaining({ productId: 'p1', quantity: 1, replace: true }));

    await act(async () => finish({ success: true, data: { items: [{ product: 'p1', quantity: 1 }] } }));
    await flush();
    expect(mockPush).toHaveBeenCalledWith('/checkout');
  }, 20000);

  it('Add to cart adds on top of the line (no replace) and stays on the page', async () => {
    mockAddToCart.mockResolvedValue({ success: true, data: { items: [{ product: 'p1', quantity: 2 }] } });

    tree = await mount();
    await act(async () => { pressable(tree!, /^Add to cart$/).props.onPress(); });
    await flush();

    expect(mockAddToCart).toHaveBeenCalledWith(expect.not.objectContaining({ replace: true }));
    expect(mockPush).not.toHaveBeenCalledWith('/checkout');
  }, 20000);

  it('opens prefilled from the cart line, so Buy now keeps the earlier pick', async () => {
    mockGetCartSummary.mockResolvedValue({
      success: true,
      data: { cart: { items: [{
        product: 'p1', quantity: 2, notes: 'gift wrap',
        selectedAttributes: [{ name: 'Colour', value: 'Blue' }],
      }] } },
    });
    mockAddToCart.mockResolvedValue({ success: true, data: { items: [] } });

    tree = await mount();
    await act(async () => { pressable(tree!, /^Buy now$/).props.onPress(); });
    await flush();

    expect(mockAddToCart).toHaveBeenCalledWith(expect.objectContaining({
      quantity: 2,
      notes: 'gift wrap',
      selectedAttributes: [{ name: 'Colour', value: 'Blue' }],
      replace: true,
    }));
  }, 20000);
});
