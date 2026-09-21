// Merchant local-currency pricing for the inventory add/edit forms.
//
// Merchants type prices and shipping fees in their local currency; products are
// stored in USD. This hook owns the amount fields so both screens share one
// conversion path, and it refuses to convert without a real rate: a 1:1
// fallback would store ₦75,000 as $75,000 (see CurrencyContext's note on the
// same trap).

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useAuth } from '@/AuthContext/AuthContext';
import { currencyForCountry } from '@/constants/countryCurrency';
import { calculateZoneRates } from '@/constants/shipping';
import type { CurrencyConfig } from '@/constants/currencies';
import type { PickerItem } from '@/components/ui/PickerSheet';

export const AMOUNT_FIELDS = [
  'price',
  'shippingSameCity',
  'shippingSameState',
  'shippingSameCountry',
  'shippingInternational',
] as const;

export type AmountField = (typeof AMOUNT_FIELDS)[number];
export type UsdAmounts = Partial<Record<AmountField, number>>;

/** Key of the base fee that auto-fills the distance zones; never submitted. */
export const BASE_FEE_KEY = 'baseShippingFee';

/** Parses a typed amount, tolerating thousands separators. NaN when not a number. */
export function parseAmount(value: string): number {
  return parseFloat(value.replace(/,/g, ''));
}

/** Formats a number for a decimal-pad input: no separators, at most 2 decimals. */
export function formatAmountInput(value: number): string {
  return Number.isInteger(value) ? value.toString() : value.toFixed(2).replace(/\.?0+$/, '');
}

/** 1 USD = X units of `code`; null when the rate is unknown. */
export function rateFor(
  code: string,
  rates: Record<string, { midRate?: number }>
): number | null {
  if (code === 'USD') return 1;
  const mid = rates[code]?.midRate;
  return mid && mid > 0 ? mid : null;
}

export function localToUsd(localAmount: string, rate: number | null): number {
  const local = parseAmount(localAmount);
  if (isNaN(local) || local <= 0 || !rate || rate <= 0) return 0;
  const rounded = Math.round((local / rate) * 100) / 100;
  // A positive price must never round down to a free product.
  return rounded <= 0 ? 0.01 : rounded;
}

export function usdToLocal(usdAmount: number | string, rate: number | null): string {
  const usd = typeof usdAmount === 'string' ? parseFloat(usdAmount) : usdAmount;
  if (isNaN(usd) || usd <= 0 || !rate || rate <= 0) return '';
  return formatAmountInput(usd * rate);
}

export function formatUsdBadge(localAmount: string, rate: number | null, isUsd: boolean): string {
  if (isUsd) return '';
  const usd = localToUsd(localAmount, rate);
  return usd > 0 ? `≈ $${usd.toFixed(2)} USD` : '';
}

/**
 * Re-expresses a typed amount in another currency. Returns '' when either rate
 * is unknown so the merchant re-enters it, rather than keeping a number that
 * now means something else.
 */
export function convertCurrencyValue(
  value: string,
  fromCode: string,
  toCode: string,
  rates: Record<string, { midRate?: number }> = {}
): string {
  if (!value.trim()) return '';
  const num = parseAmount(value);
  if (isNaN(num) || num <= 0) return value;
  const fromRate = rateFor(fromCode, rates);
  const toRate = rateFor(toCode, rates);
  if (fromRate === null || toRate === null) return '';
  return formatAmountInput((num / fromRate) * toRate);
}

export function resolveMerchantDefaultCurrency(
  kycCountry: string | null | undefined,
  activeCurrencyCode: string | null | undefined,
  pricedCodes: string[]
): string {
  const fromCountry = currencyForCountry(kycCountry);
  if (fromCountry && pricedCodes.includes(fromCountry)) return fromCountry;
  if (activeCurrencyCode && pricedCodes.includes(activeCurrencyCode)) return activeCurrencyCode;
  return pricedCodes.includes('NGN') ? 'NGN' : 'USD';
}

export function useMerchantPricing() {
  const { currentUser } = useAuth();
  const {
    currency: activeAppCurrency,
    supportedCurrencies,
    allRates,
    loading: ratesLoading,
  } = useCurrency();

  // Only currencies we can actually convert are offered.
  const pricedCurrencies = useMemo(
    () => supportedCurrencies.filter((c) => rateFor(c.code, allRates) !== null),
    [supportedCurrencies, allRates]
  );
  const pricedCodes = useMemo(() => pricedCurrencies.map((c) => c.code), [pricedCurrencies]);

  const defaultCode = useMemo(
    () => resolveMerchantDefaultCurrency(currentUser?.country, activeAppCurrency?.code, pricedCodes),
    [currentUser?.country, activeAppCurrency?.code, pricedCodes]
  );

  // Null until rates settle. Committing the default only then means amounts
  // are never typed under a provisional USD default that later flips to NGN.
  const [pickedCode, setPickedCode] = useState<string | null>(null);
  useEffect(() => {
    if (pickedCode === null && !ratesLoading) setPickedCode(defaultCode);
  }, [pickedCode, ratesLoading, defaultCode]);

  const selectedCurrencyCode = pickedCode ?? defaultCode;
  const rate = rateFor(selectedCurrencyCode, allRates);
  const isUsd = selectedCurrencyCode === 'USD';
  const ready = pickedCode !== null && rate !== null;

  const selectedCurrency: CurrencyConfig = useMemo(
    () =>
      supportedCurrencies.find((c) => c.code === selectedCurrencyCode) ?? {
        code: selectedCurrencyCode,
        symbol: selectedCurrencyCode,
        name: selectedCurrencyCode,
      },
    [selectedCurrencyCode, supportedCurrencies]
  );

  const currencyOptions: PickerItem[] = useMemo(
    () =>
      pricedCurrencies.map((c) => ({
        id: c.code,
        label: `${c.flag ? c.flag + ' ' : ''}${c.code}`,
        sublabel: c.name,
      })),
    [pricedCurrencies]
  );

  // Amounts are keyed so one store serves every merchant money input: product
  // price and shipping, service fee tiers, menu items, request quotes.
  const [amounts, setAmounts] = useState<Record<string, string>>({});
  // Stored USD values of an existing listing. A key the merchant never touches
  // is submitted from here verbatim, so saving an unrelated edit (stock,
  // photos) cannot re-round the price through today's rate.
  const [savedUsd, setSavedUsd] = useState<Record<string, number>>({});
  const [pendingLoad, setPendingLoad] = useState<Record<string, number | null> | null>(null);
  const [edited, setEdited] = useState<ReadonlySet<string>>(new Set());

  const amount = useCallback((key: string) => amounts[key] ?? '', [amounts]);

  const markEdited = useCallback((keys: string[]) => {
    setEdited((prev) => new Set([...prev, ...keys]));
  }, []);

  const setAmount = useCallback(
    (key: string, value: string) => {
      setAmounts((prev) => ({ ...prev, [key]: value }));
      markEdited([key]);
    },
    [markEdited]
  );

  /** Sets the base fee and auto-fills the four distance zones from it. */
  const setBaseShippingFee = useCallback(
    (value: string) => {
      setAmounts((prev) => ({ ...prev, [BASE_FEE_KEY]: value }));
      const base = parseAmount(value);
      if (isNaN(base) || base < 0) return;
      const zones = calculateZoneRates(base);
      setAmounts((prev) => ({
        ...prev,
        shippingSameCity: formatAmountInput(zones.sameCity),
        shippingSameState: formatAmountInput(zones.sameState),
        shippingSameCountry: formatAmountInput(zones.sameCountry),
        shippingInternational: formatAmountInput(zones.international),
      }));
      markEdited(['shippingSameCity', 'shippingSameState', 'shippingSameCountry', 'shippingInternational']);
    },
    [markEdited]
  );

  const applyPackagePreset = useCallback(
    (usdBaseFee: number) => setBaseShippingFee(usdToLocal(usdBaseFee, rate)),
    [setBaseShippingFee, rate]
  );

  /**
   * Prefills keys from stored USD values; shown once a rate is known. Merges,
   * so a form that loads its listing in several requests can call it per part.
   * Zero or missing values load as blank.
   */
  const loadSavedAmounts = useCallback((usd: Record<string, number | null | undefined>) => {
    const keys = Object.keys(usd);
    const loaded: Record<string, number | null> = {};
    for (const key of keys) {
      const value = usd[key];
      loaded[key] = value != null && value > 0 ? value : null;
    }
    setSavedUsd((prev) => {
      const next = { ...prev };
      for (const key of keys) {
        if (loaded[key] != null) next[key] = loaded[key]!;
        else delete next[key];
      }
      return next;
    });
    setEdited((prev) => new Set([...prev].filter((k) => !keys.includes(k))));
    setPendingLoad((prev) => ({ ...prev, ...loaded }));
  }, []);

  useEffect(() => {
    if (!pendingLoad || !ready) return;
    const filled: Record<string, string> = {};
    for (const [key, usd] of Object.entries(pendingLoad)) {
      filled[key] = usd != null ? usdToLocal(usd, rate) : '';
    }
    setAmounts((prev) => ({ ...prev, ...filled }));
    setPendingLoad(null);
  }, [pendingLoad, ready, rate]);

  const switchCurrency = useCallback(
    (newCode: string) => {
      if (newCode === selectedCurrencyCode) return;
      setAmounts((prev) => {
        const next: Record<string, string> = {};
        for (const [key, value] of Object.entries(prev)) {
          next[key] = convertCurrencyValue(value, selectedCurrencyCode, newCode, allRates);
        }
        return next;
      });
      setPickedCode(newCode);
    },
    [selectedCurrencyCode, allRates]
  );

  /** USD value to submit for a key; undefined when it is blank. */
  const usdFor = useCallback(
    (key: string): number | undefined => {
      const saved = savedUsd[key];
      if (!edited.has(key) && saved != null) return saved;
      const typed = amounts[key] ?? '';
      return typed.trim() ? localToUsd(typed, rate) : undefined;
    },
    [amounts, edited, savedUsd, rate]
  );

  /** USD values for the product amount fields; a field is absent when blank. */
  const usdPayload = useCallback((): UsdAmounts => {
    const out: UsdAmounts = {};
    for (const field of AMOUNT_FIELDS) {
      const usd = usdFor(field);
      if (usd != null) out[field] = usd;
    }
    return out;
  }, [usdFor]);

  const reset = useCallback(() => {
    setAmounts({});
    setSavedUsd({});
    setPendingLoad(null);
    setEdited(new Set());
  }, []);

  const formatUsd = useCallback(
    (localAmount: string) => formatUsdBadge(localAmount, rate, isUsd),
    [rate, isUsd]
  );

  const toLocal = useCallback((usd: number) => usdToLocal(usd, rate), [rate]);

  return {
    ready,
    selectedCurrencyCode,
    selectedCurrency,
    isUsd,
    currencyOptions,
    switchCurrency,
    amount,
    setAmount,
    usdFor,
    setBaseShippingFee,
    applyPackagePreset,
    loadSavedAmounts,
    usdPayload,
    reset,
    formatUsd,
    toLocal,
  };
}

export type MerchantPricing = ReturnType<typeof useMerchantPricing>;
