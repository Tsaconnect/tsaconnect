// Pure mapper: notification/push `data` payload → in-app route (or null).
// No native imports, so it's unit-testable; the push tap handler calls it.
//
// Only routes we've verified are returned. Unmapped types return null — the
// notification still displays; tapping just opens the app (no dead navigation).
// Non-P2P deep-links (orders/wallet) are a follow-on once those routes are
// disambiguated (the app currently has two `orderdetail` routes).

export interface NotificationData {
  type?: string;
  tradeId?: string;
  [key: string]: unknown;
}

export function routeForNotification(data: NotificationData | null | undefined): string | null {
  if (!data || typeof data.type !== 'string') return null;
  const type = data.type;

  // P2P trade lifecycle + chat → the trade cockpit (fallback: the trades list).
  if (type.startsWith('p2p.trade.') || type === 'p2p.message.new') {
    return data.tradeId ? `/p2p/trade/${data.tradeId}` : '/p2p/my-trades';
  }
  // P2P capital movement → the seller pool.
  if (type === 'p2p.deposit.confirmed' || type === 'p2p.withdraw.confirmed') {
    return '/p2p/pool';
  }
  return null;
}
