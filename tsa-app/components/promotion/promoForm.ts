// Pure state, validation and FormData helpers for the merchant "Put this
// listing on promotion" section, plus usePromotion which binds them to a form.
// Kept free of UI imports so it is unit-testable.
import { useCallback, useState } from 'react';
import type { Product } from '@/components/services/api';
import { parseAmount, type MerchantPricing } from '@/hooks/useMerchantPricing';

export const REFERRAL_PRESETS = ['2', '5', '10'] as const;
export type ReferralChoice = (typeof REFERRAL_PRESETS)[number] | 'manual';

export interface PromoState {
  enabled: boolean;
  referralChoice: ReferralChoice;
  manualReferrals: string;
  countdownEnabled: boolean;
  countdownMinutes: string;
  /** Typed in the same currency as the listing price. */
  promoPrice: string;
}

export const DEFAULT_PROMO_STATE: PromoState = {
  enabled: false,
  referralChoice: '5',
  manualReferrals: '',
  countdownEnabled: false,
  countdownMinutes: '',
  promoPrice: '',
};

const isWholeNumber = (text: string) => /^\d+$/.test(text.trim()) && parseInt(text, 10) >= 1;

export function requiredReferrals(state: PromoState): number {
  const text = state.referralChoice === 'manual' ? state.manualReferrals : state.referralChoice;
  return isWholeNumber(text) ? parseInt(text, 10) : NaN;
}

/** Whole-number percent off, or null when the prices do not make a discount. */
export function discountPercent(originalPrice: number, promoPrice: number): number | null {
  if (!(originalPrice > 0) || !(promoPrice > 0) || promoPrice >= originalPrice) return null;
  return Math.round((1 - promoPrice / originalPrice) * 100);
}

export function validatePromo(state: PromoState, originalPrice: number): string | null {
  if (!state.enabled) return null;
  if (isNaN(requiredReferrals(state))) return 'Required referrals must be a whole number of 1 or more';
  if (state.countdownEnabled && !isWholeNumber(state.countdownMinutes)) {
    return 'Countdown must be a whole number of minutes, 1 or more';
  }
  const promo = parseAmount(state.promoPrice);
  if (isNaN(promo) || promo <= 0) return 'Enter a discount price greater than 0';
  if (!(originalPrice > 0)) return 'Set the listing price before adding a discount';
  if (promo >= originalPrice) return 'Discount price must be lower than the listing price';
  return null;
}

/**
 * Appends the promo fields. `promoPriceUsd` overrides the typed price when the
 * form stores prices in USD but lets the merchant type another currency.
 */
export function appendPromoFields(formData: FormData, state: PromoState, promoPriceUsd?: number) {
  formData.append('promoEnabled', state.enabled ? 'true' : 'false');
  if (!state.enabled) return;
  formData.append('promoRequiredReferrals', String(requiredReferrals(state)));
  formData.append('promoPrice', String(promoPriceUsd ?? parseAmount(state.promoPrice)));
  formData.append(
    'promoCountdownSeconds',
    state.countdownEnabled ? String(parseInt(state.countdownMinutes, 10) * 60) : ''
  );
}

export function promoStateFromProduct(product: Partial<Product>): PromoState {
  if (!product.promoEnabled) return DEFAULT_PROMO_STATE;
  const referrals = String(product.promoRequiredReferrals ?? 5);
  const preset = (REFERRAL_PRESETS as readonly string[]).includes(referrals);
  const seconds = product.promoCountdownSeconds ?? 0;
  return {
    enabled: true,
    referralChoice: preset ? (referrals as ReferralChoice) : 'manual',
    manualReferrals: preset ? '' : referrals,
    countdownEnabled: seconds > 0,
    countdownMinutes: seconds > 0 ? String(Math.ceil(seconds / 60)) : '',
    promoPrice: product.promoPrice != null ? String(product.promoPrice) : '',
  };
}

/**
 * Promo state for a listing form. With `pricing` the discount price lives in
 * the shared amount store (so it follows the currency picker and is converted
 * to USD on submit); without it the typed price is sent as-is.
 */
export function usePromotion(pricing?: MerchantPricing) {
  const [local, setLocal] = useState<PromoState>(DEFAULT_PROMO_STATE);
  // A product payload without promo fields (stale route param) must not be
  // saved as "promotion off": omit promoEnabled until the merchant touches it.
  const [untouchedUnknown, setUntouchedUnknown] = useState(false);
  const state: PromoState = pricing ? { ...local, promoPrice: pricing.amount('promoPrice') } : local;

  const patch = useCallback((next: Partial<PromoState>) => {
    setUntouchedUnknown(false);
    setLocal((prev) => ({ ...prev, ...next }));
  }, []);
  const setPromoPrice = useCallback(
    (value: string) => {
      setUntouchedUnknown(false);
      if (pricing) pricing.setAmount('promoPrice', value);
      else patch({ promoPrice: value });
    },
    [pricing, patch]
  );

  /** Prefills from a stored listing. */
  const load = useCallback(
    (product: Partial<Product>) => {
      setLocal(promoStateFromProduct(product));
      setUntouchedUnknown(product.promoEnabled === undefined);
      if (pricing) pricing.loadSavedAmounts({ promoPrice: product.promoEnabled ? product.promoPrice : null });
    },
    [pricing]
  );

  const reset = useCallback(() => setLocal(DEFAULT_PROMO_STATE), []);
  const validate = (originalPrice: string) => validatePromo(state, parseAmount(originalPrice));
  const append = (formData: FormData) => {
    if (untouchedUnknown) return;
    appendPromoFields(formData, state, pricing ? pricing.usdFor('promoPrice') : undefined);
  };

  return { state, patch, setPromoPrice, load, reset, validate, append };
}
