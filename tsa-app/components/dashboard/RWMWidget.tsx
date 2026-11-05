// components/dashboard/RWMWidget.tsx
//
// "Real World Marketplace" (RWM) homepage widget — the tabbed card that replaced
// Recent Activity on the dashboard. Five tabs, four of them a browsable
// catalogue and one a history:
//
//   Orders    — GET /orders?role=buyer (history; row opens the order)
//   Products  — GET /products?type=Product              → Buy
//   Contact   — GET /products?type=Service&serviceType=contact → Contact
//   Ordering  — GET /products?type=Service&serviceType=order   → Order
//   Booking   — GET /products?type=Service&serviceType=booking → Book
//
// The four catalogue tabs are one query with different filters, because
// products and services live in one collection and are told apart by their
// CATEGORY — a merchant filing a flight as type "Product" must still land under
// Booking. The server does that classification (models.ServiceCategoryIDs); the
// tab wiring and row routing live in utils/rwmCatalogue.
//
// Every row is tappable and carries a CTA to the same destination: the listing's
// own page, where the user adds to cart, unlocks a contact, orders, or books.
// A failed tab shows a Retry empty state — never a crash, never an endless
// spinner. Presentation follows the dashboard card pattern (Surface + header +
// Divider + rows) from RecentActivity.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Image, ScrollView, View } from 'react-native';
import { router } from 'expo-router';
import { api, Product } from '@/components/services/api';
import { getOrders, formatTokenAmount } from '@/services/orderApi';
import { formatDate, formatStatus } from '@/constants/orderStatus';
import {
  CTA_LABEL,
  SERVICE_TAB_TYPE,
  listingSection,
  listingTarget,
  targetHref,
  viewAllHref,
  type RwmHref,
  type RwmTarget,
} from '@/utils/rwmCatalogue';
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
  { key: 'products', label: 'Products' },
  { key: 'orders', label: 'Orders' },
  { key: 'contact', label: 'Contact' },
  { key: 'ordering', label: 'Ordering' },
  { key: 'booking', label: 'Booking' },
];

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
  contact: 'Services you can call up will show up here.',
  ordering: 'Services you can order from will show up here.',
  booking: 'Services you can book a slot with will show up here.',
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
  /** Where tapping the row (or its CTA) goes. Rows without one are inert. */
  target?: RwmTarget;
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
/** Keeps Buy / Contact / Order / Book buttons the same width down a column. */
const CTA_MIN_WIDTH = 62;

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
    target: o?.id ? { kind: 'order', id: String(o.id), title } : undefined,
  };
}

interface LoadResult {
  ok: boolean;
  rows: RwmRow[];
  /** Why the tab came back empty — surfaced in the dev log, not the UI. */
  reason?: string;
}

const loadOrders = async (): Promise<LoadResult> => {
  try {
    const res = await withTimeout(getOrders({ page: 1, limit: LIMIT, role: 'buyer' }));
    if (!res.success || !res.data) return { ok: false, rows: [], reason: res.message || 'no data' };
    return {
      ok: true,
      rows: (res.data.orders || []).slice(0, LIMIT).map((o) => orderToRow(o, 'seller')),
    };
  } catch (err: any) {
    return { ok: false, rows: [], reason: err?.message || 'threw' };
  }
};

/**
 * The four catalogue tabs: one listings query, filtered by what the tab
 * browses. Products asks for goods; the service tabs ask for their own
 * serviceType bucket. Classification is the server's job — it reads the
 * listing's category rather than trusting the listing's own `type`.
 */
const loadCatalogue =
  (tab: Exclude<TabKey, 'orders'>) => async (): Promise<LoadResult> => {
    try {
      const res = await withTimeout(
        api.getMarketplaceProducts({
          sort: 'recent',
          limit: LIMIT,
          ...(tab === 'products'
            ? { type: 'Product' as const }
            : { type: 'Service' as const, serviceType: SERVICE_TAB_TYPE[tab] }),
        })
      );
      if (!res.success || !res.data)
        return { ok: false, rows: [], reason: res.message || 'no data' };
      const rows = (res.data.products || []).slice(0, LIMIT).map((p: Product): RwmRow => ({
        key: p.id || p._id || p.name,
        title: p.name || 'Listing',
        // The category is the row's section tag — "Flights", "Hair Extensions".
        subtitle: listingSection(p),
        amountUsd: Number.isFinite(p.price) ? p.price : undefined,
        status: p.status,
        promo: !!p.isFeatured,
        imageUrl: p.images?.[0]?.url || undefined,
        icon: EMPTY_ICONS[tab],
        target: listingTarget(p, tab),
      }));
      return { ok: true, rows };
    } catch (err: any) {
      return { ok: false, rows: [], reason: err?.message || 'threw' };
    }
  };

const LOADERS: Record<TabKey, () => Promise<LoadResult>> = {
  orders: loadOrders,
  products: loadCatalogue('products'),
  contact: loadCatalogue('contact'),
  ordering: loadCatalogue('ordering'),
  booking: loadCatalogue('booking'),
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
  const [active, setActive] = useState<TabKey>('products');
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
    if (__DEV__) {
      console.log(
        `[RWM] ${tab}: ok=${result.ok} rows=${result.rows.length}` +
          (result.reason ? ` reason="${result.reason}"` : '')
      );
    }
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

  // Navigation is guarded: a home screen must never crash on a route that a
  // parallel branch hasn't landed yet.
  const go = useCallback((href: RwmHref) => {
    try {
      router.push(href as any);
    } catch (err) {
      console.warn('RWM navigation unavailable:', err);
    }
  }, []);

  const onViewAll = () => go(viewAllHref(active));

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
          const open = row.target ? () => go(targetHref(row.target!)) : undefined;
          const ctaLabel = CTA_LABEL[active];
          // Catalogue queries only return active listings, so an "Active" badge
          // on every row is noise competing with the CTA. Anything else — sold
          // out, an order's lifecycle — still earns its place.
          const showStatus = !!row.status && !(ctaLabel && row.status === 'active');
          return (
            <View key={row.key}>
              {/* PressableScale with no onPress is inert, so history rows with no
                  destination stay non-interactive without a second component. */}
              <PressableScale
                onPress={open}
                accessibilityRole={open ? 'button' : undefined}
                accessibilityLabel={open ? `Open ${row.title}` : undefined}
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
                <View style={{ alignItems: 'flex-end', gap: 4, flexShrink: 1 }}>
                  {!!amountText && (
                    <Text variant="bodySm" weight="700" numeric numberOfLines={1}>
                      {amountText}
                    </Text>
                  )}
                  {showStatus && (
                    <Badge label={statusLabel(row.status!)} size="sm" color={tc.color} background={tc.bg} />
                  )}
                </View>

                {/* CTA — Buy / Contact / Order / Book. Same destination as the
                    row tap; it's there to say what happens next. */}
                {!!ctaLabel && !!open && (
                  <PressableScale
                    onPress={open}
                    accessibilityRole="button"
                    accessibilityLabel={`${ctaLabel} ${row.title}`}
                    style={{
                      paddingVertical: spacing.xs,
                      paddingHorizontal: spacing.md,
                      borderRadius: radius.md,
                      backgroundColor: colors.accent,
                      minWidth: CTA_MIN_WIDTH,
                      alignItems: 'center',
                    }}
                  >
                    <Text variant="caption" weight="700" style={{ color: colors.onAccent }}>
                      {ctaLabel}
                    </Text>
                  </PressableScale>
                )}
              </PressableScale>
              {!isLast && <Divider inset={spacing.lg} />}
            </View>
          );
        })
      )}
    </Surface>
  );
};

export default RWMWidget;
