/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The order screen on a sale whose deposit this phone may already have sent —
 * the place a lost answer, an app kill after the broadcast, or a failed claim
 * leaves her. Paycrest still reads awaiting_deposit for a while, so the order
 * alone would ask for the deposit again and offer to cancel under it. The
 * order's hold (services/depositGuard) says otherwise, and the screen honours
 * it: no address, no Cancel, an honest notice. A terminal order lifts it.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

const mockParams: { value: Record<string, string> } = { value: { id: 'o1' } };
jest.mock('expo-router', () => ({
  __esModule: true,
  router: { replace: jest.fn(), back: jest.fn() },
  useLocalSearchParams: () => mockParams.value,
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
      react.createElement(View, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, label(props.title)),
    Spinner: () => null,
    // Each step as `label:state`, so a test can read where the stepper says things are.
    Stepper: (props: any) =>
      react.createElement(View, null, props.steps.map((st: any) => react.createElement(RNText, { key: st.key }, `${st.label}:${st.state}`))),
    Icon: () => null,
    Card: box,
    SummaryCard: box,
    SummaryRow: (props: any) => label(`${props.label}: ${props.value}`),
    CopyableField: (props: any) => label(props.value),
  };
});
jest.mock('@/components/paycrest/DepositProgress', () => ({ __esModule: true, DepositProgress: () => null }));
jest.mock('@/components/disputes/DisputeSection', () => ({ __esModule: true, DisputeSection: () => null }));
jest.mock('@/hooks/useCountdown', () => ({
  __esModule: true,
  useCountdown: () => ({ label: '10:00', expired: false, secs: 600 }),
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

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import AsyncStorage from '@react-native-async-storage/async-storage';
import OrderScreen from '../order/[id]';
import * as RampApi from '@/services/rampApi';
import type { PaycrestOrder } from '@/services/paycrestApi';
import { DEPOSIT_OUTCOME_UNKNOWN } from '@/services/paycrestDeposit';
import { depositHoldKey, readHold, writeHold } from '@/services/payHold';

const ramp = jest.mocked(RampApi);
const HOLD = depositHoldKey('o1');
const DEPOSIT_ADDRESS = '0x2222222222222222222222222222222222222222';
const order = {
  id: 'o1', direction: 'sell', status: 'awaiting_deposit', reference: 'R1', currency: 'NGN', amount: '50000',
  chain: 'base', token: 'USDC', tokenAmount: '36.42', walletAddress: '0xuser', depositAddress: DEPOSIT_ADDRESS,
  createdAt: '2026-09-25T10:00:00Z',
} as PaycrestOrder;

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
const text = (tree: ReactTestRenderer) => textNodes(tree.toJSON()).join(' ');
const cancels = (tree: ReactTestRenderer) =>
  tree.root.findAll((n) => typeof (n as { type?: unknown }).type === 'string' && n.props.accessibilityLabel === 'button:Cancel order');

const mounted: ReactTestRenderer[] = [];
async function mount() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<OrderScreen />); });
  for (let i = 0; i < 8; i += 1) await act(async () => { await new Promise((r) => setImmediate(r)); });
  mounted.push(tree);
  return tree;
}

// The screen polls on an interval: fake the clock, and unmount so nothing outlives a test.
afterEach(async () => {
  await act(async () => { mounted.splice(0).forEach((t) => t.unmount()); });
  jest.useRealTimers();
});

beforeEach(async () => {
  jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] });
  jest.clearAllMocks();
  await AsyncStorage.clear();
  mockParams.value = { id: 'o1' };
  ramp.getRampOrder.mockResolvedValue(order);
});

it('with nothing held, asks for the deposit and offers Cancel, as before', async () => {
  const tree = await mount();
  expect(text(tree)).toContain(DEPOSIT_ADDRESS);
  expect(cancels(tree)).toHaveLength(1);
  expect(text(tree)).not.toContain(DEPOSIT_OUTCOME_UNKNOWN);
});

it('reopened after a kill mid-send or a failed claim (hold active): no address, no Cancel, an honest notice', async () => {
  await writeHold(HOLD, 'o1');
  const tree = await mount();
  expect(text(tree)).not.toContain(DEPOSIT_ADDRESS);
  expect(cancels(tree)).toHaveLength(0);
  expect(text(tree)).toContain(DEPOSIT_OUTCOME_UNKNOWN);
  expect(text(tree)).toContain('Confirming 36.42 USDC from your wallet.');
});

it('a hold that cannot be read counts as held', async () => {
  await AsyncStorage.setItem(HOLD, 'not json');
  const tree = await mount();
  expect(text(tree)).not.toContain(DEPOSIT_ADDRESS);
  expect(cancels(tree)).toHaveLength(0);
});

it('sent here with an unknown outcome and no hold (a relay that may be on-chain): the same', async () => {
  mockParams.value = { id: 'o1', unconfirmed: '1' };
  const tree = await mount();
  expect(text(tree)).not.toContain(DEPOSIT_ADDRESS);
  expect(cancels(tree)).toHaveLength(0);
  expect(text(tree)).toContain(DEPOSIT_OUTCOME_UNKNOWN);
});

it('a deposit just sent from here offers no Cancel either', async () => {
  mockParams.value = { id: 'o1', sentTx: '0xdeposit' };
  const tree = await mount();
  expect(cancels(tree)).toHaveLength(0);
});

it('a terminal order lifts the hold', async () => {
  await writeHold(HOLD, 'o1');
  ramp.getRampOrder.mockResolvedValue({ ...order, status: 'settled' });
  await mount();
  expect(await readHold(HOLD)).toBeNull();
});

it('a live order keeps it', async () => {
  await writeHold(HOLD, 'o1');
  ramp.getRampOrder.mockResolvedValue({ ...order, status: 'pending' });
  await mount();
  expect((await readHold(HOLD))?.intentId).toBe('o1');
});

describe('progress and details', () => {
  it('a held deposit is being confirmed, never drawn as sent', async () => {
    await writeHold(HOLD, 'o1');
    const tree = await mount();
    expect(text(tree)).toContain('Crypto sent:active');
    expect(text(tree)).toContain('Received by the provider:pending');
  });

  it('a deposit this phone sent, with its hash, is sent — the provider is next', async () => {
    mockParams.value = { id: 'o1', sentTx: `0x${'ab'.repeat(32)}` };
    const tree = await mount();
    expect(text(tree)).toContain('Crypto sent:done');
    expect(text(tree)).toContain('Received by the provider:active');
  });

  it('"You paid" only once the provider has seen the transfer — never on a buy that expired unpaid', async () => {
    const buy = { ...order, direction: 'buy', payAmount: '50250.00' } as PaycrestOrder;
    ramp.getRampOrder.mockResolvedValue({ ...buy, status: 'expired' });
    let tree = await mount();
    expect(text(tree)).not.toContain('You paid');
    await act(async () => { mounted.splice(0).forEach((t) => t.unmount()); });
    ramp.getRampOrder.mockResolvedValue({ ...buy, status: 'settled' });
    tree = await mount();
    expect(text(tree)).toContain('You paid');
    expect(text(tree)).toContain('₦50,250.00');
  });

  it('a settled sale: naira headline, rate per token, readable times, every step done', async () => {
    ramp.getRampOrder.mockResolvedValue({
      ...order, status: 'settled', amount: '1000.00', rate: '1347.67',
      settledAt: '2026-09-25T17:08:40.763369+02:00',
    });
    const tree = await mount();
    const shown = text(tree);
    expect(shown).toContain('₦1,000.00');
    expect(shown).toContain('₦1,347.67 per USDC');
    expect(shown).toContain('Paid to your bank:done');
    expect(shown).not.toMatch(/:(active|pending|failed)/);
    // Never the raw ISO string.
    expect(shown).not.toContain('T17:08');
    expect(shown).toContain('2026');
  });

  it('an expired order ends its steps there', async () => {
    ramp.getRampOrder.mockResolvedValue({ ...order, status: 'expired' });
    const tree = await mount();
    expect(text(tree)).toContain('Expired — no deposit arrived:failed');
    expect(text(tree)).not.toContain('Paid to your bank');
  });
});

// A converted purchase's conversion record (ITC-) is paid through its
// purchase, never into this order: no pay-in card, no transfer instruction,
// no Cancel — even while the record still reads awaiting_payment.
it('an ITC- conversion record is conversion-only: no pay-in UI and no Cancel', async () => {
  ramp.getRampOrder.mockResolvedValue({
    ...order, direction: 'buy', status: 'awaiting_payment', reference: 'ITC-7c1f', routeKind: 'converted',
    holder: 'treasury', requestedToken: 'USDT', requestedChain: 'optimism', payAmount: '10000.00',
    payAccountNumber: '', payAccountName: '', payInstitution: '',
  } as PaycrestOrder);
  const tree = await mount();
  expect(text(tree)).not.toContain('Pay into');
  expect(text(tree)).not.toContain('to the account below');
  expect(cancels(tree)).toHaveLength(0);
  expect(text(tree)).toContain('This conversion starts once your purchase is paid.');
});
