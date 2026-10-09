/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A TSA Pay merchant's refunds in the app. What this pins: her refunds list in her mode with
 * the ones waiting for her first; a refund opens on its own screen. On that screen she sends
 * a refund only from the wallet the server names (never another), sees her balance of the
 * chosen token and of gas — with a way to get USDT/USDC when she has none — authorizes
 * before anything is signed, and then watches it until it is confirmed or fails. While it
 * waits she can cancel it instead.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
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
const mockParams: { id?: string; mode?: string } = {};
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn(), replace: jest.fn(), back: jest.fn() },
    useLocalSearchParams: () => mockParams,
    useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]),
  };
});
const mockToast = { success: jest.fn(), error: jest.fn(), info: jest.fn() };
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const { getStatusVisual } = jest.requireActual('@/components/ui/statusMap');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, { onPress: props.onPress, accessibilityLabel: props.accessibilityLabel }, props.children),
    // A disabled button can't be pressed: it has no onPress, and says so.
    Button: (props: any) => react.createElement(RNText, {
      accessibilityLabel: props.accessibilityLabel ?? `button:${props.title}`,
      onPress: props.disabled ? undefined : props.onPress,
      accessibilityState: { disabled: !!props.disabled },
    }, props.title),
    ErrorState: (props: any) => react.createElement(RNText, null, props.title),
    EmptyState: (props: any) => react.createElement(RNText, null, props.title),
    Spinner: () => null,
    Icon: () => null,
    Card: box,
    AccentCard: box,
    SummaryCard: box,
    SummaryRow: (props: any) => (typeof props.value === 'string'
      ? react.createElement(RNText, null, `${props.label}: ${props.value}`)
      : react.createElement(View, null, react.createElement(RNText, null, `${props.label}:`), props.value)),
    RadioGroup: (props: any) => react.createElement(View, null, props.options.map((o: any) =>
      react.createElement(RNText, { key: o.value, accessibilityLabel: `radio:${o.value}`, onPress: () => props.onChange(o.value) }, o.label))),
    getStatusVisual,
    StatusPill: (props: any) => react.createElement(RNText, null, getStatusVisual(props.kind, props.status).label),
    PressableScale: (props: any) => react.createElement(View, {
      accessibilityLabel: props.accessibilityLabel, testID: props.testID, onPress: props.onPress,
    }, props.children),
    Sheet: (props: any) => (props.visible ? react.createElement(View, null, props.children) : null),
    useToast: () => mockToast,
  };
});
jest.mock('@/components/common/AuthorizationModal', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (props: any) => (props.visible
      ? react.createElement(Text, { accessibilityLabel: 'authorize', onPress: props.onAuthorized }, props.title, ' ', props.description)
      : null),
  };
});
jest.mock('@/services/payMerchantApi', () => {
  const actual = jest.requireActual('@/services/payMerchantApi');
  return {
    __esModule: true,
    payLinkModeOf: actual.payLinkModeOf,
    payLinkModeParam: actual.payLinkModeParam,
    getOwnMerchant: jest.fn(),
    listPayRefunds: jest.fn(),
    getPayRefund: jest.fn(),
    cancelPayRefund: jest.fn(),
  };
});
jest.mock('@/services/payRefundSend', () => ({ __esModule: true, sendPayRefund: jest.fn() }));
jest.mock('@/services/wallet', () => ({ __esModule: true, getSignableWallet: jest.fn(), nativeBalance: jest.fn() }));
jest.mock('@/services/walletApi', () => ({ __esModule: true, getWalletBalances: jest.fn(), linkActiveWallet: jest.fn() }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import {
  cancelPayRefund, getOwnMerchant, getPayRefund, listPayRefunds,
  type OwnMerchant, type PayRefund, type PayRefundDetail, type PayRefundOption, type PayRefundRow,
} from '@/services/payMerchantApi';
import { sendPayRefund } from '@/services/payRefundSend';
import { getSignableWallet, nativeBalance } from '@/services/wallet';
import { getWalletBalances, linkActiveWallet } from '@/services/walletApi';
import { unprovenWalletMessage, wrongWalletMessage } from '@/utils/payRefunds';
import PayRefunds from '../refunds/index';
import RefundDetailScreen from '../refunds/[id]';

const merchant = {
  id: 'm1', businessName: 'Sky Air', status: 'active', liveEnabled: true, liveAllowed: true, version: 7, settlementMode: 'fiat',
} as OwnMerchant;
const PAYER = '0x2222222222222222222222222222222222222222';
const row = (over: Partial<PayRefundRow> = {}): PayRefundRow => ({
  id: 'r1', paymentId: 'p1', mode: 'live', status: 'confirmed', amount: '12.50', cap: '15.00', toAddress: PAYER,
  requestedVia: 'web', createdAt: '2026-10-08T10:00:00Z', customerName: 'Ada Obi', description: 'LOS–ABV 12 Oct', ...over,
});

jest.setTimeout(30_000);

const flush = () => act(async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); });
const byLabel = (tree: ReactTestRenderer, label: string) => {
  const found = tree.root.findAll((n) => n.props.accessibilityLabel === label);
  if (!found.length) throw new Error(`nothing labelled ${label}`);
  return found[0];
};
function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
const text = (t: ReactTestRenderer) => textNodes(t.toJSON()).join(' ');
let tree: ReactTestRenderer;
async function mount(el: React.ReactElement) {
  await act(async () => { tree = TestRenderer.create(el); });
  await flush();
  return tree;
}
const press = async (label: string) => {
  await act(async () => { byLabel(tree, label).props.onPress(); });
  await flush();
};

beforeEach(() => {
  jest.useFakeTimers({ doNotFake: ['setImmediate', 'nextTick', 'queueMicrotask'] });
  jest.clearAllMocks();
  for (const k of Object.keys(mockParams) as (keyof typeof mockParams)[]) delete mockParams[k];
  jest.mocked(getOwnMerchant).mockResolvedValue(merchant);
});
afterEach(() => { act(() => tree?.unmount()); jest.useRealTimers(); });

describe('Refunds list', () => {
  it('lists her refunds in her mode, the ones waiting for her first, each opening its screen', async () => {
    jest.mocked(listPayRefunds).mockImplementation(async (_m, _p, _l, status) => ({
      refunds: status === 'requested'
        ? [row({ id: 'r2', status: 'requested', customerName: 'Tolu', createdAt: '2026-10-07T10:00:00Z' })]
        : [row({ id: 'r1' }), row({ id: 'r2', status: 'requested', customerName: 'Tolu', createdAt: '2026-10-07T10:00:00Z' })],
      total: 2, page: 1, limit: 50,
    }));
    await mount(<PayRefunds />);
    expect(listPayRefunds).toHaveBeenCalledWith('live', 1, 50);
    expect(listPayRefunds).toHaveBeenCalledWith('live', 1, 50, 'requested');
    // A mocked View appears twice in the tree (component + host): keep each testID once, in order.
    const ids = [...new Set(tree.root.findAll((n) => typeof n.props.testID === 'string' && n.props.testID.startsWith('refund-'))
      .map((n) => n.props.testID as string))];
    expect(ids).toEqual(['refund-r2', 'refund-r1']);
    expect(text(tree)).toContain('Waiting for you');
    expect(text(tree)).toContain('12.50 USD');
    await act(async () => { tree.root.findAll((n) => n.props.testID === 'refund-r2')[0].props.onPress(); });
    expect(router.push).toHaveBeenCalledWith('/merchants/refunds/r2');
  });

  it('lists the mode named in the route', async () => {
    mockParams.mode = 'test';
    jest.mocked(listPayRefunds).mockResolvedValue({ refunds: [], total: 0, page: 1, limit: 50 });
    await mount(<PayRefunds />);
    expect(listPayRefunds).toHaveBeenCalledWith('test', 1, 50);
    expect(text(tree)).toContain('No refunds yet');
  });

  it('says when her refunds could not be read', async () => {
    jest.mocked(listPayRefunds).mockRejectedValue(new Error('offline'));
    await mount(<PayRefunds />);
    expect(text(tree)).toContain('Could not load your refunds');
  });

  it('never hides an older requested refund behind a full newest page', async () => {
    jest.mocked(listPayRefunds).mockImplementation(async (_m, _p, _l, status) => ({
      refunds: status === 'requested'
        ? [row({ id: 'old', status: 'requested', createdAt: '2026-01-01T10:00:00Z' })]
        : [row({ id: 'new', createdAt: '2026-10-08T10:00:00Z' })],
      total: 99, page: 1, limit: 50,
    }));
    await mount(<PayRefunds />);
    const ids = [...new Set(tree.root.findAll((n) => typeof n.props.testID === 'string' && n.props.testID.startsWith('refund-'))
      .map((n) => n.props.testID as string))];
    expect(ids).toEqual(['refund-old', 'refund-new']);
  });

  it('shows why the network has not taken a sent refund', async () => {
    jest.mocked(listPayRefunds).mockResolvedValue({
      refunds: [row({ id: 's1', status: 'submitted', lastBroadcastError: 'insufficient funds for gas' })],
      total: 1, page: 1, limit: 50,
    });
    await mount(<PayRefunds />);
    expect(text(tree)).toContain("Couldn't broadcast: insufficient funds for gas");
  });
});

describe('Refund screen', () => {
  // A refund id is a server UUID; the route param is checked before any request.
  const RID = '3f6c2a1e-8b4d-4c2a-9e1f-5a7b9c0d1e2f';
  const OWNER = '0x1111111111111111111111111111111111111111';
  const USDT_BSC = '0x55d398326f99059fF775485246999027B3197955';
  const HASH = `0x${'ab'.repeat(32)}`;
  const bscUsdt: PayRefundOption = {
    chainId: 56, chainKey: 'bsc', chainName: 'BNB Smart Chain', tokenSymbol: 'USDT', tokenAddress: USDT_BSC, decimals: 18,
    amountBaseUnits: '12500000000000000000', amountDisplay: '12.5', isDefault: true,
  };
  const baseUsdc: PayRefundOption = {
    chainId: 8453, chainKey: 'base', chainName: 'Base', tokenSymbol: 'USDC', tokenAddress: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913',
    decimals: 6, amountBaseUnits: '12500000', amountDisplay: '12.5', isDefault: false,
  };
  const refund = (over: Partial<PayRefund> = {}): PayRefund => ({
    id: RID, paymentId: 'p1', mode: 'live', status: 'requested', amount: '12.50', cap: '15.00', reason: 'Flight cancelled',
    toAddress: PAYER, requestedVia: 'web', createdAt: '2026-10-08T10:00:00Z', ...over,
  });
  const detail = (over: Partial<PayRefund> = {}): PayRefundDetail => ({
    refund: refund(over),
    payment: { id: 'p1', code: 'TSA-7K3QX-M2P9D', description: 'LOS–ABV 12 Oct', customerTotal: 2_000_000, succeededAt: '2026-10-07T09:00:00Z', merchantName: 'Sky Air' },
    payerFirstName: 'Ada',
    fromAddress: OWNER,
    options: [baseUsdc, bscUsdt],
  });
  const sent = (over: Partial<PayRefund> = {}) => detail({ status: 'submitted', chainId: 56, tokenSymbol: 'USDT', txHash: HASH, ...over });
  const balances = (usdt: string, bnb = '0.01') => ({
    success: true,
    data: [
      { symbol: 'USDT', name: 'USDT', balance: usdt, usdValue: '0', contractAddress: USDT_BSC, decimals: 18 },
      { symbol: 'BNB', name: 'BNB', balance: bnb, usdValue: '0', contractAddress: '', decimals: 18, native: true },
    ],
  });
  const disabled = (label: string) => byLabel(tree, label).props.accessibilityState.disabled;

  beforeEach(() => {
    mockParams.id = RID;
    jest.mocked(getPayRefund).mockResolvedValue(detail());
    jest.mocked(getSignableWallet).mockResolvedValue(OWNER);
    jest.mocked(nativeBalance).mockResolvedValue(10n ** 16n);
    jest.mocked(getWalletBalances).mockResolvedValue(balances('40') as never);
  });

  it('shows who and what, the customer\'s own network first, and sends only after she authorizes', async () => {
    jest.mocked(sendPayRefund).mockResolvedValue(refund({ status: 'submitted', chainId: 56, tokenSymbol: 'USDT', txHash: HASH }));
    await mount(<RefundDetailScreen />);
    const shown = text(tree);
    expect(shown).toContain('12.50 USD');
    expect(shown).toContain('To: Ada');
    expect(shown).toContain('For: LOS–ABV 12 Oct');
    expect(shown).toContain('Payment: TSA-7K3QX-M2P9D');
    expect(shown).toContain('Customer wallet: 0x2222…2222');
    expect(shown).toContain('Sending: 12.5 USDT on BNB Smart Chain');
    expect(shown).toContain('You have 40 USDT on BNB Smart Chain');
    expect(getPayRefund).toHaveBeenCalledWith(RID);
    expect(getWalletBalances).toHaveBeenCalledWith(56, OWNER, 'mainnet');
    expect(nativeBalance).toHaveBeenCalledWith('bsc', OWNER, 'mainnet');

    await press('button:Send refund');
    expect(sendPayRefund).not.toHaveBeenCalled();
    // What she authorizes names the amount, token, network and the customer's wallet.
    expect(text(tree)).toContain('Send 12.5 USDT on BNB Smart Chain to Ada (0x2222…2222).');
    jest.mocked(getPayRefund).mockResolvedValue(sent());
    await press('authorize');
    expect(sendPayRefund).toHaveBeenCalledWith(RID, bscUsdt, PAYER, OWNER);
    expect(mockToast.success).toHaveBeenCalledWith('Refund sent. Waiting for the network to confirm it.');
    expect(text(tree)).toContain('Sending');
    expect(() => byLabel(tree, 'button:Send refund')).toThrow();
  });

  it('reads the balance again when she picks another network', async () => {
    await mount(<RefundDetailScreen />);
    await press('radio:8453:USDC');
    expect(getWalletBalances).toHaveBeenLastCalledWith(8453, OWNER, 'mainnet');
    expect(text(tree)).toContain('Sending: 12.5 USDC on Base');
  });

  it('a route id that is not a refund id is not found, and nothing is requested', async () => {
    mockParams.id = '../wallet/x';
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain('Refund not found');
    expect(getPayRefund).not.toHaveBeenCalled();
    expect(getSignableWallet).not.toHaveBeenCalled();
  });

  // Review Focus 1.
  it('names the wallet to switch to, and cannot send from another one', async () => {
    jest.mocked(getSignableWallet).mockResolvedValue('0x9999999999999999999999999999999999999999');
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain(wrongWalletMessage(OWNER));
    expect(disabled('button:Send refund')).toBe(true);
  });

  // Plans review P2: she may switch wallets in Manage Wallet and come back.
  it('reads the device wallet again on each refresh, so switching to the right one enables Send', async () => {
    jest.mocked(getSignableWallet).mockResolvedValue('0x9999999999999999999999999999999999999999');
    await mount(<RefundDetailScreen />);
    expect(disabled('button:Send refund')).toBe(true);
    jest.mocked(getSignableWallet).mockResolvedValue(OWNER);
    await act(async () => { jest.advanceTimersByTime(15_000); });
    await flush();
    expect(text(tree)).not.toContain(wrongWalletMessage(OWNER));
    expect(disabled('button:Send refund')).toBe(false);
  });

  // Task 6 review: the key is stored under the device's spelling of the address.
  it('signs with the device wallet\'s own spelling of the address the server names', async () => {
    const SERVER = '0xAbCdEf0000000000000000000000000000000001';
    jest.mocked(getPayRefund).mockResolvedValue({ ...detail(), fromAddress: SERVER });
    jest.mocked(getSignableWallet).mockResolvedValue(SERVER.toLowerCase());
    jest.mocked(sendPayRefund).mockResolvedValue(refund({ status: 'submitted', chainId: 56, tokenSymbol: 'USDT', txHash: HASH }));
    await mount(<RefundDetailScreen />);
    await press('button:Send refund');
    await press('authorize');
    expect(sendPayRefund).toHaveBeenCalledWith(RID, bscUsdt, PAYER, SERVER.toLowerCase());
  });

  // Plans review I10: until she proves her wallet the server names none (fromAddress "").
  it('an unproven wallet is proven here with the app\'s wallet proof, then the refund is read again', async () => {
    jest.mocked(getPayRefund).mockResolvedValue({ ...detail(), fromAddress: '' });
    jest.mocked(linkActiveWallet).mockResolvedValue({ success: true });
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain(unprovenWalletMessage);
    expect(text(tree)).not.toContain('Switch to that wallet');
    expect(disabled('button:Send refund')).toBe(true);
    expect(getWalletBalances).not.toHaveBeenCalled();

    jest.mocked(getPayRefund).mockResolvedValue(detail());
    await press('button:Prove this wallet');
    expect(linkActiveWallet).toHaveBeenCalledTimes(1);
    expect(text(tree)).not.toContain(unprovenWalletMessage);
    expect(disabled('button:Send refund')).toBe(false);
  });

  it('a wallet proof that fails says why and keeps Send off', async () => {
    jest.mocked(getPayRefund).mockResolvedValue({ ...detail(), fromAddress: '' });
    jest.mocked(linkActiveWallet).mockResolvedValue({ success: false, message: 'Set up a wallet first.' });
    await mount(<RefundDetailScreen />);
    await press('button:Prove this wallet');
    expect(text(tree)).toContain('Set up a wallet first.');
    expect(disabled('button:Send refund')).toBe(true);
  });

  // Plans review I6: a sent refund the network refused says why.
  it('a sent refund the network refused says why and that it is retried', async () => {
    jest.mocked(getPayRefund).mockResolvedValue(sent({ lastBroadcastError: 'Not enough gas in your wallet' }));
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain("Couldn't broadcast: Not enough gas in your wallet — top up gas; we retry automatically.");
  });

  it('with no USDT it explains how to get some, and cannot send — but can still cancel', async () => {
    jest.mocked(getWalletBalances).mockResolvedValue(balances('0') as never);
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain('No USDT on BNB Smart Chain');
    expect(text(tree)).toContain('Refunds are paid from your TSA wallet in USDT or USDC.');
    expect(disabled('button:Send refund')).toBe(true);
    expect(disabled('button:Cancel request')).toBe(false);
    await press('button:Receive');
    expect(router.push).toHaveBeenCalledWith('/wallet/receive?symbol=USDT&chainKey=bsc');
    await press('button:Buy');
    expect(router.push).toHaveBeenCalledWith('/wallet/paycrest/buy?asset=USDT');
  });

  it('with too little USDT it says how much is missing', async () => {
    jest.mocked(getWalletBalances).mockResolvedValue(balances('5') as never);
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain('You have 5 USDT; this refund needs 12.5.');
    expect(disabled('button:Send refund')).toBe(true);
  });

  it('with no gas it says so, and cannot send', async () => {
    jest.mocked(nativeBalance).mockResolvedValue(0n);
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain('You have no BNB on BNB Smart Chain for the network fee.');
    expect(disabled('button:Send refund')).toBe(true);
  });

  it('a refused send says why and reads the refund again', async () => {
    jest.mocked(sendPayRefund).mockRejectedValue(new Error('Not enough BNB on BNB Smart Chain to pay the network fee'));
    await mount(<RefundDetailScreen />);
    const reads = jest.mocked(getPayRefund).mock.calls.length;
    await press('button:Send refund');
    await press('authorize');
    expect(text(tree)).toContain('Not enough BNB on BNB Smart Chain to pay the network fee');
    expect(jest.mocked(getPayRefund).mock.calls.length).toBeGreaterThan(reads);
    expect(disabled('button:Send refund')).toBe(false);
  });

  // Review Focus 2.
  it('a lost answer re-reads the refund and offers no second send once the server has it', async () => {
    jest.mocked(sendPayRefund).mockRejectedValue(Object.assign(new Error('Could not reach the server.'), { status: 0, code: 'transport' }));
    await mount(<RefundDetailScreen />);
    jest.mocked(getPayRefund).mockResolvedValue(sent());
    await press('button:Send refund');
    await press('authorize');
    expect(text(tree)).toContain('Sending');
    expect(() => byLabel(tree, 'button:Send refund')).toThrow();
  });

  it('watches a sent refund until it is confirmed, then stops', async () => {
    jest.mocked(getPayRefund).mockResolvedValue(sent());
    await mount(<RefundDetailScreen />);
    const before = jest.mocked(getPayRefund).mock.calls.length;
    await act(async () => { jest.advanceTimersByTime(5_000); });
    await flush();
    expect(jest.mocked(getPayRefund).mock.calls.length).toBeGreaterThan(before);

    jest.mocked(getPayRefund).mockResolvedValue(sent({ status: 'confirmed', credited: '12.50' }));
    await act(async () => { jest.advanceTimersByTime(5_000); });
    await flush();
    expect(text(tree)).toContain('Ada received 12.50 USDT.');
    expect(byLabel(tree, 'button:View on explorer')).toBeTruthy();
    const settled = jest.mocked(getPayRefund).mock.calls.length;
    await act(async () => { jest.advanceTimersByTime(30_000); });
    await flush();
    expect(jest.mocked(getPayRefund).mock.calls.length).toBe(settled);
  });

  it('cancels a refund still waiting, after she confirms', async () => {
    jest.mocked(cancelPayRefund).mockResolvedValue(refund({ status: 'cancelled' }));
    await mount(<RefundDetailScreen />);
    await press('button:Cancel request');
    expect(cancelPayRefund).not.toHaveBeenCalled();
    await press('Confirm cancel request');
    expect(cancelPayRefund).toHaveBeenCalledWith(RID);
    expect(mockToast.success).toHaveBeenCalledWith('Refund request cancelled');
    expect(text(tree)).toContain('This refund request was cancelled.');
    expect(() => byLabel(tree, 'button:Send refund')).toThrow();
    expect(() => byLabel(tree, 'button:Cancel request')).toThrow();
  });

  // Plans review P12: no network can carry it right now, but she can still withdraw it.
  it('a waiting refund with no way to send it can still be cancelled', async () => {
    jest.mocked(getPayRefund).mockResolvedValue({ ...detail(), options: [] });
    await mount(<RefundDetailScreen />);
    expect(text(tree)).toContain("This refund can't be sent from the app right now.");
    expect(() => byLabel(tree, 'button:Send refund')).toThrow();
    expect(disabled('button:Cancel request')).toBe(false);
  });

  it('a cancel refused because it was sent meanwhile closes and shows it as it is', async () => {
    jest.mocked(cancelPayRefund).mockRejectedValue(Object.assign(new Error('This refund was already sent'), { status: 409 }));
    await mount(<RefundDetailScreen />);
    jest.mocked(getPayRefund).mockResolvedValue(sent());
    await press('button:Cancel request');
    await press('Confirm cancel request');
    expect(mockToast.error).toHaveBeenCalledWith('This refund was already sent');
    expect(text(tree)).toContain('Sending');
  });
});
