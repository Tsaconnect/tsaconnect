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
  // for UGX and TZS and no onramp (and, on 2026-10-02, none for NGN either).
  // A missing Buy read as a bug to the owner, so the button stays and a tap
  // says why instead of opening a screen that would fail.
  it('keeps a closed side as a message for its button', () => {
    expect(row({ currency: 'UGX', legs: { buy: false, sell: true } })).toEqual({
      buy: false,
      sell: true,
      closed: { buy: "Buying USDC with UGX isn't available right now. Please check again later." },
    });
    expect(row({ legs: { buy: true, sell: false } })).toEqual({
      buy: true,
      sell: false,
      closed: { sell: "Selling USDC for NGN isn't available right now. Please check again later." },
    });
  });

  // Distinct messages, because the user can act on exactly one of these.
  it('names a currency we do not serve', () => {
    expect(row({ currency: 'GHS', provider: '', legs: { buy: false, sell: false } })).toEqual({
      buy: false,
      sell: false,
      unavailable: 'Not available in GHS',
    });
  });

  // Nobody quoting either side is temporary too: both buttons stay and say so.
  it('reads a provider with no live legs as closed for now, not unsupported', () => {
    expect(row({ currency: 'KES', legs: { buy: false, sell: false } })).toEqual({
      buy: false,
      sell: false,
      closed: {
        buy: "Buying USDC with KES isn't available right now. Please check again later.",
        sell: "Selling USDC for KES isn't available right now. Please check again later.",
      },
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
  anyTokenRowActions({
    currency: 'NGN',
    hasWallet: true,
    swappedSellEnabled: true,
    legs: { buy: true, sell: true },
    ...over,
  });

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
  // Buy is unaffected.
  // A switched-off feature is not a gap in liquidity: no "check again later".
  it('offers buy alone when swapped-sell is off server-side', () => {
    expect(anyToken({ swappedSellEnabled: false })).toEqual({
      buy: true,
      sell: false,
      closed: { sell: "Selling other tokens isn't available at the moment." },
    });
  });

  // A buy here opens paycrest/buy and calls createBuy: the same Paycrest
  // onramp as the stablecoin rows. With no onramp in the book (NGN, 2026-10-02)
  // it would only fail at the quote, after she picked a token and an amount.
  it('follows the Paycrest legs, so a closed onramp closes Buy', () => {
    expect(anyToken({ legs: { buy: false, sell: true } })).toEqual({
      buy: false,
      sell: true,
      closed: { buy: "Buying other tokens with NGN isn't available right now. Please check again later." },
    });
    expect(anyToken({ legs: { buy: false, sell: false } })).toEqual({
      buy: false,
      sell: false,
      closed: {
        buy: "Buying other tokens with NGN isn't available right now. Please check again later.",
        sell: "Selling other tokens for NGN isn't available right now. Please check again later.",
      },
    });
  });

  // Before the currency list loads, or when it failed to, the legs are
  // UNKNOWN — not closed. The server makes the same call when it cannot read
  // Paycrest's book: report both sides open, because an over-reported side
  // costs one refused quote while an under-reported one takes the rail down.
  it('treats unknown legs as open, not closed', () => {
    expect(anyToken({ legs: null })).toEqual({ buy: true, sell: true });
    expect(anyToken({ legs: null, swappedSellEnabled: false })).toEqual({
      buy: true,
      sell: false,
      closed: { sell: "Selling other tokens isn't available at the moment." },
    });
  });
});
