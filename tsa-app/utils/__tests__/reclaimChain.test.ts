import { reclaimChainFor } from '@/utils/reclaimChain';

const chains = (...ids: number[]) => ids.map((chainId) => ({ chainId }));

describe('reclaimChainFor', () => {
  // Review I1: the liquidity dashboard's Withdraw opens the pool chain. A paused
  // LP must land on it, never on their Sonic balance.
  it('keeps the chain the screen was opened on, listed or not', () => {
    expect(reclaimChainFor(chains(146, 97), 97, 97)).toBe(97);
    expect(reclaimChainFor(chains(146), 97, 97)).toBe(97);
  });
  it('keeps a selection the list still serves', () => {
    expect(reclaimChainFor(chains(146, 56), 56)).toBe(56);
  });
  it('otherwise prefers Sonic, then the first listed chain', () => {
    expect(reclaimChainFor(chains(56, 14601), 146)).toBe(14601);
    expect(reclaimChainFor(chains(56), 146)).toBe(56);
  });
});
