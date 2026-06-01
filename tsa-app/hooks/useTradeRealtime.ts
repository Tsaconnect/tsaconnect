// useTradeRealtime — event-driven refresh for the P2P trade cockpit.
//
// The backend has no per-trade WS room; it pushes every event addressed to the
// user over one socket (contexts/NotificationContext). This hook subscribes to
// that stream and, for frames belonging to THIS trade, fires the right refresh:
//   - any `p2p.trade.*` transition  → onTrade()  (reload the trade row)
//   - `p2p.message.new`             → onMessage() (pull new chat messages)
//
// Events are best-effort (the server may drop on a full buffer, nothing is
// replayed across a disconnect, and system messages emit no event at all), so
// callers MUST keep a focus refetch + slow safety poll as a backstop — this hook
// is the fast path, not the source of truth.
import { useEffect, useRef } from 'react';
import { useNotifications } from '../contexts/NotificationContext';
import { classifyTradeFrame } from './tradeFrame';

export { classifyTradeFrame } from './tradeFrame';
export type { TradeFrameKind } from './tradeFrame';

export interface TradeRealtimeHandlers {
  /** A `p2p.trade.*` transition arrived for this trade. */
  onTrade: () => void;
  /** A new chat message arrived for this trade. */
  onMessage: () => void;
}

/** Subscribe to realtime frames for one trade while `enabled`. Handlers are read
 *  through a ref so changing their identity never rebinds the WS subscription. */
export function useTradeRealtime(
  tradeId: string | undefined,
  handlers: TradeRealtimeHandlers,
  enabled = true,
): void {
  const { subscribe } = useNotifications();
  const handlersRef = useRef(handlers);
  useEffect(() => {
    handlersRef.current = handlers;
  });

  useEffect(() => {
    if (!enabled || !tradeId) return;
    return subscribe((n) => {
      const kind = classifyTradeFrame(n, tradeId);
      if (kind === 'trade') handlersRef.current.onTrade();
      else if (kind === 'message') handlersRef.current.onMessage();
    });
  }, [subscribe, tradeId, enabled]);
}
