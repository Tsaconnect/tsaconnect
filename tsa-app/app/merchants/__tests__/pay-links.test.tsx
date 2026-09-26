/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A TSA Pay merchant's payment links. What this pins: a link is made in the
 * mode she is in (test until she can take live payments) with what she typed,
 * and a retry of the same form is the same request; an amount that is not plain
 * naira is refused on its field; the made link is shown with what the customer
 * pays, what she receives, its QR code and a share message quoting the customer's
 * total; a taken reference is said on its field; a cancelled link reads
 * Cancelled in the list; cancelling asks first and only the confirm sends it;
 * the dashboard's TSA Pay card starts links and keeps Payment settings for a
 * merchant who has TSA Pay.
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
const mockParams: { id?: string; mode?: string; listMode?: string } = {};
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn(), replace: jest.fn(), back: jest.fn() },
    useLocalSearchParams: () => mockParams,
    // Focus once, on mount — what a first visit to the screen does.
    useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]),
  };
});
const mockToast = { success: jest.fn(), error: jest.fn() };
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText, TextInput } = jest.requireActual('react-native');
  const { getStatusVisual } = jest.requireActual('@/components/ui/statusMap');
  const box = (props: any) => react.createElement(View, null, props.children);
  const input = (props: any) => react.createElement(View, null,
    react.createElement(TextInput, { accessibilityLabel: `input:${props.label}`, value: props.value, onChangeText: props.onChangeText }),
    props.error ? react.createElement(RNText, null, `error:${props.error}`) : null);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, {
      accessibilityLabel: props.accessibilityLabel ?? `button:${props.title}`, onPress: props.onPress,
    }, props.title),
    ErrorState: (props: any) => react.createElement(RNText, null, props.title),
    EmptyState: (props: any) => react.createElement(RNText, null, props.title),
    Spinner: () => null,
    Icon: () => null,
    Card: box,
    AccentCard: box,
    Badge: box,
    Divider: () => null,
    SummaryCard: box,
    SummaryRow: (props: any) => react.createElement(RNText, null, `${props.label}: ${props.value}`),
    getStatusVisual,
    StatusPill: (props: any) => react.createElement(RNText, null, getStatusVisual(props.kind, props.status).label),
    PressableScale: (props: any) => react.createElement(View, {
      accessibilityLabel: props.accessibilityLabel, testID: props.testID, onPress: props.onPress,
    }, props.children),
    Input: input,
    AmountInput: input,
    SegmentedControl: (props: any) => react.createElement(View, null, props.segments.map((s: any) =>
      react.createElement(RNText, { key: s.key, accessibilityLabel: `segment:${s.key}`, onPress: () => props.onChange(s.key) }, s.label))),
    Sheet: (props: any) => (props.visible ? react.createElement(View, null, props.children) : null),
    useToast: () => mockToast,
  };
});
jest.mock('react-native-qrcode-svg', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return { __esModule: true, default: (props: any) => react.createElement(Text, null, `qr:${props.value}`) };
});
jest.mock('expo-clipboard', () => ({ __esModule: true, setStringAsync: jest.fn(async () => true) }));
let mockKeys = 0;
jest.mock('@/services/itApi', () => ({ __esModule: true, makeIdempotencyKey: () => `key-${++mockKeys}` }));
jest.mock('@/services/payMerchantApi', () => {
  const actual = jest.requireActual('@/services/payMerchantApi');
  return {
    __esModule: true,
    OWNER_CODES: actual.OWNER_CODES,
    payLinkModeOf: actual.payLinkModeOf,
    payLinkModeParam: actual.payLinkModeParam,
    getOwnMerchant: jest.fn(),
    createPayLink: jest.fn(),
    listPayLinks: jest.fn(),
    getPayLink: jest.fn(),
    cancelPayLink: jest.fn(),
  };
});
jest.mock('../../../components/services/api', () => ({
  __esModule: true,
  default: { setToken: jest.fn(), getMerchantProducts: jest.fn(async () => ({ success: true, data: [] })) },
}));
jest.mock('@/contexts/CurrencyContext', () => ({ __esModule: true, useCurrency: () => ({ formatPrice: (n: number) => String(n) }) }));
// One object: the dashboard's focus effect depends on `reload`, so a new one each render would loop.
const mockPmStatus = { isPm: false, status: null, loading: false, reload: () => {} };
jest.mock('@/hooks/useIsPaymentMerchant', () => ({ __esModule: true, useIsPaymentMerchant: () => mockPmStatus }));

import React from 'react';
import { Share } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import {
  cancelPayLink, createPayLink, getOwnMerchant, getPayLink, listPayLinks, type OwnMerchant, type PayLink,
} from '@/services/payMerchantApi';
import NewPayLink from '../pay-links/new';
import PayLinks from '../pay-links/index';
import PayLinkDetail from '../pay-links/[id]';
import MerchantDashboard from '../dashboard/index';

const merchant = {
  id: 'm1', businessName: 'Sky Air', status: 'active', liveEnabled: true, liveAllowed: false, version: 7, settlementMode: 'fiat',
} as OwnMerchant;

const link = (over: Partial<PayLink> = {}): PayLink => ({
  id: 'p1', reference: 'link_abcdefghijkl', mode: 'test', status: 'pending', source: 'link',
  amount: 500_000, customerTotal: 505_000, merchantNet: 490_000, fee: 15_000, currency: 'NGN',
  description: 'Two bags of rice', customerName: 'Ada Obi', expiresAt: '2026-10-03T12:00:00Z',
  checkoutUrl: 'https://tsaconnectworld.com/pay?id=p1', createdAt: '2026-09-26T12:00:00Z', ...over,
});

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
const byTestID = (t: ReactTestRenderer, id: string) => {
  const found = t.root.findAll((n) => n.props.testID === id);
  if (!found.length) throw new Error(`no testID ${id}`);
  return found[0];
};
let tree: ReactTestRenderer;

async function mount(el: React.ReactElement) {
  await act(async () => { tree = TestRenderer.create(el); });
  await flush();
  return tree;
}
const type = async (label: string, value: string) => {
  await act(async () => { byLabel(tree, `input:${label}`).props.onChangeText(value); });
};
const press = async (label: string) => {
  await act(async () => { byLabel(tree, label).props.onPress(); });
  await flush();
};

beforeEach(() => {
  jest.useFakeTimers({ doNotFake: ['setImmediate', 'nextTick', 'queueMicrotask'] });
  jest.clearAllMocks();
  mockKeys = 0;
  for (const k of Object.keys(mockParams) as (keyof typeof mockParams)[]) delete mockParams[k];
  jest.mocked(getOwnMerchant).mockResolvedValue(merchant);
});
afterEach(() => { act(() => tree?.unmount()); jest.useRealTimers(); });

describe('Request payment', () => {
  async function fill() {
    await mount(<NewPayLink />);
    await type('Amount', '5000');
    await type("What it's for", '  Two bags of rice ');
    await type('Customer name (optional)', 'Ada Obi');
    await act(async () => { byLabel(tree, 'segment:3').props.onPress(); });
  }

  it('makes a test link from what she typed, then shows it ready to share', async () => {
    jest.mocked(createPayLink).mockResolvedValue(link());
    const share = jest.spyOn(Share, 'share').mockResolvedValue({ action: 'sharedAction' });
    await fill();
    expect(text(tree)).toContain('TEST LINK');
    await press('button:Create link');

    expect(createPayLink).toHaveBeenCalledWith('test', {
      amount: 500_000, description: 'Two bags of rice', customerName: 'Ada Obi', reference: undefined, expiresInDays: 3,
    }, 'key-1');
    expect(text(tree)).toContain('Your customer pays: ₦5,050.00');
    expect(text(tree)).toContain("You'll receive: ₦4,900.00");
    expect(text(tree)).toContain('qr:https://tsaconnectworld.com/pay?id=p1');

    await press('button:Share');
    expect(share).toHaveBeenCalledWith({
      message: 'Sky Air is requesting ₦5,050.00 for Two bags of rice. Pay here: https://tsaconnectworld.com/pay?id=p1',
    });
  });

  it('a live merchant (live allowed, not just switched) makes a live link', async () => {
    jest.mocked(getOwnMerchant).mockResolvedValue({ ...merchant, liveAllowed: true });
    jest.mocked(createPayLink).mockResolvedValue(link({ mode: 'live' }));
    await fill();
    expect(text(tree)).not.toContain('TEST LINK');
    await press('button:Create link');
    expect(jest.mocked(createPayLink).mock.calls[0][0]).toBe('live');
  });

  it('a retry of the same form is the same request; a changed form is a new one', async () => {
    jest.mocked(createPayLink).mockRejectedValue(Object.assign(new Error('Could not reach the server.'), { status: 0 }));
    await fill();
    await press('button:Create link');
    await press('button:Create link');
    await type('Amount', '6000');
    await press('button:Create link');
    const keys = jest.mocked(createPayLink).mock.calls.map((c) => c[2]);
    expect(keys[0]).toBe(keys[1]);
    expect(keys[2]).not.toBe(keys[0]);
  });

  it('asks for an amount and what it is for, each on its own field, before sending anything', async () => {
    await mount(<NewPayLink />);
    await press('button:Create link');
    expect(text(tree)).toContain('error:Enter the amount to request.');
    expect(text(tree)).toContain("error:Say what it's for");
    expect(createPayLink).not.toHaveBeenCalled();
  });

  it.each(['5000,50', '5,000', '12.345', '1e3'])('refuses %s rather than guessing the amount', async (typed) => {
    await mount(<NewPayLink />);
    await type('Amount', typed);
    await type("What it's for", 'Rice');
    await press('button:Create link');
    expect(text(tree)).toContain('error:Enter naira with a point for kobo');
    expect(createPayLink).not.toHaveBeenCalled();
  });

  it('opened from the list, "See all" goes back to it; a link in the other mode opens that list', async () => {
    mockParams.listMode = 'test';
    jest.mocked(createPayLink).mockResolvedValue(link());
    await fill();
    await press('button:Create link');
    await press('button:See all payment links');
    expect(router.back).toHaveBeenCalled();
    expect(router.replace).not.toHaveBeenCalled();

    mockParams.listMode = 'live';
    jest.mocked(router.back).mockClear();
    await fill();
    await press('button:Create link');
    await press('button:See all payment links');
    expect(router.back).not.toHaveBeenCalled();
    expect(router.replace).toHaveBeenCalledWith('/merchants/pay-links?mode=test');
  });

  it('a reference she already used is said on the reference field', async () => {
    jest.mocked(createPayLink).mockRejectedValue(Object.assign(
      new Error("You've already used this reference — use another or leave it blank"), { status: 409, code: 'reference_taken' }));
    await fill();
    await type('Your reference (optional)', 'INV-1');
    await press('button:Create link');
    expect(text(tree)).toContain("error:You've already used this reference");
  });

  it('with nowhere to be paid yet, she is sent to set it', async () => {
    jest.mocked(createPayLink).mockRejectedValue(Object.assign(
      new Error('Set where you want to be paid before you request a payment'), { status: 409, code: 'settlement_not_set' }));
    await fill();
    await press('button:Create link');
    await press("button:Set where you're paid");
    expect(router.push).toHaveBeenCalledWith('/merchants/pay-settings');
  });
});

describe('Payment links', () => {
  it('lists her links, a cancelled one as Cancelled, with today\'s counts', async () => {
    const today = new Date().toISOString();
    jest.mocked(listPayLinks).mockResolvedValue({
      items: [
        link({ id: 'a' }),
        link({ id: 'b', status: 'succeeded', succeededAt: today, customerName: undefined }),
        link({ id: 'c', status: 'expired', cancelled: true }),
        link({ id: 'd', status: 'expired' }),
      ],
      total: 4, page: 1, limit: 50,
    });
    await mount(<PayLinks />);
    expect(listPayLinks).toHaveBeenCalledWith('test', 1, 50);
    const out = text(tree);
    expect(out).toContain('Cancelled');
    expect(out).toContain('Expired');
    expect(out).toContain('Awaiting payment 1');
    expect(out).toContain('Paid today 1');
    expect(out).toContain('No customer name');

    const row = byTestID(tree, 'pay-link-c');
    expect(row.props.accessibilityLabel).toBe('Two bags of rice, ₦5,000.00, Cancelled');
    await act(async () => { row.props.onPress(); });
    expect(router.push).toHaveBeenCalledWith('/merchants/pay-links/c?mode=test');
  });

  it('keeps itself fresh every 15 s', async () => {
    jest.mocked(listPayLinks).mockResolvedValue({ items: [], total: 0, page: 1, limit: 50 });
    await mount(<PayLinks />);
    expect(listPayLinks).toHaveBeenCalledTimes(1);
    await act(async () => { jest.advanceTimersByTime(15_000); });
    await flush();
    expect(listPayLinks).toHaveBeenCalledTimes(2);
  });
});

describe('A payment link', () => {
  beforeEach(() => { mockParams.id = 'p1'; mockParams.mode = 'test'; });

  it('cancelling asks first, and only the confirm cancels it', async () => {
    jest.mocked(getPayLink).mockResolvedValue(link());
    jest.mocked(cancelPayLink).mockResolvedValue(link({ status: 'expired', cancelled: true }));
    await mount(<PayLinkDetail />);
    expect(getPayLink).toHaveBeenCalledWith('test', 'p1');
    expect(text(tree)).toContain('qr:https://tsaconnectworld.com/pay?id=p1');

    await press('button:Cancel link');
    expect(cancelPayLink).not.toHaveBeenCalled();
    expect(text(tree)).toContain("The link will stop working. This can't be undone.");

    await press('Confirm cancel link');
    expect(cancelPayLink).toHaveBeenCalledWith('test', 'p1');
    expect(mockToast.success).toHaveBeenCalledWith('Link cancelled');
    expect(text(tree)).toContain('Cancelled');
    // A cancelled link is not shared again.
    expect(text(tree)).not.toContain('qr:');
  });

  it('keeping the link sends nothing', async () => {
    jest.mocked(getPayLink).mockResolvedValue(link());
    await mount(<PayLinkDetail />);
    await press('button:Cancel link');
    await press('button:Keep link');
    expect(cancelPayLink).not.toHaveBeenCalled();
  });

  it('a link someone started paying cannot be cancelled, and is shown as it now is', async () => {
    jest.mocked(getPayLink).mockResolvedValueOnce(link()).mockResolvedValue(link({ status: 'processing' }));
    jest.mocked(cancelPayLink).mockRejectedValue(Object.assign(
      new Error("This link can't be cancelled — it's already being paid, paid, or closed"), { status: 409, code: 'not_cancellable' }));
    await mount(<PayLinkDetail />);
    await press('button:Cancel link');
    await press('Confirm cancel link');
    // Nothing left to confirm: the sheet closes, she is told why, and the link shows as it now is.
    expect(text(tree)).not.toContain("This can't be undone");
    expect(mockToast.error).toHaveBeenCalledWith("This link can't be cancelled — it's already being paid, paid, or closed");
    expect(text(tree)).toContain('Being paid');
  });

  it('shows the one time that matters for where the link stands', async () => {
    jest.mocked(getPayLink).mockResolvedValue(link());
    await mount(<PayLinkDetail />);
    expect(text(tree)).toContain('Open until:');
    expect(text(tree)).toContain("You'll receive: ₦4,900.00");
    act(() => tree.unmount());

    jest.mocked(getPayLink).mockResolvedValue(link({ status: 'expired' }));
    await mount(<PayLinkDetail />);
    expect(text(tree)).toContain('Expired:');
    expect(text(tree)).not.toContain('Open until');
    expect(text(tree)).not.toContain("You'll receive");
    act(() => tree.unmount());

    for (const over of [{ status: 'processing' as const }, { status: 'failed' as const }, { status: 'expired' as const, cancelled: true }]) {
      jest.mocked(getPayLink).mockResolvedValue(link(over));
      await mount(<PayLinkDetail />);
      expect(text(tree)).not.toMatch(/Open until|Expired:|Paid:/);
      act(() => tree.unmount());
    }
  });
});

describe('Merchant dashboard', () => {
  it('a merchant with TSA Pay gets Request payment and her Payment links on the TSA Pay card', async () => {
    await mount(<MerchantDashboard />);
    expect(text(tree)).not.toContain('Accept payments (TSA Pay)');
    await press('button:Request payment');
    expect(router.push).toHaveBeenCalledWith('/merchants/pay-links/new');
    await press('Payment links');
    expect(router.push).toHaveBeenCalledWith('/merchants/pay-links');
    await press('Payment settings');
    expect(router.push).toHaveBeenCalledWith('/merchants/pay-settings');
  });

  it('shows no TSA Pay card until it knows whether she has TSA Pay', async () => {
    jest.mocked(getOwnMerchant).mockReturnValue(new Promise(() => {}));
    await mount(<MerchantDashboard />);
    expect(text(tree)).not.toContain('Accept payments (TSA Pay)');
    expect(() => byLabel(tree, 'button:Request payment')).toThrow();
  });

  it('anyone else still gets the TSA Pay application card', async () => {
    jest.mocked(getOwnMerchant).mockRejectedValue(Object.assign(new Error('none'), { status: 404, code: 'no_merchant' }));
    await mount(<MerchantDashboard />);
    expect(text(tree)).toContain('Accept payments (TSA Pay)');
    expect(() => byLabel(tree, 'button:Request payment')).toThrow();
  });
});
