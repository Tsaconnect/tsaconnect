/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * The tab's half of the Spot→All Swap hand-off.
 *
 * openSpotTrade writes four params into the URL and All Swap reads four props;
 * nothing but this screen connects the two, so a renamed or dropped param
 * would leave both of their test suites green and the feature dead. This
 * mounts the tab and reads what All Swap was actually handed.
 */
// Reached transitively: the tab imports singleParam, which sits in the same
// module as the LiFi chain table.
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(), setItem: jest.fn(), removeItem: jest.fn() },
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
  }),
}));

jest.mock('@/components/ui', () => ({ __esModule: true, SegmentedControl: () => null }));
jest.mock('@/components/otc/OtcListingList', () => ({ __esModule: true, default: () => null }));
jest.mock('@/components/spot/SpotList', () => ({ __esModule: true, default: () => null }));

jest.mock('@/screens/all-swap', () => {
  // Every props object it was ever handed, not just the last: whether the
  // preselection is stable is a question about the sequence.
  const state: { seen: any[] } = { seen: [] };
  return {
    __esModule: true,
    __state: state,
    default: (props: any) => { state.seen.push(props); return null; },
  };
});

jest.mock('expo-router', () => {
  const state: { params: Record<string, string | string[]> } = { params: {} };
  return {
    __esModule: true,
    __state: state,
    // Rebuilt per call, arrays included — exactly what expo-router does.
    useLocalSearchParams: () => Object.fromEntries(
      Object.entries(state.params).map(([k, v]) => [k, Array.isArray(v) ? [...v] : v]),
    ),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as ExpoRouter from 'expo-router';
import * as AllSwap from '@/screens/all-swap';
import EasySwap from '../easyswap';

const routerState = (ExpoRouter as unknown as {
  __state: { params: Record<string, string | string[]> };
}).__state;
const allSwap = (AllSwap as unknown as { __state: { seen: any[] } }).__state;
const lastProps = () => allSwap.seen[allSwap.seen.length - 1];

async function mount(params: Record<string, string | string[]>) {
  routerState.params = params;
  allSwap.seen = [];
  let tree: ReactTestRenderer | undefined;
  await act(async () => { tree = TestRenderer.create(React.createElement(EasySwap)); });
  await act(async () => { await Promise.resolve(); });
  return tree!;
}

describe('Easyswap tab', () => {
  it('hands All Swap both legs of a preselected pair', async () => {
    await mount({
      tab: 'allswap', fromSymbol: 'USDT', fromChain: 'bsc', toSymbol: 'BTC', toChain: 'btc',
    });

    expect(lastProps().preselect).toEqual({
      fromSymbol: 'USDT', fromChain: 'bsc', toSymbol: 'BTC', toChain: 'btc',
    });
  });

  it('hands All Swap the funded leg, and drops one it cannot read', async () => {
    await mount({ tab: 'allswap', fromSymbol: 'USDT', fromChain: '', toSymbol: 'BTC', toChain: 'btc', fundedLeg: 'from' });
    expect(lastProps().preselect.fundedLeg).toBe('from');

    await mount({ tab: 'allswap', fromSymbol: 'USDT', toSymbol: 'BTC', toChain: 'btc', fundedLeg: 'sideways' });
    expect(lastProps().preselect.fundedLeg).toBeUndefined();
  });

  it('hands All Swap nothing when the tab is opened on its own', async () => {
    await mount({ tab: 'allswap' });

    expect(lastProps().preselect).toBeUndefined();
  });

  it('keeps the preselection object stable across renders', async () => {
    // A new object each render restarts All Swap's token lookups, which is how
    // a preselection turns into a request loop.
    const tree = await mount({ tab: 'allswap', fromSymbol: 'USDT', fromChain: 'bsc' });
    // Re-render the tab on the SAME url, which is what every keystroke in the
    // swap form does. expo-router rebuilds its params object each time.
    await act(async () => { tree.update(React.createElement(EasySwap)); });

    // Assert there was a second render at all, or the comparison below has
    // nothing to say.
    expect(allSwap.seen.length).toBeGreaterThan(1);
    const references = new Set(allSwap.seen.map(p => p.preselect));
    expect(references.size).toBe(1);
  });
});
