// hooks/useSwappableHeld.ts
//
// Held tokens nobody vouches for (the wallet's discovered ERC-20s, SPL
// tokens…) as picker Assets — only those LiFi can swap. Every picker flow is a
// LiFi route, and an airdrop LiFi does not carry is nothing she can use. The
// Asset is built from LiFi's token, so a coin LiFi verified is labelled so.
//
// One request answers a whole batch (GET /swap/lifi/tokens/all?tokens=…). A
// token LiFi carries is remembered for the app session; "LiFi does not carry
// it" only for an hour, since LiFi's list grows daily. A failed request is
// asked again on the next open.

import { useEffect, useMemo, useState } from 'react';
import { getLiFiAllChainTokens, type LiFiToken } from '@/services/lifi';
import { attachHoldings, groupAssets, type Asset, type Holding } from '@/services/assetCatalog';

const NOT_ON_LIFI_TTL_MS = 60 * 60 * 1000;
const MAX_PER_REQUEST = 200; // the endpoint's cap

const holdingKey = (h: Pick<Holding, 'chainId' | 'address'>) => `${h.chainId}:${h.address.toLowerCase()}`;

const answers = new Map<string, { token: LiFiToken | null; at: number }>();
const answerFor = (h: Holding) => {
  const a = answers.get(holdingKey(h));
  if (!a) return undefined;
  if (!a.token && Date.now() - a.at > NOT_ON_LIFI_TTL_MS) return undefined;
  return a;
};

async function lookUp(holdings: Holding[]): Promise<void> {
  const todo = holdings.filter(h => !answerFor(h));
  for (let i = 0; i < todo.length; i += MAX_PER_REQUEST) {
    const batch = todo.slice(i, i + MAX_PER_REQUEST);
    const page = await getLiFiAllChainTokens({
      tokens: batch.map(h => ({ chainId: h.chainId, address: h.address })),
      limit: 500,
    });
    const found = new Map(page.tokens.map(t => [holdingKey({ chainId: t.chainId ?? 0, address: t.address }), t]));
    const at = Date.now();
    for (const h of batch) answers.set(holdingKey(h), { token: found.get(holdingKey(h)) ?? null, at });
  }
}

/** The ones among `unvouched` LiFi can swap, balances attached. Re-asks on every open (`visible`). */
export function useSwappableHeld(unvouched: Holding[], visible: boolean): Asset[] {
  const key = unvouched.map(holdingKey).sort().join(',');
  const [tokens, setTokens] = useState<LiFiToken[]>([]);
  useEffect(() => {
    let cancelled = false;
    const known = () => unvouched.flatMap(h => answerFor(h)?.token ?? []);
    setTokens(known());
    lookUp(unvouched)
      .then(() => { if (!cancelled) setTokens(known()); })
      .catch(() => {}); // stays unshown until an open where the request succeeds
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- key stands for unvouched's content.
  }, [key, visible]);

  return useMemo(
    () => attachHoldings(groupAssets(tokens), unvouched).assets.filter(a => a.balance > 0),
    [tokens, unvouched],
  );
}

/** Test seam: forget every answer. */
export function resetSwappableHeldCache() {
  answers.clear();
}
