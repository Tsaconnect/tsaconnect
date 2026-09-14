/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * Opening All Swap with a pair already chosen.
 *
 * A Spot listing names its legs by SYMBOL, and LiFi only trades addresses, so
 * nothing can be selected until the chain's token list answers. That list is
 * fetched per chain, is multi-MB, and on a phone it arrives late, arrives
 * wrong, or never arrives at all — so the interesting behaviour is not "state
 * was set", it is what the user is looking at while the answer is outstanding
 * and after it fails to come.
 *
 * These mount the real screen and read what it rendered, because every hazard
 * here lives in the ordering between an effect and a tap; a unit test of the
 * resolver would pass with all of them broken.
 *
 * Mock state lives on the mocked modules rather than in test-file variables,
 * so nothing a jest factory touches is out of its own scope.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(), setItem: jest.fn(), removeItem: jest.fn() },
}));

jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn(), back: jest.fn() } }));

// The screen reloads balances on focus; there is no navigator here to focus it,
// and balances are not what these tests are about.
jest.mock('@react-navigation/native', () => ({ __esModule: true, useFocusEffect: () => {} }));

jest.mock('react-native-safe-area-context', () => {
  const { View } = jest.requireActual('react-native');
  return { __esModule: true, SafeAreaView: View };
});

jest.mock('react-native-reanimated', () => {
  const { View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: { View },
    useSharedValue: () => ({ value: 0 }),
    useAnimatedStyle: () => ({}),
    withSpring: (v: unknown) => v,
    withTiming: (v: unknown) => v,
    withDelay: (_d: unknown, v: unknown) => v,
    withSequence: (...v: unknown[]) => v[0],
  };
});

jest.mock('@/theme', () => ({
  __esModule: true,
  // Every colour/spacing token the screen reaches for, without pinning the
  // test to the palette's key list.
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, props, props.children);
  const label = (text: any) => react.createElement(RNText, null, text);
  return {
    __esModule: true,
    Text: (props: any) => label(props.children),
    Button: (props: any) => label(props.title),
    Card: box,
    Icon: () => null,
    // Rendered as nothing so that the form's first pressable is the "you pay"
    // token pill — which is how a test can open the picker on a known leg.
    IconButton: () => null,
    PressableScale: box,
    Toggle: () => null,
    ScreenHeader: () => null,
  };
});

// Captures its props so a test can drive the picker the way a user does.
jest.mock('@/components/swap/TokenPickerModal', () => {
  const state: { props: any } = { props: null };
  return {
    __esModule: true,
    __state: state,
    default: (props: any) => { state.props = props; return null; },
  };
});

jest.mock('@/services/wallet', () => ({
  __esModule: true,
  getActiveWallet: jest.fn(async () => '0xwallet'),
  readErc20BalanceOf: jest.fn(async () => 0n),
  getProvider: jest.fn(() => ({ getBalance: async () => 0n })),
}));
jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(async () => ({ success: true, data: [] })),
  normalizeWalletBalances: jest.fn(() => []),
}));
jest.mock('@/services/chains/solanaWallet', () => ({
  __esModule: true, getSolanaAddress: jest.fn(async () => null), getSolanaBalances: jest.fn(async () => []),
}));
jest.mock('@/services/chains/btcWallet', () => ({
  __esModule: true, getBtcAddress: jest.fn(async () => 'bc1qtest'), getBtcBalances: jest.fn(async () => []),
}));
jest.mock('@/services/chains/suiWallet', () => ({
  __esModule: true, getSuiAddress: jest.fn(async () => null), getSuiBalances: jest.fn(async () => []),
}));
jest.mock('@/services/lifiFlow', () => ({ __esModule: true, executeLiFiSwap: jest.fn() }));

// The LiFi chain ids are read at module scope by utils/spotChains, so the mock
// carries the real values or nothing resolves at all. The two token lookups
// are driven per test through `__state`.
jest.mock('@/services/lifi', () => {
  const SOLANA_LIFI_CHAIN_ID = 1151111081099710;
  const SUI_LIFI_CHAIN_ID = 9270000000000000;
  const BTC_LIFI_CHAIN_ID = 20000000000001;
  const state: {
    token: (chainId: number, symbol: string) => Promise<any>;
    tokens: (chainId: number) => Promise<any[]>;
  } = { token: async () => null, tokens: async () => [] };
  return {
    __esModule: true,
    __state: state,
    SOLANA_LIFI_CHAIN_ID,
    SUI_LIFI_CHAIN_ID,
    BTC_LIFI_CHAIN_ID,
    SOLANA_NATIVE_ADDRESS: '11111111111111111111111111111111',
    isSolanaLiFiChain: (id: number) => id === SOLANA_LIFI_CHAIN_ID,
    isSuiLiFiChain: (id: number) => id === SUI_LIFI_CHAIN_ID,
    isBtcLiFiChain: (id: number) => id === BTC_LIFI_CHAIN_ID,
    isNonEVMLiFiChain: (id: number) =>
      id === SOLANA_LIFI_CHAIN_ID || id === SUI_LIFI_CHAIN_ID || id === BTC_LIFI_CHAIN_ID,
    getLiFiToken: jest.fn((chainId: number, symbol: string) => state.token(chainId, symbol)),
    getLiFiTokens: jest.fn((chainId: number) => state.tokens(chainId)),
    getLiFiAllChainTokens: jest.fn(async () => ({ tokens: [], total: 0, offset: 0, limit: 0 })),
    getLiFiQuote: jest.fn(),
    settleSwapFee: jest.fn(async () => undefined),
    getSwapDelivery: jest.fn(),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as Lifi from '@/services/lifi';
import * as Picker from '@/components/swap/TokenPickerModal';
import * as WalletApi from '@/services/walletApi';
import AllSwapScreen from '../all-swap';

const BTC_CHAIN_ID = 20000000000001;
const BSC_CHAIN_ID = 56;
const ETH_CHAIN_ID = 1;

const lifi = Lifi as unknown as {
  __state: {
    token: (chainId: number, symbol: string) => Promise<any>;
    tokens: (chainId: number) => Promise<any[]>;
  };
  getLiFiToken: jest.Mock;
  getLiFiTokens: jest.Mock;
};
const picker = Picker as unknown as { __state: { props: any } };
const walletApi = WalletApi as unknown as { getWalletBalances: jest.Mock; normalizeWalletBalances: jest.Mock };
const ARBITRUM_CHAIN_ID = 42161;

const token = (symbol: string, address: string, decimals = 18) =>
  ({ symbol, address, decimals, name: symbol, priceUSD: '1' });

const USDT = token('USDT', '0x55d398326f99059ff775485246999027b3197955');
const BTC = token('BTC', 'bitcoin', 8);
const DAI = token('DAI', '0x6b175474e89094c44da98b954eedeac495271d0f');

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

/** Every string the screen actually rendered. */
function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach(n => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}

async function flush() {
  for (let i = 0; i < 10; i += 1) {
    await act(async () => { await Promise.resolve(); });
  }
}

async function mount(props: React.ComponentProps<typeof AllSwapScreen>) {
  let tree: ReactTestRenderer | undefined;
  await act(async () => {
    tree = TestRenderer.create(React.createElement(AllSwapScreen, props));
  });
  await flush();
  return tree!;
}

const rendered = (tree: ReactTestRenderer) => textNodes(tree.toJSON());

beforeEach(() => {
  lifi.__state.token = async () => null;
  lifi.__state.tokens = async () => [];
  lifi.getLiFiToken.mockClear();
  lifi.getLiFiTokens.mockClear();
  walletApi.getWalletBalances.mockImplementation(async () => ({ success: true, data: [] }));
  walletApi.normalizeWalletBalances.mockImplementation(() => []);
  picker.__state.props = null;
});

// The first mount pays for loading react-native and ethers into the module
// registry, which on a cold run is slower than jest's default per-test budget.
jest.setTimeout(30_000);

const BTC_PAIR = { fromSymbol: 'USDT', fromChain: 'bsc', toSymbol: 'BTC', toChain: 'btc' };
const POLYGON_CHAIN_ID = 137;

describe('All Swap preselection', () => {
  it('selects a leg whose token list only arrives after mount', async () => {
    const btcList = deferred<any[]>();
    // LiFi's single-token lookup misses on Bitcoin, so this leg can only be
    // resolved from the chain's list — the slow path, and the normal one.
    lifi.__state.token = async (chainId, symbol) =>
      (chainId === BSC_CHAIN_ID && symbol === 'USDT' ? USDT : null);
    lifi.__state.tokens = (chainId) => (chainId === BTC_CHAIN_ID ? btcList.promise : Promise.resolve([]));

    const tree = await mount({ preselect: BTC_PAIR });

    // The leg that resolved is already in place; the outstanding one says so
    // without hiding the form behind it.
    expect(rendered(tree)).toContain('USDT');
    expect(rendered(tree)).toContain('Select');
    expect(rendered(tree)).toContain('Selecting this pair…');
    expect(rendered(tree)).toContain('Review Swap');

    await act(async () => { btcList.resolve([token('WBTC', '0xwbtc'), BTC]); });
    await flush();

    expect(rendered(tree)).toContain('BTC');
    expect(rendered(tree)).not.toContain('Selecting this pair…');
    expect(rendered(tree)).not.toContain('Select');
    // Both legs are on their own chain — the whole point of routing here.
    expect(rendered(tree)).toEqual(expect.arrayContaining(['BEP20', 'BTC']));
  });

  it('leaves a usable form, and stops, when a symbol never resolves', async () => {
    lifi.__state.token = async (chainId, symbol) =>
      (chainId === BSC_CHAIN_ID && symbol === 'USDT' ? USDT : null);
    lifi.__state.tokens = (chainId) => (chainId === BTC_CHAIN_ID
      ? Promise.reject(new Error('Failed to fetch token list'))
      : Promise.resolve([]));

    const tree = await mount({ preselect: BTC_PAIR });

    expect(rendered(tree)).not.toContain('Selecting this pair…');
    expect(rendered(tree)).toContain('Couldn’t preselect BTC — pick it below to continue.');
    // Usable: the resolved leg stands, the unresolved one is a picker the user
    // can open, and the form's own submit is still there.
    expect(rendered(tree)).toContain('USDT');
    expect(rendered(tree)).toContain('Select');
    expect(rendered(tree)).toContain('Review Swap');

    // And it stopped asking. A failed lookup that keeps retrying is the same
    // permanent spinner wearing a different hat.
    const callsAfterFailure = lifi.getLiFiTokens.mock.calls.length;
    await flush();
    expect(lifi.getLiFiTokens.mock.calls.length).toBe(callsAfterFailure);
  });

  it('does not overwrite a token the user picked while the lookup was in flight', async () => {
    const bscList = deferred<any[]>();
    lifi.__state.token = async () => null;
    lifi.__state.tokens = (chainId) => (chainId === BSC_CHAIN_ID ? bscList.promise : Promise.resolve([]));

    const tree = await mount({ preselect: { fromSymbol: 'USDT', fromChain: 'bsc' } });

    // Open the picker on the "you pay" leg and choose something else, exactly
    // as a user would while the list is still loading.
    const payPill = tree.root.findAll(n =>
      n.props?.accessibilityLabel === 'Select token to pay' && typeof n.props?.onPress === 'function');
    await act(async () => { payPill[0].props.onPress(); });
    // `visible` is what proves the picker actually opened: `mode` alone falls
    // back to 'from' when nothing is open, so asserting it would pass on a
    // press that did nothing.
    expect(picker.__state.props.visible).toBe(true);
    expect(picker.__state.props.mode).toBe('from');
    await act(async () => { picker.__state.props.onSelect(DAI, ETH_CHAIN_ID); });

    await act(async () => { bscList.resolve([USDT]); });
    await flush();

    expect(rendered(tree)).toContain('DAI');
    expect(rendered(tree)).not.toContain('USDT');
  });

  it('lets a newly tapped pair outrank the legs chosen for the last one', async () => {
    lifi.__state.token = async (_chainId, symbol) => (symbol === 'USDT' ? USDT : BTC);

    const tree = await mount({ preselect: { fromSymbol: 'USDT', fromChain: 'bsc' } });
    const payPill = tree.root.findAll(n =>
      n.props?.accessibilityLabel === 'Select token to pay' && typeof n.props?.onPress === 'function');
    await act(async () => { payPill[0].props.onPress(); });
    await act(async () => { picker.__state.props.onSelect(DAI, ETH_CHAIN_ID); });
    expect(rendered(tree)).toContain('DAI');

    // The user goes back to Spot and taps a different listing, which re-renders
    // this screen with new params rather than remounting it.
    await act(async () => {
      tree.update(React.createElement(AllSwapScreen, {
        preselect: { fromSymbol: 'BTC', fromChain: 'btc' },
      }));
    });
    await flush();

    expect(rendered(tree)).toContain('BTC');
    expect(rendered(tree)).not.toContain('DAI');
  });

  it('moves a funded leg onto the chain where the user holds the token', async () => {
    walletApi.normalizeWalletBalances.mockImplementation(() => [
      { chainKey: 'bsc', symbol: 'USDT', balance: '4' },
      { chainKey: 'polygon', symbol: 'USDT', balance: '250' },
    ]);
    const POLYGON_USDT = token('USDT', '0xc2132d05d31c914a87c6611c10748aeb04b58e8f', 6);
    lifi.__state.token = async (chainId, symbol) => {
      if (symbol === 'USDT') return chainId === POLYGON_CHAIN_ID ? POLYGON_USDT : USDT;
      return null;
    };
    lifi.__state.tokens = async (chainId) => (chainId === BTC_CHAIN_ID ? [BTC] : []);

    const tree = await mount({ preselect: { ...BTC_PAIR, fundedLeg: 'from' } });

    // Resolved on Polygon, never on the bsc fallback it started from.
    expect(lifi.getLiFiToken).toHaveBeenCalledWith(POLYGON_CHAIN_ID, 'USDT');
    expect(lifi.getLiFiToken).not.toHaveBeenCalledWith(BSC_CHAIN_ID, 'USDT');
    expect(rendered(tree)).toEqual(expect.arrayContaining(['USDT', 'BTC']));
    expect(rendered(tree).some(t => t.startsWith('Couldn’t preselect'))).toBe(false);
  });

  it('keeps a funded leg on its fallback chain when the user holds none', async () => {
    lifi.__state.token = async (chainId, symbol) => (chainId === BSC_CHAIN_ID && symbol === 'USDT' ? USDT : null);
    lifi.__state.tokens = async (chainId) => (chainId === BTC_CHAIN_ID ? [BTC] : []);

    const tree = await mount({ preselect: { ...BTC_PAIR, fundedLeg: 'from' } });

    expect(lifi.getLiFiToken).toHaveBeenCalledWith(BSC_CHAIN_ID, 'USDT');
    expect(rendered(tree)).toEqual(expect.arrayContaining(['USDT', 'BTC', 'BEP20']));
  });

  it('delivers a sold token to the chain where the user holds that token', async () => {
    walletApi.normalizeWalletBalances.mockImplementation(() => [
      { chainKey: 'polygon', symbol: 'USDT', balance: '90' },
    ]);
    lifi.__state.token = async (_chainId, symbol) => (symbol === 'USDT' ? USDT : null);
    lifi.__state.tokens = async (chainId) => (chainId === BTC_CHAIN_ID ? [BTC] : []);

    await mount({ preselect: { fromSymbol: 'BTC', fromChain: 'btc', toSymbol: 'USDT', toChain: 'bsc', fundedLeg: 'to' } });

    expect(lifi.getLiFiToken).toHaveBeenCalledWith(POLYGON_CHAIN_ID, 'USDT');
    expect(lifi.getLiFiToken).not.toHaveBeenCalledWith(BSC_CHAIN_ID, 'USDT');
  });

  it('keeps a token the user picked while the balance read was in flight', async () => {
    const read = deferred<any>();
    walletApi.getWalletBalances.mockImplementation(() => read.promise);
    walletApi.normalizeWalletBalances.mockImplementation(() => [
      { chainKey: 'polygon', symbol: 'USDT', balance: '90' },
    ]);
    lifi.__state.token = async (_chainId, symbol) => (symbol === 'USDT' ? USDT : null);
    lifi.__state.tokens = async (chainId) => (chainId === BTC_CHAIN_ID ? [BTC] : []);

    const tree = await mount({ preselect: { ...BTC_PAIR, fundedLeg: 'from' } });
    const payPill = tree.root.findAll(n =>
      n.props?.accessibilityLabel === 'Select token to pay' && typeof n.props?.onPress === 'function');
    await act(async () => { payPill[0].props.onPress(); });
    await act(async () => { picker.__state.props.onSelect(DAI, ETH_CHAIN_ID); });

    await act(async () => { read.resolve({ success: true, data: {} }); });
    await flush();

    expect(rendered(tree)).toContain('DAI');
    expect(rendered(tree)).not.toContain('USDT');
    expect(lifi.getLiFiToken).not.toHaveBeenCalledWith(POLYGON_CHAIN_ID, 'USDT');
  });

  it('finds USDT on Arbitrum, where LiFi lists it as USDT0', async () => {
    const USDT0 = token('USDT0', '0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9', 6);
    lifi.__state.token = async (chainId, symbol) => (chainId === ARBITRUM_CHAIN_ID && symbol === 'USDT0' ? USDT0 : null);
    lifi.__state.tokens = async (chainId) => (chainId === BTC_CHAIN_ID ? [BTC] : []);

    const tree = await mount({ preselect: { ...BTC_PAIR, fromChain: 'arbitrum' } });

    expect(rendered(tree)).toContain('USDT0');
    expect(rendered(tree).some(t => t.startsWith('Couldn’t preselect'))).toBe(false);
  });

  it('does not alias USDT to USDT0 on chains where they are different tokens', async () => {
    lifi.__state.token = async (chainId, symbol) => (chainId === BSC_CHAIN_ID && symbol === 'USDT0' ? token('USDT0', '0xother') : null);
    lifi.__state.tokens = async (chainId) => (chainId === BTC_CHAIN_ID ? [BTC] : []);

    const tree = await mount({ preselect: BTC_PAIR });

    expect(lifi.getLiFiToken).not.toHaveBeenCalledWith(BSC_CHAIN_ID, 'USDT0');
    expect(rendered(tree)).toContain('Couldn’t preselect USDT — pick it below to continue.');
  });

  it('starts on the defaults, and asks LiFi nothing, when no pair is given', async () => {
    const tree = await mount({});

    expect(lifi.getLiFiToken).not.toHaveBeenCalled();
    // The two default chains, and nothing else, got a token list.
    expect(lifi.getLiFiTokens.mock.calls.map(c => c[0]).sort()).toEqual([ETH_CHAIN_ID, BSC_CHAIN_ID]);
    expect(rendered(tree).filter(t => t === 'Select')).toHaveLength(2);
    expect(rendered(tree)).not.toContain('Selecting this pair…');
    expect(rendered(tree).some(t => t.startsWith('Couldn’t preselect'))).toBe(false);
  });
});

/**
 * Selection while no leg is open.
 *
 * The picker's handlers used to test only for 'from' and let everything else
 * fall through to 'to', so an absent mode wrote the leg the user was not on.
 * Nothing in the UI could reach that — the modal renders invisible — but it
 * stays mounted across every close, and preselection now drives the same
 * state from a timer the user does not control, so the handlers are reachable
 * with no leg open and that is the state these pin down.
 *
 * The mocked picker here is exactly that shape: it keeps its last props
 * forever, so calling `onSelect` off a closed picker is the real hazard, not
 * an artefact. `visible` is asserted first every time — `mode` carries a
 * 'from' fallback for the closed render and would pass either way.
 */
describe('All Swap picker with no leg open', () => {
  it('ignores a selection made while the picker is closed', async () => {
    // The "to" leg never resolves, which is what leaves a notice on screen for
    // a stray selection to wipe.
    lifi.__state.token = async (chainId, symbol) =>
      (chainId === BSC_CHAIN_ID && symbol === 'USDT' ? USDT : null);
    lifi.__state.tokens = () => Promise.resolve([]);

    const tree = await mount({ preselect: BTC_PAIR });

    const notice = 'Couldn\u2019t preselect BTC \u2014 pick it below to continue.';
    expect(rendered(tree)).toContain(notice);

    const amountInput = () => tree.root.findAll(n =>
      n.props?.placeholder === '0.00' && typeof n.props?.onChangeText === 'function')[0];
    await act(async () => { amountInput().props.onChangeText('1.5'); });
    expect(amountInput().props.value).toBe('1.5');

    // Nothing was tapped, so nothing is open.
    expect(picker.__state.props.visible).toBe(false);
    await act(async () => { picker.__state.props.onSelect(DAI, ETH_CHAIN_ID); });
    await flush();

    // The "to" leg is untouched, and so is everything the handler clears on
    // its way past: the notice that leg still needs, and the typed amount.
    expect(rendered(tree)).not.toContain('DAI');
    expect(rendered(tree)).toContain('Select');
    expect(rendered(tree)).toContain(notice);
    expect(amountInput().props.value).toBe('1.5');
    // The leg that did resolve is not collateral damage either.
    expect(rendered(tree)).toContain('USDT');
  });

  it('ignores a chain change made while the picker is closed', async () => {
    lifi.__state.token = async () => null;
    lifi.__state.tokens = () => Promise.resolve([]);

    const tree = await mount({});
    expect(rendered(tree).filter(t => t === 'Select')).toHaveLength(2);

    lifi.getLiFiTokens.mockClear();
    expect(picker.__state.props.visible).toBe(false);
    await act(async () => { picker.__state.props.onChainChange(POLYGON_CHAIN_ID); });
    await flush();

    // A moved chain is observable through the list it makes the screen fetch,
    // which is the same fetch that would have offered the user the wrong
    // chain's tokens on a leg they never opened.
    expect(lifi.getLiFiTokens.mock.calls.map(c => c[0])).not.toContain(POLYGON_CHAIN_ID);
    expect(lifi.getLiFiTokens).not.toHaveBeenCalled();
  });
});

// A new pair arriving must invalidate everything priced against the old one.
//
// The tab stays mounted, so tapping a listing elsewhere pushes new params into
// a screen that may already hold a quote. Picking a token by hand clears the
// amount and quote; preselection did not. validate() only checks that A quote
// exists, never that it matches these legs, so Review stayed enabled and the
// swap handler would sign the PREVIOUS pair's route under the new pair's name.
describe('All Swap preselection against a live quote', () => {
  it('clears a quote priced for the pair it replaces', async () => {
    lifi.__state.token = async (chainId: number, symbol: string) =>
      ({ chainId, symbol, address: `0x${symbol}`, decimals: 18, name: symbol });

    const tree = await mount({ preselect: { fromSymbol: 'USDT', fromChain: 'bsc', toSymbol: 'DAI', toChain: 'ethereum' } });
    expect(rendered(tree)).toContain('USDT');

    // The user types an amount, priced against THIS pair.
    // Found by placeholder because the input carries no label — and asserted
    // to exist, since a selector matching nothing would make this test pass
    // whatever the code does. That is precisely how the first draft of this
    // test was vacuous.
    const amountInputs = tree.root.findAllByProps({ placeholder: '0.00' });
    expect(amountInputs.length).toBeGreaterThan(0);
    await act(async () => { amountInputs[0].props.onChangeText('1.5'); });
    await flush();
    // Read the input's own value: the amount lives in a prop, not a text node,
    // so asserting over rendered text would never see it either way.
    const amountValue = () => tree.root.findAllByProps({ placeholder: '0.00' })[0].props.value;
    expect(amountValue()).toBe('1.5');

    // Now a different listing is tapped: new params into the same instance.
    await act(async () => {
      tree.update(React.createElement(AllSwapScreen, {
        preselect: { fromSymbol: 'BTC', fromChain: 'btc', toSymbol: 'USDT', toChain: 'ethereum' },
      }));
    });
    await flush();

    // The amount priced against the old pair must not survive into the new one.
    expect(amountValue()).toBe('');
    expect(rendered(tree)).toContain('BTC');
  });
});
