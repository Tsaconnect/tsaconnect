// utils/__tests__/stableSellQuote.test.ts
//
// Pure logic for the stablecoin sell screen. Tested here rather than by
// mounting the screen: this repo has no @testing-library/react-native, so
// money math is extracted to be testable at all (same convention as
// buyQuote.ts / sellQuote.ts).

// stableSellQuote.ts imports formatTokenAmount from services/p2p, which
// transitively imports walletApi.ts -> AsyncStorage. Mocked the same way
// services/p2p.test.ts does, so this module can load under plain Jest (no
// native module available) without touching real storage.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));

import {
  STABLE_MIN_SEND,
  STABLE_MAX_DECIMALS,
  POLYGON_STABLE_DECIMALS,
  ErrArrivalUnpriceable,
  belowMinimum,
  exceedsStablePrecision,
  estimateArrival,
  formatSendVsArrive,
} from '../stableSellQuote';

describe('belowMinimum', () => {
  it('rejects amounts under the provider floor', () => {
    // Flutterwave's documented outbound floor is 10 USDC/USDT. Quoting under
    // it lets a user broadcast irreversibly and only then be rejected, with
    // the funds already at Flutterwave.
    expect(belowMinimum('USDC', '1')).toBe(true);
    expect(belowMinimum('USDC', '9.999999')).toBe(true);
    expect(belowMinimum('USDT', '0')).toBe(true);
  });

  it('accepts the floor exactly and above', () => {
    expect(belowMinimum('USDC', STABLE_MIN_SEND.USDC)).toBe(false);
    expect(belowMinimum('USDC', '10')).toBe(false);
    expect(belowMinimum('USDT', '100.5')).toBe(false);
  });

  it('treats junk as below the minimum rather than throwing', () => {
    // The amount comes from a text input; a half-typed "1." must not crash
    // the screen or, worse, pass the guard.
    expect(belowMinimum('USDC', '')).toBe(true);
    expect(belowMinimum('USDC', '.')).toBe(true);
    expect(belowMinimum('USDC', 'abc')).toBe(true);
  });

  it('rejects scientific notation, which the very next call would throw on', () => {
    // Number('1e1') is 10 and clears the floor, but parseTokenAmount
    // (p2p.ts:874-880) THROWS on scientific notation. A guard that admits an
    // input the next call rejects turns a typo into a crashed quote effect.
    // Both use the same /^\d+(\.\d+)?$/ shape.
    expect(belowMinimum('USDC', '1e1')).toBe(true);
    expect(belowMinimum('USDC', '1e3')).toBe(true);
    expect(belowMinimum('USDC', '0x10')).toBe(true);
  });
});

describe('exceedsStablePrecision', () => {
  it('accepts up to six fractional digits', () => {
    expect(exceedsStablePrecision('10')).toBe(false);
    expect(exceedsStablePrecision('10.1')).toBe(false);
    expect(exceedsStablePrecision('10.123456')).toBe(false);
    expect(STABLE_MAX_DECIMALS).toBe(6);
  });

  it('rejects more than six, which the backend 400s on', () => {
    // StableToBaseUnits rejects anything with more than 6 fractional digits
    // (stable_assets.go:89-92) → ErrStablePrecision → 400. Caught at the input
    // instead, a user typing 10.1234567 gets "at most 6 decimal places"
    // rather than an unexplained failure at the quote.
    expect(exceedsStablePrecision('10.1234567')).toBe(true);
    expect(exceedsStablePrecision('0.0000001')).toBe(true);
  });
});

describe('estimateArrival', () => {
  it('returns the sent amount unchanged when there is no bridge', () => {
    // Polygon source: no LiFi, so what is sent is what arrives.
    expect(estimateArrival('100', null, 6)).toBe('100');
  });

  it('converts LiFi base units to a major-unit string', () => {
    // 99.884217 USDC delivered against 100 sent.
    expect(estimateArrival('100', '99884217', 6)).toBe('99.884217');
  });

  it('does not confuse 6-decimal and 18-decimal scaling', () => {
    // The whole point of threading decimals through: 99884217 at 18 decimals
    // is dust, not 99.88. Pin the EXACT wrong answer rather than asserting
    // "something changed" — `.not.toBe(...)` passes against any bug at all.
    expect(estimateArrival('100', '99884217', 18)).toBe('0.000000000099884217');
  });

  it('THROWS when LiFi returned nothing usable, rather than quoting the typed amount', () => {
    // The bug this replaces: treating '0' as "no bridge" made the app quote
    // against what the user TYPED, which is exactly the bait-and-switch the
    // arrival estimate exists to prevent — and silently. `null` means "no
    // bridge" (Polygon only); an unusable LiFi answer must block the quote.
    //
    // Assert the CODE, not the message. toThrow(instance) compares message
    // equality, so a message-only assertion also passes for an unrelated
    // error a later refactor happens to throw with the same text.
    expect(() => estimateArrival('100', '', 6)).toThrow(ErrArrivalUnpriceable);
    expect(() => estimateArrival('100', '0', 6)).toThrow(ErrArrivalUnpriceable);
    try {
      estimateArrival('100', 'not-a-number', 6);
      throw new Error('expected estimateArrival to throw');
    } catch (e) {
      expect(e).toBeInstanceOf(ErrArrivalUnpriceable);
      expect((e as ErrArrivalUnpriceable).code).toBe('arrival_unpriceable');
    }
  });

  it('uses DESTINATION decimals, never the source chain\'s', () => {
    // LiFi's toAmount is always denominated on Polygon (toChain 137), where
    // USDC/USDT are 6 decimals. BSC stablecoins are genuinely 18. Using the
    // source's decimals here yields 0.000000000099884217, which the backend
    // rejects outright as over-precise.
    expect(estimateArrival('100', '99884217', POLYGON_STABLE_DECIMALS)).toBe('99.884217');
  });
});

describe('formatSendVsArrive', () => {
  it('returns null when nothing is lost in transit', () => {
    // Same-chain: showing "send 100 · arrives 100" is noise.
    expect(formatSendVsArrive('100', '100')).toBeNull();
  });

  it('describes the shortfall when a bridge takes a cut', () => {
    expect(formatSendVsArrive('100', '99.884217')).toBe('You send 100 · about 99.884217 arrives');
  });
});
