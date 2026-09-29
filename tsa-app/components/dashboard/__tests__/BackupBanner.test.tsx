/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The backup reminder is for wallets this app generated. An imported wallet was
 * restored from a phrase the user already holds, so it is recorded as backed up
 * and must not be nagged about.
 */
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));

const mockStore: Record<string, string> = {};
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async (k: string) => mockStore[k] ?? null), setItem: jest.fn() },
}));

const mockGetActiveWallet = jest.fn();
const mockGetWalletList = jest.fn();
jest.mock('@/services/wallet', () => ({
  __esModule: true,
  getActiveWallet: () => mockGetActiveWallet(),
  getWalletList: () => mockGetWalletList(),
}));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/components/ui', () => {
  const R = jest.requireActual('react');
  const host = (name: string) => (props: any) => R.createElement(name, props, props.children);
  return {
    __esModule: true,
    AccentCard: host('mock-card'),
    Icon: () => null,
    IconButton: host('mock-iconbutton'),
    PressableScale: host('mock-pressable'),
    Text: host('mock-text'),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { BackupBanner } from '../BackupBanner';

const ADDR = '0xabc0000000000000000000000000000000000001';

const render = async () => {
  let tree!: ReactTestRenderer;
  await act(async () => {
    tree = TestRenderer.create(<BackupBanner />);
  });
  return tree;
};

const showsBackupReminder = (r: ReactTestRenderer) =>
  r.root.findAll((n) => (n as any).props?.accessibilityLabel === 'Dismiss backup reminder').length > 0;

beforeEach(() => {
  for (const k of Object.keys(mockStore)) delete mockStore[k];
  mockGetActiveWallet.mockResolvedValue(ADDR);
});

describe('BackupBanner', () => {
  it('reminds a generated wallet that has not been backed up', async () => {
    mockGetWalletList.mockResolvedValue([{ address: ADDR, label: 'Wallet 1', backedUp: false, createdAt: '' }]);
    expect(showsBackupReminder(await render())).toBe(true);
  });

  it('does not remind an imported wallet', async () => {
    mockGetWalletList.mockResolvedValue([{ address: ADDR, label: 'Wallet 1', backedUp: true, createdAt: '' }]);
    expect(showsBackupReminder(await render())).toBe(false);
  });

  it('matches the active wallet case-insensitively', async () => {
    mockGetWalletList.mockResolvedValue([
      { address: ADDR.toUpperCase().replace('0X', '0x'), label: 'Wallet 1', backedUp: true, createdAt: '' },
    ]);
    expect(showsBackupReminder(await render())).toBe(false);
  });

  it('checks the active wallet, not any wallet', async () => {
    mockGetWalletList.mockResolvedValue([
      { address: '0xother', label: 'Imported', backedUp: true, createdAt: '' },
      { address: ADDR, label: 'Created', backedUp: false, createdAt: '' },
    ]);
    expect(showsBackupReminder(await render())).toBe(true);
  });

  it('honours the pre-multi-wallet flag when the wallet has no list entry', async () => {
    mockGetWalletList.mockResolvedValue([]);
    mockStore.seedPhraseBackedUp = 'true';
    expect(showsBackupReminder(await render())).toBe(false);
  });
});
