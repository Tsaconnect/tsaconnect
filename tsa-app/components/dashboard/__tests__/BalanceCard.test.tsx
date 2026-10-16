/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The balance card carries the controls that used to sit in Home's second
 * header row: the display-currency picker and Activity, beside the hide toggle.
 */
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/contexts/CurrencyContext', () => ({
  __esModule: true,
  useCurrency: () => ({ formatDualPrice: (v: number) => ({ primary: `$${v.toFixed(2)}`, secondary: `USD ${v.toFixed(2)}` }) }),
}));

jest.mock('@/components/currency/CurrencySelector', () => {
  const R = jest.requireActual('react');
  return { __esModule: true, default: (props: any) => R.createElement('mock-currency', props) };
});

// Host-like stand-ins so the tree can be searched by type and props.
jest.mock('@/components/ui', () => {
  const R = jest.requireActual('react');
  const host = (name: string) => (props: any) => R.createElement(name, props, props.children);
  return {
    __esModule: true,
    Card: host('mock-card'),
    Divider: () => null,
    Icon: () => null,
    IconButton: host('mock-iconbutton'),
    PressableScale: host('mock-pressable'),
    Text: host('mock-text'),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import { BalanceCard } from '../BalanceCard';

const render = (over: Partial<React.ComponentProps<typeof BalanceCard>> = {}) => {
  let tree!: ReactTestRenderer;
  act(() => {
    tree = TestRenderer.create(
      <BalanceCard totalUsdValue={9.46} dailyChange={0} isValuesHidden={false} onToggleVisibility={jest.fn()} {...over} />,
    );
  });
  return tree;
};

const hosts = (r: ReactTestRenderer, kind: string) => r.root.findAll((n) => (n as any).type === kind);
const button = (r: ReactTestRenderer, label: string) =>
  hosts(r, 'mock-iconbutton').find((b) => b.props.accessibilityLabel === label)!;

beforeEach(() => jest.clearAllMocks());

it('offers the currency picker as a compact pill', () => {
  const tree = render();
  expect(hosts(tree, 'mock-currency')[0].props.variant).toBe('header');
});

it('opens Activity from the history button', () => {
  const tree = render();
  act(() => button(tree, 'Activity').props.onPress());
  expect(router.push).toHaveBeenCalledWith('/activity');
});

it('still hides balances from the eye toggle', () => {
  const onToggleVisibility = jest.fn();
  const tree = render({ onToggleVisibility });
  act(() => button(tree, 'Hide balances').props.onPress());
  expect(onToggleVisibility).toHaveBeenCalled();
});

it('offers Pay (TSA Pay by code) in place of Wallet, which has its own tab', () => {
  const tree = render();
  const labels = hosts(tree, 'mock-text').map((t) => t.props.children);
  expect(labels).toEqual(expect.arrayContaining(['Buy', 'Services', 'Trade', 'TSA Pay']));
  expect(labels).not.toContain('Wallet');
  const pay = hosts(tree, 'mock-pressable').find((p) => p.findAll((n) => (n as any).type === 'mock-text' && n.props.children === 'TSA Pay').length > 0)!;
  act(() => pay.props.onPress());
  expect(router.push).toHaveBeenCalledWith('/pay-code');
});
