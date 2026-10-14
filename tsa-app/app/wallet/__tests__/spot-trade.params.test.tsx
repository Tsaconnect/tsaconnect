/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * A repeated query param must cost the Spot Trade desk nothing.
 *
 * expo-router rebuilds its params object — and every array inside it — on each
 * render, so `?chain=solana&chain=sui` hands the screen a NEW array every time
 * even though the URL never changed. React compares dependency lists with
 * Object.is, so the raw value never settles: the chain memo recomputes, the
 * setup callback is rebuilt, and the mount effect re-fires on every render.
 *
 * Measured on this branch before the fix, that churn cost one extra render and
 * one extra run of `setup` rather than an unbounded spin — `setup` short-
 * circuits synchronously when the chain will not resolve, and React batches its
 * loading/error toggle back to where it started, so the cycle reaches a fixed
 * point. It sits one `await` above that `throw` away from not doing so, which
 * is why the churn is worth pinning rather than tolerating.
 *
 * The property under test is therefore relative, not a magic number: the same
 * screen, reached by a repeated param and by an equivalent scalar one, must
 * settle in the same number of renders. The cap inside the mock is only a
 * brake, so a future regression into a real loop fails the suite instead of
 * hanging it.
 *
 * Mock state lives on the mocked module rather than in a test-file variable, so
 * nothing a factory touches is out of its own scope.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(), setItem: jest.fn(), removeItem: jest.fn() },
}));

jest.mock('expo-router', () => {
  // Every render calls useLocalSearchParams exactly once, so its call count IS
  // the render count.
  const state: { renders: number; cap: number; params: Record<string, string | string[]> } = {
    renders: 0, cap: 200, params: {},
  };
  return {
    __esModule: true,
    __state: state,
    router: { back: jest.fn() },
    Stack: { Screen: () => null },
    useLocalSearchParams: () => {
      state.renders += 1;
      if (state.renders > state.cap) {
        throw new Error(`render loop: re-rendered more than ${state.cap} times without settling`);
      }
      // Rebuilt per call, arrays included — exactly what expo-router does.
      return Object.fromEntries(
        Object.entries(state.params).map(([k, v]) => [k, Array.isArray(v) ? [...v] : v]),
      );
    },
  };
});

jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: {
      background: '#000', danger: '#f00', success: '#0f0',
      textPrimary: '#fff', textSecondary: '#ccc', textMuted: '#999', accent: '#00f',
    },
    spacing: { xs: 4, sm: 8, md: 12, lg: 16, xl: 24 },
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  const label = (text: any) => react.createElement(RNText, null, text);
  return {
    __esModule: true,
    Text: (props: any) => label(props.children),
    Button: (props: any) => label(props.title),
    Spinner: (props: any) => label(props.label ?? 'spinner'),
    Icon: () => null,
    Card: box,
    Divider: box,
    PressableScale: box,
    SummaryCard: box,
    SummaryRow: (props: any) => label(`${props.label}: ${props.value}`),
    AmountInput: (props: any) => label(props.label),
  };
});

jest.mock('@/components/common/AuthorizationModal', () => ({ __esModule: true, default: () => null }));

jest.mock('@/services/wallet', () => ({
  __esModule: true,
  getActiveWallet: jest.fn(async () => '0xwallet'),
  ensureGas: jest.fn(async () => true),
  readErc20BalanceOf: jest.fn(async () => 0n),
  getProvider: jest.fn(() => ({ getBalance: async () => 0n })),
}));

// utils/spotChains reads the LiFi chain-id constants at module scope, so the
// mock has to carry the real values or the resolver stops resolving anything.
jest.mock('@/services/lifi', () => ({
  __esModule: true,
  SOLANA_LIFI_CHAIN_ID: 1151111081099710,
  SUI_LIFI_CHAIN_ID: 9270000000000000,
  BTC_LIFI_CHAIN_ID: 20000000000001,
  TRON_LIFI_CHAIN_ID: 728126428,
  SOLANA_NATIVE_ADDRESS: '11111111111111111111111111111111',
  TRON_NATIVE_ADDRESS: 'T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb',
  getLiFiToken: jest.fn(async () => null),
  getLiFiTokens: jest.fn(async () => []),
  getLiFiQuote: jest.fn(),
  settleSwapFee: jest.fn(async () => undefined),
}));

jest.mock('@/services/lifiFlow', () => ({ __esModule: true, executeLiFiSwap: jest.fn() }));
jest.mock('@/services/chains/solanaWallet', () => ({
  __esModule: true, getSolanaAddress: jest.fn(async () => null), getSolanaBalances: jest.fn(async () => []),
}));
jest.mock('@/services/chains/suiWallet', () => ({
  __esModule: true, getSuiAddress: jest.fn(async () => null), getSuiBalances: jest.fn(async () => []),
}));
jest.mock('@/services/chains/tronWallet', () => ({
  __esModule: true, getTronAddress: jest.fn(async () => null), getTronBalances: jest.fn(async () => []),
}));
jest.mock('@/services/chains/btcWallet', () => ({
  __esModule: true,
  getBtcAddress: jest.fn(async () => 'bc1qtrader'),
  getBtcBalances: jest.fn(async () => [{ symbol: 'BTC', balance: '0.5', contractAddress: '' }]),
}));
jest.mock('@/services/quoteChain', () => ({ __esModule: true, fundedQuoteChain: jest.fn(async () => 'polygon') }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as ExpoRouter from 'expo-router';
import SpotTrade from '../spot-trade';
import * as LiFi from '@/services/lifi';

const routerState = (ExpoRouter as unknown as {
  __state: { renders: number; cap: number; params: Record<string, string | string[]> };
}).__state;

/** Mounts the screen on one URL and reports what it took to settle. */
async function mount(params: Record<string, string | string[]>) {
  routerState.params = params;
  routerState.renders = 0;
  let tree: ReactTestRenderer | undefined;
  await act(async () => {
    tree = TestRenderer.create(React.createElement(SpotTrade));
  });
  // Drain what the mount scheduled. A settled screen has nothing left to do
  // after a pass or two; a churning one keeps refilling the queue.
  for (let i = 0; i < 10; i += 1) {
    await act(async () => { await Promise.resolve(); });
  }
  return { renders: routerState.renders, text: JSON.stringify(tree!.toJSON()) };
}

describe('SpotTrade route params', () => {
  it('costs no more renders for a repeated chain param than for a scalar one', async () => {
    // Both URLs land on the same unsupported-network card, so any difference in
    // renders is the param's SHAPE churning a dependency and nothing else.
    const scalar = await mount({ side: 'buy', base: 'USDC', quote: 'USDT', chain: 'notachain' });
    const repeated = await mount({ side: 'buy', base: 'USDC', quote: 'USDT', chain: ['solana', 'sui'] });

    expect(scalar.text).toContain('not supported on this device');
    expect(repeated.text).toContain('not supported on this device');
    expect(repeated.renders).toBe(scalar.renders);
  });

  it('keeps Bitcoin on its own wording when a BTC pair arrives with no quote chain', async () => {
    const btc = await mount({ side: 'buy', base: 'BTC', quote: 'USDT', chain: 'btc' });

    expect(btc.text).toContain('another network');
  });

  it('survives repeated symbol params, which used to crash on .toUpperCase()', async () => {
    const { text } = await mount({
      side: ['buy', 'sell'], base: ['USDC', 'ETH'], quote: 'USDT', chain: 'btc',
    });

    expect(text).toContain('another network');
  });

  describe('cross-chain pair', () => {
    const getLiFiToken = LiFi.getLiFiToken as jest.Mock;
    beforeEach(() => {
      getLiFiToken.mockImplementation(async (chainId: number, symbol: string) => (
        { address: symbol === 'BTC' ? 'bitcoin' : '0xusdt', symbol, decimals: symbol === 'BTC' ? 8 : 18, chainId }
      ));
    });
    afterEach(() => getLiFiToken.mockImplementation(async () => null));

    it('opens a BTC pair on the form with each leg on its own chain', async () => {
      const { text } = await mount({ side: 'buy', base: 'BTC', quote: 'USDT', chain: 'btc', quoteChain: 'bsc' });

      expect(text).toContain('Amount to spend');
      expect(text).toContain('on BNB Smart Chain');
      expect(text).toContain('0.5 BTC on Bitcoin');
      // The quote leg is looked up on BSC, the base leg on Bitcoin.
      expect(getLiFiToken).toHaveBeenCalledWith(56, 'USDT');
      expect(getLiFiToken).toHaveBeenCalledWith(20000000000001, 'BTC');
    });

    it('puts an auto listing’s quote leg where the user holds it', async () => {
      await mount({ side: 'sell', base: 'BTC', quote: 'USDT', chain: 'btc', quoteChain: 'bsc', quoteAuto: '1' });

      expect(getLiFiToken).toHaveBeenCalledWith(137, 'USDT');
    });
  });
});
