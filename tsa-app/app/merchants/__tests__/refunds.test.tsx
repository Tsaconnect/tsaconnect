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
      ? react.createElement(Text, { accessibilityLabel: 'authorize', onPress: props.onAuthorized }, props.title)
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
import { getOwnMerchant, listPayRefunds, type OwnMerchant, type PayRefundRow } from '@/services/payMerchantApi';
import PayRefunds from '../refunds/index';

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
