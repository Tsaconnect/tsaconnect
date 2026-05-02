// ──────────────────────────────────────────────
// CurrencyContext — manages selected currency & exchange rates
// ──────────────────────────────────────────────

import React, {
  createContext,
  useContext,
  useEffect,
  useState,
  ReactNode,
  useCallback,
  useRef,
} from "react";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { CurrencyConfig, CURRENCY_MAP, DEFAULT_CURRENCY } from "../constants/currencies";
import { exchangeRateService, P2PRate } from "../services/exchangeRateApi";
import { useAuth } from "../AuthContext/AuthContext";

const SELECTED_CURRENCY_KEY = "selected_currency";

// Map ISO 3166-1 alpha-2 country codes to supported currency codes
const COUNTRY_CURRENCY_MAP: Record<string, string> = {
  NG: "NGN",
  GH: "GHS",
  KE: "KES",
  ZA: "ZAR",
  UG: "UGX",
  TZ: "TZS",
  RW: "RWF",
  SN: "XOF",
  CI: "XOF",
  ML: "XOF",
  BF: "XOF",
  BJ: "XOF",
  TG: "XOF",
  NE: "XOF",
  CM: "XAF",
  CF: "XAF",
  TD: "XAF",
  GQ: "XAF",
  GA: "XAF",
  CG: "XAF",
  GB: "GBP",
  US: "USD",
  DE: "EUR",
  FR: "EUR",
  IT: "EUR",
  ES: "EUR",
  NL: "EUR",
  BE: "EUR",
};

interface CurrencyContextType {
  /** Currently selected currency config */
  currency: CurrencyConfig;
  /** All supported currencies */
  supportedCurrencies: CurrencyConfig[];
  /** Exchange rate for selected currency (1 USD = X local) */
  rate: number;
  /** All raw P2P rates from backend */
  allRates: Record<string, P2PRate>;
  /** Whether rates are being fetched */
  loading: boolean;
  /** Error message if fetching failed */
  error: string | null;
  /** Switch to a different currency */
  setCurrency: (code: string) => Promise<void>;
  /** Force refresh exchange rates */
  refreshRates: () => Promise<void>;
  /** Format a USD price in the selected currency */
  formatPrice: (usdAmount: number) => string;
}

const CurrencyContext = createContext<CurrencyContextType>({
  currency: DEFAULT_CURRENCY,
  supportedCurrencies: [],
  rate: 1,
  allRates: {},
  loading: true,
  error: null,
  setCurrency: async () => {},
  refreshRates: async () => {},
  formatPrice: () => "$0.00",
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
  const [initialized, setInitialized] = useState(false);
  const userChosenRef = useRef(false);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // ── Derive the rate for the selected currency ──
  const rate = allRates[currency.code]?.midRate ?? 1;

  // ── Fetch rates ──
  const fetchRates = useCallback(async () => {
    try {
      const response = await exchangeRateService.getRates();
      setAllRates(response.rates);
      setError(null);
    } catch (err: any) {
      setError(err.message || "Failed to fetch exchange rates");
      // Keep old rates on failure
    } finally {
      setLoading(false);
    }
  }, []);

  // ── Force refresh ──
  const refreshRates = useCallback(async () => {
    setLoading(true);
    try {
      await exchangeRateService.clearCache();
      const response = await exchangeRateService.getRates(true);
      setAllRates(response.rates);
      setError(null);
    } catch (err: any) {
      setError(err.message || "Failed to refresh exchange rates");
    } finally {
      setLoading(false);
    }
  }, []);

  // ── Load saved currency preference + fetch rates ──
  useEffect(() => {
    async function init() {
      try {
        const savedCode = await AsyncStorage.getItem(SELECTED_CURRENCY_KEY);
        if (savedCode && CURRENCY_MAP[savedCode]) {
          setCurrencyState(CURRENCY_MAP[savedCode]);
          userChosenRef.current = true;
        }
        // No saved preference — will be set by the currentUser effect below
      } catch {}
      await fetchRates();
      setInitialized(true);
    }
    init();

    // Poll for rates every 60 seconds
    intervalRef.current = setInterval(fetchRates, 60_000);
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, [fetchRates]);

  // ── Set default currency from user's country when profile loads ──
  useEffect(() => {
    if (userChosenRef.current) return; // User has explicitly chosen
    if (!currentUser?.country) return; // Profile not loaded yet

    const country = currentUser.country.toUpperCase();
    const currencyCode = COUNTRY_CURRENCY_MAP[country];

    if (currencyCode && CURRENCY_MAP[currencyCode]) {
      setCurrencyState(CURRENCY_MAP[currencyCode]);
    } else {
      // Country not in our supported list — keep USD default
      setCurrencyState(DEFAULT_CURRENCY);
    }
  }, [currentUser]);

  // ── Switch currency ──
  const setCurrency = useCallback(async (code: string) => {
    const config = CURRENCY_MAP[code.toUpperCase()];
    if (!config) return;

    userChosenRef.current = true;
    setCurrencyState(config);
    try {
      await AsyncStorage.setItem(SELECTED_CURRENCY_KEY, code.toUpperCase());
    } catch {}
  }, []);

  // ── Format a USD price in the selected currency ──
  const formatPriceFn = useCallback(
    (usdAmount: number): string => {
      if (currency.code === "USD" || rate === 1) {
        return `$${usdAmount.toLocaleString("en-US", {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        })}`;
      }

      const localAmount = usdAmount * rate;

      try {
        return new Intl.NumberFormat(currency.locale, {
          style: "currency",
          currency: currency.code,
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        }).format(localAmount);
      } catch {
        const sym = currency.symbol || currency.code;
        return `${sym}${localAmount.toLocaleString("en-US", {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        })}`;
      }
    },
    [currency, rate],
  );

  return (
    <CurrencyContext.Provider
      value={{
        currency,
        supportedCurrencies: Object.values(CURRENCY_MAP).filter((c) => allRates[c.code] || c.code === "USD"),
        rate,
        allRates,
        loading,
        error,
        setCurrency,
        refreshRates,
        formatPrice: formatPriceFn,
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
