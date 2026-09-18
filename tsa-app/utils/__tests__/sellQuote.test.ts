// utils/__tests__/sellQuote.test.ts
//
// Tests for Task 2.6: the IT sell screen's currency/rail destination picker
// and live FX quote. We test the pure logic that drives the screen —
// needsQuote / pickDefaultDestination / formatReceiveEstimate /
// isSellBlockedByQuote / destination-completeness — rather than mounting the
// screen itself (no @testing-library/react-native in this repo; the p2p
// pm-cta tests set the precedent for this pattern).

import {
  needsQuote,
  pickDefaultDestination,
  formatReceiveEstimate,
  isSellBlockedByQuote,
  isMobileMoneyDestinationComplete,
  isDestinationReadyToSubmit,
  mcgpAmountToNgn,
  sellQuoteRequest,
  exceedsSpendable,
  formatKobo,
  formatMcgp,
  sellQuoteMoved,
  WithdrawDestination,
  WithdrawQuote,
} from '@/utils/sellQuote';

const CATALOG: WithdrawDestination[] = [
  { currency: 'NGN', rail: 'bank', label: 'Nigerian Bank Account' },
  { currency: 'GHS', rail: 'bank', label: 'Ghanaian Bank Account' },
  { currency: 'XOF', rail: 'mobile_money', network: 'wave', label: 'Wave Mobile Money' },
];

describe('needsQuote', () => {
  it('is false for NGN (source currency — no FX quote)', () => {
    expect(needsQuote('NGN')).toBe(false);
  });

  it('is case-insensitive and trims whitespace for NGN', () => {
    expect(needsQuote(' ngn ')).toBe(false);
  });

  it('is true for any non-NGN currency', () => {
    expect(needsQuote('GHS')).toBe(true);
    expect(needsQuote('XOF')).toBe(true);
  });
});

describe('pickDefaultDestination', () => {
  it("defaults to the user's display currency when it's in the catalog", () => {
    expect(pickDefaultDestination('GHS', CATALOG)).toEqual(CATALOG[1]);
  });

  it('is case-insensitive on the display currency code', () => {
    expect(pickDefaultDestination('ghs', CATALOG)).toEqual(CATALOG[1]);
  });

  it('falls back to NGN when the display currency is not in the catalog', () => {
    expect(pickDefaultDestination('USD', CATALOG)).toEqual(CATALOG[0]);
  });

  it('falls back to the first catalog entry when neither the display currency nor NGN is present', () => {
    const noNgn = CATALOG.slice(1);
    expect(pickDefaultDestination('USD', noNgn)).toEqual(noNgn[0]);
  });

  it('returns null for an empty catalog', () => {
    expect(pickDefaultDestination('NGN', [])).toBeNull();
  });
});

describe('formatReceiveEstimate', () => {
  it('is empty for NGN — no quote row shown', () => {
    expect(
      formatReceiveEstimate({ currency: 'NGN', quote: null, loading: false, error: null }),
    ).toBe('');
  });

  it('shows a loading message while the quote is in flight', () => {
    expect(
      formatReceiveEstimate({ currency: 'GHS', quote: null, loading: true, error: null }),
    ).toBe('Fetching live rate…');
  });

  it('surfaces the error message when the quote failed (e.g. 503)', () => {
    expect(
      formatReceiveEstimate({
        currency: 'GHS',
        quote: null,
        loading: false,
        error: 'Instant Transfer FX quote is temporarily unavailable',
      }),
    ).toBe('Instant Transfer FX quote is temporarily unavailable');
  });

  it('formats "≈ amount CCY" from a live quote', () => {
    const quote: WithdrawQuote = { destCurrency: 'GHS', destAmount: 1234.5, rate: 0.0074, stale: false };
    expect(
      formatReceiveEstimate({ currency: 'GHS', quote, loading: false, error: null }),
    ).toBe('≈ 1,234.5 GHS');
  });

  it('is empty when nothing has loaded yet and there is no error', () => {
    expect(
      formatReceiveEstimate({ currency: 'GHS', quote: null, loading: false, error: null }),
    ).toBe('');
  });
});

describe('isSellBlockedByQuote', () => {
  it('never blocks NGN (no quote needed)', () => {
    expect(
      isSellBlockedByQuote({ currency: 'NGN', quote: null, loading: false, error: null }),
    ).toBe(false);
  });

  it('blocks while the quote is loading', () => {
    expect(
      isSellBlockedByQuote({ currency: 'GHS', quote: null, loading: true, error: null }),
    ).toBe(true);
  });

  it('blocks when the quote errored (400/503)', () => {
    expect(
      isSellBlockedByQuote({ currency: 'GHS', quote: null, loading: false, error: 'unavailable' }),
    ).toBe(true);
  });

  it('blocks when no quote has been fetched yet', () => {
    expect(
      isSellBlockedByQuote({ currency: 'GHS', quote: null, loading: false, error: null }),
    ).toBe(true);
  });

  it('blocks when the quote is flagged stale', () => {
    const quote: WithdrawQuote = { destCurrency: 'GHS', destAmount: 100, rate: 0.0074, stale: true };
    expect(
      isSellBlockedByQuote({ currency: 'GHS', quote, loading: false, error: null }),
    ).toBe(true);
  });

  it('does not block on a fresh, non-stale quote', () => {
    const quote: WithdrawQuote = { destCurrency: 'GHS', destAmount: 100, rate: 0.0074, stale: false };
    expect(
      isSellBlockedByQuote({ currency: 'GHS', quote, loading: false, error: null }),
    ).toBe(false);
  });
});

describe('isMobileMoneyDestinationComplete', () => {
  it('is false when country is missing', () => {
    expect(isMobileMoneyDestinationComplete({ mobileCountry: '', mobileMsisdn: '221771234567' })).toBe(false);
  });

  it('is false when phone number is missing', () => {
    expect(isMobileMoneyDestinationComplete({ mobileCountry: 'SN', mobileMsisdn: '' })).toBe(false);
  });

  it('is true when both are present', () => {
    expect(isMobileMoneyDestinationComplete({ mobileCountry: 'SN', mobileMsisdn: '221771234567' })).toBe(true);
  });
});

describe('mcgpAmountToNgn', () => {
  it('converts MCGP amount to NGN via kobo-per-MCGP rate', () => {
    // 10 MCGP at 150000 kobo (₦1,500) per MCGP = ₦15,000
    expect(mcgpAmountToNgn('10', '150000')).toBe(15000);
  });

  it('returns 0 for a non-numeric amount', () => {
    expect(mcgpAmountToNgn('', '150000')).toBe(0);
    expect(mcgpAmountToNgn('abc', '150000')).toBe(0);
  });

  it('returns 0 for a zero or negative amount', () => {
    expect(mcgpAmountToNgn('0', '150000')).toBe(0);
    expect(mcgpAmountToNgn('-5', '150000')).toBe(0);
  });

  it('returns 0 when the rate is missing or zero', () => {
    expect(mcgpAmountToNgn('10', '0')).toBe(0);
    expect(mcgpAmountToNgn('10', '')).toBe(0);
  });
});

describe('isDestinationReadyToSubmit', () => {
  it('is false with no destination selected', () => {
    expect(isDestinationReadyToSubmit(null, { mobileCountry: '', mobileMsisdn: '' })).toBe(false);
  });

  it('is true for a bank rail regardless of bank fields (server allows manual fallback)', () => {
    expect(isDestinationReadyToSubmit(CATALOG[0], { mobileCountry: '', mobileMsisdn: '' })).toBe(true);
  });

  it('is false for mobile_money without country/phone', () => {
    expect(isDestinationReadyToSubmit(CATALOG[2], { mobileCountry: '', mobileMsisdn: '' })).toBe(false);
  });

  it('is true for mobile_money once country/phone are filled', () => {
    expect(
      isDestinationReadyToSubmit(CATALOG[2], { mobileCountry: 'SN', mobileMsisdn: '221771234567' }),
    ).toBe(true);
  });
});

// ─── Two-step MCGP sell (amount → payout) ─────────────────────────────────

describe('sellQuoteRequest', () => {
  it('turns an MCGP amount into exact wei', () => {
    expect(sellQuoteRequest('mcgp', '1.5')).toEqual({ ok: true, mcgpAmount: '1500000000000000000' });
  });
  it('turns a naira amount into kobo without float drift', () => {
    expect(sellQuoteRequest('ngn', '50000')).toEqual({ ok: true, receiveKobo: '5000000' });
    // 0.1 + 0.2 style drift would give 1000.2999… → 100029
    expect(sellQuoteRequest('ngn', '1000.30')).toEqual({ ok: true, receiveKobo: '100030' });
  });
  it('is silent for an empty or zero entry', () => {
    expect(sellQuoteRequest('mcgp', '')).toEqual({ ok: false, error: null });
    expect(sellQuoteRequest('ngn', '0')).toEqual({ ok: false, error: null });
    expect(sellQuoteRequest('ngn', '.')).toEqual({ ok: false, error: null });
  });
  it('refuses sub-kobo naira instead of rounding it', () => {
    const r = sellQuoteRequest('ngn', '100.555');
    expect(r.ok).toBe(false);
    expect(r.ok === false && r.error).toMatch(/2 decimal/);
  });
});

describe('exceedsSpendable', () => {
  const spendable = '5000000000000000000'; // 5 MCGP
  it('allows exactly the spendable balance', () => {
    expect(exceedsSpendable('5000000000000000000', spendable)).toBe(false);
  });
  it('refuses one wei over', () => {
    expect(exceedsSpendable('5000000000000000001', spendable)).toBe(true);
  });
  it('fails closed on an unreadable figure', () => {
    expect(exceedsSpendable('abc', spendable)).toBe(true);
    expect(exceedsSpendable('1', '')).toBe(true);
  });
});

describe('formatKobo', () => {
  it('renders kobo as grouped naira with two decimals', () => {
    expect(formatKobo('5000000')).toBe('₦50,000.00');
    expect(formatKobo('12345678905')).toBe('₦123,456,789.05');
    expect(formatKobo('7')).toBe('₦0.07');
  });
  it('shows a dash for nothing usable', () => {
    expect(formatKobo('')).toBe('—');
  });
});

describe('formatMcgp', () => {
  it('rounds UP so the amount leaving the wallet is never understated', () => {
    expect(formatMcgp('1754385964912280702')).toBe('1.754386');
    expect(formatMcgp('1000000000000000001')).toBe('1.000001');
  });
  it('trims trailing zeros and groups thousands', () => {
    expect(formatMcgp('1754000000000000000000')).toBe('1,754');
    expect(formatMcgp('1500000000000000000')).toBe('1.5');
  });
});

describe('sellQuoteMoved', () => {
  const quoted = { mcgpAmount: '100', netKobo: '5000000' };
  it('is null when nothing moved', () => {
    expect(sellQuoteMoved(quoted, { ...quoted })).toBeNull();
  });
  it('names the new payout when the receive figure moved', () => {
    expect(sellQuoteMoved(quoted, { mcgpAmount: '100', netKobo: '4990000' })).toBe(
      'The rate moved: you now receive ₦49,900.00 (was ₦50,000.00).',
    );
  });
  it('names the new MCGP when only the amount sold moved', () => {
    const msg = sellQuoteMoved(
      { mcgpAmount: '1000000000000000000', netKobo: '5000000' },
      { mcgpAmount: '1100000000000000000', netKobo: '5000000' },
    );
    expect(msg).toBe('The rate moved: you now sell 1.1 MCGP (was 1 MCGP) to receive ₦50,000.00.');
  });
});
