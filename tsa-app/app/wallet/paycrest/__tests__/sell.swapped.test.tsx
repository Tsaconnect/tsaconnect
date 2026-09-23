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
      react.createElement(View, {
        accessibilityLabel: `amount:${props.label}`, onChangeText: props.onChangeText, onMax: props.onMax, value: props.value,
      },
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
    value: {
      stage: 'idle', error: undefined, order: undefined, depositTx: undefined,
      start: jest.fn(), resume: jest.fn(), cancel: jest.fn(async () => {}),
    },
  };
  const actual = jest.requireActual('@/hooks/useSwappedSell');
  return { __esModule: true, __state: state, useSwappedSell: () => state.value, canCancelSale: actual.canCancelSale };
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
  getSwappedSellState: jest.fn(async () => ({ enabled: true, intent: null })),
  getSellSwapQuote: jest.fn(),
}));
jest.mock('@/services/paycrestDeposit', () => ({
  __esModule: true,
  balanceOf: () => null,
  checkFunding: () => ({ ok: true }),
  sendSponsoredPaycrestDeposit: jest.fn(),
  CHAIN_LABELS: { base: 'Base', polygon: 'Polygon', bsc: 'BNB Smart Chain', arbitrum: 'Arbitrum', ethereum: 'Ethereum' },
  fundingMessage: () => null,
  nativeBalanceFor: () => null,
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
import { Alert } from 'react-native';
import SellScreen from '../sell';
import * as SellSwap from '@/services/paycrestSellSwap';
import * as Swapped from '@/hooks/useSwappedSell';
import * as Holdings from '@/hooks/useWalletHoldings';
import * as Wallet from '@/services/wallet';
import * as PaycrestApi from '@/services/paycrestApi';
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
  sellSwap.getSwappedSellState.mockResolvedValue({ enabled: true, intent: null });
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

// Once a quote is live, it — not the stablecoin shortlist — says which picks
// are direct, and a direct pick selects its own route.
it('a pick a live route takes selects that route; any other coin is swapped', async () => {
  const venue = (chain: string, token: string) => ({ chain, token, rate: '1500', tokenAmount: 10 });
  jest.mocked(PaycrestApi.getQuote).mockResolvedValue({
    venues: [venue('base', 'USDC'), venue('bsc', 'USDT')], best: venue('base', 'USDC'),
  } as any);
  const tree = await mount();
  await act(async () => { byLabel(tree, 'amount:You receive').props.onChangeText('15000'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  expect(text(tree)).toContain('You send: USDC on Base');

  await pick(tree, { ...BNB, symbol: 'USDT', address: '0xusdt' }, 56);
  expect(text(tree)).toContain('You send: USDT on BNB Smart Chain');
  expect(text(tree)).not.toContain('You sell');

  await pick(tree, { ...BNB, symbol: 'USDC', address: '0xusdc' }, 10); // a stablecoin no live route takes
  expect(text(tree)).toContain('You sell');
});

it('offers to finish a sale that stopped part-way', async () => {
  const intent = { id: 'i1', status: 'swapped', swapTxHash: '0xs' };
  sellSwap.getSwappedSellState.mockResolvedValue({ enabled: true, intent: intent as any });
  const tree = await mount();
  expect(text(tree)).toContain("You have a sale that didn't finish");
  await act(async () => { byLabel(tree, 'button:Finish your sale').props.onPress(); });
  expect(swappedState.value.resume).toHaveBeenCalledWith(intent, '0xUser');
});

// ─── Whole-branch fixes ────────────────────────────────────────────────────

describe('swapped sells switched off on the server (RAMP_CONVERSION_SELL unset)', () => {
  it('shows no swapped-sell UI at all — the screen is the stablecoin sale it was', async () => {
    sellSwap.getSwappedSellState.mockResolvedValue({ enabled: false, intent: null });
    const tree = await mount();
    expect(tree.root.findAll((n) => n.props.accessibilityLabel === 'row:What are you selling?')).toHaveLength(0);
    expect(tree.root.findAll((n) => typeof n.props.accessibilityLabel === 'string'
      && n.props.accessibilityLabel.startsWith('picker:'))).toHaveLength(0);
    // The direct sale is untouched.
    expect(text(tree)).toContain('You receive');
    expect(text(tree)).not.toContain("You have a sale that didn't finish");
    expect(sellSwap.getSellSwapQuote).not.toHaveBeenCalled();
  });

  it('offers the picker once the server says swapped sells are on', async () => {
    const tree = await mount();
    expect(() => byLabel(tree, 'row:What are you selling?')).not.toThrow();
  });

  it('keeps it hidden while the answer is unknown (no reply yet, or a network failure)', async () => {
    sellSwap.getSwappedSellState.mockRejectedValue(new TypeError('Network request failed'));
    const tree = await mount();
    expect(tree.root.findAll((n) => n.props.accessibilityLabel === 'row:What are you selling?')).toHaveLength(0);
  });
});

// Solana joins Bitcoin and Sui: nothing can check a route she would sign
// there, so the server refuses it — and the screen says so at the pick.
it.each([
  ['BTC', 'bitcoin', 8, 20000000000001, /BTC from Bitcoin isn't supported yet/],
  ['SUI', '0x2::sui::SUI', 9, 9270000000000000, /SUI from Sui isn't supported yet/],
  ['BONK', 'BonkMint111', 5, SOLANA_LIFI_CHAIN_ID, /BONK from Solana isn't supported yet/],
])('refuses a %s pick up front, rather than failing its quote', async (symbol, address, decimals, chainId, message) => {
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  const tree = await mount();
  await pick(tree, { ...BNB, symbol, address, decimals }, chainId);
  expect(alert).toHaveBeenCalledWith('Not supported yet', expect.stringMatching(message));
  expect(text(tree)).not.toContain('You sell');
  expect(sellSwap.getSellSwapQuote).not.toHaveBeenCalled();
  alert.mockRestore();
});

describe('cancelling a stuck sale from Finish your sale', () => {
  const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();

  it('is offered once the sale has sat 10 minutes before its payout, and cancels on confirm', async () => {
    const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
    const intent = { id: 'i1', status: 'swapped', swapTxHash: '0xs', updatedAt: ago(15) };
    sellSwap.getSwappedSellState.mockResolvedValue({ enabled: true, intent: intent as any });
    const tree = await mount();
    await act(async () => { byLabel(tree, 'button:Cancel this sale').props.onPress(); });
    const buttons = alert.mock.calls[0][2] as { text: string; onPress?: () => Promise<void> }[];
    await act(async () => { await buttons.find((b) => b.text === 'Cancel sale')!.onPress!(); });
    await flush();
    expect(swappedState.value.cancel).toHaveBeenCalledWith(intent);
    alert.mockRestore();
  });

  it('is not offered while the sale may still be going through', async () => {
    sellSwap.getSwappedSellState.mockResolvedValue({
      enabled: true, intent: { id: 'i1', status: 'swapped', swapTxHash: '0xs', updatedAt: ago(2) } as any,
    });
    const tree = await mount();
    expect(text(tree)).toContain('Finish your sale');
    expect(text(tree)).not.toContain('Cancel this sale');
  });
});

it('MAX on a sale of the chain\'s own coin holds its gas back', async () => {
  jest.mocked(Wallet.nativeGasReserve).mockResolvedValue(10n ** 16n); // 0.01 BNB for the swap's gas
  const tree = await mount();
  await pick(tree, BNB, 56);
  await act(async () => { await byLabel(tree, 'amount:You sell').props.onMax(); });
  await flush();
  expect(byLabel(tree, 'amount:You sell').props.value).toBe('0.49');
});

it('a new pick starts from an empty amount', async () => {
  const tree = await mount();
  await pick(tree, BNB, 56);
  await act(async () => { byLabel(tree, 'amount:You sell').props.onChangeText('0.2'); });
  await pick(tree, BNB, 56);
  expect(byLabel(tree, 'amount:You sell').props.value).toBe('');
});

// A sale resumed from "Finish your sale" runs with nothing picked: its
// progress still shows, and the direct sale cannot be started over it.
it('shows a running sale on the direct form too', async () => {
  swappedState.value = { ...swappedState.value, stage: 'waiting' };
  const tree = await mount();
  expect(text(tree)).toContain('Waiting for your swap to arrive');
  expect(byLabel(tree, 'button:Continue').props.disabled).toBe(true);
});
