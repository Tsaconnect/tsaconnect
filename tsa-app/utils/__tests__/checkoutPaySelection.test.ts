/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

// Checkout's unified "Pay with" picker (owner decision 2026-09-29): what she
// can pick, and what checkout should default to before she has chosen for
// herself. Pure logic — no rendering.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn(async () => null),
  setItem: jest.fn(async () => {}),
  removeItem: jest.fn(async () => {}),
}));
jest.mock('@/hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));

import {
  defaultPaySelection,
  isStableSymbol,
  orderTokenFor,
  payShortfallMessage,
  swapCandidatesFromHoldings,
  type SwapCandidate,
} from '../checkoutPaySelection';
import type { Holding } from '@/services/assetCatalog';

const tok = (symbol: string, chainId: number, usdValue: number): SwapCandidate => ({
  token: { address: `0x${symbol.toLowerCase()}${chainId}`, symbol, name: symbol, decimals: 6, logoURI: '', chainId },
  chainId,
  usdValue,
});

const holding = (over: Partial<Holding>): Holding => ({
  chainId: 56, address: '0xabc', native: false, trusted: true, symbol: 'BNB', name: 'BNB',
  decimals: 18, balance: 1, usdValue: 10, ...over,
});

describe('defaultPaySelection', () => {
  it('picks Sonic USDC when it covers the total', () => {
    const sel = defaultPaySelection({
      usdcBalance: 20, usdcNeeded: 15, mcgpBalance: 0, mcgpNeeded: 100, totalUsd: 15, swapCandidates: [],
    });
    expect(sel).toEqual({ kind: 'direct', token: 'USDC' });
  });

  it('falls to Spendable MCGP when USDC is short but MCGP covers it', () => {
    const sel = defaultPaySelection({
      usdcBalance: 2, usdcNeeded: 15, mcgpBalance: 500, mcgpNeeded: 400, totalUsd: 15, swapCandidates: [],
    });
    expect(sel).toEqual({ kind: 'mcgp' });
  });

  it('falls to the smallest sufficient swap holding when both are short, preferring stablecoins', () => {
    const sel = defaultPaySelection({
      usdcBalance: 0, usdcNeeded: 100, mcgpBalance: 0, mcgpNeeded: 400, totalUsd: 100,
      swapCandidates: [
        tok('BNB', 56, 500), // sufficient but not a stablecoin, and bigger
        tok('USDT', 56, 103), // sufficient, a stablecoin, smallest sufficient stablecoin
        tok('DAI', 1, 150), // sufficient stablecoin, bigger than USDT
        tok('SHIB', 1, 102), // sufficient but not a stablecoin, smaller than USDT — still loses to a stablecoin
      ],
    });
    expect(sel).toEqual({ kind: 'swap', token: expect.objectContaining({ symbol: 'USDT', chainId: 56 }), chainId: 56 });
  });

  it('picks the smallest sufficient holding when none are stablecoins', () => {
    const sel = defaultPaySelection({
      usdcBalance: 0, usdcNeeded: 100, mcgpBalance: 0, mcgpNeeded: 400, totalUsd: 100,
      swapCandidates: [tok('BNB', 56, 500), tok('SHIB', 1, 103)],
    });
    expect(sel).toEqual({ kind: 'swap', token: expect.objectContaining({ symbol: 'SHIB' }), chainId: 1 });
  });

  it('falls back to Sonic USDC (with the shortfall) when nothing covers the total', () => {
    const sel = defaultPaySelection({
      usdcBalance: 2, usdcNeeded: 100, mcgpBalance: 1, mcgpNeeded: 400, totalUsd: 100,
      swapCandidates: [tok('BNB', 56, 50)],
    });
    expect(sel).toEqual({ kind: 'direct', token: 'USDC' });
  });

  it('never blocks on an unknown (null) balance — it is skipped, not treated as short', () => {
    const sel = defaultPaySelection({
      usdcBalance: null, usdcNeeded: null, mcgpBalance: null, mcgpNeeded: null, totalUsd: null, swapCandidates: [],
    });
    expect(sel).toEqual({ kind: 'direct', token: 'USDC' });
  });

  it('requires the swap buffer (total × 1.02), not just the bare total', () => {
    const sel = defaultPaySelection({
      usdcBalance: 0, usdcNeeded: 100, mcgpBalance: 0, mcgpNeeded: 400, totalUsd: 100,
      swapCandidates: [tok('USDT', 56, 100.5)], // short of 102
    });
    expect(sel).toEqual({ kind: 'direct', token: 'USDC' });
  });
});

describe('orderTokenFor', () => {
  it('is USDC for no selection yet', () => expect(orderTokenFor(null)).toBe('USDC'));
  it('is the direct token for a direct pick', () => expect(orderTokenFor({ kind: 'direct', token: 'USDT' })).toBe('USDT'));
  it('is MCGP for a Spendable MCGP pick', () => expect(orderTokenFor({ kind: 'mcgp' })).toBe('MCGP'));
  it('is always USDC for a swap, whatever the source token is', () => {
    expect(orderTokenFor({ kind: 'swap', token: tok('BNB', 56, 1).token, chainId: 56 })).toBe('USDC');
  });
});

describe('swapCandidatesFromHoldings', () => {
  it('offers an EVM holding, in USD value', () => {
    const out = swapCandidatesFromHoldings([holding({ symbol: 'BNB', chainId: 56, balance: 1, usdValue: 600 })]);
    expect(out).toEqual([expect.objectContaining({ chainId: 56, usdValue: 600 })]);
  });

  it('excludes wallet MCGP', () => {
    const MCGP = '0x517600323e5e2938207fa2e2e915b9d80e5b2b21';
    const out = swapCandidatesFromHoldings([holding({ symbol: 'MCGP', chainId: 146, address: MCGP, balance: 10, usdValue: 5 })]);
    expect(out).toEqual([]);
  });

  it('excludes non-EVM holdings', () => {
    const out = swapCandidatesFromHoldings([holding({ symbol: 'SOL', chainId: 1151111081099710, native: true, balance: 1, usdValue: 100 })]);
    expect(out).toEqual([]);
  });

  it('excludes Sonic USDC itself (a swap into itself is not a swap)', () => {
    const SONIC_USDC = '0x29219dd400f2Bf60E5a23d13Be72B486D4038894';
    const out = swapCandidatesFromHoldings([holding({ symbol: 'USDC', chainId: 146, address: SONIC_USDC, balance: 5, usdValue: 5 })]);
    expect(out).toEqual([]);
  });

  it('keeps Sonic USDT — it can still swap into Sonic USDC', () => {
    const SONIC_USDT = '0x6047828dc181963ba44974801ff68e538da5eaf9';
    const out = swapCandidatesFromHoldings([holding({ symbol: 'USDT', chainId: 146, address: SONIC_USDT, balance: 5, usdValue: 5 })]);
    expect(out).toHaveLength(1);
  });

  it('skips zero balances', () => {
    expect(swapCandidatesFromHoldings([holding({ balance: 0 })])).toEqual([]);
  });
});

describe('isStableSymbol', () => {
  it('recognises the common stables, case-insensitively', () => {
    expect(isStableSymbol('usdc')).toBe(true);
    expect(isStableSymbol('USDT')).toBe(true);
    expect(isStableSymbol('DAI')).toBe(true);
  });
  it('is false for a volatile coin', () => {
    expect(isStableSymbol('BNB')).toBe(false);
    expect(isStableSymbol('SHIB')).toBe(false);
  });
});

describe('payShortfallMessage', () => {
  it('names the chain for a direct token shortfall', () => {
    expect(payShortfallMessage({ kind: 'direct', token: 'USDC', neededLabel: '0.16 USDC', haveLabel: '0.02 USDC' }))
      .toBe('Not enough USDC on Sonic. You need 0.16 USDC; you have 0.02 USDC. Try another way to pay.');
  });

  it('says Spendable MCGP is off-chain and that wallet MCGP cannot pay', () => {
    const msg = payShortfallMessage({ kind: 'mcgp', neededLabel: '10.0000 MCGP', haveLabel: '2.0000 MCGP' });
    expect(msg).toContain('Not enough Spendable MCGP');
    expect(msg).toContain("MCGP in your wallet can't pay");
  });
});
