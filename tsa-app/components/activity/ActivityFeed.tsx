import React, { useState, useEffect, useCallback } from 'react';
import {
  View, Text, FlatList, TouchableOpacity, StyleSheet,
  RefreshControl, ActivityIndicator, Linking,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import {
  fetchActivityItems, ALL_TYPES, TYPE_META,
  type ActivityItem, type ActivityType,
} from '../../services/activityService';
import { CHAINS } from '../../constants/chains';

const GOLD = '#D4AF37';
const PAGE_SIZE = 20;
const EMBEDDED_SIZE = 5;

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
  const chain = Object.values(CHAINS).find(c => c.chainId === item.chainId);
  return chain ? `${chain.explorerUrl}/tx/${item.txHash}` : null;
}

function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { color: string; bg: string }> = {
    confirmed: { color: '#22C55E', bg: '#F0FDF4' },
    completed: { color: '#22C55E', bg: '#F0FDF4' },
    pending:   { color: '#F59E0B', bg: '#FFFBEB' },
    pending_payment: { color: '#F59E0B', bg: '#FFFBEB' },
    failed:    { color: '#EF4444', bg: '#FEF2F2' },
    cancelled: { color: '#EF4444', bg: '#FEF2F2' },
    disputed:  { color: '#EF4444', bg: '#FEF2F2' },
  };
  const st = map[status] ?? map.pending;
  const label = status.replace(/_/g, ' ');
  return (
    <View style={[s.badge, { backgroundColor: st.bg }]}>
      <Text style={[s.badgeText, { color: st.color }]}>{label}</Text>
    </View>
  );
}

function ActivityRow({ item }: { item: ActivityItem }) {
  const url = explorerUrl(item);
  return (
    <TouchableOpacity
      style={s.row}
      activeOpacity={url ? 0.7 : 1}
      onPress={() => url && Linking.openURL(url)}
    >
      <View style={[s.iconWrap, { backgroundColor: item.iconBg }]}>
        <Ionicons name={item.iconName as any} size={18} color={item.iconColor} />
      </View>
      <View style={s.rowMid}>
        <Text style={s.rowTitle}>{item.title}</Text>
        <Text style={s.rowSub} numberOfLines={1}>{item.subtitle}</Text>
      </View>
      <View style={s.rowRight}>
        {item.amount && (
          <Text style={[s.rowAmount, { color: item.positive ? '#22C55E' : '#1A1A1A' }]}>
            {item.positive ? '+' : ''}{item.amount}
          </Text>
        )}
        <Text style={s.rowDate}>{formatDate(item.timestamp)}</Text>
        <StatusBadge status={item.status} />
      </View>
    </TouchableOpacity>
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
  const [activeTypes, setActiveTypes] = useState<ActivityType[]>(types.length > 0 ? types : []);
  const [items, setItems] = useState<ActivityItem[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);

  const limit = embedded ? EMBEDDED_SIZE : PAGE_SIZE;

  const load = useCallback(async (pg: number, refresh: boolean) => {
    if (pg === 1) { refresh ? setRefreshing(true) : setLoading(true); }
    else setLoadingMore(true);

    const fetched = await fetchActivityItems(activeTypes, pg, limit);
    setItems(prev => pg === 1 ? fetched : [...prev, ...fetched]);
    setHasMore(fetched.length >= limit && !embedded);
    setPage(pg);

    setLoading(false);
    setRefreshing(false);
    setLoadingMore(false);
  }, [activeTypes, limit, embedded]);

  useEffect(() => { load(1, false); }, [load]);

  const onRefresh = () => load(1, true);
  const onLoadMore = () => { if (hasMore && !loadingMore) load(page + 1, false); };

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
    <View style={s.center}>
      <ActivityIndicator color={GOLD} />
    </View>
  );

  return (
    <View style={embedded ? undefined : { flex: 1 }}>
      {/* Filter chips */}
      {showFilterBar && (
        <View style={s.filterWrap}>
          <TouchableOpacity
            style={[s.chip, activeTypes.length === 0 && s.chipActive]}
            onPress={() => setActiveTypes([])}
            activeOpacity={0.7}
          >
            <Text style={[s.chipText, activeTypes.length === 0 && s.chipTextActive]}>All</Text>
          </TouchableOpacity>
          {availableTypes.map(t => {
            const meta = TYPE_META[t];
            const active = activeTypes.includes(t);
            return (
              <TouchableOpacity
                key={t}
                style={[s.chip, active && s.chipActive]}
                onPress={() => toggleType(t)}
                activeOpacity={0.7}
              >
                <Ionicons name={meta.iconName as any} size={12} color={active ? '#FFF' : '#888'} />
                <Text style={[s.chipText, active && s.chipTextActive]}>{meta.label}</Text>
              </TouchableOpacity>
            );
          })}
        </View>
      )}

      {items.length === 0 ? (
        <View style={s.center}>
          <Ionicons name="receipt-outline" size={36} color="#DDD" />
          <Text style={s.emptyText}>No activity yet</Text>
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
            <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={GOLD} colors={[GOLD]} />
          ) : undefined}
          ListFooterComponent={
            embedded ? (
              onSeeAll ? (
                <TouchableOpacity style={s.seeAll} onPress={onSeeAll}>
                  <Text style={s.seeAllText}>See all activity</Text>
                  <Ionicons name="chevron-forward" size={14} color={GOLD} />
                </TouchableOpacity>
              ) : null
            ) : loadingMore ? (
              <ActivityIndicator color={GOLD} style={{ padding: 16 }} />
            ) : !hasMore && items.length > 0 ? (
              <Text style={s.endText}>All caught up</Text>
            ) : null
          }
          contentContainerStyle={!embedded ? { paddingBottom: 40 } : undefined}
        />
      )}
    </View>
  );
}

const s = StyleSheet.create({
  center: { padding: 40, alignItems: 'center', gap: 10 },
  emptyText: { fontSize: 14, color: '#BBB', fontWeight: '600' },
  endText: { fontSize: 12, color: '#CCC', textAlign: 'center', padding: 16 },

  filterWrap: {
    flexDirection: 'row', flexWrap: 'wrap', gap: 8,
    paddingHorizontal: 16, paddingBottom: 12,
  },
  chip: {
    flexDirection: 'row', alignItems: 'center', gap: 4,
    paddingHorizontal: 12, paddingVertical: 6,
    borderRadius: 20, backgroundColor: '#F0F0F0',
  },
  chipActive: { backgroundColor: '#1A1A1A' },
  chipText: { fontSize: 12, fontWeight: '600', color: '#888' },
  chipTextActive: { color: '#FFF' },

  row: {
    flexDirection: 'row', alignItems: 'center', gap: 12,
    paddingHorizontal: 16, paddingVertical: 12,
    borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: '#F5F5F5',
  },
  iconWrap: { width: 40, height: 40, borderRadius: 20, alignItems: 'center', justifyContent: 'center' },
  rowMid: { flex: 1 },
  rowTitle: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  rowSub: { fontSize: 12, color: '#AAA', marginTop: 2 },
  rowRight: { alignItems: 'flex-end', gap: 3 },
  rowAmount: { fontSize: 13, fontWeight: '700' },
  rowDate: { fontSize: 11, color: '#AAA' },

  badge: { paddingHorizontal: 6, paddingVertical: 2, borderRadius: 6 },
  badgeText: { fontSize: 9, fontWeight: '700', textTransform: 'capitalize' },

  seeAll: {
    flexDirection: 'row', alignItems: 'center', justifyContent: 'center',
    gap: 4, paddingVertical: 14,
  },
  seeAllText: { fontSize: 14, fontWeight: '600', color: GOLD },
});
