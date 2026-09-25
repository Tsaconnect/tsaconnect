// Pure maths and formatting for the token price chart (components/wallet/PriceChart).
// No React, no I/O.

export type ChartMode = 'line' | 'candle';

export interface PricePoint {
  timestamp: number;
  price: number;
}

export interface Candle {
  timestamp: number;
  open: number;
  high: number;
  low: number;
  close: number;
}

export const CHART_RANGES = [
  { label: '24H', days: 1 },
  { label: '7D', days: 7 },
  { label: '1M', days: 30 },
  { label: '3M', days: 90 },
  { label: '1Y', days: 365 },
] as const;

/**
 * Y-axis bounds for a series. The span never drops below `minSpanRatio` of the
 * mid price: without that floor a stablecoin's $0.0003 wobble is stretched to
 * the full chart height and reads as a crash.
 */
export function priceDomain(
  values: number[],
  { minSpanRatio = 0.005, padRatio = 0.08 }: { minSpanRatio?: number; padRatio?: number } = {},
): { lo: number; hi: number } {
  let lo = Math.min(...values);
  let hi = Math.max(...values);
  const mid = (lo + hi) / 2;
  const minSpan = Math.abs(mid) * minSpanRatio || 1;
  if (hi - lo < minSpan) {
    lo = mid - minSpan / 2;
    hi = mid + minSpan / 2;
  }
  const pad = (hi - lo) * padRatio;
  return { lo: lo - pad, hi: hi + pad };
}

/**
 * The sample under x. Line points sit on the edges of their spacing
 * (i * width / (count - 1)); candles fill equal slots (width / count).
 */
export function indexAtX(x: number, width: number, count: number, mode: ChartMode): number {
  if (count <= 1 || width <= 0) return 0;
  const raw = mode === 'candle'
    ? Math.floor((x / width) * count)
    : Math.round((x / width) * (count - 1));
  return Math.min(count - 1, Math.max(0, raw));
}

/** Fraction digits that show a price's movement without noise. */
export function priceFractionDigits(price: number): number {
  const p = Math.abs(price);
  if (p >= 1000) return 2;
  if (p >= 1) return 4;
  if (p >= 0.01) return 6;
  return 8;
}

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const pad2 = (n: number) => String(n).padStart(2, '0');

/** Label for a scrubbed point, as precise as the range needs. Local time. */
export function formatChartTime(ts: number, days: number): string {
  const d = new Date(ts);
  const clock = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  if (days <= 1) return clock;
  if (days <= 7) return `${WEEKDAYS[d.getDay()]} ${clock}`;
  const date = `${d.getDate()} ${MONTHS[d.getMonth()]}`;
  return days >= 365 ? `${date} ${d.getFullYear()}` : date;
}

/** Signed % change from `from` to `to`; 0 when there is no base to compare against. */
export function pctChange(from: number, to: number): number {
  return from > 0 ? ((to - from) / from) * 100 : 0;
}
