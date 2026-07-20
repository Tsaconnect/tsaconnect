import type { CurrencyConfig } from "../../constants/currencies";
import { formatMicroPrice, formatPrice, formatUSD, isMicroPrice } from "../formatPrice";

const NGN: CurrencyConfig = {
  code: "NGN",
  symbol: "₦",
  name: "Nigerian Naira",
  locale: "en-NG",
};

const ANY_SUBSCRIPT = /[₀-₉]/u;

describe("isMicroPrice", () => {
  it("accepts the open interval (0, 0.01)", () => {
    expect(isMicroPrice(0.0000012)).toBe(true);
    expect(isMicroPrice(0.009999)).toBe(true);
  });
  it("rejects 0.01 and above (boundary is exclusive)", () => {
    expect(isMicroPrice(0.01)).toBe(false);
    expect(isMicroPrice(1.23)).toBe(false);
  });
  it("rejects zero, negatives, and non-finite values", () => {
    expect(isMicroPrice(0)).toBe(false);
    expect(isMicroPrice(-0.001)).toBe(false);
    expect(isMicroPrice(NaN)).toBe(false);
    expect(isMicroPrice(Infinity)).toBe(false);
  });
});

describe("formatMicroPrice", () => {
  it("compresses leading zeros into a subscript count (no currency symbol)", () => {
    expect(formatMicroPrice(0.0000012)).toBe("0.0₅12");
    expect(formatMicroPrice(0.000001)).toBe("0.0₅1");
    expect(formatMicroPrice(0.00001234)).toBe("0.0₄1234");
  });

  it("keeps the plain form below 4 leading zeros (compression threshold)", () => {
    // 0.00234 is inside the micro band but has only 2 leading zeros — the
    // plain form is just as short and easier to read, so no subscript.
    expect(formatMicroPrice(0.00234)).toBe("0.00234");
    expect(formatMicroPrice(0.0001)).toBe("0.0001"); // 3 zeros → plain
    expect(formatMicroPrice(0.00009999)).toBe("0.0₄9999"); // 4 zeros → compressed
  });

  it("never leaks scientific notation for exponent-style inputs", () => {
    expect(formatMicroPrice(1e-7)).toBe("0.0₆1");
    expect(formatMicroPrice(1.2e-7)).toBe("0.0₆12");
    expect(formatMicroPrice(1e-7)).not.toContain("e");
  });

  it("caps at 4 significant digits and trims trailing zeros", () => {
    expect(formatMicroPrice(0.000012345678)).toBe("0.0₄1235"); // rounded to 4 sig
    expect(formatMicroPrice(0.00005)).toBe("0.0₄5"); // "5000" → "5"
    expect(formatMicroPrice(0.000123)).toBe("0.000123"); // "1230" → "123"
    expect(formatMicroPrice(0.009999)).toBe("0.009999");
  });

  it("absorbs binary float noise via decimal rounding", () => {
    // 0.0000012 * 1600 = 0.0019200000000000003 in IEEE-754.
    expect(formatMicroPrice(0.0000012 * 1600)).toBe("0.00192");
  });

  it("falls back to the plain form when rounding carries out of the band", () => {
    expect(formatMicroPrice(0.00999999)).toBe("0.01"); // → no subscript
  });

  it("never compresses out-of-band input (safety-net fallback, plain style)", () => {
    expect(formatMicroPrice(0)).toBe("0.00");
    expect(formatMicroPrice(NaN)).toBe("0.00");
    expect(formatMicroPrice(Infinity)).toBe("0.00");
    expect(formatMicroPrice(0.01)).toBe("0.01");
    expect(formatMicroPrice(1.23)).toBe("1.23");
    expect(formatMicroPrice(-0.5)).toBe("-0.50");
    expect(formatMicroPrice(1234.5)).toBe("1,234.50");
  });
});

describe("formatUSD micro-price integration", () => {
  it("compresses sub-cent prices instead of rendering $0.00", () => {
    expect(formatUSD(0.0000012)).toBe("$0.0₅12");
    expect(formatUSD(0.0000012, { showCode: true })).toBe("$0.0₅12 USD");
  });

  it("shows plain significant digits for the un-compressed micro band", () => {
    expect(formatUSD(0.00234)).toBe("$0.00234");
  });

  it("keeps the existing 2-decimal path at $0.01 and above", () => {
    expect(formatUSD(0.01)).toBe("$0.01");
    expect(formatUSD(1.23)).toBe("$1.23");
  });

  it("keeps the existing behavior for zero and negatives", () => {
    expect(formatUSD(0)).toBe("$0.00");
    expect(formatUSD(-0.0000012)).toBe("-$0.00"); // negatives are never compressed
  });
});

describe("formatPrice micro-price integration", () => {
  it("routes the default USD path through the micro formatter", () => {
    expect(formatPrice(0.0000012)).toBe("$0.0₅12");
  });

  it("compresses sub-cent local-currency amounts with the local symbol", () => {
    // 0.0000012 USD * 10 = ₦0.000012 → 4 zeros → compressed.
    expect(formatPrice(0.0000012, { rate: 10, currency: NGN })).toBe("₦0.0₄12");
    expect(formatPrice(0.0000012, { rate: 10, currency: NGN, showCode: true })).toBe(
      "₦0.0₄12 NGN",
    );
    // 0.0000012 USD * 1600 = ₦0.00192 → 2 zeros → plain sig-digit form.
    expect(formatPrice(0.0000012, { rate: 1600, currency: NGN })).toBe("₦0.00192");
  });

  it("respects caller-pinned decimals (no auto-compression)", () => {
    const out = formatPrice(0.0000012, {
      rate: 10,
      currency: NGN,
      minDecimals: 2,
      maxDecimals: 8,
    });
    expect(out).not.toMatch(ANY_SUBSCRIPT);
  });

  it("leaves normal-sized conversions untouched", () => {
    expect(formatPrice(100, { rate: 1617.75, currency: NGN })).toContain("161,775");
  });
});
