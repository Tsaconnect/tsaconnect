// Calendar days as YYYY-MM-DD, the format the API reads dates in (booking
// stays, slot ranges). Always the LOCAL day: toISOString() would give the UTC
// day, which is yesterday for an evening in Lagos.

const pad = (n: number) => String(n).padStart(2, '0');

/** A local calendar day as YYYY-MM-DD. */
export function toDateString(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** YYYY-MM-DD as a local Date, or undefined when it is not one. */
export function fromDateString(s?: string): Date | undefined {
  const m = s ? /^(\d{4})-(\d{2})-(\d{2})$/.exec(s) : null;
  return m ? new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])) : undefined;
}

/** The YYYY-MM-DD day n days after s, or undefined when s is not a date. */
export function addDays(s: string | undefined, n: number): string | undefined {
  const d = fromDateString(s);
  if (!d) return undefined;
  d.setDate(d.getDate() + n);
  return toDateString(d);
}
