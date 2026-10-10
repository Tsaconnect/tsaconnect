// Days as the API reads them: YYYY-MM-DD in West Africa Time (UTC+1 all year,
// no DST), the end day included.

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const HOUR_MS = 60 * 60_000;
const DAY_MS = 24 * HOUR_MS;

/** A real calendar day written YYYY-MM-DD ("2026-02-30" is not one). */
function isDay(d: string): boolean {
  if (!DAY.test(d)) return false;
  const t = Date.parse(`${d}T00:00:00Z`);
  return !Number.isNaN(t) && new Date(t).toISOString().slice(0, 10) === d;
}

/** Today's date in Lagos ("2026-10-08"), whatever the viewer's own zone. */
export const watToday = (now: Date = new Date()) => new Date(now.getTime() + HOUR_MS).toISOString().slice(0, 10);

/** The 1st of this month to today, in Lagos. */
export function monthToDate(now: Date = new Date()): { from: string; to: string } {
  const to = watToday(now);
  return { from: `${to.slice(0, 7)}-01`, to };
}

/**
 * Why a period can't be used, in the merchant's words, or null when it can (no
 * dates at all is no period, and fine). Both days or neither; real days; the end
 * on or after the start; at most `maxDays` days, both ends counted.
 */
export function periodProblem(from: string, to: string, maxDays?: number): string | null {
  if (!from && !to) return null;
  if (!from || !to) return 'Choose both a start and an end date.';
  if (!isDay(from) || !isDay(to)) return 'Enter dates as YYYY-MM-DD.';
  const days = (Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / DAY_MS + 1;
  if (days < 1) return 'The end date is before the start date.';
  if (maxDays !== undefined && days > maxDays) return `Choose at most ${maxDays} days.`;
  return null;
}
