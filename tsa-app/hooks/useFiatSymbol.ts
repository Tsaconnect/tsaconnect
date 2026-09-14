// hooks/useFiatSymbol.ts
//
// A stable (code) => symbol function over the loaded currency list, with the
// static fallback in utils/itActivity.ts for when that list has not loaded.
import { useCallback } from 'react';
import { useCurrency } from '@/contexts/CurrencyContext';
import { fiatSymbol, type SymbolFor } from '@/utils/itActivity';

export function useFiatSymbol(): SymbolFor {
  const { supportedCurrencies } = useCurrency();
  return useCallback((code: string) => fiatSymbol(code, supportedCurrencies), [supportedCurrencies]);
}
