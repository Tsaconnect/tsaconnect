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
  exceedsBalance,
  exceedsStablePrecision,
  estimateArrival,
  formatKobo,
  formatSendVsArrive,
  isLikelyTxHash,
  subtractKobo,
  truncateToStableDecimals,
  STABLE_SELL_STATUSES,
  statusToHops,
  isTerminalStableStatus,
} from '../stableSellQuote';

describe('belowMinimum', () => {
  it('rejects amounts under the provider floor', () => {
    // Quoting under the floor lets a user broadcast irreversibly and only then
    // be rejected, with the funds already at Flutterwave. Derived from
    // STABLE_MIN_SEND rather than hardcoded: the floor is our own assumption
    // and gets tuned, and a test that pins the number would either fail on
    // every change or, worse, be "fixed" by deleting the case.
    const justUnder = (Number(STABLE_MIN_SEND.USDC) - 0.000001).toFixed(6);
    expect(belowMinimum('USDC', justUnder)).toBe(true);
    expect(belowMinimum('USDT', '0')).toBe(true);
  });

  it('accepts the floor exactly and above', () => {
    expect(belowMinimum('USDC', STABLE_MIN_SEND.USDC)).toBe(false);
    expect(belowMinimum('USDT', String(Number(STABLE_MIN_SEND.USDT) + 90.5))).toBe(false);
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

describe('belowMinimum applied to the ARRIVAL', () => {
  it('catches a bridged send that clears the floor but lands under it', () => {
    // The floor is the OUTBOUND minimum, so it governs what ARRIVES. Sending
    // exactly the floor from BSC clears it; a 1.2% bridge shortfall does not,
    // and by then the funds are at Flutterwave and nothing app-side can undo
    // the send. The screen composes the quote against the arrival for exactly
    // this reason — the floor has to follow the same rule.
    //
    // Derived from STABLE_MIN_SEND so the case keeps its meaning at any floor.
    const floor = STABLE_MIN_SEND.USDC;
    const shortfall = String(Math.round(Number(floor) * 0.988 * 1e6));
    const arrival = estimateArrival(floor, shortfall, POLYGON_STABLE_DECIMALS);
    expect(Number(arrival)).toBeLessThan(Number(floor));
    expect(belowMinimum('USDC', floor)).toBe(false);
    expect(belowMinimum('USDC', arrival)).toBe(true);
  });
});

describe('formatKobo', () => {
  it('renders a minor-unit integer string without ever touching a float', () => {
    expect(formatKobo('15500000')).toBe('₦155,000.00');
    expect(formatKobo('14820055')).toBe('₦148,200.55');
    expect(formatKobo('5')).toBe('₦0.05');
    expect(formatKobo('-2500')).toBe('-₦25.00');
  });

  it('keeps full precision on a payout Number() would round', () => {
    // 9007199254740993 kobo is Number.MAX_SAFE_INTEGER + 2: `Number(kobo)/100`
    // loses the last digit here. String math does not, which is the whole
    // reason money stays a string until this function.
    expect(formatKobo('9007199254740993')).toBe('₦90,071,992,547,409.93');
  });

  it('shows a dash rather than ₦0.00 for a missing figure', () => {
    // An absent field is not zero naira. Rendering "₦0.00" for one would tell
    // a user their payout is nothing.
    expect(formatKobo(undefined)).toBe('—');
    expect(formatKobo(null)).toBe('—');
    expect(formatKobo('')).toBe('—');
  });
});

describe('subtractKobo', () => {
  it('subtracts as integers', () => {
    expect(subtractKobo('15500000', '15100000')).toBe('400000');
  });

  it('returns null rather than a wrong number when either side is missing', () => {
    expect(subtractKobo(null, '1')).toBeNull();
    expect(subtractKobo('1', undefined)).toBeNull();
    expect(subtractKobo('abc', '1')).toBeNull();
  });
});

describe('truncateToStableDecimals', () => {
  it('truncates an 18-decimal balance to what the backend accepts', () => {
    // BSC's USDT/USDC really are 18 decimals, so Max would otherwise fill in a
    // value the app's own precision guard rejects.
    expect(truncateToStableDecimals('12.345678901234567890')).toBe('12.345678');
    expect(exceedsStablePrecision(truncateToStableDecimals('12.345678901234567890'))).toBe(false);
  });

  it('never rounds UP past the real balance', () => {
    // Rounding would push Max above what the wallet holds and fail the send
    // on-chain, after gas.
    expect(truncateToStableDecimals('12.3456789')).toBe('12.345678');
  });

  it('drops a trailing-zero fraction entirely', () => {
    expect(truncateToStableDecimals('12.000000000')).toBe('12');
    expect(truncateToStableDecimals('12')).toBe('12');
  });
});

describe('exceedsBalance', () => {
  it('compares as strings, not floats', () => {
    expect(exceedsBalance('10.5', '10.4')).toBe(true);
    expect(exceedsBalance('10.4', '10.5')).toBe(false);
    expect(exceedsBalance('10', '10')).toBe(false);
    // Different fractional widths must line up before comparing.
    expect(exceedsBalance('10.1', '10.05')).toBe(true);
    expect(exceedsBalance('9.999999999999999999', '10')).toBe(false);
  });

  it('does not trip on the last digit of an 18-decimal balance', () => {
    // Number('1234.567890123456789') === Number('1234.5678901234567891'):
    // a float comparison here calls a valid Max send an over-send.
    const balance = '1234.567890123456789';
    expect(exceedsBalance(balance, balance)).toBe(false);
    expect(exceedsBalance('1234.567890123456790', balance)).toBe(true);
  });

  it('SKIPS the guard rather than failing it on unparseable input', () => {
    // An unknown or malformed balance must not become a locked form with no
    // explanation — the chain rejects an over-send anyway.
    expect(exceedsBalance('10', 'unknown')).toBe(false);
    expect(exceedsBalance('1e1', '5')).toBe(false);
    expect(exceedsBalance('', '5')).toBe(false);
  });
});

describe('isLikelyTxHash', () => {
  it('accepts a 32-byte hex hash in either case', () => {
    expect(isLikelyTxHash(`0x${'a'.repeat(64)}`)).toBe(true);
    expect(isLikelyTxHash(`0x${'A1b2'.repeat(16)}`)).toBe(true);
    expect(isLikelyTxHash(` 0x${'0'.repeat(64)} `)).toBe(true);
  });

  it('rejects a paste error before it is reported against an intent', () => {
    expect(isLikelyTxHash('')).toBe(false);
    expect(isLikelyTxHash('0xdeadbeef')).toBe(false); // truncated
    expect(isLikelyTxHash('a'.repeat(64))).toBe(false); // no 0x
    expect(isLikelyTxHash(`0x${'z'.repeat(64)}`)).toBe(false); // not hex
    expect(isLikelyTxHash(`0x${'a'.repeat(65)}`)).toBe(false); // too long
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

describe('statusToHops', () => {
  it('maps every status the backend can emit', () => {
    // The guard that matters: the backend added four statuses in one branch.
    // If a fifth arrives and nothing maps it, this fails here rather than
    // rendering an empty progress screen to someone whose money is in flight.
    //
    // `length > 0` and "labels are non-empty" are NOT enough: the unknown
    // fallback returns all four hops with their labels, all `pending`, so an
    // unmapped status would sail through. Assert something only a MAPPED
    // status can satisfy — at least one hop is not `pending`. True for all
    // nine below (settled is all-done; reversed/failed/cancelled carry a
    // `failed`; the rest carry an `active`) and false for a tenth nobody
    // mapped.
    for (const status of STABLE_SELL_STATUSES) {
      const hops = statusToHops(status);
      expect(hops.length).toBeGreaterThan(0);
      expect(hops.every((h) => h.label.length > 0)).toBe(true);
      expect(hops.some((h) => h.state !== 'pending')).toBe(true);
    }
  });

  it('advances the active hop as the sell progresses', () => {
    const reported = statusToHops('deposit_reported');
    expect(reported.find((h) => h.key === 'sent')?.state).toBe('done');
    expect(reported.find((h) => h.key === 'arrived')?.state).toBe('active');

    const converting = statusToHops('converting');
    expect(converting.find((h) => h.key === 'arrived')?.state).toBe('done');
    expect(converting.find((h) => h.key === 'converted')?.state).toBe('active');
  });

  it('marks every hop done when settled', () => {
    expect(statusToHops('settled').every((h) => h.state === 'done')).toBe(true);
  });

  it('fails the PAYOUT hop on a reversal, not the wallet transfer', () => {
    // `reversed` means the funds DID send, DID arrive and WERE converted — the
    // payout is what failed. Marking hop 0 failed would tell a user their
    // wallet transfer failed when it succeeded and their money is sitting at
    // Flutterwave. Assert WHICH hop failed, not merely that one did.
    const hops = statusToHops('reversed');
    expect(hops.find((h) => h.key === 'sent')?.state).toBe('done');
    expect(hops.find((h) => h.key === 'arrived')?.state).toBe('done');
    expect(hops.find((h) => h.key === 'converted')?.state).toBe('done');
    expect(hops.find((h) => h.key === 'paid')?.state).toBe('failed');
  });

  it('fails the FIRST hop when the sell was cancelled before anything moved', () => {
    // cancelled/failed can occur before a deposit ever lands, so nothing may
    // be claimed as done. Assert WHICH hop failed — `every(h => h.state !==
    // 'done')` is the same too-weak shape this suite already rejected for
    // `reversed`, and passes against an all-`pending` implementation that
    // shows no failure at all.
    const hops = statusToHops('cancelled');
    expect(hops[0].state).toBe('failed');
    expect(hops.every((h) => h.state !== 'done')).toBe(true);
  });

  it('never claims progress for an unknown status', () => {
    // Fail visibly-neutral, not falsely-complete. Assert the exact fallback
    // shape: `!== 'done'` alone also passes an implementation that returns
    // `active` for every hop, which reads as four things happening at once.
    const hops = statusToHops('something_new');
    expect(hops.every((h) => h.state === 'pending')).toBe(true);
  });
});

describe('isTerminalStableStatus', () => {
  it('stops polling only on terminal states', () => {
    expect(isTerminalStableStatus('settled')).toBe(true);
    expect(isTerminalStableStatus('reversed')).toBe(true);
    expect(isTerminalStableStatus('failed')).toBe(true);
    expect(isTerminalStableStatus('cancelled')).toBe(true);

    expect(isTerminalStableStatus('pending_deposit')).toBe(false);
    expect(isTerminalStableStatus('deposit_reported')).toBe(false);
    expect(isTerminalStableStatus('deposit_confirmed')).toBe(false);
    expect(isTerminalStableStatus('converting')).toBe(false);
    // `debited` means the payout is in flight but the money has NOT reached
    // the user's bank — polling must continue.
    expect(isTerminalStableStatus('debited')).toBe(false);
  });
});
