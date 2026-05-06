// CurrencyContext — manages selected currency & exchange rates.
//
// The list of supported currencies and their metadata is driven by the backend
// rates response. Only USD is hardcoded as a guaranteed offline fallback.

import React, {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  ReactNode,
  useCallback,
  useRef,
} from "react";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { CurrencyConfig, DEFAULT_CURRENCY } from "../constants/currencies";
import { exchangeRateService, P2PRate } from "../services/exchangeRateApi";
import { useAuth } from "../AuthContext/AuthContext";

const SELECTED_CURRENCY_KEY = "selected_currency";

const COUNTRY_CURRENCY_MAP: Record<string, string> = {
  NG: "NGN", GH: "GHS", KE: "KES", ZA: "ZAR", UG: "UGX", TZ: "TZS", RW: "RWF",
  SN: "XOF", CI: "XOF", ML: "XOF", BF: "XOF", BJ: "XOF", TG: "XOF", NE: "XOF",
  CM: "XAF", CF: "XAF", TD: "XAF", GQ: "XAF", GA: "XAF", CG: "XAF",
  GB: "GBP", US: "USD",
  DE: "EUR", FR: "EUR", IT: "EUR", ES: "EUR", NL: "EUR", BE: "EUR",
};

function rateToConfig(rate: P2PRate): CurrencyConfig {
  return {
    code: rate.currency,
    symbol: rate.symbol || rate.currency,
    name: rate.name || rate.currency,
    flag: rate.flag,
  };
}

// Compare CurrencyConfigs by value, not identity. Used to decide whether
// metadata genuinely changed before triggering a re-render of every consumer.
function configsEqual(a: CurrencyConfig, b: CurrencyConfig): boolean {
  return (
    a.code === b.code &&
    a.symbol === b.symbol &&
    a.name === b.name &&
    a.flag === b.flag &&
    a.locale === b.locale
  );
}

interface CurrencyContextType {
  currency: CurrencyConfig;
  supportedCurrencies: CurrencyConfig[];
  /** Exchange rate for the selected currency (1 USD = X local). null when rates haven't loaded for this currency. */
  rate: number | null;
  allRates: Record<string, P2PRate>;
  loading: boolean;
  error: string | null;
  /** True when the selected currency's rate is stale (Bybit fallback, expired cache, or missing). */
  isSelectedRateStale: boolean;
  /** Seconds since the last successful backend fetch; absent on cold start failure. */
  staleSeconds: number | null;
  setCurrency: (code: string) => Promise<void>;
  refreshRates: () => Promise<void>;
  /** Single-line price formatted in the selected currency (e.g. "₦27,500.00" or "$20.00"). */
  formatPrice: (usdAmount: number) => string;
  /**
   * Dual-line price: primary is always USD with `$` prefix; secondary is the
   * same value in the selected currency, with the ISO code prefix when on USD
   * (so the two lines are visually distinct: "$20.00" / "USD 20.00").
   */
  formatDualPrice: (usdAmount: number) => { primary: string; secondary: string };
}

const CurrencyContext = createContext<CurrencyContextType>({
  currency: DEFAULT_CURRENCY,
  supportedCurrencies: [DEFAULT_CURRENCY],
  rate: null,
  allRates: {},
  loading: true,
  error: null,
  isSelectedRateStale: false,
  staleSeconds: null,
  setCurrency: async () => {},
  refreshRates: async () => {},
  formatPrice: () => "$0.00",
  formatDualPrice: () => ({ primary: "$0.00", secondary: "USD 0.00" }),
});

interface CurrencyProviderProps {
  children: ReactNode;
}

export function CurrencyProvider({ children }: CurrencyProviderProps) {
  const { currentUser } = useAuth();
  const [currency, setCurrencyState] = useState<CurrencyConfig>(DEFAULT_CURRENCY);
  const [allRates, setAllRates] = useState<Record<string, P2PRate>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [staleSeconds, setStaleSeconds] = useState<number | null>(null);
  const userChosenRef = useRef(false);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const currencyMap = useMemo<Record<string, CurrencyConfig>>(() => {
    const map: Record<string, CurrencyConfig> = { USD: DEFAULT_CURRENCY };
    for (const [code, rate] of Object.entries(allRates)) {
      map[code] = rateToConfig(rate);
    }
    return map;
  }, [allRates]);

  const supportedCurrencies = useMemo<CurrencyConfig[]>(() => {
    const all = Object.values(currencyMap);
    const usd = all.find((c) => c.code === "USD") ?? DEFAULT_CURRENCY;
    const others = all
      .filter((c) => c.code !== "USD")
      .sort((a, b) => a.name.localeCompare(b.name));
    return [usd, ...others];
  }, [currencyMap]);

  // Stub configs created during init (saved-pref load, country-default) only carry
  // the code. Once `currencyMap` is populated, replace the stub with real metadata.
  // Compares by value to avoid re-rendering every consumer on each rate poll, when
  // `currencyMap` is rebuilt with new object identities but the same data.
  // `currency` is intentionally excluded from deps — including it would loop.
  useEffect(() => {
    const fresh = currencyMap[currency.code];
    if (fresh && !configsEqual(fresh, currency)) {
      setCurrencyState(fresh);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currencyMap]);

  // selectedRow may be undefined on cold start before rates have loaded.
  // We deliberately do NOT default to a 1:1 USD-equivalent rate — that would
  // silently render NGN prices as if 1 USD == 1 NGN (≈100x mispricing).
  const selectedRow = allRates[currency.code];
  const rate = selectedRow?.midRate ?? null;
  const isSelectedRateStale = !!selectedRow?.stale || (rate === null && currency.code !== "USD");

  // tagAllAsStale marks every row in the prior allRates as stale=true and
  // recomputes staleSeconds from each row's updatedAt. Used when fetchRates
  // fails — keeping the rows visible at all is a UX choice (better than
  // forcing the app into an empty state) but the FE must surface the staleness.
  const tagAllAsStale = useCallback(() => {
    const nowSec = Math.floor(Date.now() / 1000);
    setAllRates((prev) => {
      const out: Record<string, P2PRate> = {};
      let oldestUpdated = nowSec;
      for (const [k, v] of Object.entries(prev)) {
        if (typeof v.updatedAt === "number" && v.updatedAt > 0 && v.updatedAt < oldestUpdated) {
          oldestUpdated = v.updatedAt;
        }
        out[k] = { ...v, stale: true };
      }
      // Bump staleSeconds based on the oldest row we still hold.
      if (Object.keys(out).length > 0) {
        setStaleSeconds(Math.max(0, nowSec - oldestUpdated));
      }
      return out;
    });
  }, []);

  // User-facing error strings. Technical detail goes to console.warn; the
  // UI sees one of these consistent messages so banners don't churn between
  // "AbortError" / "Exchange rate API returned 500" / friendly text.
  const ERROR_STALE = "Showing cached exchange rates; live rates are temporarily unavailable.";
  const ERROR_UNAVAILABLE = "Exchange rates are temporarily unavailable.";

  // Apply a rates response to context state. When the response is stale
  // (FE service served from AsyncStorage fallback, or backend served from its
  // own expired-cache rescue), set a non-null `error` so consumers that
  // surface `error` as a banner — and not just `isSelectedRateStale` —
  // still inform users their quote is degraded.
  const applyRatesResponse = useCallback((response: { rates: Record<string, P2PRate>; staleSeconds?: number }) => {
    setAllRates(response.rates);
    setStaleSeconds(response.staleSeconds ?? null);
    if (response.staleSeconds && response.staleSeconds > 0) {
      setError(ERROR_STALE);
    } else {
      setError(null);
    }
  }, []);

  const fetchRates = useCallback(async () => {
    try {
      const response = await exchangeRateService.getRates();
      applyRatesResponse(response);
    } catch (err) {
      console.warn("[CurrencyContext] rate fetch failed", err);
      setError(ERROR_UNAVAILABLE);
      // Keep prior rates so the app remains usable, but mark them stale so
      // formatPrice and any consumer checking `isSelectedRateStale` knows.
      tagAllAsStale();
    } finally {
      setLoading(false);
    }
  }, [applyRatesResponse, tagAllAsStale]);

  const refreshRates = useCallback(async () => {
    setLoading(true);
    try {
      await exchangeRateService.clearCache();
      const response = await exchangeRateService.getRates(true);
      applyRatesResponse(response);
    } catch (err) {
      console.warn("[CurrencyContext] rate refresh failed", err);
      setError(ERROR_UNAVAILABLE);
      tagAllAsStale();
    } finally {
      setLoading(false);
    }
  }, [applyRatesResponse, tagAllAsStale]);

  useEffect(() => {
    async function init() {
      try {
        const savedCode = await AsyncStorage.getItem(SELECTED_CURRENCY_KEY);
        if (savedCode) {
          setCurrencyState({
            code: savedCode,
            name: savedCode,
            symbol: savedCode,
          });
          userChosenRef.current = true;
        }
      } catch (e) {
        console.warn("[CurrencyContext] AsyncStorage read failed", e);
      }
      await fetchRates();
    }
    init();

    // 60s polling: keeps the FE responsive to backend cache rotations and lets
    // a backend recovery propagate within a minute even when the previous fetch
    // returned a stale snapshot.
    intervalRef.current = setInterval(fetchRates, 60_000);
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, [fetchRates]);

  useEffect(() => {
    if (userChosenRef.current) return;
    if (!currentUser?.country) return;

    const country = currentUser.country.toUpperCase();
    const currencyCode = COUNTRY_CURRENCY_MAP[country];
    if (!currencyCode) {
      setCurrencyState(DEFAULT_CURRENCY);
      return;
    }
    const config = currencyMap[currencyCode];
    if (config) {
      setCurrencyState(config);
    } else {
      setCurrencyState({ code: currencyCode, name: currencyCode, symbol: currencyCode });
    }
  }, [currentUser, currencyMap]);

  const setCurrency = useCallback(
    async (code: string) => {
      const upper = code.toUpperCase();
      const config = currencyMap[upper];
      if (!config) return;

      userChosenRef.current = true;
      setCurrencyState(config);
      try {
        await AsyncStorage.setItem(SELECTED_CURRENCY_KEY, upper);
      } catch (e) {
        console.warn("[CurrencyContext] AsyncStorage write failed", e);
      }
    },
    [currencyMap],
  );

  const formatPriceFn = useCallback(
    (usdAmount: number): string => {
      // Plain USD path — no rate needed.
      if (currency.code === "USD") {
        return `$${usdAmount.toLocaleString("en-US", {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        })}`;
      }

      // Rates haven't loaded yet for the selected non-USD currency. Render
      // the USD value rather than silently quoting in local currency at 1:1.
      // Caller should also be checking isSelectedRateStale to disable trades.
      if (rate === null) {
        return `$${usdAmount.toLocaleString("en-US", {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        })}`;
      }

      const localAmount = usdAmount * rate;

      let formatted: string;
      try {
        formatted = new Intl.NumberFormat(currency.locale ?? "en-US", {
          style: "currency",
          currency: currency.code,
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        }).format(localAmount);
      } catch (e) {
        console.warn(`[CurrencyContext] Intl format failed for ${currency.code}`, e);
        const sym = currency.symbol || currency.code;
        formatted = `${sym}${localAmount.toLocaleString("en-US", {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        })}`;
      }

      // Visually flag stale rates so consumers that haven't been audited for
      // the Stale flag still surface *something* to the user instead of
      // silently quoting an outdated rate.
      return isSelectedRateStale ? `${formatted}*` : formatted;
    },
    [currency, rate, isSelectedRateStale],
  );

  // Helper used by formatDualPrice for the USD primary line. Always renders
  // the value with the `$` symbol regardless of the user's selected currency.
  const formatUSD = (usdAmount: number) =>
    `$${usdAmount.toLocaleString("en-US", {
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    })}`;

  const formatDualPriceFn = useCallback(
    (usdAmount: number): { primary: string; secondary: string } => {
      const primary = formatUSD(usdAmount);

      // When the user is on USD (or rates aren't loaded yet) we prefix the
      // secondary line with the ISO code so the two lines stay visually
      // distinct: "$20.00" / "USD 20.00".
      if (currency.code === "USD" || rate === null) {
        const secondary = `USD ${usdAmount.toLocaleString("en-US", {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        })}`;
        return { primary, secondary };
      }

      // Non-USD: reuse formatPrice for the local-symbol line. It already
      // appends "*" when the rate is stale, which we want here too.
      return { primary, secondary: formatPriceFn(usdAmount) };
    },
    [currency, rate, formatPriceFn],
  );

  return (
    <CurrencyContext.Provider
      value={{
        currency,
        supportedCurrencies,
        rate,
        allRates,
        loading,
        error,
        isSelectedRateStale,
        staleSeconds,
        setCurrency,
        refreshRates,
        formatPrice: formatPriceFn,
        formatDualPrice: formatDualPriceFn,
      }}
    >
      {children}
    </CurrencyContext.Provider>
  );
}

export function useCurrency() {
  return useContext(CurrencyContext);
}

export default CurrencyContext;
