/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A TSA Pay merchant changing where they are paid. What this pins: nothing is
 * sent without PIN/biometric AND the account password; the change sent is the
 * one she filled in; a refused name says whose account the bank thinks it is.
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
  useTheme: () => ({ colors: new Proxy({}, { get: () => '#000000' }), spacing: new Proxy({}, { get: () => 8 }) }),
}));
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
  confirmSettlement: jest.fn(),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { confirmSettlement, getOwnMerchant, updateSettlement, type OwnMerchant } from '@/services/payMerchantApi';
import PaySettings from '../pay-settings';

const merchant = {
  id: 'm1', businessName: 'Sky Air', status: 'active', version: 7, settlementMode: 'fiat',
  bankCode: '044', accountNumber: '******6789', accountName: 'SKY AIR LTD',
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

it('sends nothing until PIN/biometric, then the change she entered with her password', async () => {
  jest.mocked(updateSettlement).mockResolvedValue({ merchant: { ...merchant, accountNumber: '******3210' } });
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
  expect(mockToast.success).toHaveBeenCalled();
});

it('a wallet waits for the emailed code, and only the code applies it', async () => {
  jest.mocked(updateSettlement).mockResolvedValue({ confirmation: { expiresAt: '2026-09-24T15:00:00Z', sentTo: 'o***@skyair.ng' } });
  jest.mocked(confirmSettlement).mockResolvedValue({
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
  expect(confirmSettlement).toHaveBeenCalledWith('123456');
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

it('a refused name says whose account the bank thinks it is', async () => {
  jest.mocked(updateSettlement).mockRejectedValue(Object.assign(
    new Error("That account isn't in your business name or your verified name."), { status: 422, code: 'name_mismatch' }));
  const tree = await mount();
  await fillBankChange(tree, 'correct-horse');
  await act(async () => { byLabel(tree, 'button:Save').props.onPress(); });
  await act(async () => { mockAuthorization.props.onAuthorized(); });
  await flush();
  expect(text(tree)).toContain('The bank says this account belongs to EMEKA NWOSU.');
});
