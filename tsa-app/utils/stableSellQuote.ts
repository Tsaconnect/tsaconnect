// utils/stableSellQuote.ts
//
// Pure money logic for the stablecoin sell flow. No React, no I/O — the
// screen is untestable in this repo (no @testing-library/react-native), so
// anything that touches an amount lives here and is unit-tested.

import { formatTokenAmount } from '@/services/p2p';

export type StableAsset = 'USDC' | 'USDT';

/** USDC/USDT decimals on the DESTINATION chain. LiFi's toAmount is always
 *  denominated on Polygon (toChain 137) regardless of where the user sends
 *  from — BSC stablecoins are 18 decimals, and using that here is wrong by
 *  10^12. Source-side parsing uses the SOURCE token's decimals instead. */
export const POLYGON_STABLE_DECIMALS = 6;

/**
 * LiFi could not price the bridge. Blocks the quote rather than letting the
 * screen fall back to the amount the user typed.
 *
 * A CLASS, not a shared module-level `new Error(...)`. One shared instance
 * carries a single stack captured at module load, so every report points at
 * module init instead of the failing call; and `e instanceof <instance>` — the
 * obvious next thing someone writes — throws `TypeError: Right-hand side of
 * 'instanceof' is not callable`. `code` is the stable thing to branch on,
 * because Jest's toThrow(err) matches on MESSAGE equality alone.
 */
export class ErrArrivalUnpriceable extends Error {
  readonly code = 'arrival_unpriceable';
  constructor(message = 'Could not price this route — try another chain') {
    super(message);
    this.name = 'ErrArrivalUnpriceable';
    // Required for `instanceof` to survive TS's ES5 class-extends-builtin
    // downlevelling; harmless when targeting ES2015+.
    Object.setPrototypeOf(this, ErrArrivalUnpriceable.prototype);
  }
}

/**
 * Fractional digits the backend accepts on a stablecoin amount.
 * `StableToBaseUnits` rejects anything longer (stable_assets.go:89-92) with
 * `ErrStablePrecision` → 400, so this is checked at the input rather than
 * discovered as an unexplained failure after a round trip.
 */
export const STABLE_MAX_DECIMALS = 6;

/** Strict decimal shape. Deliberately the SAME grammar `parseTokenAmount`
 *  enforces (p2p.ts:874-880) so no guard here admits an input the next call
 *  throws on. Rejects "1e1", "1.2.3", "0x10", "abc", "" and ".". */
const DECIMAL_RE = /^\d+(\.\d+)?$/;

/** True when the typed amount carries more precision than the backend accepts. */
export function exceedsStablePrecision(amountMajor: string): boolean {
  const frac = amountMajor.trim().split('.')[1] ?? '';
  return frac.length > STABLE_MAX_DECIMALS;
}

/**
 * Flutterwave's documented outbound floor is 10 USDC/USDT.
 *
 * This is a USABILITY guard, not the enforcement point: a client-side check
 * is bypassable and does not protect a direct API caller. The backend's own
 * minimum (its review finding I5) is the real fix and is still open. Without
 * either, a 1 USDC sell quotes cleanly, the user broadcasts irreversibly, and
 * the provider rejects the conversion with the funds already at Flutterwave.
 */
export const STABLE_MIN_SEND: Record<StableAsset, string> = {
  USDC: '10',
  USDT: '10',
};

/** True when the amount is unusable or under the provider floor. */
export function belowMinimum(asset: StableAsset, amountMajor: string): boolean {
  const trimmed = amountMajor.trim();
  // Shape first, then value. Number('1e1') is 10 and would clear the floor,
  // but parseTokenAmount throws on scientific notation — so a Number()-only
  // guard passes an input the next call rejects.
  if (!DECIMAL_RE.test(trimmed)) return true;
  const n = Number(trimmed);
  if (!Number.isFinite(n) || n <= 0) return true;
  return n < Number(STABLE_MIN_SEND[asset]);
}

/**
 * What we expect to ARRIVE at Flutterwave, in major units.
 *
 * The backend pays out `received / quoted`, so the fiat quote must be composed
 * against this rather than against the amount typed — otherwise the figure the
 * user was shown always shrinks, which reads as a bait-and-switch.
 *
 * `lifiToAmountBase` is LiFi's estimated destination amount in BASE units, or
 * null on a same-chain (Polygon) send where there is no bridge at all.
 */
export function estimateArrival(
  sentMajor: string,
  lifiToAmountBase: string | null,
  destDecimals: number
): string {
  // null means there is genuinely no bridge (Polygon source): what is sent is
  // what arrives. Anything else falsy means we could NOT price the bridge, and
  // those two cases must never be conflated — quoting the typed amount because
  // LiFi failed is the silent bait-and-switch this function exists to prevent.
  if (lifiToAmountBase === null) return sentMajor;
  const asNumber = Number(lifiToAmountBase);
  if (!lifiToAmountBase || !Number.isFinite(asNumber) || asNumber <= 0) {
    throw new ErrArrivalUnpriceable();
  }
  // destDecimals is the DESTINATION chain's (Polygon = 6), never the source's.
  // formatTokenAmount is pure string math; displayDecimals is pinned so we do
  // not silently round away real value.
  return formatTokenAmount(lifiToAmountBase, destDecimals, destDecimals);
}

/** A "you send X · about Y arrives" line, or null when they are equal. */
export function formatSendVsArrive(sentMajor: string, arriveMajor: string): string | null {
  if (Number(sentMajor) === Number(arriveMajor)) return null;
  return `You send ${sentMajor} · about ${arriveMajor} arrives`;
}
