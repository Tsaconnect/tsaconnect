/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Promotions Arena: loading, empty, list and filter change. The API only
 * filters by Product|Service, so the service kinds are narrowed client-side by
 * the listing's category serviceType.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Chip: (p: any) => react.createElement(RNText, { accessibilityLabel: `chip:${p.label}`, onPress: p.onPress }, p.label),
    EmptyState: (p: any) => react.createElement(RNText, null, p.title),
    ErrorState: (p: any) => react.createElement(RNText, null, p.title),
    Spinner: () => react.createElement(View, { accessibilityLabel: 'spinner' }),
  };
});
jest.mock('@/components/marketplace/ProductCard', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (p: any) => react.createElement(RNText, { accessibilityLabel: `row:${p.product.id}`, onPress: p.onPress }, p.product.name),
  };
});
const mockPush = jest.fn();
jest.mock('expo-router', () => ({ __esModule: true, useRouter: () => ({ push: mockPush }) }));
jest.mock('react-native-safe-area-context', () => ({
  __esModule: true,
  SafeAreaView: (p: any) => jest.requireActual('react').createElement(jest.requireActual('react-native').View, null, p.children),
}));
jest.mock('@/components/services/api', () => ({
  __esModule: true,
  default: { getPromotions: jest.fn(), getCategories: jest.fn() },
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import api from '@/components/services/api';
import PromotionsArenaScreen from '@/app/(detail)/promotions';

const getPromotions = api.getPromotions as jest.Mock;
const getCategories = api.getCategories as jest.Mock;
const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };

const item = (id: string, type: 'Product' | 'Service', category = 'c-product') =>
  ({ id, name: `Item ${id}`, type, category, images: [] });
const page = (products: any[]) => ({ success: true, data: { products, pagination: { page: 1, limit: 20, total: products.length } } });
const text = (t: ReactTestRenderer) =>
  (t.root as any).findAll((n: any) => typeof n.type === 'string' && n.type === 'Text').map((n: any) => n.children.join('')).join('|');

async function render() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<PromotionsArenaScreen />); });
  await flush();
  return tree;
}

beforeEach(() => {
  jest.clearAllMocks();
  getCategories.mockResolvedValue({
    success: true,
    data: [{ id: 'c-order', serviceType: 'order' }, { id: 'c-booking', serviceType: 'booking' }],
  });
});

it('shows a spinner while loading', async () => {
  getPromotions.mockReturnValue(new Promise(() => {}));
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<PromotionsArenaScreen />); });
  expect(tree.root.findAllByProps({ accessibilityLabel: 'spinner' }).length).toBeGreaterThan(0);
});

it('shows the empty state when nothing is on promotion', async () => {
  getPromotions.mockResolvedValue(page([]));
  const tree = await render();
  expect(text(tree)).toContain('No promotions right now');
});

it('lists promoted items and opens product vs service detail', async () => {
  getPromotions.mockResolvedValue(page([item('p1', 'Product'), item('s1', 'Service', 'c-order')]));
  const tree = await render();
  expect(getPromotions).toHaveBeenCalledWith({ page: 1, limit: 20, type: undefined });
  await act(async () => { (tree.root as any).findByProps({ accessibilityLabel: 'row:p1' }).props.onPress(); });
  expect(mockPush.mock.calls[0][0]).toContain('/product/p1');
  await act(async () => { (tree.root as any).findByProps({ accessibilityLabel: 'row:s1' }).props.onPress(); });
  expect(mockPush.mock.calls[1][0].pathname).toBe('/servicedetail');
});

it('Products chip asks the API for type=Product', async () => {
  getPromotions.mockResolvedValue(page([item('p1', 'Product')]));
  const tree = await render();
  await act(async () => { (tree.root as any).findByProps({ accessibilityLabel: 'chip:Products' }).props.onPress(); });
  await flush();
  expect(getPromotions).toHaveBeenLastCalledWith({ page: 1, limit: 20, type: 'Product' });
});

it('a service-kind chip asks for Services and narrows by category serviceType', async () => {
  getPromotions.mockResolvedValue(page([item('s1', 'Service', 'c-order'), item('s2', 'Service', 'c-booking')]));
  const tree = await render();
  await act(async () => { (tree.root as any).findByProps({ accessibilityLabel: 'chip:Booking' }).props.onPress(); });
  await flush();
  expect(getPromotions).toHaveBeenLastCalledWith({ page: 1, limit: 20, type: 'Service' });
  expect(text(tree)).toContain('Item s2');
  expect(text(tree)).not.toContain('Item s1');
});

it('shows an error state when the feed fails', async () => {
  getPromotions.mockResolvedValue({ success: false, message: 'boom' });
  const tree = await render();
  expect(text(tree)).toContain("Couldn't load promotions");
});
