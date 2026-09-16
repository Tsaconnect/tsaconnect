// services/__tests__/mcgpSellGuard.test.ts
//
// Pins the pre-prepare cap check and the revert→plain-message mapping that
// stop a naira buyer from ever seeing a raw "execution reverted: Insufficient
// bought premium" gas-estimation error.

import {
  checkSellableForUsdc,
  mapMcgpSellError,
  NAIRA_ONLY_SELL_MESSAGE,
  USDC_SELL_UNAVAILABLE_MESSAGE,
} from '../mcgpSellGuard';

describe('checkSellableForUsdc', () => {
  it('blocks an amount that exceeds the bought bucket', () => {
    const amount = '10000000000000000000'; // 10 MCGP
    const bought = '3000000000000000000'; // 3 MCGP bought
    expect(checkSellableForUsdc(amount, bought)).toBe(NAIRA_ONLY_SELL_MESSAGE);
  });

  it('blocks a naira-only wallet (bought = 0) outright', () => {
    expect(checkSellableForUsdc('1000000000000000000', '0')).toBe(NAIRA_ONLY_SELL_MESSAGE);
  });

  it('allows an amount fully covered by the bought bucket', () => {
    expect(checkSellableForUsdc('3000000000000000000', '5000000000000000000')).toBeNull();
  });

  it('allows an amount exactly equal to the bought bucket (boundary)', () => {
    expect(checkSellableForUsdc('5000000000000000000', '5000000000000000000')).toBeNull();
  });

  it('does not block a non-positive amount — the "enter an amount" validation owns that', () => {
    expect(checkSellableForUsdc('0', '0')).toBeNull();
    expect(checkSellableForUsdc('', '0')).toBeNull();
  });

  it('treats garbage input as zero rather than throwing', () => {
    expect(checkSellableForUsdc('not-a-number', '0')).toBeNull();
    expect(() => checkSellableForUsdc('1000000000000000000', 'not-a-number')).not.toThrow();
    expect(checkSellableForUsdc('1000000000000000000', 'not-a-number')).toBe(NAIRA_ONLY_SELL_MESSAGE);
  });
});

describe('mapMcgpSellError', () => {
  it('maps the "Insufficient bought premium" revert to the naira-only message', () => {
    const raw = 'Prepare failed: gas estimation failed: execution reverted: Insufficient bought premium';
    expect(mapMcgpSellError(raw)).toBe(NAIRA_ONLY_SELL_MESSAGE);
  });

  it('maps the "Insufficient USDC" revert to the temporarily-unavailable message', () => {
    const raw = 'Prepare failed: gas estimation failed: execution reverted: Insufficient USDC';
    expect(mapMcgpSellError(raw)).toBe(USDC_SELL_UNAVAILABLE_MESSAGE);
  });

  it('passes through an unrelated failure unchanged', () => {
    expect(mapMcgpSellError('Network error')).toBe('Network error');
  });

  it('falls back to a generic message when nothing is given', () => {
    expect(mapMcgpSellError(undefined)).toBe('Sell failed');
    expect(mapMcgpSellError(null)).toBe('Sell failed');
    expect(mapMcgpSellError('')).toBe('Sell failed');
  });
});
