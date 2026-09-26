import { anyTokenRowActions, mcgpRowActions, stableRowActions } from '@/utils/rampRow';

describe('mcgpRowActions', () => {
  it('offers both actions in NGN with a wallet', () => {
    expect(mcgpRowActions({ currency: 'NGN', hasWallet: true })).toEqual({ buy: true, sell: true });
  });

  // MCGP trades against a wallet this device can sign for, exactly like the
  // stablecoins, so the row must not offer Buy/Sell without one.
  it('asks for a wallet before anything else', () => {
    const noWallet = { buy: false, sell: false, unavailable: 'Connect a wallet to trade' };
    expect(mcgpRowActions({ currency: 'NGN', hasWallet: false })).toEqual(noWallet);
    expect(mcgpRowActions({ currency: 'UGX', hasWallet: false })).toEqual(noWallet);
  });

  // The MCGP buy/sell screens are NGN-only.
  it('names a currency the MCGP screens do not serve', () => {
    expect(mcgpRowActions({ currency: 'UGX', hasWallet: true })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'Not available in UGX',
    });
  });
});

const row = (over: Partial<Parameters<typeof stableRowActions>[0]> = {}) =>
  stableRowActions({
    token: 'USDC',
    currency: 'NGN',
    provider: 'paycrest',
    legs: { buy: true, sell: true },
    hasWallet: true,
    ...over,
  });

describe('stableRowActions', () => {
  it('offers both actions where both legs trade', () => {
    expect(row()).toEqual({ buy: true, sell: true });
  });

  // The case this whole mechanism exists for: Paycrest has offramp providers
  // for UGX and TZS and no onramp, so Buy must not be rendered at all. A
  // disabled button would still read as "try me"; absence is honest.
  it('offers sell alone for an offramp-only currency', () => {
    expect(row({ currency: 'UGX', legs: { buy: false, sell: true } })).toEqual({
      buy: false,
      sell: true,
    });
  });

  it('offers buy alone when only the onramp trades', () => {
    expect(row({ legs: { buy: true, sell: false } })).toEqual({ buy: true, sell: false });
  });

  // Distinct messages, because the user can act on exactly one of these.
  it('names a currency we do not serve', () => {
    expect(row({ currency: 'GHS', provider: '', legs: { buy: false, sell: false } })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'Not available in GHS',
    });
  });

  it('reads a provider with no live legs as paused, not unsupported', () => {
    expect(row({ currency: 'KES', legs: { buy: false, sell: false } })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'USDC trading is paused in KES',
    });
  });

  it('asks for a wallet before anything else', () => {
    expect(row({ hasWallet: false })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'Connect a wallet to trade',
    });
    // Even when the ramp itself is fine, and even when it is not.
    expect(row({ hasWallet: false, provider: '', legs: { buy: false, sell: false } })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'Connect a wallet to trade',
    });
  });

  // Legs never outrank the provider gate: a currency we do not serve cannot be
  // traded because the order book happens to mention it.
  it('never offers an action without a provider', () => {
    expect(row({ provider: '', legs: { buy: true, sell: true } })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'Not available in NGN',
    });
  });
});

const anyToken = (over: Partial<Parameters<typeof anyTokenRowActions>[0]> = {}) =>
  anyTokenRowActions({ currency: 'NGN', hasWallet: true, swappedSellEnabled: true, ...over });

describe('anyTokenRowActions', () => {
  it('offers both actions in NGN with a wallet and swapped-sell live', () => {
    expect(anyToken()).toEqual({ buy: true, sell: true });
  });

  // Buying delivers into her wallet the same way MCGP/stablecoins do, so the
  // same "wallet first" rule applies.
  it('asks for a wallet before anything else', () => {
    const noWallet = { buy: false, sell: false, unavailable: 'Connect a wallet to trade' };
    expect(anyToken({ hasWallet: false })).toEqual(noWallet);
    expect(anyToken({ hasWallet: false, currency: 'UGX' })).toEqual(noWallet);
  });

  // The any-token ramp is NGN-only.
  it('names a currency the any-token ramp does not serve', () => {
    expect(anyToken({ currency: 'UGX' })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'Not available in UGX',
    });
  });

  // RAMP_CONVERSION_SELL can be off while buy is live — Sell alone drops,
  // Buy is unaffected. There is no equivalent buy-side flag to check yet.
  it('offers buy alone when swapped-sell is off server-side', () => {
    expect(anyToken({ swappedSellEnabled: false })).toEqual({ buy: true, sell: false });
  });
});
