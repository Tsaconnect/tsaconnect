jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));
jest.mock('@/services/walletApi', () => ({ getAuthHeaders: jest.fn(async () => ({})) }));

/* eslint-disable import/first -- jest.mock factories hoist above these imports. */
import { formatTokenAmount, p2pTokenDecimals } from '@/services/p2p';
import { hasEscrowBalance, reclaimChainFor } from '@/utils/reclaimChain';
/* eslint-enable import/first */

const chains = (...ids: number[]) => ids.map((chainId) => ({ chainId }));

describe('reclaimChainFor', () => {
  // Review I1: the liquidity dashboard's Withdraw opens the pool chain. A paused
  // LP must land on it, never on their Sonic balance.
  it('keeps the chain the screen was opened on, listed or not', () => {
    expect(reclaimChainFor(chains(146, 97), { requested: 97, funded: [146] })).toBe(97);
    expect(reclaimChainFor(chains(146), { requested: 97 })).toBe(97);
  });
  it('keeps a pick the list still serves, over a funded chain', () => {
    expect(reclaimChainFor(chains(146, 56), { picked: 146, funded: [56] })).toBe(146);
    expect(reclaimChainFor(chains(56), { picked: 146 })).toBe(56);
  });
  // Review M3: a late balance probe never moves her off the chain she typed
  // an amount on (its decimals and balance are what the amount means).
  it('keeps the chain she typed an amount on, over a funded chain', () => {
    expect(reclaimChainFor(chains(56, 146), { current: 56, engaged: true, funded: [146] })).toBe(56);
    expect(reclaimChainFor(chains(56, 146), { current: 56, engaged: false, funded: [146] })).toBe(146);
    expect(reclaimChainFor(chains(56), { current: 146, engaged: true })).toBe(56);
    expect(reclaimChainFor(chains(56, 146), { picked: 146, current: 56, engaged: true })).toBe(146);
  });
  it('prefers the chain holding a balance — an old Sonic balance included', () => {
    expect(reclaimChainFor(chains(56, 146), { funded: [146] })).toBe(146);
    expect(reclaimChainFor(chains(146, 56), { funded: [146, 56] })).toBe(56);
  });
  it('with no balance anywhere, prefers BSC, then Sonic, then the first listed chain', () => {
    expect(reclaimChainFor(chains(146, 56))).toBe(56);
    expect(reclaimChainFor(chains(8453, 14601))).toBe(14601);
    expect(reclaimChainFor(chains(8453, 1))).toBe(1);
  });
});

describe('hasEscrowBalance', () => {
  it('counts available or locked base units', () => {
    expect(hasEscrowBalance({ available: '0', locked: '5' })).toBe(true);
    expect(hasEscrowBalance({ available: '1', locked: '0' })).toBe(true);
    expect(hasEscrowBalance({ available: '0', locked: '0' })).toBe(false);
    expect(hasEscrowBalance({ available: '—', locked: '' })).toBe(false);
    expect(hasEscrowBalance(null)).toBe(false);
  });
});

describe('pool balance card decimals', () => {
  it('formats a BSC balance at 18 dp and a Sonic one at 6', () => {
    expect(formatTokenAmount('12500000000000000000', p2pTokenDecimals(56, 'USDT'), 4)).toBe('12.5');
    expect(formatTokenAmount('12500000', p2pTokenDecimals(146, 'USDT'), 4)).toBe('12.5');
  });
});
