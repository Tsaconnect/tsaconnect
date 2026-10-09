// hooks/usePendingTradeTx.ts
//
// The trade screen's view of runP2PTx's lost-confirm stash for one trade:
// the newest unresolved tx (shown with a retry-confirm) and whether it is
// stale (the action buttons come back). Re-read whenever the trade changes —
// the trade moving past a stashed tx drops it from the store — and on
// refresh(), which the screen calls after every action: a confirm-failed
// stashes before its reload, and that reload can fail too.
import { useCallback, useEffect, useState } from 'react';
import type { P2PTrade } from '@/services/p2p';
import { listPendingForTrade, removePending, type PendingTradeTx } from '@/services/p2pPending';
import { splitPendingTradeTxs } from '@/utils/p2pTradeView';

export interface PendingTradeTxState {
  pending: PendingTradeTx | null;
  stale: boolean;
  refresh: () => void;
}

export function usePendingTradeTx(trade: P2PTrade | null): PendingTradeTxState {
  const [pending, setPending] = useState<PendingTradeTx | null>(null);
  const [stale, setStale] = useState(false);
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    if (!trade) return;
    let cancelled = false;
    (async () => {
      const split = splitPendingTradeTxs(await listPendingForTrade(trade.id), trade, Date.now());
      for (const rec of split.settled) {
        try {
          await removePending(rec.txHash);
        } catch (err) {
          console.warn('[usePendingTradeTx] drop settled stash failed', err);
        }
      }
      if (cancelled) return;
      setPending(split.active);
      setStale(split.stale);
    })();
    return () => {
      cancelled = true;
    };
  }, [trade, tick]);

  return { pending, stale, refresh };
}
