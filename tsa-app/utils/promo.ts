// Pure helpers for the Promotions Arena buyer UI. The server decides the price
// at checkout; these only drive what the listing shows.
import type { Product, PromoStatus } from '@/components/services/api';

export type PromoFields = Pick<
  Product,
  'price' | 'promoEnabled' | 'promoPrice' | 'promoDiscountPercent'
>;

/** True when a listing should be shown as promoted (old price, promo price, % off). */
export function isPromoted(p: Partial<PromoFields> | null | undefined): boolean {
  return !!p && !!p.promoEnabled && Number(p.promoPrice) > 0 && Number(p.promoDiscountPercent) > 0;
}

export function remainingMs(expiresAt: string | undefined, now: number): number {
  if (!expiresAt) return 0;
  const t = new Date(expiresAt).getTime();
  return Number.isFinite(t) ? Math.max(0, t - now) : 0;
}

export function formatMmSs(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(Math.floor(s / 60))}:${pad(s % 60)}`;
}

export type PromoPhase = 'unclaimed' | 'progress' | 'unlocked' | 'expired';

export function promoPhase(s: PromoStatus | null | undefined): PromoPhase {
  if (!s || !s.claimed) return 'unclaimed';
  if (s.unlocked) return 'unlocked';
  if (s.expired) return 'expired';
  return 'progress';
}
