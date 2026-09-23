/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The sell screen's swapped path, from her side: pick a coin she holds, type
 * how much, see one "~₦" figure, tap Sell once. And a sale that stopped is
 * offered back as "Finish your sale", not lost.
 *
 * The hook that runs the sale is mocked here — useSwappedSell's own tests pin
 * what happens after the tap. These pin what the screen hands it.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null), removeItem: jest.fn() },
}));

jest.mock('expo-router', () => ({
  __esModule: true,
  router: { replace: jest.fn(), back: jest.fn() },
  useLocalSearchParams: () => ({}),
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

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  const label = (text: any) => react.createElement(RNText, null, text);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) =>
      react.createElement(View, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress, disabled: props.disabled },
        label(props.title)),
    Spinner: () => null,
    Card: box,
    SummaryCard: box,
    SummaryRow: (props: any) => label(`${props.label}: ${props.value}`),
    CopyableField: (props: any) => label(props.value),
    SelectRow: (props: any) =>
      react.createElement(View, { accessibilityLabel: `row:${props.label}`, onPress: props.onPress },
        label(`${props.label}: ${props.value ?? props.placeholder ?? ''}`)),
    AmountInput: (props: any) =>
      react.createElement(View, { accessibilityLabel: `amount:${props.label}`, onChangeText: props.onChangeText },
        label(props.label), label(props.helper ?? '')),
    Input: (props: any) =>
      react.createElement(View, { accessibilityLabel: `input:${props.label}`, onChangeText: props.onChangeText }),
    PickerSheet: (props: any) =>
      props.visible
        ? react.createElement(View, { accessibilityLabel: `sheet:${props.title}`, onSelect: props.onSelect, items: props.items })
        : null,
  };
});

jest.mock('@/components/paycrest/DepositProgress', () => ({ __esModule: true, DepositProgress: () => null }));

// The picker is Task 15's, tested on its own; here it only hands back a choice.
jest.mock('@/components/assets/AssetPicker', () => {
  const react = jest.requireActual('react');
  const { View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (props: any) =>
      props.visible
        ? react.createElement(View, {
            accessibilityLabel: `picker:${props.title}:${props.holdingsOnly ? 'holdings' : 'all'}:${props.side}`,
            onSelect: props.onSelect,
          })
        : null,
  };
});

jest.mock('@/hooks/useSignableWallet', () => ({
  __esModule: true,
  useSignableWallet: () => ({ address: '0xUser', ready: true }),
}));
jest.mock('@/hooks/useCountdown', () => ({
  __esModule: true,
  useCountdown: () => ({ label: '10:00', expired: false, secs: 600 }),
}));
jest.mock('@/hooks/useRampCurrency', () => {
  const value = {
    code: 'NGN', currency: { symbol: '₦' }, setCode: () => {}, options: [{ id: 'NGN', label: 'NGN' }],
  };
  return { __esModule: true, useRampCurrency: () => value, rampCurrencyLabel: () => 'Naira' };
});
jest.mock('@/hooks/useScreenTitle', () => ({ __esModule: true, useScreenTitle: () => {} }));
jest.mock('@/hooks/useSwappedSell', () => {
  const state = {
    value: { stage: 'idle', error: undefined, order: undefined, depositTx: undefined, start: jest.fn(), resume: jest.fn() },
  };
  return { __esModule: true, __state: state, useSwappedSell: () => state.value };
});
jest.mock('@/hooks/useWalletHoldings', () => {
  const state = { value: { holdings: [] as any[], loading: false } };
  return { __esModule: true, __state: state, useWalletHoldings: () => state.value };
});

jest.mock('@/services/ngBanks', () => ({
  __esModule: true,
  resolveAccountName: jest.fn(async () => ({ ok: true, accountName: 'ADA OBI' })),
}));
jest.mock('@/services/paycrestApi', () => ({
  ...jest.requireActual('@/services/paycrestApi'),
  getQuote: jest.fn(),
  getPayoutInstitutions: jest.fn(async () => [{ code: '058', name: 'GTBank' }]),
  createSell: jest.fn(),
  cancelOrder: jest.fn(),
  claimPayment: jest.fn(),
}));
jest.mock('@/services/paycrestSellSwap', () => ({
  __esModule: true,
  getOpenSellIntent: jest.fn(async () => null),
  getSellSwapQuote: jest.fn(),
}));
jest.mock('@/services/paycrestDeposit', () => ({
  __esModule: true,
  balanceOf: () => null,
  checkFunding: () => ({ ok: true }),
  sendSponsoredPaycrestDeposit: jest.fn(),
}));
jest.mock('@/services/gasSponsorship', () => ({
  __esModule: true,
  getSponsoredRoutes: jest.fn(async () => []),
  getSponsorshipOffer: jest.fn(async () => ({ mode: 'none' })),
  isGasSponsored: () => false,
}));
jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(async () => ({ success: false })),
  normalizeWalletBalances: () => [],
  getAuthHeaders: jest.fn(async () => ({})),
}));
jest.mock('@/services/wallet', () => ({ __esModule: true, nativeGasReserve: jest.fn(async () => null) }));
jest.mock('@/services/chains/solanaWallet', () => ({ __esModule: true, getSolanaAddress: jest.fn(async () => 'SoLAddr1111') }));
jest.mock('@/services/chains/btcWallet', () => ({ __esModule: true, getBtcAddress: jest.fn(async () => null) }));
jest.mock('@/services/chains/suiWallet', () => ({ __esModule: true, getSuiAddress: jest.fn(async () => null) }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import SellScreen from '../sell';
import * as SellSwap from '@/services/paycrestSellSwap';
import * as Swapped from '@/hooks/useSwappedSell';
import * as Holdings from '@/hooks/useWalletHoldings';
import { PaycrestError } from '@/services/paycrestApi';
import { SOLANA_LIFI_CHAIN_ID } from '@/services/lifi';

const sellSwap = jest.mocked(SellSwap);
const swappedState = (Swapped as unknown as { __state: { value: any } }).__state;
const holdingsState = (Holdings as unknown as { __state: { value: { holdings: any[]; loading: boolean } } }).__state;

jest.setTimeout(30_000);

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
const text = (tree: ReactTestRenderer) => textNodes(tree.toJSON()).join(' ');
const byLabel = (tree: ReactTestRenderer, l: string) => {
  const hit = tree.root.findAll((n) => n.props.accessibilityLabel === l)[0];
  if (!hit) throw new Error(`nothing labelled "${l}"`);
  return hit;
};
const flush = async () => {
  for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); });
};

const BNB = { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', name: 'BNB', decimals: 18, logoURI: '' };
const quote = {
  quote: { transactionRequest: {} }, costBps: 10, gasBps: 2, feeBps: 38, usdcAmount: '9.9', fiatEstimate: '15000', rate: '1515',
};

async function mount() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<SellScreen />); });
  await flush();
  return tree;
}

async function pick(tree: ReactTestRenderer, token: object, chainId: number) {
  await act(async () => { byLabel(tree, 'row:What are you selling?').props.onPress(); });
  const picker = byLabel(tree, 'picker:Choose what to sell:holdings:from');
  await act(async () => { await picker.props.onSelect(token, chainId); });
  await flush();
}

async function fillBank(tree: ReactTestRenderer) {
  await act(async () => { byLabel(tree, 'row:Pay into').props.onPress(); });
  await act(async () => { byLabel(tree, 'sheet:Choose a destination').props.onSelect({ id: '058' }); });
  await act(async () => { byLabel(tree, 'input:Account number').props.onChangeText('0123456789'); });
  await flush();
}

beforeEach(() => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  sellSwap.getOpenSellIntent.mockResolvedValue(null);
  swappedState.value = { ...swappedState.value, stage: 'idle', error: undefined, order: undefined };
  holdingsState.value = { holdings: [{ chainId: 56, address: '', native: true, balance: 0.5 }], loading: false };
});
afterEach(() => { jest.useRealTimers(); });

it('sells a coin Paycrest cannot take in one tap, with one ~₦ figure', async () => {
  sellSwap.getSellSwapQuote.mockResolvedValue(quote as any);
  const tree = await mount();
  await pick(tree, BNB, 56);

  expect(text(tree)).toContain('You sell');
  expect(text(tree)).toContain('Balance: 0.5 BNB');
  // Where the naira goes is still asked for.
  expect(text(tree)).toContain('Pay into');

  await act(async () => { byLabel(tree, 'amount:You sell').props.onChangeText('0.016'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(sellSwap.getSellSwapQuote).toHaveBeenCalledWith({
    fromChainId: 56, fromToken: BNB.address, fromAmount: '16000000000000000',
    fromAddress: '0xUser', walletAddress: '0xUser', currency: 'NGN',
  });
  expect(text(tree)).toMatch(/~₦\s*15,000/);

  await fillBank(tree);
  const sell = byLabel(tree, 'button:Sell');
  expect(sell.props.disabled).toBe(false);
  await act(async () => { sell.props.onPress(); });
  expect(swappedState.value.start).toHaveBeenCalledWith(expect.objectContaining({
    sourceChainId: 56, amount: '0.016', walletAddress: '0xUser', sourceAddress: '0xUser',
    bank: { code: '058', account: '0123456789', name: 'ADA OBI' },
  }));
});

it('says plainly when a token cannot be sold for naira', async () => {
  sellSwap.getSellSwapQuote.mockRejectedValue(new PaycrestError('too costly', 422));
  const tree = await mount();
  await pick(tree, BNB, 56);
  await act(async () => { byLabel(tree, 'amount:You sell').props.onChangeText('1'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(text(tree)).toContain("This token can't be sold for naira right now");
});

it('keeps a stablecoin Paycrest takes on the direct sale', async () => {
  const tree = await mount();
  await pick(tree, { ...BNB, symbol: 'USDC', address: '0xusdc' }, 8453);
  expect(text(tree)).not.toContain('You sell');
  expect(text(tree)).toContain('You receive');
});

it('quotes a Solana coin from her Solana address, and asks for SOL when she has none', async () => {
  holdingsState.value = { holdings: [], loading: false };
  sellSwap.getSellSwapQuote.mockResolvedValue(quote as any);
  const tree = await mount();
  await pick(tree, { ...BNB, symbol: 'BONK', address: 'BonkMint111', decimals: 5 }, SOLANA_LIFI_CHAIN_ID);
  await act(async () => { byLabel(tree, 'amount:You sell').props.onChangeText('100'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(sellSwap.getSellSwapQuote).toHaveBeenCalledWith(expect.objectContaining({
    fromAddress: 'SoLAddr1111', walletAddress: '0xUser',
  }));
  expect(text(tree)).toContain('You need a little SOL to send this');
  await fillBank(tree);
  expect(byLabel(tree, 'button:Sell').props.disabled).toBe(true);
});

it('offers to finish a sale that stopped part-way', async () => {
  const intent = { id: 'i1', status: 'swapped', swapTxHash: '0xs' };
  sellSwap.getOpenSellIntent.mockResolvedValue(intent as any);
  const tree = await mount();
  expect(text(tree)).toContain("You have a sale that didn't finish");
  await act(async () => { byLabel(tree, 'button:Finish your sale').props.onPress(); });
  expect(swappedState.value.resume).toHaveBeenCalledWith(intent, '0xUser');
});
