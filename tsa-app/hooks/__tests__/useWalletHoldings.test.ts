const mockStore = new Map<string, string>();
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: {
    getItem: jest.fn(async (k: string) => mockStore.get(k) ?? null),
    setItem: jest.fn(async (k: string, v: string) => { mockStore.set(k, v); }),
    removeItem: jest.fn(async (k: string) => { mockStore.delete(k); }),
  },
}));
jest.mock('@/services/wallet', () => ({ getActiveWallet: jest.fn() }));
jest.mock('@/services/walletApi', () => ({
  ...jest.requireActual('@/services/walletApi'),
  getWalletBalances: jest.fn(),
  // The rows below are already normalized.
  normalizeWalletBalances: (rows: unknown) => rows,
}));
jest.mock('@/services/chains/solanaWallet', () => ({ getSolanaAddress: jest.fn(async () => null), getSolanaBalances: jest.fn() }));
jest.mock('@/services/chains/btcWallet', () => ({ getBtcAddress: jest.fn(async () => null), getBtcBalances: jest.fn() }));
jest.mock('@/services/chains/suiWallet', () => ({ getSuiAddress: jest.fn(async () => null), getSuiBalances: jest.fn() }));

// eslint-disable-next-line import/first -- jest hoists the mocks above.
import { holdingFromRow, useWalletHoldings, invalidateWalletHoldings, prefetchWalletHoldings } from '../useWalletHoldings';
// eslint-disable-next-line import/first
import React from 'react';
// eslint-disable-next-line import/first
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
// eslint-disable-next-line import/first
import { getActiveWallet } from '@/services/wallet';
// eslint-disable-next-line import/first
import { getWalletBalances } from '@/services/walletApi';
// eslint-disable-next-line import/first
import { getSolanaAddress } from '@/services/chains/solanaWallet';
// eslint-disable-next-line import/first
import { getBtcAddress } from '@/services/chains/btcWallet';
// eslint-disable-next-line import/first
import { getSuiAddress } from '@/services/chains/suiWallet';
// eslint-disable-next-line import/first
import type { WalletBalanceWithChain } from '@/services/walletApi';

const row = (over: Partial<WalletBalanceWithChain>): WalletBalanceWithChain => ({
  symbol: 'USDC', name: 'USD Coin', balance: '5', usdValue: '5', contractAddress: '', decimals: 6,
  chainKey: 'base', chainName: 'Base', ...over,
});

it('takes the native flag from the API', () => {
  const h = holdingFromRow(row({ symbol: 'ETH', native: true, decimals: 18 }), true)!;
  expect(h).toMatchObject({ chainId: 8453, native: true, address: '' });
});

it("treats a row with no address as native only when it is the chain's own coin", () => {
  expect(holdingFromRow(row({ symbol: 'ETH' }), true)!.native).toBe(true);
  // An older API sends USDC with no contract: it must not become ETH.
  expect(holdingFromRow(row({}), true)!.native).toBe(false);
});

it("reads Sui's padded native coin type as native SUI", () => {
  const h = holdingFromRow(row({ chainKey: 'sui', symbol: 'SUI', contractAddress: `0x${'0'.repeat(63)}2::sui::SUI` }), false)!;
  expect(h).toMatchObject({ native: true, address: '', trusted: true });
  const fake = holdingFromRow(row({ chainKey: 'sui', symbol: 'SUI', contractAddress: '0xabc::sui::SUI' }), false)!;
  expect(fake).toMatchObject({ native: false, trusted: false });
});

it('keeps the contract address for a token row', () => {
  const h = holdingFromRow(row({ contractAddress: '0xabc' }), true)!;
  expect(h).toMatchObject({ native: false, address: '0xabc', trusted: true });
});

it('does not trust a non-native token from outside our supported list', () => {
  const h = holdingFromRow(row({ chainKey: 'solana', symbol: 'BONK', contractAddress: 'Mint111' }), false)!;
  expect(h.trusted).toBe(false);
  expect(holdingFromRow(row({ chainKey: 'solana', symbol: 'SOL' }), false)!.trusted).toBe(true);
});

it('trusts a non-EVM token only when the API lists it', () => {
  const listed = holdingFromRow(row({ chainKey: 'sui', contractAddress: '0xdba1::usdc::USDC', listed: true }), false)!;
  expect(listed.trusted).toBe(true);
  const airdrop = holdingFromRow(row({ chainKey: 'sui', contractAddress: '0xbad1::usdc::USDC' }), false)!;
  expect(airdrop.trusted).toBe(false);
});

it('skips empty balances', () => {
  expect(holdingFromRow(row({ balance: '0' }), true)).toBeNull();
});

describe('loading', () => {
  let seen: { holdings: unknown[]; loading: boolean }[] = [];
  function Probe({ enabled }: { enabled: boolean }) {
    seen.push(useWalletHoldings(enabled));
    return null;
  }
  beforeEach(() => { seen = []; mockStore.clear(); invalidateWalletHoldings(); });

  it('reports loading from the first render, before the active wallet is even known', async () => {
    let resolveWallet!: (w: string | null) => void;
    (getActiveWallet as jest.Mock).mockReturnValue(new Promise(r => { resolveWallet = r; }));
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
    // Not a single render may say "loaded, holds nothing" while the read is pending.
    expect(seen.every(s => s.loading)).toBe(true);

    await act(async () => { resolveWallet(null); });
    expect(seen[seen.length - 1]).toEqual({ holdings: [], loading: false });
    tree.unmount();
  });

  it('is not loading while disabled', async () => {
    await act(async () => { TestRenderer.create(React.createElement(Probe, { enabled: false })); });
    expect(seen[seen.length - 1].loading).toBe(false);
  });

  // Balances as the API answers them: one USDC row, `amount` of it.
  const answer = (amount: string) => ({ success: true, data: [row({ contractAddress: '0xusdc', balance: amount, usdValue: amount })] });
  const balancesShown = () => (seen[seen.length - 1].holdings as { balance: number }[]).map(h => h.balance);
  /** A balances read that answers only when told to. */
  function pendingBalances() {
    let resolve!: (v: unknown) => void;
    (getWalletBalances as jest.Mock).mockReturnValueOnce(new Promise(r => { resolve = r; }));
    return (v: unknown) => act(async () => { resolve(v); });
  }
  /**
   * Leave balances on the device as an earlier app launch would have: read and
   * stored an hour ago, with nothing left in memory.
   */
  async function rememberFromEarlierLaunch(wallet: string, amount: string) {
    (getActiveWallet as jest.Mock).mockResolvedValue(wallet);
    (getWalletBalances as jest.Mock).mockResolvedValueOnce(answer(amount));
    await act(async () => { await prefetchWalletHoldings(); });
    const onDevice = new Map(mockStore);
    // Forgetting memory also clears the device copy, so put it back, aged.
    await act(async () => { invalidateWalletHoldings(); });
    onDevice.forEach((v, k) => {
      const entry = JSON.parse(v);
      mockStore.set(k, JSON.stringify({ ...entry, at: entry.at - 3_600_000 }));
    });
  }

  it('shows the balances from the last launch at once, then the fresh ones', async () => {
    await rememberFromEarlierLaunch('0xW', '5');
    const settle = pendingBalances();
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
    // No spinner: the stored balances stand in while the read is out.
    expect(seen[seen.length - 1].loading).toBe(false);
    expect(balancesShown()).toEqual([5]);

    await settle(answer('7'));
    expect(balancesShown()).toEqual([7]);
    tree.unmount();
  });

  it("never shows another wallet's stored balances", async () => {
    await rememberFromEarlierLaunch('0xOTHER', '5');
    (getActiveWallet as jest.Mock).mockResolvedValue('0xW');
    const settle = pendingBalances();
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
    expect(seen.every(s => s.loading && (s.holdings as unknown[]).length === 0)).toBe(true);
    await settle(answer('1'));
    expect(balancesShown()).toEqual([1]);
    tree.unmount();
  });

  it('does not show stored balances once a trade has invalidated them', async () => {
    await rememberFromEarlierLaunch('0xW', '5');
    await act(async () => { invalidateWalletHoldings(); });
    const settle = pendingBalances();
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
    expect(seen.every(s => (s.holdings as unknown[]).length === 0)).toBe(true);
    expect(seen[seen.length - 1].loading).toBe(true);
    await settle(answer('2'));
    expect(balancesShown()).toEqual([2]);
    tree.unmount();
  });

  it('opening the picker joins a prefetch already under way instead of reading twice', async () => {
    (getActiveWallet as jest.Mock).mockResolvedValue('0xW');
    (getWalletBalances as jest.Mock).mockClear();
    const settle = pendingBalances();
    const prefetch = prefetchWalletHoldings();
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
    await settle(answer('3'));
    await prefetch;
    expect(getWalletBalances).toHaveBeenCalledTimes(1);
    expect(balancesShown()).toEqual([3]);
    tree.unmount();
  });

  it('a read started before a trade does not refill the cache afterwards', async () => {
    (getActiveWallet as jest.Mock).mockResolvedValue('0xW');
    const settleOld = pendingBalances();
    const prefetch = prefetchWalletHoldings();
    await act(async () => { await Promise.resolve(); });
    invalidateWalletHoldings();
    await settleOld(answer('9'));
    await prefetch;

    (getWalletBalances as jest.Mock).mockResolvedValueOnce(answer('4'));
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
    expect(seen.some(s => (s.holdings as { balance: number }[]).some(h => h.balance === 9))).toBe(false);
    expect(balancesShown()).toEqual([4]);
    tree.unmount();
  });

  it('keeps the stored balances when the fresh read fails on every chain', async () => {
    await rememberFromEarlierLaunch('0xW', '5');
    const settle = pendingBalances();
    for (const lookup of [getSolanaAddress, getBtcAddress, getSuiAddress]) {
      (lookup as jest.Mock).mockRejectedValueOnce(new Error('offline'));
    }
    let tree!: ReactTestRenderer;
    await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
    await settle({ success: false, message: 'offline' });
    expect(balancesShown()).toEqual([5]);
    expect(seen[seen.length - 1].loading).toBe(false);
    tree.unmount();
  });

  it('shows balances read over a minute ago while it reads them again', async () => {
    (getActiveWallet as jest.Mock).mockResolvedValue('0xW');
    (getWalletBalances as jest.Mock).mockResolvedValueOnce(answer('5'));
    await act(async () => { await prefetchWalletHoldings(); });
    const now = Date.now();
    const clock = jest.spyOn(Date, 'now').mockReturnValue(now + 61_000);
    try {
      const settle = pendingBalances();
      let tree!: ReactTestRenderer;
      await act(async () => { tree = TestRenderer.create(React.createElement(Probe, { enabled: true })); });
      // The read is still out, and no spinner for it.
      expect(seen[seen.length - 1].loading).toBe(false);
      expect(balancesShown()).toEqual([5]);
      await settle(answer('6'));
      expect(balancesShown()).toEqual([6]);
      tree.unmount();
    } finally {
      clock.mockRestore();
    }
  });
});
