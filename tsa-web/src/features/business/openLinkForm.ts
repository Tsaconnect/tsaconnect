// A reusable link's label, limits and payment expiry as typed — shared by Request payment
// (create) and the link's Edit (update), so both check them the same way.
import { parseNaira } from '@/lib/format';
import type { NewOpenLink, OpenLink } from './api';

/** `paymentExpiresIn`: seconds as text, '0' for no expiry. */
export type OpenLinkDraft = { label: string; min: string; max: string; paymentExpiresIn: string };
export type OpenLinkErrors = Partial<Record<keyof OpenLinkDraft, string>>;

export const AMOUNT_HINT = 'Enter an amount in naira, like 5000 or 5000.50.';

/** Blank → 0 (the business's own limit); anything else must be an amount. */
const optionalKobo = (text: string) => (text.trim() ? parseNaira(text) : 0);

// How long each payment started from the link stays open, in seconds; 0: never expires.
const PAYMENT_EXPIRY = [
  { value: '0', label: 'No expiry' },
  { value: '1800', label: '30 min' },
  { value: '3600', label: '1 hour' },
  { value: '86400', label: '24 hours' },
];

const duration = (secs: number) => {
  const hours = secs / 3600;
  return Number.isInteger(hours) ? `${hours} ${hours === 1 ? 'hour' : 'hours'}` : `${Math.round(secs / 60)} min`;
};

/** The expiry choices, plus the link's own when it was set to another time elsewhere (the app, say), so Edit shows it. */
export function paymentExpiryOptions(current: string) {
  if (PAYMENT_EXPIRY.some((o) => o.value === current)) return PAYMENT_EXPIRY;
  return [...PAYMENT_EXPIRY, { value: current, label: duration(Number(current)) }].sort((a, b) => Number(a.value) - Number(b.value));
}

export const emptyOpenLinkDraft: OpenLinkDraft = { label: '', min: '', max: '', paymentExpiresIn: '0' };

/** What an existing link has, as the form shows it (a limit of 0 is a blank box). */
export const openLinkDraft = (l: OpenLink): OpenLinkDraft => ({
  label: l.label,
  min: l.minAmount ? String(l.minAmount / 100) : '',
  max: l.maxAmount ? String(l.maxAmount / 100) : '',
  paymentExpiresIn: String(l.paymentExpiresIn ?? 0),
});

export function checkOpenLink(d: OpenLinkDraft): OpenLinkErrors {
  const errors: OpenLinkErrors = {};
  const min = optionalKobo(d.min);
  const max = optionalKobo(d.max);
  if (!d.label.trim()) errors.label = 'Give the link a label — your customers see it.';
  if (min === null) errors.min = AMOUNT_HINT;
  if (max === null) errors.max = AMOUNT_HINT;
  if (min && max && min > max) errors.max = 'The maximum must be at least the minimum.';
  return errors;
}

/** The request body, for a draft checkOpenLink passed. */
export const toOpenLink = (d: OpenLinkDraft): NewOpenLink => ({
  label: d.label.trim(),
  minAmount: optionalKobo(d.min) ?? 0,
  maxAmount: optionalKobo(d.max) ?? 0,
  paymentExpiresIn: Number(d.paymentExpiresIn),
});
