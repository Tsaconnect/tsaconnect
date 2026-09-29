// utils/payLinks.ts
//
// The pure pieces of a TSA Pay payment link shared by the app's link screens
// (app/merchants/pay-links/*): the message she shares, the status a link reads
// as, the times and money it shows, the amount she types — and, for a reusable
// link, its share message, amount rule and range.
import type { OpenLink, OpenLinkNotTakingReason, OpenLinkRange, PayLink } from '@/services/payMerchantApi';
import { formatKobo, koboFromNaira } from '@/utils/stableSellQuote';

/** How long a link stays open; the server takes exactly these. */
export const LINK_OPEN_DAYS = [1, 3, 7] as const;
export type LinkOpenDays = (typeof LINK_OPEN_DAYS)[number];

/**
 * The one-time link's share message. It quotes what the customer will be
 * charged (fee included), not the merchant's amount: the link must not ask for
 * less than the checkout does.
 */
export function payLinkShareMessage(businessName: string, link: Pick<PayLink, 'customerTotal' | 'description' | 'checkoutUrl'>): string {
  return `${businessName} is requesting ${formatKobo(String(link.customerTotal))} for ${link.description}. Pay here: ${link.checkoutUrl}`;
}

/** A reusable link's share: whom it pays, what for, and its address. */
export const openLinkShareMessage = (businessName: string, link: Pick<OpenLink, 'label' | 'url'>): string =>
  `Pay ${businessName} — ${link.label}: ${link.url}`;

/**
 * What a customer may pay through a reusable link, in words — the effective
 * range, worked out as the server does: the link's own range narrowed by the
 * merchant's limits (a 0 bound on the link being the merchant's own).
 */
export function openLinkAmountRule(
  link: Pick<OpenLink, 'minAmount' | 'maxAmount' | 'merchantMinAmount' | 'merchantMaxAmount'>,
): string {
  const lo = Math.max(link.minAmount, link.merchantMinAmount);
  const hi = link.merchantMaxAmount > 0
    ? Math.min(link.maxAmount || link.merchantMaxAmount, link.merchantMaxAmount)
    : link.maxAmount;
  const min = lo > 0 ? formatKobo(String(lo)) : '';
  const max = hi > 0 ? formatKobo(String(hi)) : '';
  if (min && max) return `Any amount from ${min} to ${max}`;
  if (min) return `Any amount from ${min}`;
  if (max) return `Any amount up to ${max}`;
  return 'Any amount';
}

/** A reusable link's status key (StatusPill's openLink namespace): taking payments, paused by her, or closed for another reason. */
export function openLinkStatus(link: Pick<OpenLink, 'active' | 'takingPayments' | 'notTakingReason'>): 'active' | 'paused' | 'not_taking' {
  if (link.takingPayments) return 'active';
  return !link.active || link.notTakingReason === 'paused' ? 'paused' : 'not_taking';
}

const NOT_TAKING_WHY: Record<OpenLinkNotTakingReason, string> = {
  paused: "You paused it. A customer who opens it is told you aren't taking payments.",
  suspended: 'Your merchant account is suspended. Contact support to take payments again.',
  live_off: "Live payments are switched off for your business.",
  no_payout: "Set where you're paid in Payment Settings before customers can pay.",
  range_outside_limits: 'Your amount range no longer fits your limits — edit the link.',
};

/** Why a reusable link isn't taking payments, in her words; undefined while it is. */
export function openLinkNotTakingWhy(link: Pick<OpenLink, 'active' | 'takingPayments' | 'notTakingReason'>): string | undefined {
  const status = openLinkStatus(link);
  if (status === 'active') return undefined;
  if (status === 'paused') return NOT_TAKING_WHY.paused;
  return link.notTakingReason ? NOT_TAKING_WHY[link.notTakingReason] : "Customers can't pay through it right now.";
}

/**
 * What a payment through a reusable link was for: the link's name, or — when
 * the customer wrote one — her note, marked as hers.
 */
export function openPaymentPurpose(p: Pick<PayLink, 'description' | 'linkLabel'>, fallbackLabel: string):
  { title: string; note: boolean } {
  const note = p.description.trim();
  return note ? { title: note, note: true } : { title: p.linkLabel || fallbackLabel, note: false };
}

/** "You received ₦X from N payments" — what reached her through a reusable link. */
export function openLinkReceived(link: Pick<OpenLink, 'receivedTotal' | 'paidCount'>): string {
  return `You received ${formatKobo(String(link.receivedTotal))} from ${link.paidCount} ${link.paidCount === 1 ? 'payment' : 'payments'}`;
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

/** Said on an amount field whose text is not plain naira and kobo. */
export const NAIRA_FORMAT_HINT = 'Enter naira with a point for kobo, like 5000 or 5000.50.';

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

/** What is wrong with an amount field's text, or undefined when it is a positive amount. */
export function nairaInputError(text: string): string | undefined {
  if (koboFromNairaInput(text) !== null) return undefined;
  return /^\d+(\.\d{1,2})?$/.test(text.trim()) ? 'Enter an amount more than ₦0.' : NAIRA_FORMAT_HINT;
}

/**
 * A reusable link's lowest/highest fields as a range, or what is wrong with
 * each. Blank is the merchant's own bound (0). The server still checks both
 * against the merchant's limits.
 */
export function openLinkRangeFrom(min: string, max: string):
  { range: OpenLinkRange } | { errors: Partial<Record<'min' | 'max', string>> } {
  const bound = (text: string) => (text.trim() ? koboFromNairaInput(text) : 0);
  const lo = bound(min);
  const hi = bound(max);
  const errors: Partial<Record<'min' | 'max', string>> = {};
  if (lo === null) errors.min = nairaInputError(min);
  if (hi === null) errors.max = nairaInputError(max);
  if (lo && hi && lo > hi) errors.max = "The highest amount can't be less than the lowest.";
  if (lo === null || hi === null || errors.max) return { errors };
  return { range: { minAmount: lo, maxAmount: hi } };
}

/** Kobo back into an amount field's text ("150050" → "1500.50"); 0 is left blank. */
export const nairaFieldText = (kobo: number): string => (kobo > 0 ? formatKobo(String(kobo)).replace(/[₦,]/g, '') : '');
