// components/dashboard/RWMWidget.tsx
//
// "Real World Marketplace" (RWM) homepage widget — the tabbed card that replaced
// Recent Activity on the dashboard. Five tabs (Orders | Products | Contact |
// Ordering | Booking) each lazily fetch ~5 recent rows from their own source:
//
//   Orders    — GET /orders?role=buyer (services/orderApi.getOrders)
//   Products  — GET /products?sort=recent (api.getMarketplaceProducts)
//   Contact   — GET /services/contact-payments?role=caller (serviceContactApi)
//   Ordering  — GET /orders?role=seller (merchant's incoming orders)
//   Booking   — GET /services/requests?role=buyer (bookingRequestApi)
//
// Ordering/Contact degrade to the "Nothing here yet" empty state on any
// failure (their detail screens land on a parallel branch) — never a crash,
// never an endless spinner. The other three sources show a Retry empty state
// on failure instead. Presentation follows the dashboard card pattern
// (Surface + header + Divider + rows) from RecentActivity.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Image, ScrollView, View } from 'react-native';
import { router } from 'expo-router';
import { api, Product } from '@/components/services/api';
import { getOrders, formatTokenAmount } from '@/services/orderApi';
import { listBookingRequests, BookingRequestRow } from '@/services/bookingRequestApi';
import { listContactPayments } from '@/services/serviceContactApi';
import { formatDate, formatStatus } from '@/constants/orderStatus';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useTheme } from '@/theme';
import {
  Badge,
  Chip,
  Divider,
  EmptyState,
  Icon,
  PressableScale,
  SkeletonGroup,
  Surface,
  Text,
} from '@/components/ui';
import type { IconName } from '@/components/ui';

// ── Tabs ──

type TabKey = 'orders' | 'products' | 'contact' | 'ordering' | 'booking';

const TABS: { key: TabKey; label: string }[] = [
  { key: 'orders', label: 'Orders' },
  { key: 'products', label: 'Products' },
  { key: 'contact', label: 'Contact' },
  { key: 'ordering', label: 'Ordering' },
  { key: 'booking', label: 'Booking' },
];

// "View all" destinations. Ordering/Contact screens land on a parallel branch
// (feat/request-screens) — navigation is wrapped in try/catch so a missing
// route never crashes the home.
const VIEW_ALL_ROUTES: Record<TabKey, string> = {
  orders: '/orderlist',
  products: '/products',
  contact: '/(dashboard)/contact-requests',
  ordering: '/(dashboard)/ordering-requests',
  booking: '/booking-requests',
};

const EMPTY_ICONS: Record<TabKey, IconName> = {
  orders: 'cart',
  products: 'storefront',
  contact: 'mail',
  ordering: 'list',
  booking: 'clock',
};

const EMPTY_HINTS: Record<TabKey, string> = {
  orders: 'Your marketplace orders will show up here.',
  products: 'New marketplace listings will show up here.',
  contact: 'Contacts you unlock will show up here.',
  ordering: 'Incoming orders from buyers will show up here.',
  booking: 'Your booking requests will show up here.',
};

// ── Row model (one shape for all five tabs) ──

interface RwmRow {
  key: string;
  title: string;
  subtitle?: string;
  /** Preformatted amount (e.g. "600.00 USDT"). Takes precedence over amountUsd. */
  amount?: string;
  /** USD amount formatted at render via CurrencyContext (stays currency-aware). */
  amountUsd?: number;
  /** Masked with •••• when the dashboard hide-values toggle is on. */
  sensitive?: boolean;
  status?: string;
  promo?: boolean;
  imageUrl?: string;
  icon?: IconName;
}

type TabStatus = 'idle' | 'loading' | 'ready' | 'error';

interface TabState {
  status: TabStatus;
  rows: RwmRow[];
}

const freshTabs = (): Record<TabKey, TabState> => ({
  orders: { status: 'idle', rows: [] },
  products: { status: 'idle', rows: [] },
  contact: { status: 'idle', rows: [] },
  ordering: { status: 'idle', rows: [] },
  booking: { status: 'idle', rows: [] },
});

// ── Status presentation (theme tones, mirrors ui/statusMap's approach) ──

type Tone = 'success' | 'warning' | 'danger' | 'info' | 'escrow' | 'neutral';

const STATUS_TONES: Record<string, Tone> = {
  active: 'success',
  completed: 'success',
  delivered: 'success',
  approved: 'success',
  converted: 'success',
  released: 'success',
  paid: 'info',
  shipped: 'info',
  quoted: 'info',
  escrowed: 'escrow',
  pending: 'warning',
  pending_payment: 'warning',
  pending_review: 'warning',
  requested: 'warning',
  cancel_requested: 'warning',
  refund_requested: 'warning',
  disputed: 'danger',
  rejected: 'danger',
  failed: 'danger',
  cancelled: 'neutral',
  inactive: 'neutral',
  expired: 'neutral',
  refunded: 'neutral',
  sold_out: 'neutral',
  // Contact payments have no lifecycle — every row is an unlocked contact.
  revealed: 'success',
};

// Compact labels where formatStatus() would be too wide for the pill.
const STATUS_LABELS: Record<string, string> = {
  pending_payment: 'Pending',
  refund_requested: 'Refund',
  cancel_requested: 'Cancelling',
  converted: 'Paid',
  sold_out: 'Sold out',
};

function statusLabel(status: string): string {
  return STATUS_LABELS[status] || formatStatus(status);
}

// ── Data loading ──

const LIMIT = 5;
const TIMEOUT_MS = 12000;

/** Guard a service call so a hung request can never leave the tab spinning. */
function withTimeout<T>(promise: Promise<T>, ms: number = TIMEOUT_MS): Promise<T> {
  return Promise.race([
    promise,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error('RWM request timed out')), ms)),
  ]);
}

/** Map an escrow-order-shaped record to a row (shared by Orders + Ordering). */
function orderToRow(o: any, counterpart: 'seller' | 'buyer'): RwmRow {
  const title =
    o?.product?.name || (o?.id ? `Order #${String(o.id).slice(0, 8)}` : 'Order');
  const who = o?.[counterpart]?.name || o?.[counterpart]?.username;
  const amount =
    typeof o?.totalAmount === 'string' && typeof o?.token === 'string'
      ? formatTokenAmount(o.totalAmount, o.token)
      : undefined;
  return {
    key: String(o?.id ?? title),
    title,
    subtitle: [who, formatDate(o?.createdAt)].filter(Boolean).join(' · '),
    amount,
    sensitive: true,
    status: typeof o?.status === 'string' ? o.status : undefined,
    imageUrl: typeof o?.product?.imageUrl === 'string' ? o.product.imageUrl : undefined,
    icon: 'cart',
  };
}

interface LoadResult {
  ok: boolean;
  rows: RwmRow[];
}

const loadOrders = async (): Promise<LoadResult> => {
  try {
    const res = await withTimeout(getOrders({ page: 1, limit: LIMIT, role: 'buyer' }));
    if (!res.success || !res.data) return { ok: false, rows: [] };
    return {
      ok: true,
      rows: (res.data.orders || []).slice(0, LIMIT).map((o) => orderToRow(o, 'seller')),
    };
  } catch {
    return { ok: false, rows: [] };
  }
};

const loadProducts = async (): Promise<LoadResult> => {
  try {
    const res = await withTimeout(api.getMarketplaceProducts({ sort: 'recent', limit: LIMIT }));
    if (!res.success || !res.data) return { ok: false, rows: [] };
    const rows = (res.data.products || []).slice(0, LIMIT).map((p: Product): RwmRow => ({
      key: p.id || p._id || p.name,
      title: p.name || 'Product',
      subtitle:
        (typeof p.category === 'object' && p.category ? p.category.title : p.categoryName) ||
        p.location ||
        undefined,
      amountUsd: Number.isFinite(p.price) ? p.price : undefined,
      status: p.status,
      promo: !!p.isFeatured,
      imageUrl: p.images?.[0]?.url || undefined,
      icon: 'storefront',
    }));
    return { ok: true, rows };
  } catch {
    return { ok: false, rows: [] };
  }
};

const loadBooking = async (): Promise<LoadResult> => {
  try {
    const res = await withTimeout(listBookingRequests('buyer'));
    if (!res.success || !res.data) return { ok: false, rows: [] };
    const rows = (res.data.requests || []).slice(0, LIMIT).map((r: BookingRequestRow): RwmRow => ({
      key: r.request?.id || `${r.serviceName}-${r.request?.createdAt}`,
      title: r.serviceName || 'Service booking',
      subtitle: formatDate(r.request?.createdAt),
      status: r.request?.status,
      icon: 'clock',
    }));
    return { ok: true, rows };
  } catch {
    return { ok: false, rows: [] };
  }
};

// Ordering = the merchant side of the same escrow orders list (role=seller).
// Non-merchants simply get an empty list; any failure degrades to the empty
// state ("Nothing here yet"), never the error/Retry state.
const loadOrdering = async (): Promise<LoadResult> => {
  try {
    const res = await withTimeout(getOrders({ page: 1, limit: LIMIT, role: 'seller' }));
    if (!res.success || !res.data) return { ok: true, rows: [] };
    return {
      ok: true,
      rows: (res.data.orders || []).slice(0, LIMIT).map((o) => orderToRow(o, 'buyer')),
    };
  } catch {
    return { ok: true, rows: [] };
  }
};

// Contact = the caller's paid contact unlocks. There is no lifecycle for these
// (each row is a completed ContactFeePaid event), so status is a constant
// "Revealed". Failures degrade to the empty state, never the Retry state.
const loadContact = async (): Promise<LoadResult> => {
  try {
    const res = await withTimeout(listContactPayments({ role: 'caller', page: 1, limit: LIMIT }));
    if (!res.success || !res.data) return { ok: true, rows: [] };
    const rows = (res.data.items || []).slice(0, LIMIT).map((c): RwmRow => ({
      key: c.id,
      title: c.service?.name || 'Service contact',
      subtitle: [c.counterparty?.name || c.counterparty?.username, formatDate(c.createdAt)]
        .filter(Boolean)
        .join(' · '),
      amount: Number.isFinite(c.feeUSD) ? `Paid $${c.feeUSD.toFixed(2)}` : undefined,
      sensitive: true,
      status: 'revealed',
      icon: 'mail',
    }));
    return { ok: true, rows };
  } catch {
    return { ok: true, rows: [] };
  }
};

const LOADERS: Record<TabKey, () => Promise<LoadResult>> = {
  orders: loadOrders,
  products: loadProducts,
  contact: loadContact,
  ordering: loadOrdering,
  booking: loadBooking,
};

// ── Component ──

interface RWMWidgetProps {
  isValuesHidden?: boolean;
  /** Bump to re-fetch (wired to the dashboard's pull-to-refresh). */
  refreshSignal?: number;
}

export const RWMWidget: React.FC<RWMWidgetProps> = ({ isValuesHidden, refreshSignal }) => {
  const { colors, radius, spacing } = useTheme();
  const { formatPrice } = useCurrency();
  const [active, setActive] = useState<TabKey>('orders');
  const [tabs, setTabs] = useState<Record<TabKey, TabState>>(freshTabs);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const load = useCallback(async (tab: TabKey) => {
    setTabs((prev) => ({ ...prev, [tab]: { ...prev[tab], status: 'loading' } }));
    const result = await LOADERS[tab]();
    if (!mounted.current) return;
    setTabs((prev) => ({
      ...prev,
      [tab]: { status: result.ok ? 'ready' : 'error', rows: result.rows },
    }));
  }, []);

  // Lazy-load a tab the first time it becomes active.
  useEffect(() => {
    if (tabs[active].status === 'idle') load(active);
  }, [active, tabs, load]);

  // Pull-to-refresh: invalidate everything; the effect above refetches the active tab.
  useEffect(() => {
    if (!refreshSignal) return;
    setTabs(freshTabs());
  }, [refreshSignal]);

  const onViewAll = () => {
    try {
      router.push(VIEW_ALL_ROUTES[active] as any);
    } catch (err) {
      // Ordering/Contact screens ship on a parallel branch — never crash the home.
      console.warn('RWM view-all navigation unavailable:', err);
    }
  };

  const toneColors = (tone: Tone): { color: string; bg: string } => {
    switch (tone) {
      case 'success':
        return { color: colors.success, bg: colors.successBg };
      case 'warning':
        return { color: colors.warning, bg: colors.warningBg };
      case 'danger':
        return { color: colors.danger, bg: colors.dangerBg };
      case 'info':
        return { color: colors.info, bg: colors.infoBg };
      case 'escrow':
        return { color: colors.escrow, bg: colors.escrowBg };
      default:
        return { color: colors.textSecondary, bg: colors.surfaceAlt };
    }
  };

  const state = tabs[active];

  return (
    <Surface elevation="sm" style={{ marginTop: spacing.lg, overflow: 'hidden' }}>
      {/* Header */}
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.sm,
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: spacing.sm,
        }}
      >
        <Text variant="bodySm" weight="700" numberOfLines={1} style={{ flexShrink: 1 }}>
          Real World Marketplace
        </Text>
        <Badge label="RWM" size="sm" color={colors.accent} background={colors.accentMuted} />
        <View style={{ flex: 1 }} />
        <PressableScale onPress={onViewAll} accessibilityRole="link" hitSlop={8}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 2 }}>
            <Text variant="caption" weight="700" color="accent">
              View all
            </Text>
            <Icon name="chevronRight" size={12} color={colors.accent} />
          </View>
        </PressableScale>
      </View>

      {/* Tab chips */}
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        contentContainerStyle={{
          paddingHorizontal: spacing.lg,
          paddingBottom: spacing.md,
          gap: spacing.sm,
        }}
      >
        {TABS.map((tab) => (
          <Chip
            key={tab.key}
            label={tab.label}
            selected={tab.key === active}
            onPress={() => setActive(tab.key)}
          />
        ))}
      </ScrollView>
      <Divider />

      {/* Body */}
      {state.status === 'loading' || state.status === 'idle' ? (
        <View style={{ paddingVertical: spacing.sm }}>
          <SkeletonGroup shape="tradeRow" count={3} />
        </View>
      ) : state.status === 'error' ? (
        <View style={{ paddingVertical: spacing.lg }}>
          <EmptyState
            icon="warning"
            title="Couldn't load"
            subtitle="Check your connection and try again."
            actionLabel="Retry"
            onAction={() => load(active)}
          />
        </View>
      ) : state.rows.length === 0 ? (
        <View style={{ paddingVertical: spacing.lg }}>
          <EmptyState
            icon={EMPTY_ICONS[active]}
            title="Nothing here yet"
            subtitle={EMPTY_HINTS[active]}
          />
        </View>
      ) : (
        state.rows.map((row, index) => {
          const isLast = index === state.rows.length - 1;
          const rawAmount =
            row.amount ?? (row.amountUsd !== undefined ? formatPrice(row.amountUsd) : undefined);
          const amountText = isValuesHidden && row.sensitive && rawAmount ? '••••' : rawAmount;
          const tone = row.status ? STATUS_TONES[row.status.toLowerCase()] ?? 'neutral' : 'neutral';
          const tc = toneColors(tone);
          return (
            <View key={row.key}>
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.md,
                  paddingHorizontal: spacing.lg,
                  paddingVertical: spacing.md,
                }}
              >
                {row.imageUrl ? (
                  <Image
                    source={{ uri: row.imageUrl }}
                    style={{
                      width: 36,
                      height: 36,
                      borderRadius: radius.sm,
                      backgroundColor: colors.surfaceAlt,
                    }}
                    resizeMode="cover"
                  />
                ) : (
                  <View
                    style={{
                      width: 36,
                      height: 36,
                      borderRadius: 18,
                      backgroundColor: colors.accentMuted,
                      justifyContent: 'center',
                      alignItems: 'center',
                    }}
                  >
                    <Icon name={row.icon ?? 'storefront'} size={16} color={colors.accent} />
                  </View>
                )}
                <View style={{ flex: 1 }}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                    <Text variant="bodySm" weight="600" numberOfLines={1} style={{ flexShrink: 1 }}>
                      {row.title}
                    </Text>
                    {row.promo && (
                      <Badge
                        label="Promo"
                        size="sm"
                        icon="pricetag"
                        color={colors.accent}
                        background={colors.accentMuted}
                      />
                    )}
                  </View>
                  {!!row.subtitle && (
                    <Text variant="caption" color="textMuted" numberOfLines={1}>
                      {row.subtitle}
                    </Text>
                  )}
                </View>
                <View style={{ alignItems: 'flex-end', gap: 4 }}>
                  {!!amountText && (
                    <Text variant="bodySm" weight="700" numeric>
                      {amountText}
                    </Text>
                  )}
                  {!!row.status && (
                    <Badge label={statusLabel(row.status)} size="sm" color={tc.color} background={tc.bg} />
                  )}
                </View>
              </View>
              {!isLast && <Divider inset={spacing.lg} />}
            </View>
          );
        })
      )}
    </Surface>
  );
};

export default RWMWidget;
