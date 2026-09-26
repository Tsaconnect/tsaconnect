// hooks/useSellSwapQuote.ts
//
// The one price a swapped sale shows: "~₦X" for this amount of this coin,
// asked of the server (which re-runs it again when the sale opens). Debounced
// like the direct sale's quote, and only ever true for the amount it was asked
// about — a newer keystroke's answer always wins over an older one's.

import { useEffect, useRef, useState } from 'react';
import { ethers } from 'ethers';
import { PaycrestError } from '@/services/paycrestApi';
import { getSellSwapQuote, type SellSwapQuote } from '@/services/paycrestSellSwap';
import type { LiFiToken } from '@/services/lifi';

const QUOTE_DEBOUNCE_MS = 450;

export type SellSwapQuoteArgs = {
  token: LiFiToken;
  chainId: number;
  /** Her address on the source chain. */
  sourceAddress: string;
  /** Human units, as she typed it. */
  amount: string;
  /** Her EVM wallet: where the USDC lands. */
  walletAddress: string | null | undefined;
  currency: string;
};

export function useSellSwapQuote({ token, chainId, sourceAddress, amount, walletAddress, currency }: SellSwapQuoteArgs) {
  const [quote, setQuote] = useState<SellSwapQuote | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const seqRef = useRef(0);

  useEffect(() => {
    const raw = amount.trim();
    if (!walletAddress || !raw || !(Number(raw) > 0)) {
      seqRef.current++;
      setQuote(null);
      setError(null);
      setQuoting(false);
      return;
    }
    let fromAmount: string;
    try {
      fromAmount = ethers.parseUnits(raw, token.decimals).toString();
    } catch {
      setQuote(null);
      setError('Enter a valid amount.');
      return;
    }
    const seq = ++seqRef.current;
    setQuoting(true);
    const t = setTimeout(async () => {
      try {
        const q = await getSellSwapQuote({
          fromChainId: chainId,
          fromToken: token.address,
          fromAmount,
          fromAddress: sourceAddress,
          walletAddress,
          currency,
        });
        if (seq !== seqRef.current) return;
        setQuote(q);
        setError(null);
      } catch (e) {
        if (seq !== seqRef.current) return;
        setQuote(null);
        setError(
          e instanceof PaycrestError && e.status === 422
            ? "This token can't be sold for naira right now"
            : e instanceof PaycrestError
              ? e.message
              : 'Could not fetch a rate. Check your connection.',
        );
      } finally {
        if (seq === seqRef.current) setQuoting(false);
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [token, chainId, sourceAddress, amount, walletAddress, currency]);

  return { quote, quoting, error };
}
