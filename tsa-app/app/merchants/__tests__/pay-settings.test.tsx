/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A TSA Pay merchant changing where they are paid. What this pins: nothing is
 * sent without PIN/biometric AND the account password; the change sent is the
 * one she filled in; a bank account or a wallet applies only with the emailed
 * code; a refused name says whose account the bank thinks it is. Her payment
 * links start here too, and a suspended merchant can read them but not request.
 * A business rename is guarded the same way and takes effect only once an
 * admin approves it: the name in use stays, the waiting one is shown beside it.
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
let mockParams: Record<string, string> = {};
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  return {
    __esModule: true,
    router: { push: jest.fn() },
    Stack: { Screen: () => null },
    useLocalSearchParams: () => mockParams,
    useFocusEffect: (cb: () => void | (() => void)) => react.useEffect(cb, [cb]),
  };
});
const mockToast = { success: jest.fn(), error: jest.fn() };
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText, TextInput } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, props.title),
    ErrorState: (props: any) => react.createElement(RNText, null, props.title),
    Spinner: () => null,
    Card: box,
    Icon: () => null,
    PressableScale: (props: any) => react.createElement(View, { accessibilityLabel: props.accessibilityLabel, onPress: props.onPress }, props.children),
    Input: (props: any) => react.createElement(TextInput, { accessibilityLabel: `input:${props.label}`, value: props.value, onChangeText: props.onChangeText }),
    SelectRow: (props: any) => react.createElement(RNText, { accessibilityLabel: `select:${props.label}`, onPress: props.onPress }, props.value ?? props.placeholder),
    SegmentedControl: (props: any) => react.createElement(View, null, props.segments.map((s: any) =>
      react.createElement(RNText, { key: s.key, accessibilityLabel: `segment:${s.key}`, onPress: () => props.onChange(s.key) }, s.label))),
    PickerSheet: (props: any) => (props.visible
      ? react.createElement(View, null, props.items.map((i: any) =>
        react.createElement(RNText, { key: i.id, accessibilityLabel: `bank:${i.id}`, onPress: () => props.onSelect(i) }, i.label)))
      : null),
    useToast: () => mockToast,
  };
});
const mockAuthorization: { props: any } = { props: {} };
jest.mock('@/components/common/AuthorizationModal', () => ({
  __esModule: true,
  default: (props: any) => { mockAuthorization.props = props; return null; },
}));
const mockAssetPicker: { props: any } = { props: {} };
jest.mock('@/components/assets/AssetPicker', () => ({
  __esModule: true,
  default: (props: any) => { mockAssetPicker.props = props; return null; },
}));
jest.mock('@/hooks/useSignableWallet', () => ({ __esModule: true, useSignableWallet: () => ({ address: '0xPayer' }) }));
jest.mock('@/services/ngBanks', () => ({
  __esModule: true,
  getNigerianBanks: jest.fn(async () => [{ code: '044', name: 'Access Bank' }]),
  resolveAccountName: jest.fn(async () => ({ ok: true, accountName: 'EMEKA NWOSU' })),
}));
jest.mock('@/services/payMerchantApi', () => ({
  __esModule: true,
  OWNER_CODES: jest.requireActual('@/services/payMerchantApi').OWNER_CODES,
  getOwnMerchant: jest.fn(),
  updateSettlement: jest.fn(),
  updateBusinessName: jest.fn(),
  confirmOwnerAction: jest.fn(),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import { confirmOwnerAction, getOwnMerchant, updateBusinessName, updateSettlement, type OwnMerchant } from '@/services/payMerchantApi';
import PaySettings from '../pay-settings';

const merchant = {
  id: 'm1', businessName: 'Sky Air', status: 'active', liveEnabled: false, version: 7, settlementMode: 'fiat',
  bankCode: '044', accountNumber: '******6789', accountName: 'SKY AIR LTD', businessNameChangeEnabled: true,
} as OwnMerchant;

const flush = () => act(async () => { for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r)); });
const byLabel = (tree: ReactTestRenderer, label: string) => {
  const found = tree.root.findAll((n) => n.props.accessibilityLabel === label);
  if (!found.length) throw new Error(`nothing labelled ${label}`);
  return found[0];
};
const text = (tree: ReactTestRenderer) => JSON.stringify(tree.toJSON());

async function mount() {
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<PaySettings />); });
  await flush();
  return tree;
}

async function fillBankChange(tree: ReactTestRenderer, password: string) {
  await act(async () => { byLabel(tree, 'select:Bank').props.onPress(); });
  await act(async () => { byLabel(tree, 'bank:044').props.onPress(); });
  await act(async () => { byLabel(tree, 'input:Account number').props.onChangeText('9876543210'); });
  await act(async () => { byLabel(tree, 'input:Account password').props.onChangeText(password); });
  jest.advanceTimersByTime(500);
  await flush();
}

beforeEach(() => {
  jest.useFakeTimers({ doNotFake: ['setImmediate', 'nextTick', 'queueMicrotask'] });
  jest.clearAllMocks();
  jest.mocked(getOwnMerchant).mockResolvedValue(merchant);
});
afterEach(() => jest.useRealTimers());

it('shows where she is paid today', async () => {
  const tree = await mount();
  expect(text(tree)).toContain('SKY AIR LTD · Access Bank ******6789');
});

const pendingCode = {
  confirmation: { id: 'act-1', kind: 'settlement', expiresAt: '2026-09-24T15:00:00Z', sentTo: 'o***@skyair.ng' },
};

it('sends nothing until PIN/biometric, then the change she entered with her password', async () => {
  jest.mocked(updateSettlement).mockResolvedValue(pendingCode);
  const tree = await mount();
  await fillBankChange(tree, 'correct-horse');
  await act(async () => { byLabel(tree, 'button:Save').props.onPress(); });
  expect(updateSettlement).not.toHaveBeenCalled();
  expect(mockAuthorization.props.visible).toBe(true);

  // A double biometric success must still send one change.
  await act(async () => { mockAuthorization.props.onAuthorized(); mockAuthorization.props.onAuthorized(); });
  await flush();
  expect(updateSettlement).toHaveBeenCalledTimes(1);
  expect(updateSettlement).toHaveBeenCalledWith(
    { settlementMode: 'fiat', bankCode: '044', accountNumber: '9876543210' }, 'correct-horse', 7);
});

it('a bank account waits for the emailed code, and only the code applies it', async () => {
  jest.mocked(updateSettlement).mockResolvedValue(pendingCode);
  jest.mocked(confirmOwnerAction).mockResolvedValue({ merchant: { ...merchant, version: 8, accountNumber: '******3210', accountName: 'SKY AIR LIMITED' } });
  const tree = await mount();
  await fillBankChange(tree, 'correct-horse');
  await act(async () => { byLabel(tree, 'button:Save').props.onPress(); });
  await act(async () => { mockAuthorization.props.onAuthorized(); });
  await flush();
  expect(text(tree)).toContain('o***@skyair.ng');
  expect(text(tree)).toContain('confirm the new bank account');
  expect(text(tree)).toContain('SKY AIR LTD · Access Bank ******6789');
  expect(mockToast.success).not.toHaveBeenCalled();

  await act(async () => { byLabel(tree, 'input:Code').props.onChangeText('123456'); });
  await act(async () => { byLabel(tree, 'button:Confirm').props.onPress(); });
  await flush();
  expect(confirmOwnerAction).toHaveBeenCalledWith('act-1', '123456');
  expect(mockToast.success).toHaveBeenCalled();
  expect(text(tree)).toContain('SKY AIR LIMITED · Access Bank ******3210');
});

it('a wallet waits for the emailed code, and only the code applies it', async () => {
  jest.mocked(updateSettlement).mockResolvedValue(pendingCode);
  jest.mocked(confirmOwnerAction).mockResolvedValue({
    merchant: { ...merchant, settlementMode: 'crypto', chainId: 8453, tokenSymbol: 'USDC', walletAddress: '0x52908400098527886E0F7030069857D2E4169EE7' },
  });
  const tree = await mount();
  await act(async () => { byLabel(tree, 'segment:crypto').props.onPress(); });
  await act(async () => {
    mockAssetPicker.props.onSelect({ address: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', symbol: 'USDC', decimals: 6 }, 8453);
  });
  await act(async () => { byLabel(tree, 'input:Wallet address').props.onChangeText('0x52908400098527886E0F7030069857D2E4169EE7'); });
  await act(async () => { byLabel(tree, 'input:Account password').props.onChangeText('correct-horse'); });
  await act(async () => { byLabel(tree, 'button:Save').props.onPress(); });
  await act(async () => { mockAuthorization.props.onAuthorized(); });
  await flush();
  expect(updateSettlement).toHaveBeenCalledWith({
    settlementMode: 'crypto', chainId: 8453, tokenAddress: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913',
    walletAddress: '0x52908400098527886E0F7030069857D2E4169EE7',
  }, 'correct-horse', 7);
  expect(text(tree)).toContain('o***@skyair.ng');
  expect(mockToast.success).not.toHaveBeenCalled();

  await act(async () => { byLabel(tree, 'input:Code').props.onChangeText('123456'); });
  await act(async () => { byLabel(tree, 'button:Confirm').props.onPress(); });
  await flush();
  expect(confirmOwnerAction).toHaveBeenCalledWith('act-1', '123456');
  expect(mockToast.success).toHaveBeenCalled();
  expect(text(tree)).toContain('USDC on Base to 0x5290…9EE7');
});

it('only the server saying "no merchant" sends her to apply', async () => {
  jest.mocked(getOwnMerchant).mockRejectedValue(Object.assign(new Error('Payment settings are not available right now.'), { status: 404 }));
  let tree = await mount();
  expect(text(tree)).toContain('Could not load your payment settings');
  jest.mocked(getOwnMerchant).mockRejectedValue(Object.assign(new Error('none'), { status: 404, code: 'no_merchant' }));
  tree = await mount();
  expect(text(tree)).toContain('No TSA Pay account');
});

it('asks for the password before anything else', async () => {
  const tree = await mount();
  await fillBankChange(tree, '');
  await act(async () => { byLabel(tree, 'button:Save').props.onPress(); });
  expect(mockAuthorization.props.visible).toBeFalsy();
  expect(text(tree)).toContain('Enter your account password to confirm.');
});

it('starts her payment links: Request payment and the Payment links row', async () => {
  const tree = await mount();
  await act(async () => { byLabel(tree, 'button:Request payment').props.onPress(); });
  expect(router.push).toHaveBeenCalledWith('/merchants/pay-links/new');
  await act(async () => { byLabel(tree, 'Payment links').props.onPress(); });
  expect(router.push).toHaveBeenCalledWith('/merchants/pay-links');
});

it('opens her refunds from the Refunds row', async () => {
  const tree = await mount();
  await act(async () => { byLabel(tree, 'Refunds').props.onPress(); });
  expect(router.push).toHaveBeenCalledWith('/merchants/refunds');
});

it('a suspended merchant can read her links but not request a payment', async () => {
  jest.mocked(getOwnMerchant).mockResolvedValue({ ...merchant, status: 'suspended' });
  const tree = await mount();
  expect(() => byLabel(tree, 'button:Request payment')).toThrow();
  expect(byLabel(tree, 'Payment links')).toBeTruthy();
});

describe('business name', () => {
  const renamePending = {
    confirmation: { id: 'act-2', kind: 'business_name', expiresAt: '2026-09-28T15:00:00Z', sentTo: 'o***@skyair.ng' },
  };
  const waiting = { ...merchant, version: 8, pendingBusinessName: 'Sky Air Travels', pendingSince: '2026-09-28T14:00:00Z' };

  async function rename(tree: ReactTestRenderer, name: string, password: string) {
    await act(async () => { byLabel(tree, 'button:Change business name').props.onPress(); });
    await act(async () => { byLabel(tree, 'input:New business name').props.onChangeText(name); });
    await act(async () => { byLabel(tree, 'input:Account password').props.onChangeText(password); });
    await act(async () => { byLabel(tree, 'button:Request new name').props.onPress(); });
  }

  it('is sent only after PIN/biometric, with her password; the code files it — and the name stays until approval', async () => {
    jest.mocked(updateBusinessName).mockResolvedValue(renamePending);
    // Confirming files the request: the name in use is UNCHANGED, the rename waits.
    jest.mocked(confirmOwnerAction).mockResolvedValue({ merchant: waiting });
    const tree = await mount();
    await rename(tree, '  Sky   Air Travels ', 'correct-horse');
    expect(updateBusinessName).not.toHaveBeenCalled();
    expect(mockAuthorization.props.title).toBe('Authorize changing your business name');

    await act(async () => { mockAuthorization.props.onAuthorized(); mockAuthorization.props.onAuthorized(); });
    await flush();
    expect(updateBusinessName).toHaveBeenCalledTimes(1);
    expect(updateBusinessName).toHaveBeenCalledWith('Sky Air Travels', 'correct-horse', 7);
    expect(updateSettlement).not.toHaveBeenCalled();
    expect(text(tree)).toContain('send your new business name for approval');
    expect(mockToast.success).not.toHaveBeenCalled();

    await act(async () => { byLabel(tree, 'input:Code').props.onChangeText('654321'); });
    await act(async () => { byLabel(tree, 'button:Confirm').props.onPress(); });
    await flush();
    expect(confirmOwnerAction).toHaveBeenCalledWith('act-2', '654321');
    expect(mockToast.success).toHaveBeenCalledTimes(1);
    expect(mockToast.success).toHaveBeenCalledWith('New business name sent for approval');
    expect(text(tree)).toContain('"Sky Air"');
    expect(text(tree)).toContain("Your new name 'Sky Air Travels' is waiting for approval. Customers and payouts keep 'Sky Air' until then.");
  });

  it('shows a rename already waiting, with the name still in use', async () => {
    jest.mocked(getOwnMerchant).mockResolvedValue(waiting);
    const tree = await mount();
    expect(text(tree)).toContain("Your new name 'Sky Air Travels' is waiting for approval. Customers and payouts keep 'Sky Air' until then.");
  });

  it('whitespace is normalised before the unchanged check; the same pending name is not asked twice', async () => {
    const tree = await mount();
    await rename(tree, '  Sky    Air ', 'correct-horse');
    expect(text(tree)).toContain('That is already your business name.');
    expect(mockAuthorization.props.visible).toBeFalsy();

    jest.mocked(getOwnMerchant).mockResolvedValue(waiting);
    const again = await mount();
    await rename(again, 'Sky Air  Travels', 'correct-horse');
    expect(text(again)).toContain('That name is already waiting for approval.');
    expect(mockAuthorization.props.visible).toBeFalsy();
  });

  it('asking for the current name back withdraws a waiting rename: a 200 { merchant } — nothing to confirm, no toast', async () => {
    jest.mocked(getOwnMerchant).mockResolvedValue(waiting);
    jest.mocked(updateBusinessName).mockResolvedValue({ merchant: { ...merchant, version: 9 } });
    const tree = await mount();
    await rename(tree, 'Sky Air', 'correct-horse');
    await act(async () => { mockAuthorization.props.onAuthorized(); });
    await flush();
    expect(updateBusinessName).toHaveBeenCalledWith('Sky Air', 'correct-horse', 8);
    expect(confirmOwnerAction).not.toHaveBeenCalled();
    expect(mockToast.success).not.toHaveBeenCalled();
    expect(text(tree)).not.toContain('waiting for approval');
    expect(text(tree)).not.toContain('input:Code');
  });

  it('asks for a name and the password before anything else', async () => {
    const tree = await mount();
    await rename(tree, '', 'correct-horse');
    expect(text(tree)).toContain('Enter the new business name.');
    await act(async () => { byLabel(tree, 'input:New business name').props.onChangeText('Sky Air Travels'); });
    await act(async () => { byLabel(tree, 'input:Account password').props.onChangeText(''); });
    await act(async () => { byLabel(tree, 'button:Request new name').props.onPress(); });
    expect(text(tree)).toContain('Enter your account password to confirm.');
    expect(mockAuthorization.props.visible).toBeFalsy();
  });

  it('a refused name (business_name_invalid) is told as refused, with the server\'s reason — keyed on the code, not its words', async () => {
    jest.mocked(updateBusinessName).mockRejectedValue(Object.assign(
      new Error('a business name can\'t contain "TSA Pay"'), { status: 422, code: 'business_name_invalid' }));
    const tree = await mount();
    await rename(tree, 'TSA Pay Refunds', 'correct-horse');
    await act(async () => { mockAuthorization.props.onAuthorized(); });
    await flush();
    expect(text(tree)).toContain('That name can’t be used: a business name can\'t contain \\"TSA Pay\\"');
  });

  it('a name outside 2–100 characters, counted as characters (not UTF-16 units), is caught before anything is sent', async () => {
    const tree = await mount();
    await rename(tree, 'S', 'correct-horse');
    expect(text(tree)).toContain('Enter a name of 2 to 100 characters.');
    // 100 emoji are 200 UTF-16 units but 100 characters: allowed.
    await act(async () => { byLabel(tree, 'input:New business name').props.onChangeText('😀'.repeat(100)); });
    await act(async () => { byLabel(tree, 'button:Request new name').props.onPress(); });
    expect(mockAuthorization.props.visible).toBe(true);
    await act(async () => { byLabel(tree, 'input:New business name').props.onChangeText('a'.repeat(101)); });
    await act(async () => { byLabel(tree, 'button:Request new name').props.onPress(); });
    expect(text(tree)).toContain('Enter a name of 2 to 100 characters.');
  });

  it('with renames switched off (or an API that does not say), the name shows and there is no way to ask', async () => {
    for (const off of [{ businessNameChangeEnabled: false }, { businessNameChangeEnabled: undefined }]) {
      jest.mocked(getOwnMerchant).mockResolvedValue({ ...waiting, ...off });
      const tree = await mount();
      expect(text(tree)).toContain('"Sky Air"');
      expect(text(tree)).toContain('waiting for approval');
      expect(() => byLabel(tree, 'button:Change business name')).toThrow();
    }
  });

  it('a suspended merchant cannot rename', async () => {
    jest.mocked(getOwnMerchant).mockResolvedValue({ ...merchant, status: 'suspended' });
    const tree = await mount();
    expect(() => byLabel(tree, 'button:Change business name')).toThrow();
  });
});

describe('business name on its own (?section=name)', () => {
  beforeEach(() => {
    mockParams = { section: 'name' };
    jest.mocked(getOwnMerchant).mockResolvedValue({ ...merchant, businessNameChangeEnabled: true });
  });
  afterEach(() => { mockParams = {}; });

  it('shows the name and the way to change it, without payout settings or pay links', async () => {
    const tree = await mount();
    expect(text(tree)).toContain('Business name');
    expect(() => byLabel(tree, 'button:Change business name')).not.toThrow();
    expect(text(tree)).not.toContain('is paid to');
    expect(text(tree)).not.toContain('Change where you are paid');
    expect(text(tree)).not.toContain('Get paid with a link');
  });

  it('the same screen without the section still shows payout settings', async () => {
    mockParams = {};
    const tree = await mount();
    expect(text(tree)).toContain('Change where you are paid');
  });
});
