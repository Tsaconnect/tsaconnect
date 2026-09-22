/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The picker's job, from the user's side: a short list of coins, one tap for a
 * coin that lives on one network, a second step only when it lives on several,
 * nothing offered that cannot be used, and a clear label on anything unverified.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
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
  useCurrency: () => ({ formatPrice: (usd: number) => `₦${Math.round(usd * 1500)}` }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, props, props.children);
  return {
    __esModule: true,
    Sheet: (props: any) =>
      props.visible
        ? react.createElement(View, { accessibilityLabel: `sheet:${props.title}`, onBack: props.onBack }, props.children)
        : null,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    PressableScale: box,
    Input: (props: any) => react.createElement(View, { accessibilityLabel: props.accessibilityLabel, onChangeText: props.onChangeText }),
    Icon: () => null,
    Spinner: () => null,
  };
});

jest.mock('@/components/wallet/TokenAvatar', () => ({ __esModule: true, default: () => null }));

// Stable values, as the real hooks return state.
jest.mock('@/hooks/useSpotListings', () => {
  // MCGP on Sonic, and USDT listed on BSC only.
  const value = {
    listings: [
      { id: 'l1', baseSymbol: 'MCGP', quoteSymbol: 'USDC', chain: 'sonic', enabled: true },
      { id: 'l2', baseSymbol: 'USDT', quoteSymbol: 'USDC', chain: 'bsc', enabled: true },
    ],
  };
  return { __esModule: true, useSpotListings: () => value };
});

jest.mock('@/hooks/useWalletHoldings', () => {
  // 2.33 USDT on BSC, and an imported token LiFi's lists don't carry.
  const value = {
    holdings: [
      { chainId: 56, address: '0xusdt56', native: false, trusted: true, symbol: 'USDT', name: 'Tether', decimals: 18, balance: 2.33, usdValue: 2.33 },
      { chainId: 56, address: '0xcrepe', native: false, trusted: true, symbol: 'CREPE', name: 'Crepe', decimals: 18, balance: 5, usdValue: 3 },
      // Native S on Sonic: not a major.
      { chainId: 146, address: '', native: true, trusted: true, symbol: 'S', name: 'Sonic', decimals: 18, balance: 12, usdValue: 5 },
      // Airdropped on Sui: its metadata says "USDC", nobody vouches for it.
      { chainId: 9270000000000000, address: '0xbad1::usdc::USDC', native: false, trusted: false, symbol: 'USDC', name: 'USDC', decimals: 6, balance: 500, usdValue: 0 },
    ],
  };
  return { __esModule: true, useWalletHoldings: () => value };
});

jest.mock('@/services/lifi', () => {
  const actual = jest.requireActual('@/services/lifi');
  const t = (symbol: string, chainId: number, extra: object = {}) => ({
    address: `0x${symbol.toLowerCase()}${chainId}`, symbol, name: symbol, decimals: 6, logoURI: '',
    chainId, coinKey: symbol, verificationStatus: 'verified', ...extra,
  });
  const majors = [
    t('USDC', 8453), t('USDC', 56), t('USDC', 324 /* zkSync: not signable */),
    t('USDT', 8453), { ...t('USDT', 56), address: '0xusdt56' },
    t('BNB', 56, { address: '0x0000000000000000000000000000000000000000' }),
  ];
  const listing = t('MCGP', 146);
  // What an API without the coinKeys filter sends back: alphabetical junk.
  const oldApiJunk = [t('SHOP', 1, { coinKey: undefined }), t('$COOL', 8453, { verificationStatus: 'unverified' })];
  const state = { oldApi: false };
  const search = [
    t('SHOP', 1, { coinKey: undefined, verificationStatus: 'unverified', name: 'Shopping.io' }),
  ];
  return {
    ...actual,
    __state: state,
    getLiFiAllChainTokens: jest.fn(async (p: any) => {
      if (p.coinKeys) {
        const tokens = state.oldApi ? oldApiJunk : majors;
        return { tokens, total: tokens.length, offset: 0, limit: 500 };
      }
      if (p.chains) {
        const hit = p.q === 'MCGP' ? [listing] : majors.filter(x => x.symbol === p.q && p.chains.includes(x.chainId));
        return { tokens: hit, total: hit.length, offset: 0, limit: 50 };
      }
      return { tokens: search.filter(x => x.symbol.toLowerCase().includes(String(p.q).toLowerCase())), total: 1, offset: 0, limit: 100 };
    }),
    getLiFiChains: jest.fn(async () => [{ id: 324, name: 'zkSync', key: 'zks', coin: 'ETH' }]),
    getLiFiChainsCached: jest.fn(async () => [{ id: 324, name: 'zkSync', key: 'zks', coin: 'ETH' }]),
    getLiFiToken: jest.fn(async () => null),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestInstance, type ReactTestRenderer } from 'react-test-renderer';
import AssetPicker from '../AssetPicker';
import { resetAssetCatalogCache } from '@/hooks/useAssetCatalog';
import * as Lifi from '@/services/lifi';

const lifiState = (Lifi as unknown as { __state: { oldApi: boolean } }).__state;

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach(n => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
const text = (tree: ReactTestRenderer) => textNodes(tree.toJSON()).join(' ');
/** The text inside one row. */
const rowText = (row: ReactTestInstance) =>
  row.findAll(n => typeof n.props.children === 'string').map(n => n.props.children as string).join(' ');
const flush = async () => { for (let i = 0; i < 8; i += 1) await act(async () => { await Promise.resolve(); }); };
const pressLabel = (tree: ReactTestRenderer, start: string) => {
  const hit = tree.root.findAll(n => typeof n.props.accessibilityLabel === 'string'
    && n.props.accessibilityLabel.startsWith(start) && typeof n.props.onPress === 'function')[0];
  if (!hit) throw new Error(`no pressable starting "${start}"`);
  return act(async () => { hit.props.onPress(); });
};

async function open(props: Partial<React.ComponentProps<typeof AssetPicker>> = {}) {
  const onSelect = jest.fn();
  let tree!: ReactTestRenderer;
  await act(async () => {
    tree = TestRenderer.create(
      <AssetPicker visible side="to" onSelect={onSelect} onClose={() => {}} {...props} />,
    );
  });
  await flush();
  return { tree, onSelect };
}

// The first render loads react-native cold, slower than jest's default budget.
jest.setTimeout(30_000);

beforeEach(() => { resetAssetCatalogCache(); lifiState.oldApi = false; });

it('shows On TSA, then the majors, then other coins in the wallet', async () => {
  const { tree } = await open();
  const t = text(tree);
  expect(t.indexOf('On TSA')).toBeLessThan(t.indexOf('Stablecoins & major coins'));
  expect(t.indexOf('Stablecoins & major coins')).toBeLessThan(t.indexOf('In your wallet'));
  expect(t).toContain('MCGP');
  expect(t).toContain('CREPE');
  // A balance is shown as a figure and its value, no extra wording.
  expect(t).toMatch(/2\.33\s+USDT/);
  expect(t).not.toContain('You have');
});

it('asks for the network only when a coin lives on several, recommending the one you hold', async () => {
  const { tree, onSelect } = await open({ side: 'from' });

  await pressLabel(tree, 'USDT,');
  await flush();
  expect(text(tree)).toContain('Recommended');
  // Held on BSC, so BSC comes first and is the recommended one.
  await pressLabel(tree, 'USDT on BNB');
  expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ symbol: 'USDT', chainId: 56 }), 56);
});

it('picks a one-network coin in a single tap', async () => {
  const { tree, onSelect } = await open();
  await pressLabel(tree, 'BNB,');
  expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ symbol: 'BNB' }), 56);
  expect(text(tree)).not.toContain('choose network');
});

it('never offers a network this phone cannot sign on as the paying side', async () => {
  const { tree } = await open({ side: 'from' });
  await pressLabel(tree, 'USDC,');
  await flush();
  expect(text(tree)).not.toContain('zkSync');

  const receiving = await open({ side: 'to' });
  await pressLabel(receiving.tree, 'USDC,');
  await flush();
  expect(text(receiving.tree)).toContain('zkSync');
});

it("honours a screen's own network limit", async () => {
  const { tree, onSelect } = await open({ allow: id => id === 8453 });
  // USDC is now Base-only, so one tap picks it.
  await pressLabel(tree, 'USDC,');
  expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ symbol: 'USDC' }), 8453);
});

it('labels unverified search results and warns before they are traded', async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  try {
    const { tree } = await open();
    const search = tree.root.findAll(n => n.props.accessibilityLabel === 'Search coins')[0];
    await act(async () => { search.props.onChangeText('shop'); });
    await act(async () => { jest.advanceTimersByTime(350); });
    await flush();

    const t = text(tree);
    expect(t).toContain('SHOP');
    expect(t).toContain('Unverified');
    expect(t).toContain('Anyone can create a token');
    expect(t).not.toContain('On TSA');
  } finally {
    jest.useRealTimers();
  }
});

it('never shows junk as major coins, even when the API ignored the coinKeys filter', async () => {
  lifiState.oldApi = true;
  const { tree } = await open();
  const t = text(tree);
  expect(t).not.toContain('SHOP');
  expect(t).not.toContain('$COOL');
  expect(t).not.toContain('Stablecoins & major coins');
});

it('keeps every network of a coin the admin lists on one chain', async () => {
  const { tree } = await open({ side: 'to' });
  const t = text(tree);
  // USDT is listed on BSC, so it sits under On TSA — with both of its networks.
  expect(t.indexOf('USDT')).toBeLessThan(t.indexOf('Stablecoins & major coins'));
  expect(t).toMatch(/Tether\s+·\s+2 networks/);
});

it('hands a held native coin back with its real native address', async () => {
  const { tree, onSelect } = await open({ side: 'from' });
  await pressLabel(tree, 'S,');
  expect(onSelect).toHaveBeenCalledWith(
    expect.objectContaining({ symbol: 'S', address: '0x0000000000000000000000000000000000000000' }), 146,
  );
});

it('labels a coin in the wallet that nobody vouches for, even outside search', async () => {
  const { tree } = await open({ side: 'to' });
  const fake = tree.root.findAll(n => typeof n.props.accessibilityLabel === 'string'
    && n.props.accessibilityLabel.startsWith('USDC, Sui') && typeof n.props.onPress === 'function')[0];
  expect(rowText(fake)).toContain('Unverified');
  expect(text(tree)).toContain('Anyone can create a token');
  // The real, verified USDC row carries no such label.
  const real = tree.root.findAll(n => typeof n.props.accessibilityLabel === 'string'
    && n.props.accessibilityLabel.startsWith('USDC, 3 networks') && typeof n.props.onPress === 'function')[0];
  expect(rowText(real)).not.toContain('Unverified');
});
