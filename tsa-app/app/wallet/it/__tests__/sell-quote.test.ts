// app/wallet/it/__tests__/sell-quote.test.ts
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
  WithdrawDestination,
  WithdrawQuote,
} from '../sell-quote';

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
