import React, { useState, useEffect, useCallback } from 'react';
import {
  View, Text, StyleSheet, FlatList, TouchableOpacity,
  RefreshControl, ActivityIndicator, Linking,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { getTransactionHistory, type Transaction } from '@/services/walletApi';
import { CHAINS } from '@/constants/chains';

const GOLD = '#D4AF37';
const DARK = '#1A1A1A';
const PAGE_SIZE = 20;

type TxFilter = 'all' | 'send' | 'receive' | 'swap';

const FILTERS: { key: TxFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'send', label: 'Send' },
  { key: 'receive', label: 'Receive' },
  { key: 'swap', label: 'Swap' },
];

function txIcon(type: string): { name: keyof typeof Ionicons.glyphMap; color: string; bg: string } {
  switch (type) {
    case 'send':    return { name: 'arrow-up', color: '#EF4444', bg: '#FEF2F2' };
    case 'receive': return { name: 'arrow-down', color: '#22C55E', bg: '#F0FDF4' };
    case 'swap':    return { name: 'swap-horizontal', color: GOLD, bg: '#FFF8E1' };
    case 'approve': return { name: 'checkmark-circle-outline', color: '#888', bg: '#F5F5F5' };
    default:        return { name: 'ellipse-outline', color: '#888', bg: '#F5F5F5' };
  }
}

function formatDate(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const diffMs = now.getTime() - d.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return 'Just now';
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHrs = Math.floor(diffMins / 60);
  if (diffHrs < 24) return `${diffHrs}h ago`;
  const diffDays = Math.floor(diffHrs / 24);
  if (diffDays < 7) return `${diffDays}d ago`;
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

function truncateAddr(addr: string): string {
  if (!addr || addr.length < 10) return addr;
  return `${addr.slice(0, 6)}…${addr.slice(-4)}`;
}

function explorerUrl(tx: Transaction): string | null {
  const chain = Object.values(CHAINS).find(c => c.chainId === tx.chainId);
  return chain ? `${chain.explorerUrl}/tx/${tx.txHash}` : null;
}

function TxRow({ tx }: { tx: Transaction }) {
  const icon = txIcon(tx.txType ?? tx.type);
  const isSend = tx.txType === 'send' || tx.type === 'send';
  const url = explorerUrl(tx);

  return (
    <TouchableOpacity
      style={s.row}
      activeOpacity={url ? 0.7 : 1}
      onPress={() => url && Linking.openURL(url)}
    >
      <View style={[s.iconWrap, { backgroundColor: icon.bg }]}>
        <Ionicons name={icon.name} size={18} color={icon.color} />
      </View>
      <View style={s.rowMid}>
        <Text style={s.rowTitle}>
          {(tx.txType ?? tx.type ?? '').charAt(0).toUpperCase() + (tx.txType ?? tx.type ?? '').slice(1)}{' '}
          <Text style={s.rowToken}>{tx.tokenSymbol}</Text>
        </Text>
        <Text style={s.rowSub}>
          {isSend ? `To: ${truncateAddr(tx.toAddress)}` : `From: ${truncateAddr(tx.fromAddress)}`}
        </Text>
      </View>
      <View style={s.rowRight}>
        <Text style={[s.rowAmount, { color: isSend ? '#EF4444' : '#22C55E' }]}>
          {isSend ? '−' : '+'}{tx.amount} {tx.tokenSymbol}
        </Text>
        <Text style={s.rowDate}>{formatDate(tx.createdAt)}</Text>
        <StatusBadge status={tx.status} />
      </View>
    </TouchableOpacity>
  );
}

function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { color: string; bg: string }> = {
    confirmed: { color: '#22C55E', bg: '#F0FDF4' },
    pending:   { color: '#F59E0B', bg: '#FFFBEB' },
    failed:    { color: '#EF4444', bg: '#FEF2F2' },
  };
  const style = map[status] ?? map.pending;
  return (
    <View style={[s.badge, { backgroundColor: style.bg }]}>
      <Text style={[s.badgeText, { color: style.color }]}>{status}</Text>
    </View>
  );
}

interface Props {
  chainId?: number;
  tokenSymbol?: string;
  /** When true renders as an embedded list (no filter bar, limited rows) */
  embedded?: boolean;
  onSeeAll?: () => void;
}

export default function WalletHistory({ chainId, tokenSymbol, embedded, onSeeAll }: Props) {
  const [filter, setFilter] = useState<TxFilter>('all');
  const [txs, setTxs] = useState<Transaction[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);

  const load = useCallback(async (pg: number, reset: boolean) => {
    if (pg === 1) reset ? setRefreshing(true) : setLoading(true);
    else setLoadingMore(true);

    const limit = embedded ? 5 : PAGE_SIZE;
    const res = await getTransactionHistory(pg, limit, chainId);

    if (res.success && res.data) {
      const fetched = res.data.transactions ?? [];
      const filtered = fetched.filter(t => {
        if (filter !== 'all' && (t.txType ?? t.type) !== filter) return false;
        if (tokenSymbol && t.tokenSymbol !== tokenSymbol) return false;
        return true;
      });
      setTxs(prev => (pg === 1 ? filtered : [...prev, ...filtered]));
      setHasMore(fetched.length === limit);
      setPage(pg);
    }

    setLoading(false);
    setRefreshing(false);
    setLoadingMore(false);
  }, [filter, chainId, tokenSymbol, embedded]);

  useEffect(() => { load(1, false); }, [load]);

  const onRefresh = () => load(1, true);
  const onLoadMore = () => { if (hasMore && !loadingMore) load(page + 1, false); };

  if (loading) return (
    <View style={s.center}>
      <ActivityIndicator color={GOLD} />
    </View>
  );

  return (
    <View style={embedded ? undefined : { flex: 1, backgroundColor: '#FAFAFA' }}>
      {/* Filter bar — full screen only */}
      {!embedded && (
        <View style={s.filterRow}>
          {FILTERS.map(f => (
            <TouchableOpacity
              key={f.key}
              style={[s.filterChip, filter === f.key && s.filterChipActive]}
              onPress={() => { setFilter(f.key); load(1, false); }}
              activeOpacity={0.7}
            >
              <Text style={[s.filterText, filter === f.key && s.filterTextActive]}>{f.label}</Text>
            </TouchableOpacity>
          ))}
        </View>
      )}

      {txs.length === 0 ? (
        <View style={s.center}>
          <Ionicons name="receipt-outline" size={40} color="#DDD" />
          <Text style={s.emptyText}>No transactions yet</Text>
        </View>
      ) : (
        <FlatList
          data={txs}
          keyExtractor={t => t.id}
          renderItem={({ item }) => <TxRow tx={item} />}
          scrollEnabled={!embedded}
          showsVerticalScrollIndicator={false}
          refreshControl={!embedded ? (
            <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={GOLD} colors={[GOLD]} />
          ) : undefined}
          onEndReached={!embedded ? onLoadMore : undefined}
          onEndReachedThreshold={0.3}
          ListFooterComponent={
            embedded ? (
              onSeeAll ? (
                <TouchableOpacity style={s.seeAll} onPress={onSeeAll}>
                  <Text style={s.seeAllText}>See all transactions</Text>
                  <Ionicons name="chevron-forward" size={14} color={GOLD} />
                </TouchableOpacity>
              ) : null
            ) : loadingMore ? (
              <ActivityIndicator color={GOLD} style={{ padding: 16 }} />
            ) : null
          }
          contentContainerStyle={embedded ? undefined : { paddingBottom: 40 }}
        />
      )}
    </View>
  );
}

const s = StyleSheet.create({
  center: { padding: 40, alignItems: 'center', gap: 12 },
  emptyText: { fontSize: 15, color: '#BBB', fontWeight: '600' },

  filterRow: { flexDirection: 'row', gap: 8, padding: 16, paddingBottom: 8 },
  filterChip: { paddingHorizontal: 16, paddingVertical: 7, borderRadius: 20, backgroundColor: '#F0F0F0' },
  filterChipActive: { backgroundColor: GOLD },
  filterText: { fontSize: 13, fontWeight: '600', color: '#888' },
  filterTextActive: { color: '#FFF' },

  row: { flexDirection: 'row', alignItems: 'center', gap: 12, paddingHorizontal: 16, paddingVertical: 12, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: '#F5F5F5' },
  iconWrap: { width: 40, height: 40, borderRadius: 20, alignItems: 'center', justifyContent: 'center' },
  rowMid: { flex: 1 },
  rowTitle: { fontSize: 14, fontWeight: '600', color: DARK },
  rowToken: { fontWeight: '700', color: DARK },
  rowSub: { fontSize: 12, color: '#AAA', marginTop: 2 },
  rowRight: { alignItems: 'flex-end', gap: 3 },
  rowAmount: { fontSize: 13, fontWeight: '700' },
  rowDate: { fontSize: 11, color: '#AAA' },

  badge: { paddingHorizontal: 6, paddingVertical: 2, borderRadius: 6 },
  badgeText: { fontSize: 10, fontWeight: '700' },

  seeAll: { flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 4, paddingVertical: 14 },
  seeAllText: { fontSize: 14, fontWeight: '600', color: GOLD },
});
