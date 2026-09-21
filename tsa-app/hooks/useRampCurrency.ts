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
  rampLegsFor,
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
 * Pure resolver: pick the ramp currency code to use, from currencies that can
 * actually trade — optionally in one specific direction.
 *
 *  1. The user's own explicit pick, if it still qualifies (a provider or a leg
 *     can be withdrawn from under a choice made earlier).
 *  2. Their own country's currency — KYC first, then device locale.
 *  3. NGN.
 *  4. Whatever currency in the list qualifies at all.
 *  5. NGN unconditionally — the caller still has to render something, and an
 *     empty/not-yet-loaded list must not crash the screen.
 *
 * `leg` is what makes this safe on a single-direction screen. Without it, a
 * Ugandan user opening Buy defaults to UGX — which Paycrest sells but never
 * buys — and every amount they type fails the quote with a generic error. A
 * screen that only buys must only ever be handed a currency that buys.
 */
export function resolveRampCurrencyCode(
  selectedCode: string | null | undefined,
  list: RampCurrency[],
  opts: { kycCountry?: string | null; deviceCountry?: string | null; leg?: RampLeg } = {},
): string {
  const qualifies = (code: string) => currencyTrades(list, code, opts.leg);

  if (selectedCode) {
    const upper = selectedCode.toUpperCase();
    if (qualifies(upper)) return upper;
  }

  const fromCountry = (country?: string | null): string | null => {
    const code = currencyForCountry(country);
    return code && qualifies(code) ? code : null;
  };

  return (
    fromCountry(opts.kycCountry) ??
    fromCountry(opts.deviceCountry) ??
    (qualifies('NGN') ? 'NGN' : null) ??
    list.find((c) => currencyTrades(list, c.code, opts.leg))?.code ??
    'NGN'
  );
}

/**
 * Short currency label for ramp screen copy ("Sell for Naira", "Sell for UGX").
 *
 * Naira keeps its word because it is the home market and the copy predates
 * other currencies. Everything else uses its code: the full names are too long
 * for a header, and "Shilling" alone would be ambiguous across KES/UGX/TZS.
 */
export function rampCurrencyLabel(code: string): string {
  return code === 'NGN' ? 'Naira' : code;
}

/** One direction of trade, for screens that only do one. */
export type RampLeg = 'buy' | 'sell';

/**
 * Whether a currency can be traded at all — or, when `leg` is given, in that
 * direction specifically.
 *
 * A provider is necessary but not sufficient: UGX and TZS have offramp
 * providers and no onramp.
 */
function currencyTrades(list: RampCurrency[], code: string, leg?: RampLeg): boolean {
  if (rampProviderFor(list, code) === '') return false;
  if (!leg) return true;
  const legs = rampLegsFor(list, code);
  return leg === 'buy' ? legs.buy : legs.sell;
}

/**
 * Ramp-scoped currency, optionally restricted to one provider and/or one
 * direction.
 *
 * Pass `providerFilter` from a provider-specific screen so its picker never
 * offers a currency that provider doesn't serve. Pass `leg` from a screen that
 * only does one direction — a buy screen handed a sell-only currency can never
 * quote, and the user finds out only after typing an amount.
 */
export function useRampCurrency(providerFilter?: RampProvider, leg?: RampLeg) {
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

  // The candidate list this screen may use: every provider on the hub (which
  // has to know about both to route stablecoin buy/sell correctly), or just the
  // one a Paycrest/Flutterwave-specific screen actually talks to — and, on a
  // one-direction screen, only the currencies that trade that way.
  const candidates = useMemo(() => {
    const byProvider = providerFilter
      ? all.filter((c) => c.rampProvider === providerFilter)
      : all.filter((c) => c.rampProvider !== '');
    if (!leg) return byProvider;
    return byProvider.filter((c) => {
      const legs = rampLegsFor(all, c.code);
      return leg === 'buy' ? legs.buy : legs.sell;
    });
  }, [all, providerFilter, leg]);

  const code = useMemo(
    () =>
      resolveRampCurrencyCode(selectedCode, candidates, {
        kycCountry: currentUser?.country,
        deviceCountry: deviceCountryCode(),
        leg,
      }),
    [selectedCode, candidates, currentUser?.country, leg],
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

  // Only offer a choice when there is one — a picker listing a single currency
  // is noise. Today Paycrest serves NGN, KES, UGX and TZS, so there is a real
  // choice; GHS is absent because Paycrest does not list it and the
  // Flutterwave path behind it still quotes and settles in naira.
  //
  // The options are currencies with a PROVIDER, not currencies that trade both
  // ways: UGX and TZS sell only. The hub reads the per-direction legs and
  // renders the live one — a currency the user can sell is still worth
  // offering here.
  const options: RampCurrencyOption[] = useMemo(
    () => candidates.map((c) => ({ id: c.code, label: c.code })),
    [candidates],
  );

  return { code, currency, setCode, options, currencies: all, loading };
}
