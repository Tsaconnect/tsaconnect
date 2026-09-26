/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The picker opens on what this device already knows: the coin list from the
 * last launch shows at once, and the reads it needs start when the screen
 * mounts, not when the sheet opens.
 */
const mockStore = new Map<string, string>();
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: {
    getItem: jest.fn(async (k: string) => mockStore.get(k) ?? null),
    setItem: jest.fn(async (k: string, v: string) => { mockStore.set(k, v); }),
    removeItem: jest.fn(async (k: string) => { mockStore.delete(k); }),
  },
}));

jest.mock('@/hooks/useSpotListings', () => {
  const state = { value: { listings: [] as unknown[], loading: false, error: null as string | null } };
  return { __esModule: true, __state: state, useSpotListings: () => state.value };
});

jest.mock('@/hooks/useWalletHoldings', () => {
  const value = { holdings: [], loading: false };
  return { __esModule: true, useWalletHoldings: () => value, prefetchWalletHoldings: jest.fn(async () => {}) };
});

jest.mock('@/services/lifi', () => ({
  ...jest.requireActual('@/services/lifi'),
  getLiFiAllChainTokens: jest.fn(),
  getLiFiChainsCached: jest.fn(async () => []),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { useAssetCatalog, resetAssetCatalogCache } from '../useAssetCatalog';
import * as Listings from '@/hooks/useSpotListings';
import { prefetchWalletHoldings } from '@/hooks/useWalletHoldings';
import { getLiFiAllChainTokens, type LiFiToken } from '@/services/lifi';

const listingsState = (Listings as unknown as {
  __state: { value: { listings: unknown[]; loading: boolean; error: string | null } };
}).__state;

const token = (symbol: string, chainId: number): LiFiToken => ({
  address: `0x${symbol.toLowerCase()}${chainId}`, symbol, name: symbol, decimals: 6, logoURI: '',
  chainId, coinKey: symbol, verificationStatus: 'verified',
});
const USDC = token('USDC', 8453);
const MCGP = { ...token('MCGP', 146), coinKey: undefined };
const MCGP_LISTING = { id: 'l1', baseSymbol: 'MCGP', quoteSymbol: 'USDC', chain: 'sonic', enabled: true };

const allTokens = getLiFiAllChainTokens as jest.Mock;
/** Answer the majors read with `tokens`, and a listing lookup with MCGP. */
function lifiAnswers(majors: LiFiToken[]) {
  allTokens.mockImplementation(async (p: { coinKeys?: string[] }) =>
    p.coinKeys
      ? { tokens: majors, total: majors.length, offset: 0, limit: 500 }
      : { tokens: [MCGP], total: 1, offset: 0, limit: 50 });
}
/** Reads that never answer: only what the device holds can show. */
const lifiOffline = () => allTokens.mockImplementation(() => new Promise(() => {}));

let seen: ReturnType<typeof useAssetCatalog>[] = [];
function Probe({ visible }: { visible: boolean }) {
  seen.push(useAssetCatalog({ visible, side: 'to', query: '' }));
  return null;
}
const last = () => seen[seen.length - 1];
const symbolsIn = (id: string) => last().sections.find(s => s.id === id)?.assets.map(a => a.symbol) ?? [];
const flush = async () => { for (let i = 0; i < 8; i += 1) await act(async () => { await Promise.resolve(); }); };

async function render(visible: boolean) {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { visible })); });
  await flush();
  return tree;
}

/** Open the picker once with the network up, then start a new launch: memory gone, device copy kept. */
async function earlierLaunch() {
  lifiAnswers([USDC]);
  listingsState.value = { listings: [MCGP_LISTING], loading: false, error: null };
  (await render(true)).unmount();
  resetAssetCatalogCache();
  seen = [];
}

beforeEach(() => {
  mockStore.clear();
  resetAssetCatalogCache();
  allTokens.mockReset();
  (prefetchWalletHoldings as jest.Mock).mockClear();
  listingsState.value = { listings: [], loading: false, error: null };
  seen = [];
});

it('shows the coins from the last launch at once, without waiting on the network', async () => {
  await earlierLaunch();
  lifiOffline();
  // Listings are still loading too, as on a cold start.
  listingsState.value = { listings: [], loading: true, error: null };
  const tree = await render(true);
  expect(last().loading).toBe(false);
  expect(symbolsIn('major')).toEqual(['USDC']);
  expect(symbolsIn('tsa')).toEqual(['MCGP']);
  tree.unmount();
});

it('keeps the stored On TSA coins when the listings fail to load', async () => {
  await earlierLaunch();
  lifiOffline();
  listingsState.value = { listings: [], loading: false, error: 'Could not load trading pairs' };
  const tree = await render(true);
  expect(symbolsIn('tsa')).toEqual(['MCGP']);
  tree.unmount();
});

it('drops a stored On TSA coin once the listings say it is no longer listed', async () => {
  await earlierLaunch();
  lifiAnswers([USDC]);
  listingsState.value = { listings: [], loading: false, error: null };
  const tree = await render(true);
  expect(symbolsIn('tsa')).toEqual([]);
  tree.unmount();
});

it('starts loading coins and balances when the screen mounts, before the sheet opens', async () => {
  lifiAnswers([USDC]);
  const tree = await render(false);
  expect(allTokens).toHaveBeenCalledWith(expect.objectContaining({ coinKeys: expect.any(Array) }));
  expect(prefetchWalletHoldings).toHaveBeenCalled();
  tree.unmount();
});

it('reads the coin list again when the stored copy is past its age', async () => {
  jest.useFakeTimers({ now: Date.now(), doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  try {
    await earlierLaunch();
    allTokens.mockClear();
    jest.setSystemTime(Date.now() + 13 * 60 * 60 * 1000);
    lifiAnswers([USDC, token('USDT', 8453)]);
    const tree = await render(true);
    // Served from the device at once, and refreshed behind it.
    expect(symbolsIn('major')).toEqual(['USDC']);
    expect(allTokens).toHaveBeenCalledWith(expect.objectContaining({ coinKeys: expect.any(Array) }));
    tree.unmount();

    resetAssetCatalogCache();
    lifiOffline();
    const next = await render(true);
    expect(symbolsIn('major')).toEqual(['USDC', 'USDT']);
    next.unmount();
  } finally {
    jest.useRealTimers();
  }
});

it('does not read the coin list again while the stored copy is recent', async () => {
  await earlierLaunch();
  allTokens.mockClear();
  lifiOffline();
  const tree = await render(true);
  expect(allTokens).not.toHaveBeenCalledWith(expect.objectContaining({ coinKeys: expect.any(Array) }));
  tree.unmount();
});

it('shows loading when opened after the mount-time read failed, while it retries', async () => {
  allTokens.mockRejectedValueOnce(new Error('offline'));
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { visible: false })); });
  await flush();
  lifiOffline();
  await act(async () => { tree.update(React.createElement(Probe, { visible: true })); });
  await flush();
  expect(last().loading).toBe(true);
  tree.unmount();
});
