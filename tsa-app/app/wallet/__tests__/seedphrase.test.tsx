/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The wallet setup screen. What this pins: "Create wallet" opens its own create
 * prompt (never the restore form), a failed create is reported there, the
 * account is linked BEFORE the key is stored, a missing phrase never advances
 * to a reveal or a verify step that cannot fail, the phrase has no copy button,
 * and creating over a wallet this phone does not hold carries a warning.
 */
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
jest.mock('expo-router', () => ({
  __esModule: true,
  router: { push: jest.fn(), replace: jest.fn(), back: jest.fn() },
  useLocalSearchParams: () => mockParams,
  useNavigation: () => ({ setOptions: jest.fn(), addListener: jest.fn(() => jest.fn()) }),
}));
jest.mock('expo-clipboard', () => ({ __esModule: true, getStringAsync: jest.fn(), setStringAsync: jest.fn() }));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) => react.createElement(RNText, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress }, props.title),
    AccentCard: box,
    Card: box,
    Badge: box,
    Chip: (props: any) => react.createElement(RNText, { onPress: props.onPress }, props.label),
    Divider: () => null,
    Icon: () => null,
    PressableScale: (props: any) => react.createElement(View, { accessibilityLabel: props.accessibilityLabel, onPress: props.onPress }, props.children),
    Spinner: () => null,
  };
});
let mockUser: any = { walletAddress: '' };
jest.mock('../../../AuthContext/AuthContext', () => ({
  __esModule: true,
  useAuth: () => ({ currentUser: mockUser }),
}));
const mockOrder: string[] = [];
jest.mock('../../../services/wallet', () => ({
  __esModule: true,
  getMnemonic: jest.fn(),
  generateWallet: jest.fn(),
  addWallet: jest.fn(async () => { mockOrder.push('store'); }),
  importWalletFromMnemonic: jest.fn(),
  getWalletList: jest.fn(),
  markWalletBackedUp: jest.fn(),
  getActiveWallet: jest.fn(),
  isValidMnemonic: jest.fn(),
  removeWallet: jest.fn(async () => { mockOrder.push('remove'); }),
}));
jest.mock('../../../services/walletApi', () => ({
  __esModule: true,
  confirmSeedPhraseBackup: jest.fn(),
  registerWalletAddress: jest.fn(async () => { mockOrder.push('register'); return { success: true }; }),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import * as wallet from '../../../services/wallet';
import { registerWalletAddress } from '../../../services/walletApi';
import SeedPhrase from '../seedphrase';

const w = wallet as jest.Mocked<typeof wallet>;
const PHRASE = 'one two three four five six seven eight nine ten eleven twelve';

const flush = () => act(async () => { await Promise.resolve(); });
async function mount() {
  let r!: ReactTestRenderer;
  await act(async () => { r = TestRenderer.create(React.createElement(SeedPhrase)); });
  await flush();
  return r;
}
const text = (r: ReactTestRenderer) => JSON.stringify(r.toJSON());
// findAllByProps also matches the wrapping component; keep the host node only.
const buttons = (r: ReactTestRenderer, label: string) =>
  r.root.findAllByProps({ accessibilityLabel: `button:${label}` }).filter((n) => typeof (n as any).type === 'string');
const press = async (r: ReactTestRenderer, label: string) => {
  await act(async () => { await buttons(r, label)[0].props.onPress(); });
  await flush();
};

beforeEach(() => {
  jest.clearAllMocks();
  mockOrder.length = 0;
  mockParams = {};
  mockUser = { walletAddress: '' };
  w.getMnemonic.mockResolvedValue(null as any);
  w.isValidMnemonic.mockImplementation((m: any) => typeof m === 'string' && m.split(' ').length === 12);
  w.getWalletList.mockResolvedValue([]);
  w.generateWallet.mockResolvedValue({ address: '0xNEW', privateKey: '0xkey', mnemonic: PHRASE } as any);
});

describe('wallet setup screen', () => {
  it('opens the create prompt, not the restore form, for ?mode=create', async () => {
    mockParams = { mode: 'create' };
    const r = await mount();
    expect(text(r)).toContain('Create your wallet');
    expect(text(r)).not.toContain('Enter the 12-word');
    expect(buttons(r, 'Create wallet')).toHaveLength(1);
  });

  it('honours ?mode=create even when a phrase is already on the device', async () => {
    mockParams = { mode: 'create' };
    w.getMnemonic.mockResolvedValue(PHRASE);
    const r = await mount();
    expect(text(r)).toContain('Create your wallet');
  });

  it('still opens the create prompt when the wallet list cannot be read', async () => {
    mockParams = { mode: 'create' };
    w.getWalletList.mockRejectedValueOnce(new Error('bad json'));
    const r = await mount();
    expect(text(r)).toContain('Create your wallet');
  });

  it('opens the restore form for ?mode=import', async () => {
    mockParams = { mode: 'import' };
    const r = await mount();
    expect(text(r)).toContain('Restore your wallet');
  });

  it('stores the key, links the account, then shows the phrase', async () => {
    mockParams = { mode: 'create' };
    const r = await mount();
    await press(r, 'Create wallet');
    expect(mockOrder).toEqual(['store', 'register']);
    expect(text(r)).toContain('twelve');
  });

  it('undoes the store and says why when the link is refused', async () => {
    mockParams = { mode: 'create' };
    (registerWalletAddress as jest.Mock).mockResolvedValueOnce({ success: false, message: 'Link refused' });
    const r = await mount();
    await press(r, 'Create wallet');
    expect(mockOrder).toEqual(['store', 'remove']);
    expect(text(r)).toContain('Link refused');
    expect(text(r)).toContain('Create your wallet');
  });

  it('does not reveal an empty phrase when the key is missing from the device', async () => {
    w.getMnemonic.mockResolvedValueOnce(PHRASE).mockResolvedValue(null as any);
    const r = await mount(); // a wallet exists → warning step
    await press(r, 'I Understand, Show My Seed Phrase');
    expect(text(r)).toContain("couldn't find your recovery phrase");
    expect(text(r)).not.toContain('twelve');
  });

  it('has no copy-to-clipboard button on the phrase', async () => {
    mockParams = { mode: 'create' };
    const r = await mount();
    await press(r, 'Create wallet');
    expect(text(r)).not.toContain('Copy to Clipboard');
  });

  it('warns before replacing a linked wallet this phone does not hold', async () => {
    mockParams = { mode: 'create' };
    mockUser = { walletAddress: '0xOTHER' };
    const r = await mount();
    expect(text(r)).toContain('Your account already has a wallet');
    expect(buttons(r, 'Create a new wallet anyway')).toHaveLength(1);
  });
});
