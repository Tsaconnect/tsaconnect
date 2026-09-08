// hooks/useRampCurrency.ts
//
// Ramp currency — which currency an actual IT/Paycrest order settles in.
//
// This is deliberately NOT the display currency from CurrencyContext
// (useCurrency): that governs price FORMATTING only — it defaults to USD,
// follows the device/KYC country as a convenience, and is safe to change
// anytime with zero consequence. Deriving what a real money order settles in
// from that preference is exactly the regression documented at
// app/wallet/it/index.tsx: a user who switched display to USD lost the
// working NGN ramp entirely. So the ramp currency is tracked separately here,
// under its own AsyncStorage key, and only ever changes when the user
// explicitly picks one from a ramp-aware screen (the IT hub, or the Paycrest
// buy/sell screens).
//
// Shape mirrors hooks/useP2pCurrency.ts (another currency scoped away from
// the display preference, for the same reason): a persisted explicit choice,
// a pure resolver for the default, and CurrencyConfig metadata (symbol/name)
// resolved from CurrencyContext's supportedCurrencies table.
import { useCallback, useEffect, useMemo, useState } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useAuth } from '@/AuthContext/AuthContext';
import { useCurrency } from '@/contexts/CurrencyContext';
import { CurrencyConfig } from '@/constants/currencies';
import { currencyForCountry } from '@/constants/countryCurrency';
import { deviceCountryCode } from '@/utils/deviceLocale';
import {
  getRampCurrencies,
  rampProviderFor,
  type RampCurrency,
  type RampProvider,
} from '@/services/itApi';

export const RAMP_CURRENCY_KEY = 'ramp_currency_v1';

export interface RampCurrencyOption {
  id: string;
  label: string;
}

/**
 * Pure resolver: pick the ramp currency code to use, from currencies that
 * actually have a live ramp provider.
 *
 *  1. The user's own explicit pick, if it still has a provider (a provider
 *     can be withdrawn from under a choice made earlier).
 *  2. Their own country's currency — KYC first, then device locale — if that
 *     currency has a provider.
 *  3. NGN, if it has a provider.
 *  4. Whatever currency in the list has a provider at all.
 *  5. NGN unconditionally — the caller still has to render something, and an
 *     empty/not-yet-loaded list must not crash the screen.
 *
 * Never returns a currency without a provider (other than the final,
 * unavoidable NGN fallback for an empty list) — offering one would let the
 * user quote or submit an order the backend is guaranteed to refuse.
 */
export function resolveRampCurrencyCode(
  selectedCode: string | null | undefined,
  list: RampCurrency[],
  opts: { kycCountry?: string | null; deviceCountry?: string | null } = {},
): string {
  const hasProvider = (code: string) => rampProviderFor(list, code) !== '';

  if (selectedCode) {
    const upper = selectedCode.toUpperCase();
    if (hasProvider(upper)) return upper;
  }

  const fromCountry = (country?: string | null): string | null => {
    const code = currencyForCountry(country);
    return code && hasProvider(code) ? code : null;
  };

  return (
    fromCountry(opts.kycCountry) ??
    fromCountry(opts.deviceCountry) ??
    (hasProvider('NGN') ? 'NGN' : null) ??
    list.find((c) => c.rampProvider !== '')?.code ??
    'NGN'
  );
}

/**
 * Ramp-scoped currency, optionally restricted to one provider (pass
 * 'paycrest' from a Paycrest-only screen so its picker never offers a
 * currency Paycrest doesn't serve, e.g. one that routes through Flutterwave
 * instead).
 */
export function useRampCurrency(providerFilter?: RampProvider) {
  const { currentUser } = useAuth();
  const { supportedCurrencies } = useCurrency();
  const [all, setAll] = useState<RampCurrency[]>([]);
  const [selectedCode, setSelectedCode] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const [list, saved] = await Promise.all([
        getRampCurrencies().catch(() => [] as RampCurrency[]),
        AsyncStorage.getItem(RAMP_CURRENCY_KEY).catch(() => null),
      ]);
      if (cancelled) return;
      setAll(list);
      if (saved) setSelectedCode(saved.toUpperCase());
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // The candidate list this screen may use — every provider on the hub
  // (which has to know about both to route stablecoin buy/sell correctly),
  // or just the one a Paycrest/Flutterwave-specific screen actually talks to.
  const candidates = useMemo(
    () =>
      providerFilter
        ? all.filter((c) => c.rampProvider === providerFilter)
        : all.filter((c) => c.rampProvider !== ''),
    [all, providerFilter],
  );

  const code = useMemo(
    () =>
      resolveRampCurrencyCode(selectedCode, candidates, {
        kycCountry: currentUser?.country,
        deviceCountry: deviceCountryCode(),
      }),
    [selectedCode, candidates, currentUser?.country],
  );

  const currency: CurrencyConfig = useMemo(
    () => supportedCurrencies.find((c) => c.code === code) ?? { code, symbol: code, name: code },
    [code, supportedCurrencies],
  );

  const setCode = useCallback((next: string) => {
    const upper = next.toUpperCase();
    setSelectedCode(upper);
    AsyncStorage.setItem(RAMP_CURRENCY_KEY, upper).catch(() => {});
  }, []);

  // Only offer a choice when there is one — a picker listing a single
  // currency is noise, and today only NGN (paycrest) and GHS (flutterwave)
  // carry a live provider.
  const options: RampCurrencyOption[] = useMemo(
    () => candidates.map((c) => ({ id: c.code, label: c.code })),
    [candidates],
  );

  return { code, currency, setCode, options, currencies: all, loading };
}
