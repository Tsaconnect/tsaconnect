/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Promotions Arena: loading, empty, list and error. Products only.
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
  default: { getPromotions: jest.fn() },
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import api from '@/components/services/api';
import PromotionsArenaScreen from '@/app/(detail)/promotions';

const getPromotions = api.getPromotions as jest.Mock;
const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };

const item = (id: string) => ({ id, name: `Item ${id}`, type: 'Product', images: [] });
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

it('lists promoted items, always asks for products, and opens product detail', async () => {
  getPromotions.mockResolvedValue(page([item('p1'), item('p2')]));
  const tree = await render();
  expect(getPromotions).toHaveBeenCalledWith({ page: 1, limit: 20, type: 'Product' });
  expect(tree.root.findAllByProps({ accessibilityLabel: 'chip:All' })).toHaveLength(0);
  await act(async () => { (tree.root as any).findByProps({ accessibilityLabel: 'row:p1' }).props.onPress(); });
  expect(mockPush.mock.calls[0][0]).toContain('/product/p1');
});

it('shows an error state when the feed fails', async () => {
  getPromotions.mockResolvedValue({ success: false, message: 'boom' });
  const tree = await render();
  expect(text(tree)).toContain("Couldn't load promotions");
});
