import { useCallback, useEffect, useMemo, useState } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useCurrency } from '../contexts/CurrencyContext';
import { CurrencyConfig } from '../constants/currencies';

export const P2P_CURRENCY_KEY = 'p2p_market_currency';

/**
 * Pure resolver: pick the CurrencyConfig for the effective P2P market code.
 * Prefers the user's explicit P2P choice (selectedCode); otherwise falls back
 * to the app-wide currency code. Resolves full metadata from supportedCurrencies,
 * with a minimal stub when the list hasn't loaded that code yet.
 */
export function resolveP2pCurrency(
  selectedCode: string | null,
  appCurrencyCode: string,
  supportedCurrencies: CurrencyConfig[],
): CurrencyConfig {
  const code = (selectedCode || appCurrencyCode || 'USD').toUpperCase();
  return (
    supportedCurrencies.find((c) => c.code === code) ?? {
      code,
      symbol: code,
      name: code,
    }
  );
}

/**
 * P2P-scoped market currency. Defaults from the app-wide currency (geolocation-
 * derived) until the user picks a P2P-specific currency, which is persisted
 * under its own AsyncStorage key — independent of the global display currency.
 */
export function useP2pCurrency() {
  const { currency: appCurrency, supportedCurrencies } = useCurrency();
  const [selectedCode, setSelectedCode] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const saved = await AsyncStorage.getItem(P2P_CURRENCY_KEY);
        if (!cancelled && saved) setSelectedCode(saved.toUpperCase());
      } catch (e) {
        console.warn('[useP2pCurrency] AsyncStorage read failed', e);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const currency = useMemo(
    () => resolveP2pCurrency(selectedCode, appCurrency.code, supportedCurrencies),
    [selectedCode, appCurrency.code, supportedCurrencies],
  );

  const setCurrency = useCallback(async (code: string) => {
    const upper = code.toUpperCase();
    setSelectedCode(upper);
    try {
      await AsyncStorage.setItem(P2P_CURRENCY_KEY, upper);
    } catch (e) {
      console.warn('[useP2pCurrency] AsyncStorage write failed', e);
    }
  }, []);

  return { currency, setCurrency };
}
