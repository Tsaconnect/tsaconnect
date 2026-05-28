// app/p2p/my-trades.tsx
//
// Trades the current user is a party to. Tabs switch between Buyer and Seller
// roles, then a status pill picker narrows to Active (in-flight) or Done.
import { router, useFocusEffect } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ActivityIndicator,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import { formatTokenAmount, tradesApi, type P2PTrade } from '../../services/p2p';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  success: '#28A745',
  error: '#DC3545',
  info: '#3B82F6',
};

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };

type Role = 'buyer' | 'seller';
type Bucket = 'active' | 'done';

const ACTIVE_STATUSES: P2PTrade['status'][] = [
  'pending_payment',
  'paid',
  'releasing',
  'disputed',
];

const MyTradesScreen: React.FC = () => {
  const [role, setRole] = useState<Role>('buyer');
  const [bucket, setBucket] = useState<Bucket>('active');
  const [trades, setTrades] = useState<P2PTrade[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    const res = await tradesApi.list({ role });
    if (res.success && res.data) setTrades(res.data);
    setLoading(false);
    setRefreshing(false);
  }, [role]);

  // useFocusEffect only fires on screen focus events, so a role-tab swap
  // while already focused wouldn't re-fetch. Pair it with a plain useEffect
  // on `load` so any dependency change (role) refreshes the list.
  useFocusEffect(useCallback(() => {
    setLoading(true);
    load();
  }, [load]));
  useEffect(() => {
    setLoading(true);
    load();
  }, [load]);

  const visible = useMemo(() => {
    const active = new Set<P2PTrade['status']>(ACTIVE_STATUSES);
    return trades.filter((t) => (bucket === 'active' ? active.has(t.status) : !active.has(t.status)));
  }, [trades, bucket]);

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView
        contentContainerStyle={styles.scroll}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <Text style={styles.heading}>My Trades</Text>

        <View style={styles.roleRow}>
          {(['buyer', 'seller'] as Role[]).map((r) => (
            <TouchableOpacity
              key={r}
              style={[styles.roleTab, role === r && styles.roleTabActive]}
              onPress={() => setRole(r)}
              activeOpacity={0.85}
            >
              <Text style={[styles.roleTabText, role === r && styles.roleTabTextActive]}>
                As {r}
              </Text>
            </TouchableOpacity>
          ))}
        </View>

        <View style={styles.bucketRow}>
          {([
            { key: 'active', label: 'Active' },
            { key: 'done', label: 'Done' },
          ] as { key: Bucket; label: string }[]).map((b) => (
            <TouchableOpacity
              key={b.key}
              style={[styles.bucket, bucket === b.key && styles.bucketActive]}
              onPress={() => setBucket(b.key)}
              activeOpacity={0.85}
            >
              <Text style={[styles.bucketText, bucket === b.key && styles.bucketTextActive]}>
                {b.label}
              </Text>
            </TouchableOpacity>
          ))}
        </View>

        {loading ? (
          <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 60 }} />
        ) : visible.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="swap-horiz" size={48} color={GOLD.muted} />
            <Text style={styles.emptyText}>
              {bucket === 'active'
                ? `No active trades as ${role}.`
                : `No completed trades as ${role} yet.`}
            </Text>
          </View>
        ) : (
          visible.map((t) => <TradeCard key={t.id} trade={t} role={role} />)
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const TradeCard: React.FC<{ trade: P2PTrade; role: Role }> = ({ trade, role }) => {
  const decimals = TOKEN_DECIMALS[trade.token] ?? 18;
  const amount = formatTokenAmount(trade.tokenAmount, decimals, 4);
  const counterparty = role === 'buyer' ? trade.sellerId : trade.buyerId;
  const statusBadge = statusBadgeStyle(trade.status);

  return (
    <TouchableOpacity
      style={styles.card}
      onPress={() => router.push(`/p2p/trade/${trade.id}`)}
      activeOpacity={0.85}
    >
      <View style={styles.cardTop}>
        <Text style={styles.cardAmount}>
          {amount} {trade.token}
        </Text>
        <View style={[styles.statusBadge, { borderColor: statusBadge.color, backgroundColor: statusBadge.bg }]}>
          <Text style={[styles.statusBadgeText, { color: statusBadge.color }]}>
            {labelForStatus(trade.status)}
          </Text>
        </View>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Fiat</Text>
        <Text style={styles.cardMetaValue}>
          ₦{Number(trade.fiatAmount).toLocaleString()}
        </Text>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Rate</Text>
        <Text style={styles.cardMetaValue}>
          ₦{trade.rate} / {trade.token}
        </Text>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>{role === 'buyer' ? 'Seller' : 'Buyer'}</Text>
        <Text style={styles.cardMetaValue}>•{counterparty.slice(0, 8)}</Text>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Updated</Text>
        <Text style={styles.cardMetaValue}>{formatRelative(trade.updatedAt)}</Text>
      </View>
      {trade.status === 'released' && (
        <TouchableOpacity
          style={styles.rateNudge}
          onPress={() => router.push(`/p2p/trade/${trade.id}`)}
          activeOpacity={0.75}
        >
          <Icon name="star-rate" size={14} color={GOLD.dark} />
          <Text style={styles.rateNudgeText}>Rate this trade</Text>
        </TouchableOpacity>
      )}
    </TouchableOpacity>
  );
};

function statusBadgeStyle(status: P2PTrade['status']) {
  switch (status) {
    case 'released':   return { color: GOLD.success, bg: '#E8F5E9' };
    case 'cancelled':  return { color: '#666',       bg: '#ECEFF1' };
    case 'disputed':   return { color: GOLD.error,   bg: '#FBE9E7' };
    case 'resolved':   return { color: GOLD.info,    bg: '#E3F2FD' };
    case 'paid':       return { color: GOLD.dark,    bg: GOLD.light };
    case 'releasing':  return { color: GOLD.info,    bg: '#E3F2FD' };
    default:           return { color: GOLD.dark,    bg: GOLD.light };
  }
}

function labelForStatus(s: P2PTrade['status']): string {
  switch (s) {
    case 'pending_payment': return 'Awaiting payment';
    case 'paid': return 'Paid — awaiting release';
    case 'releasing': return 'Releasing…';
    case 'released': return 'Released';
    case 'cancelled': return 'Cancelled';
    case 'disputed': return 'Disputed';
    case 'resolved': return 'Resolved';
    default: return s;
  }
}

function formatRelative(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return iso;
  if (ms < 60_000) return 'just now';
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m ago`;
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h ago`;
  return `${Math.floor(ms / 86_400_000)}d ago`;
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 24, fontWeight: '800', color: '#000', marginBottom: 14 },
  roleRow: { flexDirection: 'row', backgroundColor: '#FFF', borderRadius: 10, padding: 4, marginBottom: 12 },
  roleTab: { flex: 1, paddingVertical: 8, alignItems: 'center', borderRadius: 8 },
  roleTabActive: { backgroundColor: GOLD.primary },
  roleTabText: { fontWeight: '700', color: GOLD.dark, fontSize: 13 },
  roleTabTextActive: { color: '#000' },
  bucketRow: { flexDirection: 'row', gap: 10, marginBottom: 16 },
  bucket: {
    paddingVertical: 6,
    paddingHorizontal: 14,
    borderRadius: 16,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  bucketActive: { backgroundColor: GOLD.dark, borderColor: GOLD.dark },
  bucketText: { fontSize: 12, color: GOLD.dark, fontWeight: '700' },
  bucketTextActive: { color: '#FFF' },
  empty: { alignItems: 'center', paddingVertical: 60 },
  emptyText: { marginTop: 12, fontSize: 14, color: '#888', textAlign: 'center', maxWidth: 280 },
  card: {
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 16,
    marginBottom: 12,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.06,
    shadowRadius: 4,
    elevation: 2,
  },
  cardTop: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10 },
  cardAmount: { fontSize: 20, fontWeight: '800', color: '#000' },
  statusBadge: { paddingHorizontal: 8, paddingVertical: 3, borderRadius: 6, borderWidth: 1 },
  statusBadgeText: { fontSize: 11, fontWeight: '700' },
  cardMetaRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 4 },
  cardMetaLabel: { fontSize: 13, color: '#666' },
  cardMetaValue: { fontSize: 13, color: '#000', fontWeight: '600' },
  rateNudge: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 10,
    paddingTop: 10,
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
    gap: 6,
  },
  rateNudgeText: { fontSize: 12, color: GOLD.dark, fontWeight: '700' },
});

export default MyTradesScreen;
