// ──────────────────────────────────────────────
// Exchange rate API — fetches P2P rates from backend
//
// No hardcoded rate fallbacks: this drives a money-handling product, and
// quoting against stale or invented rates is far more dangerous than failing
// the call. Callers receive an error and should disable trading flows /
// surface a banner until rates are live again.
// ──────────────────────────────────────────────

import AsyncStorage from "@react-native-async-storage/async-storage";
import { API_BASE_URL } from "../constants/api/config";

export interface P2PRate {
  currency: string;
  symbol: string;
  name?: string;
  flag?: string;
  binanceBuy: number;
  binanceSell: number;
  midRate: number;
  source?: "bybit" | "oer";
  /** True when this row was served from a fallback path; FE must not quote against it. */
  stale?: boolean;
  yellowCard?: number;
  updatedAt: number;
}

export interface RatesResponse {
  base: string;
  rates: Record<string, P2PRate>;
  updatedAt: number;
  /** Seconds since this snapshot was fetched from the backend; absent when fresh. */
  staleSeconds?: number;
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
  /** True when the underlying rate is stale; callers MUST refuse to settle on a stale conversion. */
  stale?: boolean;
  source?: "bybit" | "oer";
}

const CACHE_KEY = "exchange_rates";
const MEMORY_TTL_MS = 30_000;
// AsyncStorage cache may be served past memory TTL when the backend is unreachable,
// up to this hard cap so we never quote against rates older than this on cold start.
const STALE_CACHE_MAX_AGE_MS = 6 * 60 * 60 * 1000; // 6 hours

// Validates that a value is a non-empty rate map and that every row carries a
// finite positive midRate. We reject malformed shapes here rather than letting
// them propagate, because downstream callers (formatPrice, conversions) treat
// missing/NaN rates as effectively 1:1 — a 100x mispricing for currencies like
// NGN.
function isValidRateMap(v: unknown): v is Record<string, P2PRate> {
  if (!v || typeof v !== "object" || Array.isArray(v)) return false;
  const entries = Object.entries(v as Record<string, unknown>);
  if (entries.length === 0) return false;
  for (const [, row] of entries) {
    if (!row || typeof row !== "object") return false;
    const r = row as P2PRate;
    if (typeof r.midRate !== "number" || !Number.isFinite(r.midRate) || r.midRate <= 0) {
      return false;
    }
  }
  return true;
}

class ExchangeRateService {
  private cachedRates: RatesResponse | null = null;
  private lastFetchTime = 0;

  /**
   * Fetch P2P exchange rates from the backend.
   *
   * Order: in-memory cache (≤30s) → fresh AsyncStorage cache (≤30s) → live fetch.
   * On live-fetch failure, the most recent AsyncStorage snapshot is served if it
   * is younger than STALE_CACHE_MAX_AGE_MS, marked with `staleSeconds`. Otherwise
   * the original error is re-thrown so callers can surface it.
   */
  async getRates(forceRefresh = false): Promise<RatesResponse> {
    const now = Date.now();

    if (!forceRefresh && this.cachedRates && now - this.lastFetchTime < MEMORY_TTL_MS) {
      return this.cachedRates;
    }

    if (!forceRefresh) {
      const fresh = await this.readCache(now, MEMORY_TTL_MS);
      if (fresh) {
        this.cachedRates = fresh;
        this.lastFetchTime = now;
        return fresh;
      }
    }

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
      const payload = json?.data ?? json;
      const rawRates = payload?.rates;
      const updatedAt = payload?.updatedAt;
      const staleSecondsFromBackend = payload?.staleSeconds;

      // Reject any response that doesn't carry a usable rate map. Falling back
      // to {} or guessing updatedAt would let the FE quote against an empty
      // map (silent USD-equivalent pricing) for hours.
      if (!isValidRateMap(rawRates) || typeof updatedAt !== "number") {
        throw new Error("backend returned malformed rate response");
      }

      const ratesResponse: RatesResponse = {
        base: "USD",
        rates: rawRates,
        updatedAt,
        // Forward backend staleness when present (backend served from its
        // own expired-cache rescue), so the FE banner reflects real age.
        staleSeconds: typeof staleSecondsFromBackend === "number" && staleSecondsFromBackend > 0
          ? staleSecondsFromBackend
          : undefined,
      };

      this.cachedRates = ratesResponse;
      this.lastFetchTime = now;
      void this.writeCache(ratesResponse, now);

      return ratesResponse;
    } catch (err) {
      // Live fetch failed. Try the AsyncStorage snapshot, ignoring its 30s
      // freshness window — but cap it at STALE_CACHE_MAX_AGE_MS.
      const stale = await this.readCache(now, STALE_CACHE_MAX_AGE_MS);
      if (stale) {
        const ageSec = Math.floor((now - (stale.updatedAt * 1000)) / 1000);
        console.warn(
          `[exchangeRateService] backend unreachable (${(err as Error).message}); ` +
          `serving cached rates from ${ageSec}s ago marked stale`,
        );
        const tagged: RatesResponse = {
          ...stale,
          staleSeconds: ageSec,
          rates: Object.fromEntries(
            Object.entries(stale.rates).map(([k, v]) => [k, { ...v, stale: true }]),
          ),
        };
        this.cachedRates = tagged;
        this.lastFetchTime = now;
        return tagged;
      }
      throw err;
    }
  }

  /**
   * Read AsyncStorage cache, returning the entry only if it's younger than maxAgeMs.
   * Corrupted entries are removed so they don't poison subsequent reads.
   */
  private async readCache(now: number, maxAgeMs: number): Promise<RatesResponse | null> {
    let raw: string | null = null;
    try {
      raw = await AsyncStorage.getItem(CACHE_KEY);
    } catch (e) {
      console.warn("[exchangeRateService] AsyncStorage read failed", e);
      return null;
    }
    if (!raw) return null;

    let parsed: RatesResponse & { _cachedAt: number };
    try {
      parsed = JSON.parse(raw);
    } catch (e) {
      console.warn("[exchangeRateService] cache parse failed; clearing corrupt entry", e);
      try {
        await AsyncStorage.removeItem(CACHE_KEY);
      } catch (rmErr) {
        console.warn("[exchangeRateService] AsyncStorage remove failed", rmErr);
      }
      return null;
    }

    if (typeof parsed._cachedAt !== "number" || now - parsed._cachedAt > maxAgeMs) {
      return null;
    }
    if (!isValidRateMap(parsed.rates)) {
      console.warn("[exchangeRateService] cached rates failed validation; clearing");
      try {
        await AsyncStorage.removeItem(CACHE_KEY);
      } catch (e) {
        console.warn("[exchangeRateService] AsyncStorage remove failed", e);
      }
      return null;
    }
    return parsed;
  }

  private async writeCache(response: RatesResponse, now: number) {
    try {
      await AsyncStorage.setItem(
        CACHE_KEY,
        JSON.stringify({ ...response, _cachedAt: now }),
      );
    } catch (e) {
      console.warn("[exchangeRateService] AsyncStorage write failed", e);
    }
  }

  /**
   * Get the mid-rate for a specific currency. Throws if rates are unavailable;
   * callers must NOT use this result for money-handling without checking
   * the backing P2PRate's `stale` flag via getRates() first.
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
   * Convert a USD amount to the target currency via the server-authoritative endpoint.
   * Errors are propagated so callers can fail closed; do NOT silently substitute
   * client-side math for failed conversions.
   *
   * Callers handling money MUST check the returned `stale` flag and refuse to
   * settle if true.
   */
  async convert(usdAmount: number, currency: string): Promise<ConvertResponse> {
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
    const payload = json?.data ?? json;
    if (
      !payload ||
      typeof payload.localAmount !== "number" ||
      !Number.isFinite(payload.localAmount) ||
      typeof payload.rate !== "number" ||
      !Number.isFinite(payload.rate) ||
      payload.rate <= 0
    ) {
      throw new Error("Convert API returned malformed response");
    }
    return payload as ConvertResponse;
  }

  /** Clear cached rates (call on demand if user requested refresh). */
  async clearCache() {
    this.cachedRates = null;
    this.lastFetchTime = 0;
    try {
      await AsyncStorage.removeItem(CACHE_KEY);
    } catch (e) {
      console.warn("[exchangeRateService] AsyncStorage clear failed", e);
    }
  }
}

export const exchangeRateService = new ExchangeRateService();
export default exchangeRateService;
