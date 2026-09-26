// Display formatting. Months come from a fixed list, not ICU data, so the text is
// the same in every browser ("Sep", never "Sept").
const NGN = new Intl.NumberFormat('en-NG', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const pad = (n: number) => String(n).padStart(2, '0');
const day = (d: Date) => `${d.getDate()} ${MONTHS[d.getMonth()]}`;

/** Kobo → "₦84,500.00". */
export const naira = (kobo: number) => `₦${NGN.format(kobo / 100)}`;

/** What a merchant typed as naira ("5,000.50", "₦5000") → kobo; not a positive amount → null. */
export function parseNaira(text: string): number | null {
  const plain = text.replace(/[,\s₦]/g, '');
  if (!/^\d+(\.\d{1,2})?$/.test(plain)) return null;
  const [whole, frac = ''] = plain.split('.');
  const kobo = Number(whole) * 100 + Number(frac.padEnd(2, '0'));
  return kobo > 0 && Number.isSafeInteger(kobo) ? kobo : null;
}

/** "24 Sep, 16:02" in the viewer's time zone. */
export const dateTime = (iso: string) => {
  const d = new Date(iso);
  return `${day(d)}, ${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

/** "just now" / "2 min ago" / "3 h ago", then the day ("24 Sep"). */
export function relative(iso: string, now: Date = new Date()): string {
  const mins = Math.round((now.getTime() - new Date(iso).getTime()) / 60_000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins} min ago`;
  if (mins < 24 * 60) return `${Math.round(mins / 60)} h ago`;
  return day(new Date(iso));
}

/** "Ada Obi" → "AO" for avatars. */
export function initials(name: string): string {
  const letters = name.trim().split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join('');
  return letters || '?';
}
