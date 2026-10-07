// utils/stableSellQuote.ts
//
// Pure money logic for the stablecoin sell flow. No React, no I/O — the
// screen is untestable in this repo (no @testing-library/react-native), so
// anything that touches an amount lives here and is unit-tested.

import { formatTokenAmount } from '@/services/p2p';
import type { StepState } from '@/utils/progressSteps';

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
 * The outbound floor, in major units.
 *
 * TEMPORARILY 1 FOR TESTING. The long-standing value was 10, attributed to
 * Flutterwave's documentation — but that figure appears in no artifact we
 * hold, and a production probe found NO minimum enforced: GET /fees returns
 * 400 REQUEST_NOT_VALID for crypto and carries no minimum field, while
 * POST /transfers/rates quoted successfully all the way down. The real floor,
 * if any, only surfaces at conversion — after funds are irreversibly sent.
 * Restore a conservative value once a live conversion has settled the question.
 *
 * This is a USABILITY guard, not the enforcement point: a client-side check
 * is bypassable and does not protect a direct API caller. The backend floor
 * (IT_STABLE_MIN_SELL) is the real one and must be lowered to match, or the
 * server rejects what this screen accepts.
 */
export const STABLE_MIN_SEND: Record<StableAsset, string> = {
  USDC: '1',
  USDT: '1',
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

// ─── display + comparison helpers ─────────────────────────────────────────
//
// These three were authored inside sell-stable.tsx, where this repo cannot
// test them (no @testing-library/react-native, and they are not exported).
// They are pure money functions and one of them — truncateToStableDecimals —
// already needed a fix round, so they live here with their siblings.

/**
 * A minor-unit integer STRING rendered as ₦x,xxx.yy.
 *
 * Pure string math on purpose: `Number(kobo)` on a large payout silently loses
 * precision, and money crosses the wire as an integer string precisely so it
 * never becomes a float. This is the display edge — the only place a kobo
 * string is allowed to stop being a string.
 */
export function formatKobo(kobo: string | null | undefined): string {
  if (!kobo) return '—';
  const negative = kobo.trim().startsWith('-');
  const digits = kobo.replace(/\D/g, '');
  if (!digits) return '—';
  const padded = digits.padStart(3, '0');
  const naira = padded.slice(0, -2).replace(/^0+(?=\d)/, '');
  const minor = padded.slice(-2);
  const grouped = naira.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return `${negative ? '-' : ''}₦${grouped}.${minor}`;
}

/**
 * "1362.9" → "₦1,362.90": a decimal naira string (what Paycrest orders carry),
 * rounded to the kobo, half away from zero. String maths, like formatKobo —
 * formatKobo itself would read "1362.9" as 13629 kobo.
 */
export function formatNaira(naira: string | null | undefined): string {
  const kobo = koboFromNaira(naira);
  if (kobo === null) return '—';
  // "-0.001" rounds to nothing, and nothing has no sign.
  return formatKobo(String(kobo));
}

/**
 * "1362.9" → 136290n: a decimal naira string as integer kobo, rounded to the
 * kobo half away from zero, or null when it is not a number. String maths, so
 * a large amount never passes through a float.
 */
export function koboFromNaira(naira: string | null | undefined): bigint | null {
  const m = naira?.trim().match(/^(-?)(\d*)(?:\.(\d*))?$/);
  if (!m || (!m[2] && !m[3])) return null;
  const [, sign, whole, frac = ''] = m;
  let kobo = BigInt(`${whole || '0'}${frac.padEnd(2, '0').slice(0, 2)}`);
  if (frac.length > 2 && frac[2] >= '5') kobo += BigInt(1);
  return sign ? -kobo : kobo;
}

/** gross − afterSpread, as a kobo string. Both are integers on the wire. */
export function subtractKobo(a: string | null | undefined, b: string | null | undefined): string | null {
  if (!a || !b) return null;
  try {
    return (BigInt(a) - BigInt(b)).toString();
  } catch {
    return null;
  }
}

/**
 * Trim a major-unit asset amount to the precision the backend accepts.
 *
 * TRUNCATES, never rounds. `ethers.formatUnits` renders the balance at the
 * SOURCE token's decimals, and BSC's USDT/USDC are genuinely 18 — so the Max
 * button would otherwise fill in 18 fractional digits, `exceedsStablePrecision`
 * would reject the app's own value, and the one-tap path would fail on exactly
 * the chain this flow most has to get right. Rounding up instead of truncating
 * would push Max past the real balance and fail the send on-chain.
 *
 * String math, not `toFixed` — a float round-trip is what this guards against.
 */
export function truncateToStableDecimals(major: string): string {
  const [whole, frac = ''] = major.split('.');
  const kept = frac.slice(0, STABLE_MAX_DECIMALS).replace(/0+$/, '');
  return kept ? `${whole}.${kept}` : whole;
}

/**
 * True when the typed amount is strictly greater than the balance.
 *
 * String comparison rather than `Number(a) > Number(b)`: an 18-decimal balance
 * round-tripped through a float can compare wrong at the last digit, which on
 * a Max send is exactly where it matters.
 *
 * Returns FALSE when either side is not a plain decimal. An unparseable
 * balance must skip the guard, never fail it — the chain rejects an over-send
 * anyway, and a locked form with no explanation is the worse failure.
 */
export function exceedsBalance(amountMajor: string, balanceMajor: string): boolean {
  const a = amountMajor.trim();
  const b = balanceMajor.trim();
  if (!DECIMAL_RE.test(a) || !DECIMAL_RE.test(b)) return false;
  const [aWhole, aFrac = ''] = a.split('.');
  const [bWhole, bFrac = ''] = b.split('.');
  const width = Math.max(aFrac.length, bFrac.length);
  try {
    return BigInt(aWhole + aFrac.padEnd(width, '0')) > BigInt(bWhole + bFrac.padEnd(width, '0'));
  } catch {
    return false;
  }
}

/**
 * Shape check for a transaction hash the user pasted in by hand.
 *
 * The manual-recovery paths report a hash against an intent, and reporting the
 * wrong string wastes the one attempt a stranded user has. An EVM hash is
 * exactly 32 bytes hex; anything else is a paste error, not a transfer.
 * Whether the hash is really THEIRS is the backend's call, not this one's.
 */
export function isLikelyTxHash(hash: string): boolean {
  return /^0x[0-9a-fA-F]{64}$/.test(hash.trim());
}

/** A "you send X · about Y arrives" line, or null when they are equal. */
export function formatSendVsArrive(sentMajor: string, arriveMajor: string): string | null {
  if (Number(sentMajor) === Number(arriveMajor)) return null;
  return `You send ${sentMajor} · about ${arriveMajor} arrives`;
}

export type HopState = StepState;
export type Hop = { key: string; label: string; state: HopState };

/**
 * Every status the backend's stablecoin sell can be in.
 *
 * Mirrors the ITStatus* constants in internal/models/it_models.go. Kept as an
 * explicit list so the "every status maps to hops" test can enumerate it.
 */
export const STABLE_SELL_STATUSES = [
  'pending_deposit',
  'deposit_reported',
  'deposit_confirmed',
  'converting',
  'debited',
  'settled',
  'reversed',
  // `failed` and `cancelled` are very likely UNREACHABLE for a stablecoin
  // sell: they are set only on the MCGP paths (it_service.go:373, 436, 441,
  // 621, 625; it_fiat_va.go:220), and the stable chain deliberately ends at
  // the `debited → settled` pair (it_models.go:39-41). They are listed and
  // mapped anyway because they are terminal, cheap to map, and shared with
  // MCGP on the same intent row — but do not read this list as evidence that
  // a stable sell can reach them.
  'failed',
  'cancelled',
] as const;

const TERMINAL = new Set(['settled', 'reversed', 'failed', 'cancelled']);

/** Terminal states stop the progress screen's polling. */
export function isTerminalStableStatus(status: string): boolean {
  return TERMINAL.has(status);
}

// Ordered hops the user sees. `debited` is deliberately its own hop: the fiat
// exists but has not reached the bank, and that gap is the one users ask about.
const HOPS: { key: string; label: string }[] = [
  { key: 'sent', label: 'Sent from your wallet' },
  { key: 'arrived', label: 'Received by our payout partner' },
  { key: 'converted', label: 'Converted to ₦' },
  { key: 'paid', label: 'Paid to your bank' },
];

// How far along each status is: the index of the hop currently in flight.
const PROGRESS: Record<string, number> = {
  pending_deposit: 0,
  deposit_reported: 1,
  deposit_confirmed: 2,
  converting: 2,
  debited: 3,
  settled: 4,
};

export function statusToHops(status: string): Hop[] {
  // `reversed` is NOT a generic failure: it means the deposit arrived and was
  // converted, and only the bank payout failed. Telling that user their wallet
  // transfer failed would be flatly untrue while their money sits at
  // Flutterwave. cancelled/failed can occur before anything moved, so those
  // claim nothing.
  if (status === 'reversed') {
    return HOPS.map((h) => ({
      ...h,
      state: h.key === 'paid' ? ('failed' as HopState) : ('done' as HopState),
    }));
  }
  if (status === 'failed' || status === 'cancelled') {
    return HOPS.map((h, i) => ({ ...h, state: i === 0 ? 'failed' : 'pending' }));
  }
  const reached = PROGRESS[status];
  if (reached === undefined) {
    // Unknown status: claim nothing. Better a blank-looking screen than a
    // false "paid" for someone whose money is still in flight.
    return HOPS.map((h) => ({ ...h, state: 'pending' }));
  }
  return HOPS.map((h, i) => ({
    ...h,
    state: i < reached ? 'done' : i === reached ? 'active' : 'pending',
  }));
}
