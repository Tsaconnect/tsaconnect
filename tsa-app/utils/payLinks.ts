// utils/payLinks.ts
//
// The pure pieces of a TSA Pay payment link shared by the app's link screens
// (app/merchants/pay-links/*): the message she shares, the status a link reads
// as, the times and money it shows, and the amount she types.
import type { PayLink } from '@/services/payMerchantApi';
import { formatKobo, koboFromNaira } from '@/utils/stableSellQuote';

/** How long a link stays open; the server takes exactly these. */
export const LINK_OPEN_DAYS = [1, 3, 7] as const;
export type LinkOpenDays = (typeof LINK_OPEN_DAYS)[number];

/**
 * The same words the web app shares, so a customer reads one message whichever
 * she used. It quotes what the customer will be charged (fee included), not the
 * merchant's amount: the link must not ask for less than the checkout does.
 */
export function payLinkShareMessage(businessName: string, link: Pick<PayLink, 'customerTotal' | 'description' | 'checkoutUrl'>): string {
  return `${businessName} is requesting ${formatKobo(String(link.customerTotal))} for ${link.description}. Pay here: ${link.checkoutUrl}`;
}

/** What she gets is only worth showing while it can still come: open, being paid, or paid. */
export const payLinkReceives = (link: Pick<PayLink, 'status' | 'cancelled'>): boolean =>
  !link.cancelled && (link.status === 'pending' || link.status === 'processing' || link.status === 'succeeded');

/**
 * The one time a link's detail shows beside its created time: until when it is
 * open, when it was paid, or when it expired. Being paid, failed or cancelled
 * have none worth showing.
 */
export function payLinkTimeRow(link: Pick<PayLink, 'status' | 'cancelled' | 'expiresAt' | 'succeededAt'>):
  { label: string; at: string } | null {
  if (link.cancelled) return null;
  switch (link.status) {
    case 'pending': return { label: 'Open until', at: link.expiresAt };
    case 'succeeded': return link.succeededAt ? { label: 'Paid', at: link.succeededAt } : null;
    case 'expired': return { label: 'Expired', at: link.expiresAt };
    default: return null;
  }
}

/** A cancelled link is stored as expired; to her it is cancelled. Keys StatusPill's payLink namespace. */
export const payLinkStatus = (link: Pick<PayLink, 'status' | 'cancelled'>): string =>
  (link.cancelled ? 'cancelled' : link.status);

/** "26 Sep, 3:05 pm" in her own locale and time zone. */
export function formatLinkTime(iso: string | undefined): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString(undefined, { day: 'numeric', month: 'short', hour: 'numeric', minute: '2-digit' });
}

const sameDay = (a: Date, b: Date) =>
  a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

/** The list's headline: links still waiting to be paid, and links paid today. */
export function payLinkCounts(links: PayLink[], now = new Date()): { awaiting: number; paidToday: number } {
  let awaiting = 0;
  let paidToday = 0;
  for (const l of links) {
    if (l.status === 'pending') awaiting += 1;
    else if (l.status === 'succeeded' && l.succeededAt && sameDay(new Date(l.succeededAt), now)) paidToday += 1;
  }
  return { awaiting, paidToday };
}

/**
 * What she typed in the amount field as integer kobo, or null when it is not a
 * plain positive amount: digits, then at most one point and two decimals. A
 * comma is refused, never stripped — "5000,50" is 5000.50 in a comma-decimal
 * locale and 500,050 read the other way, and a link must not guess.
 */
export function koboFromNairaInput(text: string): number | null {
  if (!/^\d+(\.\d{1,2})?$/.test(text.trim())) return null;
  const kobo = koboFromNaira(text);
  if (kobo === null || kobo <= BigInt(0) || kobo > BigInt(Number.MAX_SAFE_INTEGER)) return null;
  return Number(kobo);
}
