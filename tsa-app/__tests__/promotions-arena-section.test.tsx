/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The Promotions Arena sub-heading on the Reward hub: a short live preview with
 * See all, and a plain empty line when nothing is on promotion.
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
  const { View, Text: RNText, Pressable } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Card: (p: any) => react.createElement(View, null, p.children),
    Text: (p: any) => react.createElement(RNText, null, p.children),
    Spinner: () => react.createElement(View, { accessibilityLabel: 'spinner' }),
    PressableScale: (p: any) => react.createElement(Pressable, { onPress: p.onPress, accessibilityLabel: p.accessibilityLabel }, p.children),
  };
});
jest.mock('@/components/marketplace/ProductCard', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (p: any) => react.createElement(RNText, { accessibilityLabel: `row:${p.product.id}` }, p.product.name),
  };
});
const mockPush = jest.fn();
jest.mock('expo-router', () => ({ __esModule: true, router: { push: (...a: any[]) => mockPush(...a) } }));
jest.mock('@/components/services/api', () => ({
  __esModule: true,
  default: { getPromotions: jest.fn() },
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import api from '@/components/services/api';
import PromotionsArenaSection from '@/components/promotion/PromotionsArenaSection';

const getPromotions = api.getPromotions as jest.Mock;
const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };
const page = (products: any[]) => ({ success: true, data: { products, pagination: { page: 1, limit: 20, total: products.length } } });
const item = (id: string) => ({ id, name: `Item ${id}`, type: 'Product', images: [] });
const text = (t: ReactTestRenderer) => JSON.stringify(t.toJSON());

async function render() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<PromotionsArenaSection />); });
  await flush();
  return tree;
}

beforeEach(() => jest.clearAllMocks());

it('previews at most three promoted products', async () => {
  getPromotions.mockResolvedValue(page(['a', 'b', 'c', 'd', 'e'].map(item)));
  const tree = await render();
  expect(text(tree)).toContain('Promotions Arena');
  expect(tree.root.findAllByProps({ accessibilityLabel: 'row:a' }).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({ accessibilityLabel: 'row:c' }).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({ accessibilityLabel: 'row:d' })).toHaveLength(0);
});

it('says so when nothing is on promotion', async () => {
  getPromotions.mockResolvedValue(page([]));
  expect(text(await render())).toContain('No promotions right now');
});

it('See all opens the full Arena', async () => {
  getPromotions.mockResolvedValue(page([item('a')]));
  const tree = await render();
  await act(async () => { tree.root.findAllByProps({ accessibilityLabel: 'See all promotions' })[0].props.onPress(); });
  expect(mockPush).toHaveBeenCalledWith('/promotions');
});
