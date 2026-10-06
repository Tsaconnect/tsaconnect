// utils/itActivity.ts
//
// One activity list across the two systems behind Instant Transfer: IT intents
// (MCGP, and stablecoins on the Flutterwave rail) and Paycrest orders.
//
// Pure so the hub and the full activity screen render the same rows, and so the
// labels can be tested without a renderer. Routing stays out of it — a row
// carries its href and the screen decides how to navigate.
//
// Status labels here are SHORT on purpose. They sit in a pill beside the
// amount, and the full sentences ("Expired — no payment received") overflowed
// the row. The order and intent screens still show the long form, where there
// is room to explain.
import type { ITIntent } from '@/services/itApi';
import type { PaycrestOrder } from '@/services/paycrestApi';
import type { StatusTone } from '@/components/ui/statusMap';

export interface ITActivity {
  key: string;
  direction: 'buy' | 'sell';
  /** "USDC", "MCGP" — what was bought or sold. */
  asset: string;
  /** What the user put in: fiat on a buy, the asset on a sell. */
  amount: string;
  status: { label: string; tone: StatusTone };
  at: string;
  href: string;
}

const intentStatus: Record<string, ITActivity['status']> = {
  pending_payment: { label: 'Awaiting transfer', tone: 'warning' },
  processing: { label: 'Processing', tone: 'info' },
  credited: { label: 'Complete', tone: 'success' },
  settled: { label: 'Complete', tone: 'success' },
  debited: { label: 'Paying out', tone: 'info' },
  awaiting_rate: { label: 'Awaiting rate', tone: 'warning' },
  failed: { label: 'Failed', tone: 'danger' },
  cancelled: { label: 'Cancelled', tone: 'neutral' },
  reversed: { label: 'Reversed', tone: 'neutral' },
};

function paycrestStatus(o: PaycrestOrder): ITActivity['status'] {
  switch (o.status) {
    case 'awaiting_payment':
      return { label: 'Awaiting transfer', tone: 'warning' };
    case 'awaiting_deposit':
      return { label: 'Awaiting deposit', tone: 'warning' };
    case 'pending':
      return { label: 'Processing', tone: 'info' };
    case 'settling':
      return { label: o.direction === 'buy' ? 'Sending crypto' : 'Paying out', tone: 'info' };
    case 'settled':
      return { label: 'Complete', tone: 'success' };
    case 'refunded':
      return { label: 'Refunded', tone: 'neutral' };
    case 'expired':
      return { label: 'Expired', tone: 'neutral' };
    // The user's money is real and being recovered; "failed" would be untrue.
    case 'unmatched':
      return { label: 'Recovering', tone: 'warning' };
    case 'failed':
      return { label: 'Failed', tone: 'danger' };
    case 'cancelled':
      return { label: 'Cancelled', tone: 'neutral' };
    default:
      return { label: humanize(o.status), tone: 'neutral' };
  }
}

const humanize = (s: string) => {
  const t = (s || '').replace(/_/g, ' ');
  return t ? t[0].toUpperCase() + t.slice(1) : 'Unknown';
};

/** Fiat symbol for a currency code; a code with no symbol reads as "UGX 5,000". */
export type SymbolFor = (code: string) => string;

// Symbols for the currencies the ramp settles in, so an amount never depends on
// the rates API having loaded. The loaded list wins when it has a real symbol.
const KNOWN_SYMBOLS: Record<string, string> = { NGN: '₦', GHS: 'GH₵', KES: 'KSh' };

export function fiatSymbol(code: string, loaded: { code: string; symbol: string }[]): string {
  const upper = (code || '').toUpperCase();
  const fromList = loaded.find((c) => c.code === upper)?.symbol;
  if (fromList && fromList !== upper) return fromList;
  return KNOWN_SYMBOLS[upper] ?? upper;
}

const fiat = (symbolFor: SymbolFor, code: string, major: number | string) => {
  const n = Number(major);
  const text = Number.isFinite(n)
    ? n.toLocaleString(undefined, { maximumFractionDigits: 2 })
    : String(major);
  const sym = symbolFor(code);
  return sym && sym !== code ? `${sym}${text}` : `${code} ${text}`;
};

const tokenAmount = (value: string | undefined, asset: string) => {
  const n = Number(value);
  if (!value || !Number.isFinite(n)) return `— ${asset}`;
  return `${n.toLocaleString(undefined, { maximumFractionDigits: n >= 1 ? 4 : 6 })} ${asset}`;
};

const mcgpFromWei = (wei?: string) => {
  // 18 decimals. Precision past 4 places is noise on a list row.
  const n = Number(wei || '0') / 1e18;
  return Number.isFinite(n) ? n : 0;
};

export function intentActivity(i: ITIntent, symbolFor: SymbolFor): ITActivity {
  // A stablecoin intent carries asset/assetAmount and leaves mcgpAmount empty.
  // A converted buy names what she receives, not the USDC leg it settles on.
  const asset = i.requestedToken || (i.asset && i.asset !== 'MCGP' ? i.asset : 'MCGP');
  const currency = i.currency || 'NGN';
  const amount =
    i.direction === 'buy'
      ? fiat(symbolFor, currency, Number(i.ngnKobo || '0') / 100)
      : asset === 'MCGP'
        ? tokenAmount(String(mcgpFromWei(i.mcgpAmount)), 'MCGP')
        : tokenAmount(i.assetAmount, asset);
  return {
    key: `it-${i.id}`,
    direction: i.direction,
    asset,
    amount,
    status: intentStatus[i.status] ?? { label: humanize(i.status), tone: 'neutral' },
    at: i.createdAt,
    href: `/wallet/it/intent/${i.id}`,
  };
}

export function paycrestActivity(o: PaycrestOrder, symbolFor: SymbolFor): ITActivity {
  return {
    key: `pc-${o.id}`,
    direction: o.direction,
    asset: o.token,
    amount:
      o.direction === 'buy'
        ? fiat(symbolFor, o.currency || 'NGN', o.amount)
        : tokenAmount(o.tokenAmount ?? o.amount, o.token),
    status: paycrestStatus(o),
    at: o.createdAt,
    href: `/wallet/paycrest/order/${o.id}`,
  };
}

/**
 * Newest first across both systems. An undated row sorts last rather than
 * jumping to the top on a bad parse.
 *
 * `hideCancelledIntents` keeps the hub's short list about live money: an IT
 * intent is cancelled whenever a user backs out of a buy form, so they pile up.
 * The full list shows everything.
 */
export function buildITActivity(
  intents: ITIntent[],
  orders: PaycrestOrder[],
  symbolFor: SymbolFor,
  opts: { hideCancelledIntents?: boolean } = {},
): ITActivity[] {
  const rows = [
    ...intents
      .filter((i) => !opts.hideCancelledIntents || i.status !== 'cancelled')
      .map((i) => intentActivity(i, symbolFor)),
    ...orders.map((o) => paycrestActivity(o, symbolFor)),
  ];
  rows.sort((a, b) => (Date.parse(b.at) || 0) - (Date.parse(a.at) || 0));
  return rows;
}

/** "Just now", "5m ago", "3h ago", "2d ago", then "Sep 4". */
export function relativeTime(iso: string, now: Date = new Date()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return '';
  const mins = Math.floor((now.getTime() - t) / 60000);
  if (mins < 1) return 'Just now';
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  if (days < 7) return `${days}d ago`;
  return new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}
