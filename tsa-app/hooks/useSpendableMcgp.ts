// hooks/useSpendableMcgp.ts
//
// The active wallet's Spendable MCGP (the entitlement balance) for a coin list
// to display. Read at most once a minute per wallet: it is a contract read, and
// a screen opens its picker more than once.
//
// getPremiumMcgpAsset answers a failed read with a zero balance, so only a
// positive balance is kept: a zero is asked again on the next open, and a
// failure never passes for "no Spendable MCGP" for a whole minute.

import { useEffect, useState } from 'react';
import { getPremiumMcgpAsset } from '@/services/mcgpSwapApi';
import { getActiveWallet } from '@/services/wallet';

export type SpendableMcgp = { balance: number; usdValue: number };

const TTL_MS = 60_000;
let cached: { wallet: string; at: number; read: Promise<SpendableMcgp | null> } | null = null;

async function loadSpendableMcgp(): Promise<SpendableMcgp | null> {
  const wallet = await getActiveWallet();
  if (!wallet) return null;
  if (!cached || cached.wallet !== wallet || Date.now() - cached.at > TTL_MS) {
    const read = getPremiumMcgpAsset().catch(() => null);
    cached = { wallet, at: Date.now(), read };
    read.then(v => { if (!(v && v.balance > 0) && cached?.read === read) cached = null; });
  }
  return cached.read;
}

/** Spendable MCGP while `enabled`; null until read, or when there is none to show. */
export function useSpendableMcgp(enabled: boolean): SpendableMcgp | null {
  const [spendable, setSpendable] = useState<SpendableMcgp | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    loadSpendableMcgp().then(v => { if (!cancelled) setSpendable(v); });
    return () => { cancelled = true; };
  }, [enabled]);
  return enabled ? spendable : null;
}

/** Test seam: forget the cached read. */
export function resetSpendableMcgpCache() {
  cached = null;
}
