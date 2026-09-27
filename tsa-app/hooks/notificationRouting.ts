// Pure mapper: notification/push `data` payload → in-app route (or null).
// No native imports, so it's unit-testable; the push tap handler calls it.
//
// Only routes we've verified are returned. Unmapped types return null — the
// notification still displays; tapping just opens the app (no dead navigation).
// Non-P2P deep-links (orders/wallet) are a follow-on once those routes are
// disambiguated (the app currently has two `orderdetail` routes).
import { PAY_LINKS, PAY_SETTINGS } from '@/constants/routes';
import { isPaymentId } from '@/utils/paymentId';

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
  // P2P capital movement → funding context (deposit) or reclaim screen (withdraw).
  if (type === 'p2p.deposit.confirmed') {
    return '/p2p/my-ads';
  }
  if (type === 'p2p.withdraw.confirmed') {
    return '/p2p/pool';
  }
  // A TSA Pay merchant was paid, in the payment's own mode (a test payment is
  // on the test list). A one-time link opens that link — the payment is the
  // link; a reusable link's payment opens Payment links; one her own server
  // made (API) has no screen in the app, so she lands on Payment Settings. The
  // id is only ever used as a payment id: anything else falls back to the list.
  if (type === 'pay_payment_received') {
    if (data.source !== 'link' && data.source !== 'open') return PAY_SETTINGS;
    const mode = data.mode === 'live' || data.mode === 'test' ? data.mode : undefined;
    if (data.source === 'link' && mode && isPaymentId(data.paymentId)) {
      return `${PAY_LINKS}/${data.paymentId}?mode=${mode}`;
    }
    return mode ? `${PAY_LINKS}?mode=${mode}` : PAY_LINKS;
  }
  return null;
}
