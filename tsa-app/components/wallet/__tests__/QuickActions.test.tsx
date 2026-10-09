/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The wallet's quick actions: five tiles that must fit a 320 pt screen, the
 * fifth being TSA Pay (pay by code).
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 999 }),
  }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text: RNText, View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Icon: () => null,
    PressableScale: (props: any) => react.createElement(View, { testID: 'tile', style: props.style, onPress: props.onPress }, props.children),
    Text: (props: any) => react.createElement(RNText, { numberOfLines: props.numberOfLines }, props.children),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { QuickActions, WALLET_QUICK_ACTIONS } from '../QuickActions';

let tree: ReactTestRenderer;
const tiles = () => tree.root.findAll((n) => n.props.testID === 'tile' && typeof (n as { type?: unknown }).type === 'string');

afterEach(() => act(() => tree?.unmount()));

it('has TSA Pay as the fifth action', () => {
  expect(WALLET_QUICK_ACTIONS.map((a) => a.key)).toEqual(['fund', 'swap', 'send', 'instant-pay', 'pay-code']);
  expect(WALLET_QUICK_ACTIONS[4].label).toBe('TSA Pay');
});

// 320 − 2×16 (screen gutter) − 2×20 (card padding) = 248 pt across five tiles:
// 49.6 pt each, room for the 44 pt icon. A fixed minWidth of 56 needs 280 pt.
it('shares the row equally with no minimum width, and lets a label wrap', () => {
  const onPress = jest.fn();
  act(() => { tree = TestRenderer.create(<QuickActions onPress={onPress} />); });
  expect(tiles()).toHaveLength(5);
  for (const tile of tiles()) {
    expect(tile.props.style).toMatchObject({ flex: 1 });
    expect(tile.props.style.minWidth).toBeUndefined();
  }
  const labels = tree.root.findAll((n) => typeof (n as { type?: unknown }).type === 'string' && n.props.numberOfLines !== undefined);
  expect(labels).toHaveLength(5);
  expect(labels.every((l) => l.props.numberOfLines === 2)).toBe(true);
  act(() => { tiles()[4].props.onPress(); });
  expect(onPress).toHaveBeenCalledWith('pay-code');
});
