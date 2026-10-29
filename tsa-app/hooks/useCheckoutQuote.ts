// The active cart priced for a pay token by the backend (POST /orders/quote),
// plus formatters for showing a base-unit amount as "0.16 USDC" and "≈ ₦219".
//
// Checkout shows these numbers instead of adding up cart-summary fields on the
// client, because the quote is computed by the code that creates the orders:
// the total a buyer confirms is the total the wallet is asked to approve.
import { useCallback, useEffect, useRef, useState } from 'react';
import { ethers } from 'ethers';
import { useCurrency } from '@/contexts/CurrencyContext';
import { quoteCart, type CartQuote } from '@/services/orderApi';

interface QuoteParams {
  token: string;
  city: string;
  state: string;
  country: string;
  serviceTiers?: Record<string, string>;
  /** False while there is nothing to price (no address, empty cart). */
  enabled: boolean;
  /** Changes whenever the cart contents change, to re-price. */
  cartKey: string;
}

export function useCheckoutQuote({ token, city, state, country, serviceTiers, enabled, cartKey }: QuoteParams) {
  const [quote, setQuote] = useState<CartQuote | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Switching token or address quickly fires overlapping requests; only the
  // latest may land.
  const requestId = useRef(0);
  const tiersKey = JSON.stringify(serviceTiers ?? {});

  const refresh = useCallback(async () => {
    const id = ++requestId.current;
    if (!enabled) {
      setQuote(null);
      setError(null);
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    const tiers = JSON.parse(tiersKey) as Record<string, string>;
    const res = await quoteCart(token, city, state, country, Object.keys(tiers).length ? tiers : undefined);
    if (id !== requestId.current) return;
    if (res.success && res.data) {
      setQuote(res.data);
    } else {
      setQuote(null);
      setError(res.message || 'Could not price your cart');
    }
    setLoading(false);
    // cartKey is a dependency so a cart change re-prices.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, city, state, country, tiersKey, enabled, cartKey]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // A quote for another token is never shown, even for the frame before the
  // new one arrives.
  const current = quote && quote.token === token.toUpperCase() ? quote : null;
  return { quote: current, loading: loading || (enabled && !current && !error), error, refresh };
}

/** Adds base-unit amounts exactly (bigint), returning a base-unit string. */
export function sumBase(...parts: string[]): string {
  try {
    return parts.reduce((acc, p) => acc + BigInt(p), 0n).toString();
  } catch {
    return '0';
  }
}

/**
 * What the buyer pays for the listings themselves: the escrowed productAmount, which is the listing
 * price. OrderEscrow takes the platform fee from the seller at release, so it is never added here.
 */
export const listingAmount = (a: { productAmount: string; platformFee: string }) => a.productAmount;

/** Base units → token units as a number (display only; never for money math). */
export function quoteUnits(base: string, decimals: number): number {
  try {
    return Number(ethers.formatUnits(BigInt(base), decimals));
  } catch {
    return 0;
  }
}

/** Formats quote amounts in the pay token and, approximately, in the app currency. */
export function useQuoteFormat(quote: CartQuote | null) {
  const { currency, formatPrice } = useCurrency();

  const token = useCallback(
    (base: string) => {
      if (!quote) return '';
      const units = quoteUnits(base, quote.decimals);
      const maxDigits = quote.token === 'MCGP' ? 4 : 2;
      const text = units.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: maxDigits });
      return `${text} ${quote.token}`;
    },
    [quote],
  );

  /** "≈ ₦219.26"; empty when it would only repeat the token amount (USD stables). */
  const approx = useCallback(
    (base: string) => {
      if (!quote) return '';
      if (currency.code === 'USD' && quote.token !== 'MCGP') return '';
      return `≈ ${formatPrice(quoteUnits(base, quote.decimals) * quote.usdPerToken)}`;
    },
    [quote, currency.code, formatPrice],
  );

  return { token, approx };
}
