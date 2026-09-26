import AsyncStorage from '@react-native-async-storage/async-storage';
import { formatUnits } from 'ethers';
import { API_BASE_URL } from '../constants/api/config';
import { getTronAddress } from './chains/tronWallet';
import { getBtcAddress } from './chains/btcWallet';
import { getNonEVMHistory, type NonEVMActivity } from './chains/nonevmApi';
import { listITIntents } from './itApi';
import { listOrders as listPaycrestOrders } from './paycrestApi';
import { listMyPayments, type PayHistoryItem, type PayStatus } from './payApi';
import { formatKobo } from '../utils/stableSellQuote';
import { buildITActivity, fiatSymbol, type ITActivity, type SymbolFor } from '../utils/itActivity';
import type { StatusVisual } from '../components/ui/statusMap';

export type ActivityType = 'wallet' | 'swap' | 'tp' | 'cashback' | 'order' | 'payment' | 'service' | 'bill' | 'it';

export interface ActivityItem {
  id: string;
  type: ActivityType;
  subtype?: string;         // send/receive/swap for wallet, pending/completed for order, etc.
  title: string;
  subtitle: string;
  amount?: string;          // formatted display amount
  amountUSD?: number;
  positive?: boolean;       // true = incoming/credit, false = outgoing/debit
  status: string;           // raw status key; display-only for items that carry statusVisual
  timestamp: string;
  txHash?: string;
  chainId?: number;         // EVM chains
  chainKey?: string;        // non-EVM chains (e.g. 'tron') — no numeric chainId
  iconName: string;         // Ionicons name
  iconColor: string;
  iconBg: string;
  href?: string;            // in-app detail screen; wins over the explorer link
  statusVisual?: StatusVisual; // pre-resolved label + tone; overrides `status` in the badge
}

async function authHeaders(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('authToken');
  return {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
}

async function get<T>(path: string): Promise<T | null> {
  try {
    const res = await fetch(`${API_BASE_URL}${path}`, { headers: await authHeaders() });
    const data = await res.json();
    return data?.success ? data : null;
  } catch { return null; }
}

// ── Helpers ────────────────────────────────────────────────────

const TOKEN_DECIMALS: Record<string, number> = {
  MCGP: 18, ETH: 18, BNB: 18, MATIC: 18, AVAX: 18,
  S: 18, ARB: 18, OP: 18,
  USDC: 6, USDT: 6, DAI: 18, BUSD: 18,
};

function weiToHuman(rawWei: string | number, tokenSymbol: string): number {
  const decimals = TOKEN_DECIMALS[tokenSymbol.toUpperCase()] ?? 18;
  const str = String(rawWei ?? '0');
  if (str === '0' || str === '') return 0;
  const padded = str.padStart(decimals + 1, '0');
  const cut = padded.length - decimals;
  const whole = padded.slice(0, cut) || '0';
  const frac = padded.slice(cut).replace(/0+$/, '');
  return parseFloat(frac ? `${whole}.${frac}` : whole);
}

// ── Normalizers ────────────────────────────────────────────────

function truncate(addr: string): string {
  if (!addr || addr.length < 10) return addr;
  return `${addr.slice(0, 6)}…${addr.slice(-4)}`;
}

function fromWalletTx(tx: any): ActivityItem {
  const type = tx.txType ?? tx.type ?? '';
  const isSwap = type === 'swap';
  const isSend = type === 'send';
  const isReceive = type === 'receive';

  const titles: Record<string, string> = {
    send: `Sent ${tx.tokenSymbol}`,
    receive: `Received ${tx.tokenSymbol}`,
    swap: `Swapped ${tx.tokenSymbol}`,
    approve: `Approved ${tx.tokenSymbol}`,
    escrow: `Escrow ${tx.tokenSymbol}`,
  };

  const icons: Record<string, { name: string; color: string; bg: string }> = {
    send:    { name: 'arrow-up',           color: '#EF4444', bg: '#FEF2F2' },
    receive: { name: 'arrow-down',         color: '#22C55E', bg: '#F0FDF4' },
    swap:    { name: 'swap-horizontal',    color: '#D4AF37', bg: '#FFF8E1' },
    approve: { name: 'checkmark-circle',   color: '#888',    bg: '#F5F5F5' },
    escrow:  { name: 'lock-closed',        color: '#6366F1', bg: '#EEF2FF' },
  };

  const icon = icons[type] ?? { name: 'ellipse', color: '#888', bg: '#F5F5F5' };

  return {
    id: tx.id,
    type: isSwap ? 'swap' : 'wallet',
    subtype: type,
    title: titles[type] ?? `${type} ${tx.tokenSymbol}`,
    subtitle: isSend
      ? `To: ${truncate(tx.toAddress)}`
      : isReceive
        ? `From: ${truncate(tx.fromAddress)}`
        : tx.chain ?? '',
    amount: `${tx.amount} ${tx.tokenSymbol}`,
    positive: isReceive,
    // "pending" from the backend means the tx was broadcast but receipt not yet
    // confirmed. If a txHash exists, the tx reached the chain — show "submitted"
    // so it doesn't read as stuck. Confirmed/failed are used as-is.
    status: tx.status === 'pending' && tx.txHash ? 'submitted' : (tx.status ?? 'confirmed'),
    timestamp: tx.createdAt,
    txHash: tx.txHash,
    chainId: tx.chainId,
    iconName: icon.name,
    iconColor: icon.color,
    iconBg: icon.bg,
  };
}

function fromTPEarning(tp: any): ActivityItem {
  return {
    id: tp.id,
    type: 'tp',
    title: `Earned ${Number(tp.tpEarned ?? 0).toFixed(2)} TP`,
    subtitle: tp.sourceType ? `From ${tp.sourceType}` : 'TP reward',
    amount: `${Number(tp.tpEarned ?? 0).toFixed(2)} TP`,
    amountUSD: tp.feeAmountUsd,
    positive: true,
    status: 'completed',
    timestamp: tp.createdAt,
    iconName: 'star',
    iconColor: '#D4AF37',
    iconBg: '#FFF8E1',
  };
}

function fromCashback(cb: any): ActivityItem {
  return {
    id: cb.id,
    type: 'cashback',
    title: 'Cashback received',
    subtitle: cb.sourceType ? `From ${cb.sourceType}` : 'Cashback reward',
    amount: `$${Number(cb.amountUsd ?? 0).toFixed(2)}`,
    amountUSD: cb.amountUsd,
    positive: true,
    status: 'completed',
    timestamp: cb.createdAt,
    txHash: cb.txHash,
    iconName: 'gift',
    iconColor: '#8B5CF6',
    iconBg: '#F5F3FF',
  };
}

function fromOrder(order: any): ActivityItem {
  const statusColors: Record<string, string> = {
    completed: '#22C55E',
    pending_payment: '#F59E0B',
    shipped: '#3B82F6',
    cancelled: '#EF4444',
    disputed: '#EF4444',
  };
  const color = statusColors[order.status] ?? '#888';
  const token = (order.token ?? '').toUpperCase();
  const humanAmount = weiToHuman(order.totalAmount ?? '0', token);
  return {
    id: order.id,
    type: 'order',
    subtype: order.status,
    title: order.product?.name ? `Order: ${order.product.name}` : 'Product order',
    subtitle: `${order.status.replace(/_/g, ' ')} · ${token}`,
    amount: `${humanAmount.toLocaleString(undefined, { maximumFractionDigits: 4 })} ${token}`,
    amountUSD: undefined,
    positive: false,
    status: order.status,
    timestamp: order.createdAt,
    txHash: order.escrowTxHash,
    iconName: 'bag-handle',
    iconColor: color,
    iconBg: color + '15',
  };
}

function fromServicePayment(p: any): ActivityItem {
  return {
    id: p.id,
    type: 'service',
    title: p.service?.name ? `Service: ${p.service.name}` : 'Service payment',
    subtitle: p.counterparty?.name ?? 'Contact unlocked',
    amount: `$${Number(p.feeUSD ?? 0).toFixed(2)}`,
    amountUSD: p.feeUSD,
    positive: false,
    status: 'completed',
    timestamp: p.createdAt,
    txHash: p.payFeeTxHash,
    iconName: 'call',
    iconColor: '#0EA5E9',
    iconBg: '#F0F9FF',
  };
}

const BILL_CATEGORY_LABEL: Record<string, string> = {
  airtime: 'Airtime',
  data: 'Data',
  electricity: 'Electricity',
  cabletv: 'Cable TV',
  betting: 'Betting',
  epin: 'Exam PIN',
};

// Bill orders sit in a non-terminal `vending` state for minutes at a time —
// ClubKonnect accepts first and confirms later — so the feed must show that
// honestly rather than implying success or failure.
function fromBillOrder(o: any): ActivityItem {
  const label = BILL_CATEGORY_LABEL[o.category] ?? 'Bill';
  const target = o.customerId || o.phoneNumber || '';
  const failed = o.status === 'vend_failed' || o.status === 'failed' || o.status === 'expired';
  const refunded = o.status === 'refunded';
  // `quoted` counts as pending: the API returns every order including quotes the
  // user abandoned, and falling through to the 'completed' default would show a
  // bill they never paid for as a completed payment.
  const pending =
    o.status === 'vending' || o.status === 'paid' || o.status === 'paying' || o.status === 'quoted';
  const unpaid = o.status === 'quoted';

  let status = 'completed';
  if (pending) status = 'pending';
  else if (refunded) status = 'refunded';
  else if (failed) status = 'failed';

  return {
    id: o.id,
    type: 'bill',
    subtype: o.category,
    title: `${label}${o.providerName ? ` — ${o.providerName}` : ''}`,
    subtitle: unpaid
      ? 'Not paid'
      : pending
        ? 'Processing…'
        : refunded
        ? 'Refunded to your MCGP balance'
        : failed
          ? o.failureReason || 'Could not be completed'
          : target || o.planName || 'Delivered',
    amount: `₦${Number(o.ngnAmount ?? 0).toLocaleString()}`,
    positive: refunded,
    status,
    timestamp: o.createdAt,
    iconName: 'flash',
    iconColor: '#F59E0B',
    iconBg: '#FFFBEB',
  };
}

// ── Fetchers ───────────────────────────────────────────────────

async function fetchWalletItems(page: number, limit: number, txType?: string): Promise<ActivityItem[]> {
  const qs = new URLSearchParams({ page: String(page), limit: String(limit) });
  if (txType) qs.set('type', txType);
  const data = await get<any>(`/wallet/transactions?${qs}`);
  return (data?.data?.transactions ?? []).map(fromWalletTx);
}

function fromNonEVMActivity(chainKey: 'tron' | 'btc', chainLabel: string) {
  return (a: NonEVMActivity): ActivityItem => {
    const isIn = a.direction === 'in';
    const human = Number(formatUnits(a.amountRaw || '0', a.decimals || 6));
    const icon = isIn
      ? { name: 'arrow-down', color: '#22C55E', bg: '#F0FDF4' }
      : { name: 'arrow-up', color: '#EF4444', bg: '#FEF2F2' };
    // Pending BTC txs carry no block time yet — treat them as "just now" so
    // they sort to the top instead of 1970.
    const ts = a.blockTime > 0 ? a.blockTime * 1000 : Date.now();
    return {
      id: `${chainKey}-${a.hash}-${a.direction}`,
      type: 'wallet',
      subtype: isIn ? 'receive' : 'send',
      title: `${isIn ? 'Received' : 'Sent'} ${a.symbol}`,
      subtitle: `${isIn ? 'From' : 'To'}: ${truncate(a.counterparty)} · ${chainLabel}`,
      amount: `${human.toLocaleString(undefined, { maximumFractionDigits: 8 })} ${a.symbol}`,
      positive: isIn,
      status: a.status || 'confirmed',
      timestamp: new Date(ts).toISOString(),
      txHash: a.hash,
      chainKey,
      iconName: icon.name,
      iconColor: icon.color,
      iconBg: icon.bg,
    };
  };
}

// Tron/BTC history is derived + fetched client-side (their addresses aren't
// the EVM one). Dormant-safe: yields nothing when the chain is unconfigured
// or there's no wallet.
async function fetchTronItems(): Promise<ActivityItem[]> {
  try {
    const addr = await getTronAddress();
    if (!addr) return [];
    return (await getNonEVMHistory('tron', addr)).map(fromNonEVMActivity('tron', 'Tron'));
  } catch {
    return [];
  }
}

async function fetchBtcItems(): Promise<ActivityItem[]> {
  try {
    const addr = await getBtcAddress();
    if (!addr) return [];
    return (await getNonEVMHistory('btc', addr)).map(fromNonEVMActivity('btc', 'Bitcoin'));
  } catch {
    return [];
  }
}

async function fetchTPItems(page: number, limit: number): Promise<ActivityItem[]> {
  const data = await get<any>(`/users/tp-earnings?page=${page}&limit=${limit}`);
  const earnings = data?.data?.earnings ?? data?.data ?? [];
  return (Array.isArray(earnings) ? earnings : []).map(fromTPEarning);
}

async function fetchCashbackItems(page: number, limit: number): Promise<ActivityItem[]> {
  const data = await get<any>(`/users/cashback-earnings?page=${page}&limit=${limit}`);
  const earnings = data?.data?.earnings ?? data?.data ?? [];
  return (Array.isArray(earnings) ? earnings : []).map(fromCashback);
}

async function fetchOrderItems(page: number, limit: number): Promise<ActivityItem[]> {
  // role=buyer: without it the list includes a merchant's sales as well.
  const data = await get<any>(`/orders?page=${page}&limit=${limit}&role=buyer`);
  const orders = data?.data?.orders ?? [];
  return (Array.isArray(orders) ? orders : []).map(fromOrder);
}

async function fetchServiceItems(page: number, limit: number): Promise<ActivityItem[]> {
  const qs = new URLSearchParams({ page: String(page), limit: String(limit), role: 'caller' });
  const data = await get<any>(`/services/contact-payments?${qs}`);
  const items = data?.data?.items ?? [];
  return (Array.isArray(items) ? items : []).map(fromServicePayment);
}

// Bills are returned newest-first and capped server-side at 50, with no
// pagination — an unpaged source.
async function fetchBillItems(): Promise<ActivityItem[]> {
  const data = await get<any>('/bills/orders');
  const orders = data?.data ?? [];
  return (Array.isArray(orders) ? orders : []).map(fromBillOrder);
}

function fromITActivity(a: ITActivity): ActivityItem {
  return {
    id: a.key,
    type: 'it',
    subtype: a.direction,
    title: `${a.direction === 'buy' ? 'Buy' : 'Sell'} ${a.asset}`,
    subtitle: 'Instant Transfer',
    amount: a.amount,
    positive: false,
    status: a.status.label,
    statusVisual: a.status,
    timestamp: a.at,
    href: a.href,
    iconName: a.direction === 'buy' ? 'arrow-down' : 'arrow-up',
    iconColor: '#10B981',
    iconBg: '#10B98115',
  };
}

// Instant Transfer spans IT intents and Paycrest orders. Neither list endpoint
// pages (both cap at 100) — an unpaged source. Rows come from the same builder
// as the IT screens so labels never drift apart. Cancelled intents stay in:
// this is the record, as on the IT activity screen.
const IT_LIST_CAP = 100;

async function fetchITItems(symbolFor: SymbolFor): Promise<ActivityItem[]> {
  const [list, orders] = await Promise.all([
    listITIntents().catch(() => null),
    listPaycrestOrders(IT_LIST_CAP).catch(() => null),
  ]);
  const intents = list?.success ? list.data?.intents ?? [] : [];
  return buildITActivity(intents, orders ?? [], symbolFor).map(fromITActivity);
}

const PAYMENT_STATUS: Record<PayStatus, StatusVisual> = {
  succeeded: { label: 'Paid', tone: 'success' },
  processing: { label: 'Processing', tone: 'info' },
  // A payment she is the payer of is past pending; read one defensively as under way.
  pending: { label: 'Processing', tone: 'info' },
  failed: { label: 'Failed', tone: 'danger' },
  expired: { label: 'Expired', tone: 'neutral' },
};

// A TSA Pay payment, as what it is to her: money to a merchant. The payout
// order behind it is how it moves and is left out of the ramp's list.
function fromPayment(p: PayHistoryItem): ActivityItem {
  return {
    id: p.id,
    type: 'payment',
    subtype: p.status,
    title: `Payment to ${p.merchantName}`,
    subtitle: p.description || 'TSA Pay',
    amount: formatKobo(String(p.customerTotal)),
    positive: false,
    status: p.status,
    statusVisual: PAYMENT_STATUS[p.status] ?? { label: p.status, tone: 'neutral' },
    timestamp: p.paidAt,
    // The pay screen is the receipt: who, how much, and where it stands.
    href: `/pay?id=${p.id}`,
    iconName: 'storefront',
    iconColor: '#C8961E',
    iconBg: '#C8961E15',
  };
}

// Capped server-side with no pagination — an unpaged source, like bills.
async function fetchPaymentItems(): Promise<ActivityItem[]> {
  return (await listMyPayments()).map(fromPayment);
}

// ── Public API ─────────────────────────────────────────────────

export const ALL_TYPES: ActivityType[] = ['wallet', 'swap', 'tp', 'cashback', 'order', 'payment', 'service', 'bill', 'it'];

// A source either pages (fetch page N of `limit`) or returns its whole bounded
// list in one call. Both return newest first.
interface Source {
  fetch: (page: number, limit: number) => Promise<ActivityItem[]>;
  paged: boolean;
}

const WALLET_TX_TYPES = 'send,receive,approve,escrow';

function sourcesFor(types: ActivityType[], symbolFor: SymbolFor): Source[] {
  const active = types.length === 0 ? ALL_TYPES : types;
  const paged = (fetch: Source['fetch']): Source => ({ fetch, paged: true });
  const whole = (fetch: () => Promise<ActivityItem[]>): Source => ({ fetch, paged: false });

  const sources: Source[] = [];
  if (active.includes('wallet')) {
    sources.push(paged((p, l) => fetchWalletItems(p, l, WALLET_TX_TYPES)));
    sources.push(whole(fetchTronItems), whole(fetchBtcItems));
  }
  if (active.includes('swap')) sources.push(paged((p, l) => fetchWalletItems(p, l, 'swap')));
  if (active.includes('tp')) sources.push(paged(fetchTPItems));
  if (active.includes('cashback')) sources.push(paged(fetchCashbackItems));
  if (active.includes('order')) sources.push(paged(fetchOrderItems));
  if (active.includes('payment')) sources.push(whole(fetchPaymentItems));
  if (active.includes('service')) sources.push(paged(fetchServiceItems));
  if (active.includes('bill')) sources.push(whole(fetchBillItems));
  if (active.includes('it')) sources.push(whole(() => fetchITItems(symbolFor)));
  return sources;
}

const timeOf = (item: ActivityItem) => Date.parse(item.timestamp) || 0;

export interface ActivityPager {
  /** The next page, newest first. Overlapping calls are served in order. */
  next(): Promise<ActivityItem[]>;
  /** False once every source is exhausted and nothing is left buffered. */
  hasMore(): boolean;
}

/**
 * One newest-first list over every source for `types` (empty = all).
 *
 * Each source keeps a buffer and its own next page; a page is built by taking
 * the newest buffered item until it is full, refilling any source whose buffer
 * runs dry first. Slicing a merged page by a global offset instead drops or
 * repeats rows whenever sources interleave unevenly. A new row landing between
 * pages shifts a paged source's offsets and would repeat a row, so emitted ids
 * are remembered. A source that fails simply ends; the others carry on.
 */
export function createActivityPager(
  types: ActivityType[],
  pageSize: number,
  opts: { symbolFor?: SymbolFor } = {},
): ActivityPager {
  const symbolFor = opts.symbolFor ?? ((code: string) => fiatSymbol(code, []));
  const states = sourcesFor(types, symbolFor).map((source) => ({
    source,
    buffer: [] as ActivityItem[],
    nextPage: 1,
    done: false,
  }));
  const emitted = new Set<string>();

  const refillEmpty = () =>
    Promise.all(
      states
        .filter((s) => !s.done && s.buffer.length === 0)
        .map(async (s) => {
          const items = await s.source.fetch(s.nextPage, pageSize).catch(() => []);
          s.nextPage += 1;
          s.done = !s.source.paged || items.length < pageSize;
          s.buffer.push(...[...items].sort((a, b) => timeOf(b) - timeOf(a)));
        }),
    );

  const buildPage = async (): Promise<ActivityItem[]> => {
    const page: ActivityItem[] = [];
    while (page.length < pageSize) {
      await refillEmpty();
      let newest: (typeof states)[number] | undefined;
      for (const s of states) {
        if (s.buffer.length && (!newest || timeOf(s.buffer[0]) > timeOf(newest.buffer[0]))) newest = s;
      }
      if (!newest) break;
      const item = newest.buffer.shift()!;
      const key = `${item.type}-${item.id}`;
      if (emitted.has(key)) continue;
      emitted.add(key);
      page.push(item);
    }
    return page;
  };

  // Buffers are shared state: a second call must wait for the first.
  let queue: Promise<unknown> = Promise.resolve();
  return {
    next() {
      const page = queue.then(buildPage);
      queue = page;
      return page;
    },
    hasMore: () => states.some((s) => !s.done || s.buffer.length > 0),
  };
}

export const TYPE_META: Record<ActivityType, { label: string; iconName: string; color: string }> = {
  wallet:   { label: 'Wallet',   iconName: 'wallet-outline',      color: '#1A1A1A' },
  swap:     { label: 'Swaps',    iconName: 'swap-horizontal',     color: '#D4AF37' },
  tp:       { label: 'TP',       iconName: 'star-outline',        color: '#D4AF37' },
  cashback: { label: 'Cashback', iconName: 'gift-outline',        color: '#8B5CF6' },
  order:    { label: 'Orders',   iconName: 'bag-handle-outline',  color: '#3B82F6' },
  payment:  { label: 'Payments', iconName: 'storefront-outline',  color: '#C8961E' },
  service:  { label: 'Services', iconName: 'call-outline',        color: '#0EA5E9' },
  bill:     { label: 'Bills',    iconName: 'flash-outline',       color: '#F59E0B' },
  it:       { label: 'Instant Transfer', iconName: 'business-outline', color: '#10B981' },
};
