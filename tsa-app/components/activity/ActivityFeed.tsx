// components/activity/ActivityFeed.tsx
//
// Shared activity feed — wallet transactions + swaps + TP / cashback / orders /
// services / bills / Instant Transfer. Used full-screen (wallet history / global
// activity) and embedded (wallet home / token detail), on the TSA Signature
// design system (@/theme tokens + @/components/ui primitives).
//
// Data and pagination come from `createActivityPager`, which merges the sources
// newest first. A row opens its in-app screen when it has one (`href`), else its
// explorer link. The service owns each item's data; its raw Ionicons icon name
// is mapped to the registry and its tone derived from theme tokens, so light and
// dark both come free.
import React, { useState, useEffect, useCallback, useRef } from 'react';
import { View, FlatList, RefreshControl, Linking } from 'react-native';
import { router } from 'expo-router';
import {
  createActivityPager, ALL_TYPES, TYPE_META,
  type ActivityItem, type ActivityPager, type ActivityType,
} from '../../services/activityService';
import { txExplorerUrl, isNonEVMChain, type AnyChainKey } from '../../constants/chains';
import { useTheme } from '@/theme';
import {
  Badge,
  Chip,
  Icon,
  IconName,
  PressableScale,
  Spinner,
  Text,
  useToneColors,
} from '@/components/ui';
import type { StatusVisual } from '@/components/ui/statusMap';
import { useFiatSymbol } from '@/hooks/useFiatSymbol';

const PAGE_SIZE = 20;
const EMBEDDED_SIZE = 5;

// Map the service's raw Ionicons names to the semantic icon registry. Anything
// unmapped falls back to a neutral list glyph (the Icon primitive also no-ops on
// an unknown name as defense-in-depth).
const ITEM_ICONS: Record<string, IconName> = {
  'arrow-up': 'arrowUp',
  'arrow-down': 'arrowDown',
  'swap-horizontal': 'swap',
  'checkmark-circle': 'checkCircle',
  'lock-closed': 'lock',
  'star': 'star',
  'gift': 'gift',
  'bag-handle': 'cart',
  'call': 'mobile',
  'storefront': 'storefront',
  'ellipse': 'dots',
};

// Filter-chip icons (TYPE_META uses outline Ionicons names).
const TYPE_ICONS: Record<ActivityType, IconName> = {
  wallet: 'wallet',
  swap: 'swap',
  tp: 'star',
  cashback: 'gift',
  order: 'cart',
  payment: 'storefront',
  service: 'mobile',
  bill: 'bolt',
  it: 'bank',
};

function formatDate(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const mins = Math.floor((now.getTime() - d.getTime()) / 60000);
  if (mins < 1) return 'Just now';
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  if (days < 7) return `${days}d ago`;
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

function explorerUrl(item: ActivityItem): string | null {
  if (!item.txHash) return null;
  // Non-EVM (e.g. Tron) items carry a chainKey; EVM items carry a numeric
  // chainId. txExplorerUrl resolves either to the right explorer + path.
  const chainKey =
    item.chainKey && isNonEVMChain(item.chainKey) ? (item.chainKey as AnyChainKey) : undefined;
  return txExplorerUrl(item.txHash, { chainId: item.chainId, chainKey }) ?? null;
}

/** Opens a row's in-app screen if it has one, else its explorer link. */
export function openActivityItem(item: ActivityItem): boolean {
  if (item.href) {
    router.push(item.href as never);
    return true;
  }
  const url = explorerUrl(item);
  if (!url) return false;
  Linking.openURL(url).catch(() => {});
  return true;
}

// Map an activity status to a theme tone (color + tinted background).
function useStatusTone(status: string): { color: string; bg: string } {
  const { colors } = useTheme();
  const tones: Record<string, { color: string; bg: string }> = {
    confirmed:       { color: colors.success, bg: colors.successBg },
    completed:       { color: colors.success, bg: colors.successBg },
    submitted:       { color: colors.info, bg: colors.infoBg },
    shipped:         { color: colors.info, bg: colors.infoBg },
    pending:         { color: colors.warning, bg: colors.warningBg },
    pending_payment: { color: colors.warning, bg: colors.warningBg },
    failed:          { color: colors.danger, bg: colors.dangerBg },
    cancelled:       { color: colors.danger, bg: colors.dangerBg },
    disputed:        { color: colors.danger, bg: colors.dangerBg },
  };
  return tones[status] ?? { color: colors.warning, bg: colors.warningBg };
}

function StatusBadge({ status }: { status: string }) {
  const { color, bg } = useStatusTone(status);
  const label = status.replace(/_/g, ' ');
  return <Badge label={label} color={color} background={bg} size="sm" />;
}

function ToneBadge({ visual }: { visual: StatusVisual }) {
  const { color, bg } = useToneColors(visual.tone);
  return <Badge label={visual.label} color={color} background={bg} size="sm" />;
}

export function ActivityRow({ item }: { item: ActivityItem }) {
  const { colors, spacing, radius } = useTheme();
  const openable = !!item.href || !!explorerUrl(item);
  const iconName = ITEM_ICONS[item.iconName] ?? 'list';
  // Tone derived from the item's direction (credit = success), not the legacy
  // per-item hex. Falls back to the accent for neutral/outgoing entries.
  const tone = item.positive ? colors.success : colors.accent;
  const toneBg = item.positive ? colors.successBg : colors.accentMuted;
  const amountColor = item.positive ? colors.success : colors.textPrimary;

  return (
    <PressableScale
      onPress={() => openActivityItem(item)}
      disabled={!openable}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.md,
        borderBottomWidth: 1,
        borderBottomColor: colors.hairline,
      }}
    >
      <View
        style={{
          width: 40,
          height: 40,
          borderRadius: radius.pill,
          alignItems: 'center',
          justifyContent: 'center',
          backgroundColor: toneBg,
        }}
      >
        <Icon name={iconName} size={18} color={tone} />
      </View>
      <View style={{ flex: 1 }}>
        <Text variant="bodySm" weight="600">
          {item.title}
        </Text>
        <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 2 }}>
          {item.subtitle}
        </Text>
      </View>
      <View style={{ alignItems: 'flex-end', gap: spacing.xs }}>
        {item.amount && (
          <Text variant="bodySm" weight="700" numeric style={{ color: amountColor }}>
            {item.positive ? '+' : ''}{item.amount}
          </Text>
        )}
        <Text variant="caption" color="textMuted">
          {formatDate(item.timestamp)}
        </Text>
        {item.statusVisual
          ? <ToneBadge visual={item.statusVisual} />
          : <StatusBadge status={item.status} />}
      </View>
    </PressableScale>
  );
}

interface Props {
  /** Which activity types to show. Empty = all. */
  types?: ActivityType[];
  /** Render as compact embedded list (no filter chips, limited rows, see-all footer) */
  embedded?: boolean;
  onSeeAll?: () => void;
  /** Show filter chips even in embedded mode */
  showFilters?: boolean;
}

export default function ActivityFeed({ types = [], embedded = false, onSeeAll, showFilters = false }: Props) {
  const { colors, spacing } = useTheme();
  const [activeTypes, setActiveTypes] = useState<ActivityType[]>(types.length > 0 ? types : []);
  const [items, setItems] = useState<ActivityItem[]>([]);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);

  const limit = embedded ? EMBEDDED_SIZE : PAGE_SIZE;

  // Read through a ref so the currency list loading does not restart the feed.
  const symbolFor = useFiatSymbol();
  const symbolForRef = useRef(symbolFor);
  symbolForRef.current = symbolFor;

  const pagerRef = useRef<ActivityPager | null>(null);

  const load = useCallback(async (mode: 'initial' | 'refresh' | 'more') => {
    let pager = pagerRef.current;
    if (mode === 'more' && pager) {
      setLoadingMore(true);
    } else {
      pager = createActivityPager(activeTypes, limit, {
        symbolFor: (code) => symbolForRef.current(code),
      });
      pagerRef.current = pager;
      mode === 'refresh' ? setRefreshing(true) : setLoading(true);
    }

    const fetched = await pager.next();
    // A filter change or refresh started a new pager; this page belongs to the old one.
    if (pagerRef.current !== pager) return;

    setItems(prev => mode === 'more' ? [...prev, ...fetched] : fetched);
    setHasMore(!embedded && pager.hasMore());

    setLoading(false);
    setRefreshing(false);
    setLoadingMore(false);
  }, [activeTypes, limit, embedded]);

  useEffect(() => { load('initial'); }, [load]);

  const onRefresh = () => load('refresh');
  const onLoadMore = () => { if (hasMore && !loadingMore && !loading) load('more'); };

  const toggleType = (t: ActivityType) => {
    setActiveTypes(prev => {
      if (prev.length === 0) return [t];
      if (prev.includes(t)) return prev.length === 1 ? [] : prev.filter(x => x !== t);
      return [...prev, t];
    });
  };

  const showFilterBar = !embedded || showFilters;
  const availableTypes = types.length > 0 ? types : ALL_TYPES;

  if (loading) return (
    <View style={{ paddingVertical: spacing['4xl'], alignItems: 'center' }}>
      <Spinner />
    </View>
  );

  return (
    <View style={embedded ? undefined : { flex: 1 }}>
      {/* Filter chips */}
      {showFilterBar && (
        <View
          style={{
            flexDirection: 'row',
            flexWrap: 'wrap',
            gap: spacing.sm,
            paddingHorizontal: spacing.lg,
            paddingBottom: spacing.md,
          }}
        >
          <Chip
            label="All"
            selected={activeTypes.length === 0}
            onPress={() => setActiveTypes([])}
          />
          {availableTypes.map(t => {
            const meta = TYPE_META[t];
            const active = activeTypes.includes(t);
            return (
              <Chip
                key={t}
                label={meta.label}
                leftIcon={TYPE_ICONS[t]}
                selected={active}
                onPress={() => toggleType(t)}
              />
            );
          })}
        </View>
      )}

      {items.length === 0 ? (
        <View style={{ paddingVertical: spacing['4xl'], alignItems: 'center', gap: spacing.sm }}>
          <View
            style={{
              width: 64,
              height: 64,
              borderRadius: 32,
              backgroundColor: colors.surfaceAlt,
              alignItems: 'center',
              justifyContent: 'center',
              marginBottom: spacing.sm,
            }}
          >
            <Icon name="list" size={30} color={colors.textMuted} />
          </View>
          <Text variant="bodySm" color="textMuted" weight="600">
            No activity yet
          </Text>
        </View>
      ) : (
        <FlatList
          data={items}
          keyExtractor={item => `${item.type}-${item.id}`}
          renderItem={({ item }) => <ActivityRow item={item} />}
          scrollEnabled={!embedded}
          showsVerticalScrollIndicator={false}
          onEndReached={!embedded ? onLoadMore : undefined}
          onEndReachedThreshold={0.3}
          keyboardShouldPersistTaps="handled"
          refreshControl={!embedded ? (
            <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.accent} colors={[colors.accent]} />
          ) : undefined}
          ListFooterComponent={
            embedded ? (
              onSeeAll ? (
                <PressableScale
                  onPress={onSeeAll}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    justifyContent: 'center',
                    gap: spacing.xs,
                    paddingVertical: spacing.lg,
                  }}
                >
                  <Text variant="bodySm" weight="600" color="accent">
                    See all activity
                  </Text>
                  <Icon name="chevronRight" size={14} color={colors.accent} />
                </PressableScale>
              ) : null
            ) : loadingMore ? (
              <View style={{ padding: spacing.lg, alignItems: 'center' }}>
                <Spinner />
              </View>
            ) : !hasMore && items.length > 0 ? (
              <Text variant="caption" color="textMuted" align="center" style={{ padding: spacing.lg }}>
                All caught up
              </Text>
            ) : null
          }
          contentContainerStyle={!embedded ? { paddingBottom: spacing['4xl'] } : undefined}
        />
      )}
    </View>
  );
}
