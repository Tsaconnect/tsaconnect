// app/p2p/my-ads.tsx
//
// Seller's own ads — list with status filter, pause/resume, and on-chain close.
// Close flows through the prepare → sign → broadcast → confirm pattern just
// like create-ad and trade actions.
import { router, useFocusEffect } from 'expo-router';
import React, { useCallback, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import type { ChainKey } from '../../constants/chains';
import { adsApi, formatTokenAmount, type P2PAd, type UnsignedTx } from '../../services/p2p';
import { broadcastTransaction, getActiveWallet, signTransaction } from '../../services/wallet';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#28A745',
};

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };

type StatusFilter = 'all' | P2PAd['status'];

const STATUS_TABS: { key: StatusFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'active', label: 'Active' },
  { key: 'paused', label: 'Paused' },
  { key: 'pending', label: 'Pending' },
  { key: 'exhausted', label: 'Done' },
  { key: 'cancelled', label: 'Cancelled' },
];

const MyAdsScreen: React.FC = () => {
  const [ads, setAds] = useState<P2PAd[]>([]);
  const [filter, setFilter] = useState<StatusFilter>('all');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [acting, setActing] = useState<string>(''); // adId currently undergoing an action

  const load = useCallback(async () => {
    const res = await adsApi.listMine(filter === 'all' ? {} : { status: filter });
    if (res.success && res.data) setAds(res.data);
    setLoading(false);
    setRefreshing(false);
  }, [filter]);

  useFocusEffect(useCallback(() => {
    setLoading(true);
    load();
  }, [load]));

  const onTogglePause = async (ad: P2PAd) => {
    if (acting) return;
    setActing(ad.id);
    const res = ad.status === 'paused' ? await adsApi.resume(ad.id) : await adsApi.pause(ad.id);
    if (!res.success) {
      Alert.alert('Action failed', res.message || 'Server rejected the request');
    }
    setActing('');
    load();
  };

  const onClose = (ad: P2PAd) => {
    Alert.alert(
      'Close this ad?',
      'Closing the ad refunds the unsold portion to your wallet and stops new trades. ' +
        'Trades already in progress will continue until they finish.',
      [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Close ad', style: 'destructive', onPress: () => doClose(ad) },
      ],
    );
  };

  const doClose = async (ad: P2PAd) => {
    if (acting) return;
    let broadcastHash: string | null = null;
    try {
      setActing(ad.id);
      const sellerAddress = await getActiveWallet();
      if (!sellerAddress) throw new Error('No wallet found');
      const prep = await adsApi.prepareClose(ad.id, sellerAddress);
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Prepare failed');
      const signed = await signTransaction(toTxLike(prep.data.unsignedTx));
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;
      const conf = await adsApi.confirmClose(ad.id, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');
    } catch (err: any) {
      const detail = err?.message || String(err);
      if (broadcastHash) {
        Alert.alert(
          'Close submitted, server confirm failed',
          `Tx ${broadcastHash} was broadcast on chain but the server couldn't record it ` +
            `(${detail}). Wait a minute for the reconciler to catch up; contact support if not.`,
        );
      } else {
        Alert.alert('Close failed', detail);
      }
    } finally {
      setActing('');
      load();
    }
  };

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
        <View style={styles.headerRow}>
          <Text style={styles.heading}>My Ads</Text>
          <TouchableOpacity
            style={styles.cta}
            onPress={() => router.push('/p2p/create-ad')}
            activeOpacity={0.85}
          >
            <Icon name="add" size={18} color="#000" />
            <Text style={styles.ctaText}>New ad</Text>
          </TouchableOpacity>
        </View>

        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          style={styles.tabRow}
          contentContainerStyle={{ gap: 8, paddingHorizontal: 2 }}
        >
          {STATUS_TABS.map((t) => (
            <TouchableOpacity
              key={t.key}
              style={[styles.tab, filter === t.key && styles.tabActive]}
              onPress={() => setFilter(t.key)}
              activeOpacity={0.8}
            >
              <Text style={[styles.tabText, filter === t.key && styles.tabTextActive]}>
                {t.label}
              </Text>
            </TouchableOpacity>
          ))}
        </ScrollView>

        {loading ? (
          <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 60 }} />
        ) : ads.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="storefront" size={48} color={GOLD.muted} />
            <Text style={styles.emptyText}>
              {filter === 'all'
                ? "You haven't posted any ads yet. Tap 'New ad' to start selling."
                : `No ads in '${filter}' status.`}
            </Text>
          </View>
        ) : (
          ads.map((ad) => (
            <AdCard
              key={ad.id}
              ad={ad}
              busy={acting === ad.id}
              onTogglePause={onTogglePause}
              onClose={onClose}
            />
          ))
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const AdCard: React.FC<{
  ad: P2PAd;
  busy: boolean;
  onTogglePause: (ad: P2PAd) => void;
  onClose: (ad: P2PAd) => void;
}> = ({ ad, busy, onTogglePause, onClose }) => {
  const decimals = TOKEN_DECIMALS[ad.token] ?? 18;
  const remaining = formatTokenAmount(ad.remaining, decimals, 2);
  const reserved = formatTokenAmount(ad.reserved, decimals, 2);
  const total = formatTokenAmount(ad.amount, decimals, 2);
  const canPauseResume = ad.status === 'active' || ad.status === 'paused';
  const canClose = ad.status === 'active' || ad.status === 'paused' || ad.status === 'pending';

  return (
    <TouchableOpacity
      style={styles.card}
      onPress={() => router.push(`/p2p/ad/${ad.id}`)}
      activeOpacity={0.85}
    >
      <View style={styles.cardTop}>
        <Text style={styles.cardRate}>
          ₦{ad.rate} <Text style={styles.cardRateUnit}>/ {ad.token}</Text>
        </Text>
        <View style={[styles.statusBadge, statusBadgeColor(ad.status)]}>
          <Text style={styles.statusBadgeText}>{ad.status}</Text>
        </View>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Available</Text>
        <Text style={styles.cardMetaValue}>{remaining} {ad.token}</Text>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Reserved</Text>
        <Text style={styles.cardMetaValue}>{reserved} {ad.token}</Text>
      </View>
      <View style={styles.cardMetaRow}>
        <Text style={styles.cardMetaLabel}>Total</Text>
        <Text style={styles.cardMetaValue}>{total} {ad.token}</Text>
      </View>

      {(canPauseResume || canClose) && (
        <View style={styles.cardActions}>
          {canPauseResume && (
            <TouchableOpacity
              style={[styles.secondaryBtn, busy && styles.btnDisabled]}
              onPress={() => onTogglePause(ad)}
              disabled={busy}
            >
              <Icon
                name={ad.status === 'paused' ? 'play-arrow' : 'pause'}
                size={16}
                color={GOLD.dark}
              />
              <Text style={styles.secondaryBtnText}>
                {ad.status === 'paused' ? 'Resume' : 'Pause'}
              </Text>
            </TouchableOpacity>
          )}
          {canClose && (
            <TouchableOpacity
              style={[styles.dangerBtn, busy && styles.btnDisabled]}
              onPress={() => onClose(ad)}
              disabled={busy}
            >
              <Icon name="close" size={16} color={GOLD.error} />
              <Text style={styles.dangerBtnText}>Close ad</Text>
            </TouchableOpacity>
          )}
        </View>
      )}
    </TouchableOpacity>
  );
};

function statusBadgeColor(status: P2PAd['status']) {
  switch (status) {
    case 'active': return { backgroundColor: '#E8F5E9', borderColor: GOLD.success };
    case 'paused': return { backgroundColor: '#FFF3CD', borderColor: GOLD.dark };
    case 'pending': return { backgroundColor: GOLD.light, borderColor: GOLD.dark };
    case 'exhausted': return { backgroundColor: '#ECEFF1', borderColor: '#666' };
    case 'cancelled': return { backgroundColor: '#FBE9E7', borderColor: GOLD.error };
    default: return { backgroundColor: GOLD.light, borderColor: GOLD.dark };
  }
}

function toTxLike(tx: UnsignedTx) {
  const ensure0x = (v?: string) => (!v ? undefined : v.startsWith('0x') ? v : `0x${v}`);
  return {
    to: tx.to,
    value: tx.value || '0',
    data: ensure0x(tx.data),
    nonce: tx.nonce,
    gasLimit: tx.gasLimit,
    gasPrice: tx.gasPrice,
    chainId: tx.chainId ? Number(tx.chainId) : undefined,
  };
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  headerRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  heading: { fontSize: 24, fontWeight: '800', color: '#000' },
  cta: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD.primary,
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    gap: 4,
  },
  ctaText: { fontWeight: '700', color: '#000' },
  tabRow: { marginBottom: 14 },
  tab: {
    paddingVertical: 6,
    paddingHorizontal: 12,
    borderRadius: 16,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  tabActive: { backgroundColor: GOLD.primary, borderColor: GOLD.dark },
  tabText: { fontSize: 12, color: GOLD.dark, fontWeight: '700' },
  tabTextActive: { color: '#000' },
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
  cardRate: { fontSize: 20, fontWeight: '800', color: '#000' },
  cardRateUnit: { fontSize: 13, color: '#666', fontWeight: '600' },
  statusBadge: { paddingHorizontal: 8, paddingVertical: 3, borderRadius: 6, borderWidth: 1 },
  statusBadgeText: { fontSize: 11, color: '#000', fontWeight: '700', textTransform: 'capitalize' },
  cardMetaRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 4 },
  cardMetaLabel: { fontSize: 13, color: '#666' },
  cardMetaValue: { fontSize: 13, color: '#000', fontWeight: '600' },
  cardActions: { flexDirection: 'row', gap: 10, marginTop: 12 },
  secondaryBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: GOLD.dark,
  },
  secondaryBtnText: { fontSize: 13, color: GOLD.dark, fontWeight: '700' },
  dangerBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: GOLD.error,
  },
  dangerBtnText: { fontSize: 13, color: GOLD.error, fontWeight: '700' },
  btnDisabled: { opacity: 0.4 },
});

export default MyAdsScreen;
