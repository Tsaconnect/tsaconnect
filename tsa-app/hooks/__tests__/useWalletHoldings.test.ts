jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));
jest.mock('@/services/wallet', () => ({ getActiveWallet: jest.fn() }));
jest.mock('@/services/chains/solanaWallet', () => ({ getSolanaAddress: jest.fn(), getSolanaBalances: jest.fn() }));
jest.mock('@/services/chains/btcWallet', () => ({ getBtcAddress: jest.fn(), getBtcBalances: jest.fn() }));
jest.mock('@/services/chains/suiWallet', () => ({ getSuiAddress: jest.fn(), getSuiBalances: jest.fn() }));

// eslint-disable-next-line import/first -- jest hoists the mocks above.
import { holdingFromRow, useWalletHoldings, invalidateWalletHoldings } from '../useWalletHoldings';
// eslint-disable-next-line import/first
import React from 'react';
// eslint-disable-next-line import/first
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
// eslint-disable-next-line import/first
import { getActiveWallet } from '@/services/wallet';
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
  beforeEach(() => { seen = []; invalidateWalletHoldings(); });

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
});
