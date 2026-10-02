/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The sell screen's Send, from her side: the stablecoin deposit is sent once.
 *
 * Paycrest reports a deposit only a while after it lands, so an order that
 * still reads awaiting_deposit does not mean nothing left her wallet — after a
 * lost answer, an app kill after the broadcast, or a failed claim. What these
 * pin: a self-paid send is held under its order before it is broadcast
 * (services/depositGuard, run for real here with the real hold store); only a
 * refusal the server answered lifts it; and while it holds, nothing here sends,
 * shows where to send, or cancels.
 *
 * Only the send itself is stubbed — standing in for the real one, it runs
 * beforeBroadcast right before its "broadcast".
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

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
        label(props.label)),
    Input: (props: any) =>
      react.createElement(View, { accessibilityLabel: `input:${props.label}`, onChangeText: props.onChangeText }),
    PickerSheet: (props: any) =>
      props.visible
        ? react.createElement(View, { accessibilityLabel: `sheet:${props.title}`, onSelect: props.onSelect, items: props.items })
        : null,
  };
});

jest.mock('@/components/paycrest/DepositProgress', () => ({ __esModule: true, DepositProgress: () => null }));
jest.mock('@/components/paycrest/SwappedSellForm', () => ({
  __esModule: true, SWAPPED_STAGE_LABELS: {}, SwappedSellForm: () => null, SwappedSellStatus: () => null,
}));
jest.mock('@/components/assets/AssetPicker', () => ({ __esModule: true, default: () => null }));

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
    } as any,
  };
  return { __esModule: true, __state: state, useSwappedSell: () => state.value, canCancelSale: () => false };
});

jest.mock('@/services/ngBanks', () => ({
  __esModule: true,
  resolveAccountName: jest.fn(async () => ({ ok: true, accountName: 'ADA OBI' })),
}));
jest.mock('@/services/paycrestApi', () => ({
  ...jest.requireActual('@/services/paycrestApi'),
  getPayoutInstitutions: jest.fn(async () => [{ code: '058', name: 'GTBank' }]),
}));
jest.mock('@/services/rampApi', () => ({
  ...jest.requireActual('@/services/rampApi'),
  getRampQuote: jest.fn(),
  createRampSell: jest.fn(),
  createRampBuy: jest.fn(),
  getRampOrder: jest.fn(),
  cancelRampOrder: jest.fn(),
  claimRampPayment: jest.fn(async () => ({})),
  listRampOrders: jest.fn(async () => []),
}));
jest.mock('@/services/paycrestSellSwap', () => ({
  __esModule: true,
  getSwappedSellState: jest.fn(async () => ({ enabled: false, intent: null })),
}));
// The real deposit module, bar the send: the guard (services/depositGuard) and
// the outcome-unknown error it keys on are what run.
jest.mock('@/services/paycrestDeposit', () => ({
  ...jest.requireActual('@/services/paycrestDeposit'),
  __esModule: true,
  sendSponsoredPaycrestDeposit: jest.fn(),
}));
jest.mock('@/services/gasSponsorship', () => ({
  __esModule: true,
  getSponsoredRoutes: jest.fn(async () => []),
  getSponsorshipOffer: jest.fn(async () => ({ mode: 'none' })),
  isGasSponsored: () => false,
  SPONSORSHIP_IN_PROGRESS: 'sponsorship_in_progress',
}));
jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(async () => ({ success: false })),
  normalizeWalletBalances: () => [],
  getAuthHeaders: jest.fn(async () => ({})),
}));
jest.mock('@/services/wallet', () => ({ __esModule: true }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { Alert } from 'react-native';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import SellScreen from '../sell';
import * as Swapped from '@/hooks/useSwappedSell';
import * as RampApi from '@/services/rampApi';
import * as Deposit from '@/services/paycrestDeposit';
import { PaycrestError, type PaycrestOrder } from '@/services/paycrestApi';
import { DEPOSIT_OUTCOME_UNKNOWN, DepositOutcomeUnknownError } from '@/services/paycrestDeposit';
import { depositHoldKey, readHold, writeHold } from '@/services/payHold';

const ramp = jest.mocked(RampApi);
const send = jest.mocked(Deposit.sendSponsoredPaycrestDeposit);
const swappedState = (Swapped as unknown as { __state: { value: any } }).__state;

jest.setTimeout(30_000);

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
const text = (tree: ReactTestRenderer) => textNodes(tree.toJSON()).join(' ');
/** Host nodes only: each stubbed control is one of them. */
const all = (tree: ReactTestRenderer, l: string) =>
  tree.root.findAll((n) => typeof (n as { type?: unknown }).type === 'string' && n.props.accessibilityLabel === l);
const byLabel = (tree: ReactTestRenderer, l: string) => {
  const hit = all(tree, l)[0];
  if (!hit) throw new Error(`nothing labelled "${l}"`);
  return hit;
};
const flush = async () => {
  for (let i = 0; i < 8; i += 1) await act(async () => { await new Promise((r) => setImmediate(r)); });
};

const HOLD = depositHoldKey('o1');
const SEND = 'button:Send 36.42 USDC';
const order = {
  id: 'o1', direction: 'sell', status: 'awaiting_deposit', reference: 'R1', currency: 'NGN', amount: '50000',
  chain: 'base', token: 'USDC', tokenAmount: '36.42', walletAddress: '0xuser',
  depositAddress: '0x2222222222222222222222222222222222222222', createdAt: '2026-09-25T10:00:00Z',
} as PaycrestOrder;

type SendOpts = { beforeBroadcast?: () => Promise<void> };
/** The real send runs beforeBroadcast right before a self-paid broadcast — so does this stand-in. */
const broadcastThen = (outcome: () => Promise<string>) =>
  async (_o: unknown, _w: unknown, _n: unknown, opts?: SendOpts) => { await opts?.beforeBroadcast?.(); return outcome(); };

async function mount() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<SellScreen />); });
  await flush();
  return tree;
}

/** A direct sale, created from the form: amount → route → bank → Continue. */
async function openDirect() {
  ramp.getRampQuote.mockResolvedValue({
    venues: [{ chain: 'base', token: 'USDC', rate: '1373', tokenAmount: 36.42 }],
    best: { chain: 'base', token: 'USDC', rate: '1373', tokenAmount: 36.42 },
  } as any);
  ramp.createRampSell.mockResolvedValue(order);
  const tree = await mount();
  await act(async () => { byLabel(tree, 'amount:You receive').props.onChangeText('50000'); });
  await act(async () => { jest.advanceTimersByTime(500); });
  await flush();
  await act(async () => { byLabel(tree, 'row:Pay into').props.onPress(); });
  await act(async () => { byLabel(tree, 'sheet:Choose a destination').props.onSelect({ id: '058' }); });
  await act(async () => { byLabel(tree, 'input:Account number').props.onChangeText('0123456789'); });
  await flush();
  await act(async () => { await byLabel(tree, 'button:Continue').props.onPress(); });
  await flush();
  return tree;
}

/** Reopened on an order: a swapped sale that stopped at its deposit drops into this view. */
async function reopenOn(o: PaycrestOrder) {
  swappedState.value = { ...swappedState.value, stage: 'failed', order: o };
  return mount();
}

async function tapSend(tree: ReactTestRenderer) {
  await act(async () => { await byLabel(tree, SEND).props.onPress(); });
  await flush();
}

const offersNoWayToSend = (tree: ReactTestRenderer) => {
  expect(all(tree, SEND)).toHaveLength(0);
  expect(all(tree, 'button:Cancel this sale')).toHaveLength(0);
  expect(all(tree, 'button:Send from another wallet')).toHaveLength(0);
  expect(text(tree)).not.toContain(order.depositAddress);
  expect(text(tree)).toContain(DEPOSIT_OUTCOME_UNKNOWN);
};

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
  swappedState.value = { ...swappedState.value, stage: 'idle', error: undefined, order: undefined };
  ramp.getRampOrder.mockResolvedValue(order);
  ramp.claimRampPayment.mockResolvedValue({} as PaycrestOrder);
});
afterEach(() => { jest.useRealTimers(); });

it('a clean send is held under its order before it goes out, claimed, and followed on the order screen', async () => {
  send.mockImplementation(broadcastThen(async () => {
    expect((await readHold(HOLD))?.intentId).toBe('o1');
    return '0xdeposit';
  }));
  const tree = await openDirect();
  expect(byLabel(tree, SEND).props.disabled).toBe(false);
  expect(all(tree, 'button:Cancel this sale')).toHaveLength(1);

  await tapSend(tree);
  expect(ramp.getRampOrder).toHaveBeenCalledWith('o1');
  expect(send).toHaveBeenCalledTimes(1);
  expect(ramp.claimRampPayment).toHaveBeenCalledWith('o1');
  expect(router.replace).toHaveBeenCalledWith('/wallet/paycrest/order/o1?sentTx=0xdeposit');
  // Kept until the order is terminal: a lost claim must not reopen Send.
  expect((await readHold(HOLD))?.intentId).toBe('o1');
});

it('a broadcast whose answer was lost goes to the order with the honest notice — never back to Send', async () => {
  send.mockImplementation(broadcastThen(async () => { throw new DepositOutcomeUnknownError(); }));
  const tree = await openDirect();
  await tapSend(tree);
  expect(router.replace).toHaveBeenCalledWith('/wallet/paycrest/order/o1?unconfirmed=1');
  expect(ramp.claimRampPayment).not.toHaveBeenCalled();
  expect((await readHold(HOLD))?.intentId).toBe('o1');
});

it('a relay the server says may already be on-chain is followed the same way', async () => {
  send.mockRejectedValue(new PaycrestError('Already sending. Check the order in a moment.', 409, 'sponsorship_in_progress'));
  const tree = await openDirect();
  await tapSend(tree);
  expect(router.replace).toHaveBeenCalledWith('/wallet/paycrest/order/o1?unconfirmed=1');
});

it('a refusal the server answered lifts the hold: nothing left, so Send (and Cancel) are offered again', async () => {
  send.mockImplementation(broadcastThen(async () => { throw new Error('nonce too low'); }));
  const tree = await openDirect();
  await tapSend(tree);
  expect(text(tree)).toContain('nonce too low');
  expect(await readHold(HOLD)).toBeNull();
  expect(router.replace).not.toHaveBeenCalled();
  expect(byLabel(tree, SEND).props.disabled).toBe(false);
  expect(all(tree, 'button:Cancel this sale')).toHaveLength(1);
});

it('reopened after a kill mid-send (hold on the phone, nothing reported): no Send, no Cancel, no address', async () => {
  await writeHold(HOLD, 'o1');
  const tree = await reopenOn(order);
  offersNoWayToSend(tree);
  expect(all(tree, 'button:Check on this order')).toHaveLength(1);
});

it('reopened after a good send whose claim failed: the hold still stands, so no Send', async () => {
  send.mockImplementation(broadcastThen(async () => '0xdeposit'));
  ramp.claimRampPayment.mockRejectedValue(new Error('offline'));
  const first = await openDirect();
  await tapSend(first);
  await act(async () => { first.unmount(); });

  ramp.getRampOrder.mockResolvedValue(order); // Paycrest has not reported it, and no claim landed
  const tree = await reopenOn(order);
  offersNoWayToSend(tree);
});

it('a hold that cannot be read counts as held', async () => {
  await AsyncStorage.setItem(HOLD, 'not json');
  const tree = await reopenOn(order);
  offersNoWayToSend(tree);
});

it('Send re-reads the order and sends nothing once a hold appeared meanwhile', async () => {
  const tree = await openDirect();
  await writeHold(HOLD, 'o1'); // e.g. the swapped pipeline sent it from another screen
  await tapSend(tree);
  expect(send).not.toHaveBeenCalled();
  expect(router.replace).toHaveBeenCalledWith('/wallet/paycrest/order/o1');
});

it('Send sends nothing for an order already reported or claimed', async () => {
  const tree = await openDirect();
  ramp.getRampOrder.mockResolvedValue({ ...order, paymentClaimedAt: '2026-09-25T10:01:00Z' });
  await tapSend(tree);
  expect(send).not.toHaveBeenCalled();
  expect(router.replace).toHaveBeenCalledWith('/wallet/paycrest/order/o1');
});

it('Send refuses an order opened for a different wallet', async () => {
  const tree = await openDirect();
  ramp.getRampOrder.mockResolvedValue({ ...order, walletAddress: '0xSomeoneElse' });
  await tapSend(tree);
  expect(send).not.toHaveBeenCalled();
  expect(text(tree)).toMatch(/started from a different wallet, so nothing was sent/);
});

it.each([
  ['deposit address', { depositAddress: '0x3333333333333333333333333333333333333333' }],
  ['amount', { tokenAmount: '37.00' }],
  ['token', { token: 'USDT' }],
  ['chain', { chain: 'polygon' }],
])('Send refuses an order whose %s changed since it was shown — and shows it as it is now', async (_n, change) => {
  const tree = await openDirect();
  ramp.getRampOrder.mockResolvedValue({ ...order, ...change } as PaycrestOrder);
  await tapSend(tree);
  expect(send).not.toHaveBeenCalled();
  expect(text(tree)).toMatch(/changed since you opened it, so nothing was sent/);
});

it('an order that could not be re-read is not sent — and Send stays', async () => {
  const tree = await openDirect();
  ramp.getRampOrder.mockRejectedValue(new TypeError('Network request failed'));
  await tapSend(tree);
  expect(send).not.toHaveBeenCalled();
  expect(text(tree)).toMatch(/Could not check on your order, so nothing was sent/);
  expect(all(tree, SEND)).toHaveLength(1);
});

it('while the hold is still being read, neither Send nor Cancel is offered on a guess', async () => {
  const getItem = AsyncStorage.getItem;
  (AsyncStorage as any).getItem = (key: string) => (key === HOLD ? new Promise(() => {}) : getItem(key));
  try {
    const tree = await reopenOn(order);
    expect(byLabel(tree, SEND).props.disabled).toBe(true);
    expect(all(tree, 'button:Cancel this sale')).toHaveLength(0);
    await act(async () => { tree.unmount(); });
  } finally {
    (AsyncStorage as any).getItem = getItem;
  }
});

it('with no hold, the stopped sale offers Send and Cancel as before', async () => {
  const tree = await reopenOn(order);
  expect(byLabel(tree, SEND).props.disabled).toBe(false);
  expect(all(tree, 'button:Cancel this sale')).toHaveLength(1);
  expect(text(tree)).not.toContain(DEPOSIT_OUTCOME_UNKNOWN);
});

it('Cancel still asks first, and cancels on confirm', async () => {
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  ramp.cancelRampOrder.mockResolvedValue({ ...order, status: 'cancelled' });
  const tree = await reopenOn(order);
  await act(async () => { byLabel(tree, 'button:Cancel this sale').props.onPress(); });
  const buttons = alert.mock.calls[0][2] as { text: string; onPress?: () => Promise<void> }[];
  await act(async () => { await buttons.find((b) => b.text === 'Cancel sale')!.onPress!(); });
  expect(ramp.cancelRampOrder).toHaveBeenCalledWith('o1');
  alert.mockRestore();
});
