// ──────────────────────────────────────────────
// Exchange rate API — fetches P2P rates from backend
// ──────────────────────────────────────────────

import AsyncStorage from "@react-native-async-storage/async-storage";
import { API_BASE_URL } from "../constants/api/config";

export interface P2PRate {
  currency: string;
  symbol: string;
  binanceBuy: number;
  binanceSell: number;
  midRate: number;
  yellowCard?: number;
  updatedAt: number;
}

export interface RatesResponse {
  base: string;
  rates: Record<string, P2PRate>;
  updatedAt: number;
}

export interface ConvertResponse {
  usdAmount: number;
  localAmount: number;
  currency: string;
  symbol: string;
  rate: number;
  binanceBuy: number;
  binanceSell: number;
  exchangeRate: number;
}

const CACHE_KEY = "exchange_rates";
const CACHE_TTL_MS = 30_000; // 30 seconds

// Fallback rates used when the backend is unreachable (dev / offline).
// Updated approximate mid-market rates — replaced by live P2P rates from the backend.
const FALLBACK_RATES: Record<string, P2PRate> = {
  NGN: { currency: "NGN", symbol: "₦", binanceBuy: 1540, binanceSell: 1560, midRate: 1550, yellowCard: 1545, updatedAt: 0 },
  GHS: { currency: "GHS", symbol: "GH₵", binanceBuy: 14.8, binanceSell: 15.2, midRate: 15.0, yellowCard: 14.9, updatedAt: 0 },
  KES: { currency: "KES", symbol: "KSh", binanceBuy: 128, binanceSell: 132, midRate: 130, yellowCard: 129, updatedAt: 0 },
  ZAR: { currency: "ZAR", symbol: "R", binanceBuy: 17.6, binanceSell: 18.2, midRate: 17.9, yellowCard: 17.8, updatedAt: 0 },
  UGX: { currency: "UGX", symbol: "USh", binanceBuy: 3700, binanceSell: 3900, midRate: 3800, updatedAt: 0 },
  TZS: { currency: "TZS", symbol: "TSh", binanceBuy: 2500, binanceSell: 2700, midRate: 2600, updatedAt: 0 },
  RWF: { currency: "RWF", symbol: "FRw", binanceBuy: 1300, binanceSell: 1400, midRate: 1350, updatedAt: 0 },
  XOF: { currency: "XOF", symbol: "CFA", binanceBuy: 610, binanceSell: 630, midRate: 620, updatedAt: 0 },
  XAF: { currency: "XAF", symbol: "FCFA", binanceBuy: 610, binanceSell: 630, midRate: 620, updatedAt: 0 },
  EUR: { currency: "EUR", symbol: "€", binanceBuy: 0.91, binanceSell: 0.93, midRate: 0.92, updatedAt: 0 },
  GBP: { currency: "GBP", symbol: "£", binanceBuy: 0.78, binanceSell: 0.80, midRate: 0.79, updatedAt: 0 },
};

class ExchangeRateService {
  private cachedRates: RatesResponse | null = null;
  private lastFetchTime = 0;

  /**
   * Fetch P2P exchange rates from the backend (cached locally for 30s).
   */
  async getRates(forceRefresh = false): Promise<RatesResponse> {
    const now = Date.now();

    // Return in-memory cache if fresh
    if (!forceRefresh && this.cachedRates && now - this.lastFetchTime < CACHE_TTL_MS) {
      return this.cachedRates;
    }

    // Try AsyncStorage cache as fallback (offline)
    if (!forceRefresh) {
      try {
        const cached = await AsyncStorage.getItem(CACHE_KEY);
        if (cached) {
          const parsed: RatesResponse & { _cachedAt: number } = JSON.parse(cached);
          if (now - parsed._cachedAt < CACHE_TTL_MS) {
            this.cachedRates = parsed;
            this.lastFetchTime = now;
            return parsed;
          }
        }
      } catch {}
    }

    // Fetch from backend
    try {
      const response = await fetch(`${API_BASE_URL}/exchange/rates`, {
        method: "GET",
        headers: { Accept: "application/json" },
        signal: AbortSignal.timeout(5000),
      });

      if (!response.ok) {
        throw new Error(`Exchange rate API returned ${response.status}`);
      }

      const json = await response.json();
      const ratesResponse: RatesResponse = {
        base: "USD",
        rates: json.data?.rates || json.data || json.rates || {},
        updatedAt: json.data?.updatedAt || json.updatedAt || Math.floor(now / 1000),
      };

      // Cache in memory and AsyncStorage
      this.cachedRates = ratesResponse;
      this.lastFetchTime = now;
      try {
        await AsyncStorage.setItem(
          CACHE_KEY,
          JSON.stringify({ ...ratesResponse, _cachedAt: now }),
        );
      } catch {}

      return ratesResponse;
    } catch {
      // Backend unreachable — return fallback rates so the app still works
      console.warn("Exchange rate backend unreachable, using fallback rates");
      const fallbackResponse: RatesResponse = {
        base: "USD",
        rates: { ...FALLBACK_RATES },
        updatedAt: Math.floor(now / 1000),
      };
      this.cachedRates = fallbackResponse;
      this.lastFetchTime = now;
      return fallbackResponse;
    }
  }

  /**
   * Get the mid-rate for a specific currency.
   */
  async getRate(currency: string): Promise<number> {
    const rates = await this.getRates();
    const rate = rates.rates[currency.toUpperCase()];
    if (!rate) {
      throw new Error(`Unsupported currency: ${currency}`);
    }
    return rate.midRate;
  }

  /**
   * Convert a USD amount to the target currency.
   */
  async convert(usdAmount: number, currency: string): Promise<ConvertResponse> {
    try {
      const response = await fetch(`${API_BASE_URL}/exchange/convert`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json",
        },
        body: JSON.stringify({ amount: usdAmount, currency: currency.toUpperCase() }),
        signal: AbortSignal.timeout(5000),
      });

      if (!response.ok) {
        throw new Error(`Convert API returned ${response.status}`);
      }

      const json = await response.json();
      return json.data || json;
    } catch {
      // Backend unreachable — client-side conversion using fallback rates
      const rate = await this.getRate(currency);
      return {
        usdAmount,
        localAmount: usdAmount * rate,
        currency: currency.toUpperCase(),
        symbol: "",
        rate,
        binanceBuy: rate,
        binanceSell: rate,
        exchangeRate: rate,
      };
    }
  }

  /**
   * Clear cached rates (call on demand if user requested refresh).
   */
  async clearCache() {
    this.cachedRates = null;
    this.lastFetchTime = 0;
    await AsyncStorage.removeItem(CACHE_KEY);
  }
}

export const exchangeRateService = new ExchangeRateService();
export default exchangeRateService;
