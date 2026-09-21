// hooks/useMerchantPricing.ts
//
// Merchant local currency pricing & shipping converter.
//
// Allows merchants to enter product prices and shipping fees in their understood
// local fiat currency (e.g. NGN, UGX, KES, GHS), while automatically computing
// and submitting the canonical USD amounts to the backend.

import { useCallback, useMemo, useState } from 'react';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useAuth } from '@/AuthContext/AuthContext';
import { currencyForCountry } from '@/constants/countryCurrency';
import type { CurrencyConfig } from '@/constants/currencies';

export interface CurrencyPickerOption {
  id: string;
  label: string;
  symbol: string;
  name: string;
  flag?: string;
}

/**
 * Pure conversion helpers.
 */
export function localToUsd(localAmountStr: string, rate: number): number {
  const local = parseFloat(localAmountStr.replace(/,/g, ''));
  if (isNaN(local) || local <= 0 || !rate || rate <= 0) return 0;
  const usd = local / rate;
  const rounded = Math.round(usd * 100) / 100;
  return rounded <= 0 && local > 0 ? 0.01 : rounded;
}

export function usdToLocal(usdAmount: number | string, rate: number): string {
  const usd = typeof usdAmount === 'string' ? parseFloat(usdAmount) : usdAmount;
  if (isNaN(usd) || usd <= 0 || !rate || rate <= 0) return '';
  const local = usd * rate;
  // If whole number, format without decimals; otherwise up to 2 decimal places.
  // Never include commas so the output is safe for TextInput number/decimal-pad.
  return Number.isInteger(local)
    ? local.toString()
    : local.toFixed(2).replace(/\.?0+$/, '');
}

export function formatUsdBadge(localAmountStr: string, rate: number, isUsd: boolean): string {
  if (isUsd) return '';
  const usd = localToUsd(localAmountStr, rate);
  if (usd <= 0) return '';
  return `≈ $${usd.toFixed(2)} USD`;
}

export function resolveMerchantDefaultCurrency(
  kycCountry?: string | null,
  activeCurrencyCode?: string | null,
  supportedCodes: string[] = []
): string {
  const fromCountry = currencyForCountry(kycCountry);
  if (fromCountry && (supportedCodes.length === 0 || supportedCodes.includes(fromCountry))) {
    return fromCountry;
  }
  if (activeCurrencyCode && (supportedCodes.length === 0 || supportedCodes.includes(activeCurrencyCode))) {
    return activeCurrencyCode;
  }
  return 'NGN';
}

export function convertCurrencyValue(
  valStr: string,
  fromCode: string,
  toCode: string,
  rates: Record<string, { midRate?: number }> = {}
): string {
  if (!valStr || !valStr.trim()) return '';
  const num = parseFloat(valStr.replace(/,/g, ''));
  if (isNaN(num) || num <= 0) return valStr;

  const fromRate = fromCode === 'USD' ? 1 : rates[fromCode]?.midRate || 1;
  const toRate = toCode === 'USD' ? 1 : rates[toCode]?.midRate || 1;

  const usd = num / fromRate;
  const newLocal = usd * toRate;
  return Number.isInteger(newLocal)
    ? newLocal.toString()
    : newLocal.toFixed(2).replace(/\.?0+$/, '');
}

export function useMerchantPricing() {
  const { currentUser } = useAuth();
  const { currency: activeAppCurrency, supportedCurrencies, allRates } = useCurrency();

  const supportedCodes = useMemo(
    () => supportedCurrencies.map((c) => c.code),
    [supportedCurrencies]
  );

  // Resolve initial default currency from user's KYC / country, falling back
  // to the active app currency or NGN.
  const defaultCode = useMemo(() => {
    return resolveMerchantDefaultCurrency(
      currentUser?.country,
      activeAppCurrency?.code,
      supportedCodes
    );
  }, [currentUser?.country, activeAppCurrency?.code, supportedCodes]);

  const [selectedCurrencyCode, setSelectedCurrencyCode] = useState<string>(defaultCode);

  const selectedCurrency: CurrencyConfig = useMemo(() => {
    return (
      supportedCurrencies.find((c) => c.code === selectedCurrencyCode) ?? {
        code: selectedCurrencyCode,
        symbol: selectedCurrencyCode,
        name: selectedCurrencyCode,
      }
    );
  }, [selectedCurrencyCode, supportedCurrencies]);

  // Exchange rate: 1 USD = X local units.
  const rate: number = useMemo(() => {
    if (selectedCurrencyCode === 'USD') return 1;
    const p2p = allRates[selectedCurrencyCode];
    if (p2p && p2p.midRate > 0) return p2p.midRate;
    return 1;
  }, [selectedCurrencyCode, allRates]);

  const isUsd = selectedCurrencyCode === 'USD';

  const currencyOptions: CurrencyPickerOption[] = useMemo(() => {
    return supportedCurrencies.map((c) => ({
      id: c.code,
      label: `${c.flag ? c.flag + ' ' : ''}${c.code} · ${c.name}`,
      symbol: c.symbol,
      name: c.name,
      flag: c.flag,
    }));
  }, [supportedCurrencies]);

  const toUsd = useCallback(
    (localAmountStr: string): number => localToUsd(localAmountStr, rate),
    [rate]
  );

  const toLocal = useCallback(
    (usdAmount: number | string): string => usdToLocal(usdAmount, rate),
    [rate]
  );

  const formatUsd = useCallback(
    (localAmountStr: string): string => formatUsdBadge(localAmountStr, rate, isUsd),
    [rate, isUsd]
  );

  const convertValue = useCallback(
    (valStr: string, fromCode: string, toCode: string): string =>
      convertCurrencyValue(valStr, fromCode, toCode, allRates),
    [allRates]
  );

  return {
    selectedCurrencyCode,
    setSelectedCurrencyCode,
    selectedCurrency,
    rate,
    isUsd,
    currencyOptions,
    toUsd,
    toLocal,
    formatUsd,
    convertValue,
  };
}
