// utils/__tests__/otcSell.test.ts
//
// The OTC sell form's limit: only MCGP the wallet bought on the OTC desk can
// be sold back, and the form must agree with the API that enforces it.
import {
  otcSellBlocker, otcSellableDecimal, otcSellableDisplay, hasOtcSellable,
  OTC_NOTHING_TO_SELL, OTC_POSITION_UNAVAILABLE,
} from '../otcSell';

const E18 = BigInt('1000000000000000000');
const wei = (whole: number, frac = BigInt(0)) => (BigInt(whole) * E18 + frac).toString();

describe('otcSellBlocker', () => {
  const sellable = wei(70); // bought 100, sold 30

  it('allows up to and including the sellable amount', () => {
    expect(otcSellBlocker('70', sellable)).toBeNull();
    expect(otcSellBlocker('0.5', sellable)).toBeNull();
    expect(otcSellBlocker(' 70 ', sellable)).toBeNull();
  });

  it('refuses more than the wallet bought on OTC, naming the cap', () => {
    const msg = otcSellBlocker('70.000000000000000001', sellable);
    expect(msg).toContain('up to 70 MCGP');
    expect(msg).toContain('purchased on the OTC desk');
  });

  it('compares in wei, not floating point', () => {
    // 5.2 MCGP exactly; Number() comparisons of 18-decimal strings drift.
    const exact = wei(5, BigInt('200000000000000000'));
    expect(otcSellBlocker('5.2', exact)).toBeNull();
    expect(otcSellBlocker('5.200000000000000001', exact)).not.toBeNull();
  });

  it('explains an empty position instead of calling it a balance problem', () => {
    expect(otcSellBlocker('1', '0')).toBe(OTC_NOTHING_TO_SELL);
  });

  it('refuses while the position is unknown (fails closed)', () => {
    expect(otcSellBlocker('1', null)).toBe(OTC_POSITION_UNAVAILABLE);
    expect(otcSellBlocker('1', undefined)).toBe(OTC_POSITION_UNAVAILABLE);
    expect(otcSellBlocker('1', 'garbage')).toBe(OTC_POSITION_UNAVAILABLE);
  });

  it('rejects invalid amounts', () => {
    for (const bad of ['', '0', 'abc', '-1', '1.0000000000000000001']) {
      expect(otcSellBlocker(bad, sellable)).toBe('Enter a valid amount.');
    }
  });
});

describe('Max and display', () => {
  it('Max fills the whole sellable at full precision', () => {
    expect(otcSellableDecimal(wei(70))).toBe('70');
    expect(otcSellableDecimal(wei(5, BigInt(1)))).toBe('5.000000000000000001');
    expect(otcSellableDecimal('0')).toBe('0');
    expect(otcSellableDecimal(null)).toBe('0');
    // Whatever Max fills in must pass the limit it came from.
    const s = wei(5, BigInt(1));
    expect(otcSellBlocker(otcSellableDecimal(s), s)).toBeNull();
  });

  it('display truncates to 6 decimals but never shows dust as 0', () => {
    expect(otcSellableDisplay(wei(5, BigInt('200000000000000000')))).toBe('5.2');
    expect(otcSellableDisplay(wei(1, BigInt('999999999000000000')))).toBe('1.999999');
    expect(otcSellableDisplay('1000')).toBe('0.000000000000001');
    expect(otcSellableDisplay('0')).toBe('0');
  });

  it('hasOtcSellable distinguishes none from unknown', () => {
    expect(hasOtcSellable(wei(1))).toBe(true);
    expect(hasOtcSellable('0')).toBe(false);
    expect(hasOtcSellable(null)).toBeNull();
  });
});
