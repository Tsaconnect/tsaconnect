// utils/buyQuote.ts
//
// Pure helpers for the IT buy screen's fee breakdown. The API prices the buy
// in naira — fees come off the transfer, and only the net buys MCGP — but the
// user is buying an ASSET, so naira-denominated fee lines don't tell them what
// they actually receive. This restates the same quote in MCGP: you are paying
// for X, Y covers fees, Z lands in your wallet.
//
// The withheld MCGP is never issued, so it stays on the contract's balance —
// that is where the platform's cut is retained on a deposit.
//
// Extracted from the screen so the arithmetic is unit-testable (this repo has
// no @testing-library/react-native). All math is BigInt over wei/kobo; the
// only float is the final display rounding.

const WEI = 10n ** 18n;

/** The quote fields the breakdown needs (a subset of ITBuyQuote). */
export interface BuyQuoteInput {
  /** Gross naira the user transfers, in kobo. */
  ngnKobo: string;
  /** MCGP credited, in wei — already net of fees. */
  mcgpAmount: string;
  /** Buy rate, kobo per 1 MCGP. */
  rateKobo: string;
  /** Naira withheld as fees, in kobo. Absent on a pre-deduction API. */
  feeNgnKobo?: string;
  netNgnKobo?: string;
}

export interface BuyFeeBreakdown {
  /** What the transfer would have bought before fees, 4dp. */
  grossMcgp: string;
  /** MCGP withheld to cover fees — retained on the contract, 4dp. */
  feeMcgp: string;
  /** MCGP credited to the wallet, 4dp. */
  netMcgp: string;
  /** The same fee in naira, 2dp — for the secondary line. */
  feeNgn: string;
  /**
   * False when the quote carries no fee fields or an unusable rate. The screen
   * must then omit the breakdown rather than render a misleading zero.
   */
  hasFees: boolean;
}

/** Parses a decimal-string integer, returning null for anything unusable. */
function toBigInt(value: string | undefined): bigint | null {
  if (value == null || value.trim() === '') return null;
  try {
    const n = BigInt(value.trim());
    return n < 0n ? null : n;
  } catch {
    return null;
  }
}

/** Renders wei as a plain decimal string, trailing zeros trimmed, max 4dp. */
function formatMcgp(weiAmount: bigint): string {
  const whole = weiAmount / WEI;
  // Scale the remainder to 4dp, truncating — never show more MCGP than exists.
  const frac = ((weiAmount % WEI) * 10000n) / WEI;
  if (frac === 0n) return whole.toString();
  const padded = frac.toString().padStart(4, '0').replace(/0+$/, '');
  return `${whole.toString()}.${padded}`;
}

/** Renders kobo as a 2dp naira string. */
function formatNgn(kobo: bigint): string {
  const whole = kobo / 100n;
  const rem = kobo % 100n;
  return `${whole.toString()}.${rem.toString().padStart(2, '0')}`;
}

/**
 * Restates a buy quote in MCGP terms.
 *
 * The fee is derived as gross − net rather than converted from the naira fee
 * independently, so the three figures always reconcile exactly on screen. Two
 * separate roundings would let "10 = 8 + 1.9999" through.
 */
export function buyFeeBreakdown(quote: BuyQuoteInput): BuyFeeBreakdown {
  const net = toBigInt(quote.mcgpAmount) ?? 0n;
  const netMcgp = formatMcgp(net);
  const rate = toBigInt(quote.rateKobo);
  const ngn = toBigInt(quote.ngnKobo);
  const feeKobo = toBigInt(quote.feeNgnKobo);

  const unpriced: BuyFeeBreakdown = {
    grossMcgp: netMcgp,
    feeMcgp: '0',
    netMcgp,
    feeNgn: '0.00',
    hasFees: false,
  };
  if (rate == null || rate === 0n || ngn == null || feeKobo == null || feeKobo === 0n) {
    return unpriced;
  }

  // net === 0n covers a zero or unparseable credited amount: without it the
  // whole purchase would render as fee ("pay for 500, receive 0"), which is a
  // far worse lie than showing no breakdown at all.
  if (net === 0n) return unpriced;

  const gross = (ngn * WEI) / rate;
  if (gross <= net) return unpriced; // nothing withheld — don't invent a fee line

  return {
    grossMcgp: formatMcgp(gross),
    feeMcgp: formatMcgp(gross - net),
    netMcgp,
    feeNgn: formatNgn(feeKobo),
    hasFees: true,
  };
}
