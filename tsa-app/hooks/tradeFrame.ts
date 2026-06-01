// Pure classifier for inbound notification frames — no React / network, so it's
// trivially unit-testable. Decides whether a WS frame should refresh a given
// trade and which refresh (trade reload vs. message pull). Used by
// useTradeRealtime; kept separate so tests don't drag in the WS/AsyncStorage
// context.

export type TradeFrameKind = 'trade' | 'message' | null;

export function classifyTradeFrame(
  frame: { type?: string; data?: Record<string, unknown> | null } | null | undefined,
  tradeId: string,
): TradeFrameKind {
  if (!frame || !frame.type || !tradeId) return null;
  const frameTradeId = frame.data?.tradeId;
  if (frameTradeId == null) return null;
  // data.tradeId may arrive as a number or string; compare loosely.
  if (String(frameTradeId) !== String(tradeId)) return null;
  if (frame.type === 'p2p.message.new') return 'message';
  if (frame.type.startsWith('p2p.trade.')) return 'trade';
  return null;
}
