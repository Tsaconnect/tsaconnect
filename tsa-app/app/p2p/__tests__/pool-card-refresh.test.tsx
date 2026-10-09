/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The pinned TSA Connect card is live, not a snapshot from when the screen
 * mounted (review I-5 (3)): coming back to the marketplace, or pulling to
 * refresh, asks for it again, so a card that has since become unavailable
 * stops being tappable and its rate and limits are current.
 */
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
// Every registered focus callback, so a test can "come back" to the screen.
const mockFocus = { cbs: new Set<() => unknown>() };
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn(), back: jest.fn(), replace: jest.fn(), canGoBack: () => true },
    Stack: { Screen: () => null },
    useLocalSearchParams: () => ({}),
    useFocusEffect: (cb: () => unknown) =>
      react.useEffect(() => {
        mockFocus.cbs.add(cb);
        const cleanup = cb();
        return () => {
          mockFocus.cbs.delete(cb);
          if (typeof cleanup === 'function') cleanup();
        };
      }, [cb]),
  };
});
jest.mock('@/utils/back', () => ({ __esModule: true, goBack: jest.fn() }));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  const any = (props: any) =>
    react.createElement(RNText, null, typeof props.children === 'function' ? null : props.children);
  return new Proxy({ __esModule: true }, { get: (t: any, k) => (k in t ? t[k] : any) });
});
jest.mock('@/components/currency/CurrencySelector', () => ({ __esModule: true, default: () => null }));
jest.mock('@/constants/chains', () => ({ __esModule: true, getChainByChainId: () => undefined }));
jest.mock('@/services/p2p', () => ({
  __esModule: true,
  adsApi: { list: jest.fn(async () => ({ success: true, data: [] })) },
  chainsApi: { list: jest.fn(async () => ({ success: true, data: [] })) },
  distinctAdProviders: () => [],
  formatTokenAmount: (v: string) => v,
  p2pTokenDecimals: () => 6,
}));
jest.mock('@/hooks/useUserRole', () => ({ __esModule: true, useUserRole: () => ({ isMerchant: true, loading: false }) }));
jest.mock('@/hooks/useIsPaymentMerchant', () => ({
  __esModule: true,
  useIsPaymentMerchant: () => ({ isPm: true, kind: 'pm', liquidityAvailable: false, loading: false }),
}));
jest.mock('@/hooks/useP2pCurrency', () => ({
  __esModule: true,
  useP2pCurrency: () => ({ currency: { code: 'NGN' }, setCurrency: jest.fn() }),
}));
jest.mock('@/services/liquidityApi', () => ({ __esModule: true, liquidityApi: { poolCard: jest.fn() } }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { liquidityApi, type PoolCard } from '@/services/liquidityApi';
import P2PMarketScreen from '../index';

const poolCard = liquidityApi.poolCard as jest.Mock;

const available: PoolCard = {
  enabled: true, name: 'TSA Connect', badge: 'Recommended', available: true, rate: '1361.51',
  fundingFeePercent: '1.4', minNgn: '2762.00', maxNgn: '171000.00',
  token: { symbol: 'USDT', address: '0x55d398326f99059fF775485246999027B3197955', decimals: 18 },
  chain: { id: 56, key: 'bsc', name: 'BNB Smart Chain' },
};
const unavailable: PoolCard = { ...available, available: false };

const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });
function textOf(r: ReactTestRenderer): string {
  const out: string[] = [];
  const walk = (n: unknown): void => {
    if (typeof n === 'string') out.push(n);
    else if (Array.isArray(n)) n.forEach(walk);
    else if (n && typeof n === 'object' && 'children' in n) walk((n as { children: unknown }).children);
  };
  walk(r.toJSON());
  return out.join(' ');
}

async function mount(): Promise<ReactTestRenderer> {
  let r!: ReactTestRenderer;
  await act(async () => { r = TestRenderer.create(<P2PMarketScreen />); });
  await flush();
  return r;
}

beforeEach(() => {
  mockFocus.cbs.clear();
  poolCard.mockReset();
  poolCard.mockResolvedValueOnce({ success: true, data: available });
  poolCard.mockResolvedValue({ success: true, data: unavailable });
});

it('asks for the card again when she comes back to the marketplace', async () => {
  const r = await mount();
  expect(poolCard).toHaveBeenCalledTimes(1);
  expect(textOf(r)).toContain('₦1,361.51');

  await act(async () => { mockFocus.cbs.forEach((cb) => cb()); });
  await flush();

  expect(poolCard).toHaveBeenCalledTimes(2);
  expect(textOf(r)).toContain('Unavailable right now');
  expect(textOf(r)).not.toContain('₦1,361.51');
});

it('asks for the card again on pull-to-refresh', async () => {
  const r = await mount();
  expect(poolCard).toHaveBeenCalledTimes(1);

  const scroll = r.root.findAll((n) => !!n.props.refreshControl)[0];
  await act(async () => { scroll.props.refreshControl.props.onRefresh(); });
  await flush();

  expect(poolCard).toHaveBeenCalledTimes(2);
  expect(textOf(r)).toContain('Unavailable right now');
});
