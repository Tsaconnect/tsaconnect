// app/p2p/index.tsx
//
// P2P marketplace — browse sell-ads, filter by token / fiat. Phase 1 ships
// the sell side only; the buy-side UI is a documented Phase-1.5 follow-up.
import { router } from 'expo-router';
import React, { useCallback, useEffect, useState } from 'react';
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
import {
  adsApi,
  formatTokenAmount,
  type P2PAd,
} from '../../services/p2p';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
};

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };

const P2PMarketScreen: React.FC = () => {
  const [ads, setAds] = useState<P2PAd[]>([]);
  const [token, setToken] = useState<string>('USDT');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    const res = await adsApi.list({ side: 'sell', token, fiatCurrency: 'NGN' });
    if (res.success && res.data) {
      setAds(res.data);
    }
    setLoading(false);
    setRefreshing(false);
  }, [token]);

  useEffect(() => {
    load();
  }, [load]);

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
          <Text style={styles.heading}>Buy {token}</Text>
          <View style={styles.actions}>
            <TouchableOpacity
              style={styles.iconBtn}
              onPress={() => router.push('/p2p/create-ad')}
              activeOpacity={0.8}
            >
              <Icon name="add-circle" size={22} color={GOLD.dark} />
              <Text style={styles.iconBtnText}>Sell</Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={styles.iconBtn}
              onPress={() => router.push('/p2p/payment-methods')}
              activeOpacity={0.8}
            >
              <Icon name="account-balance" size={22} color={GOLD.dark} />
              <Text style={styles.iconBtnText}>Banks</Text>
            </TouchableOpacity>
          </View>
        </View>

        <Text style={styles.subheading}>
          Buy {token} for NGN with bank transfer. Funds are escrowed on chain
          until the seller releases.
        </Text>

        <View style={styles.tokenRow}>
          {['USDT', 'USDC', 'MCGP'].map((t) => (
            <TouchableOpacity
              key={t}
              style={[styles.tokenChip, token === t && styles.tokenChipActive]}
              onPress={() => setToken(t)}
              activeOpacity={0.8}
            >
              <Text
                style={[styles.tokenChipText, token === t && styles.tokenChipTextActive]}
              >
                {t}
              </Text>
            </TouchableOpacity>
          ))}
        </View>

        {loading ? (
          <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 60 }} />
        ) : ads.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="storefront" size={48} color={GOLD.muted} />
            <Text style={styles.emptyText}>
              No {token} ads available right now. Pull to refresh or try a different token.
            </Text>
          </View>
        ) : (
          ads.map((ad) => <AdCard key={ad.id} ad={ad} />)
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const AdCard: React.FC<{ ad: P2PAd }> = ({ ad }) => {
  const decimals = TOKEN_DECIMALS[ad.token] ?? 18;
  const remaining = formatTokenAmount(ad.remaining, decimals, 2);
  return (
    <TouchableOpacity
      style={styles.card}
      onPress={() => router.push(`/p2p/ad/${ad.id}`)}
      activeOpacity={0.85}
    >
      <View style={styles.cardTop}>
        <Text style={styles.cardSeller}>
          Seller •{ad.sellerId.slice(0, 6)}
        </Text>
        <View style={styles.statusBadge}>
          <Text style={styles.statusBadgeText}>{ad.status}</Text>
        </View>
      </View>
      <View style={styles.cardRow}>
        <Text style={styles.cardRate}>₦{ad.rate}</Text>
        <Text style={styles.cardRateUnit}> / {ad.token}</Text>
      </View>
      <View style={styles.cardMeta}>
        <Text style={styles.cardMetaText}>
          Available: {remaining} {ad.token}
        </Text>
        {ad.minLimit && ad.maxLimit && (
          <Text style={styles.cardMetaText}>
            ₦{ad.minLimit} – ₦{ad.maxLimit}
          </Text>
        )}
      </View>
    </TouchableOpacity>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  headerRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  heading: { fontSize: 24, fontWeight: '800', color: '#000' },
  actions: { flexDirection: 'row', gap: 12 },
  iconBtn: { alignItems: 'center' },
  iconBtnText: { fontSize: 11, color: GOLD.dark, marginTop: 2, fontWeight: '600' },
  subheading: { fontSize: 14, color: '#666', marginTop: 6, marginBottom: 16, lineHeight: 20 },
  tokenRow: { flexDirection: 'row', gap: 10, marginBottom: 18 },
  tokenChip: {
    paddingVertical: 8,
    paddingHorizontal: 16,
    borderRadius: 8,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  tokenChipActive: { backgroundColor: GOLD.primary, borderColor: GOLD.dark },
  tokenChipText: { fontWeight: '700', color: GOLD.dark, fontSize: 13 },
  tokenChipTextActive: { color: '#000' },
  empty: { alignItems: 'center', paddingVertical: 60 },
  emptyText: {
    marginTop: 12,
    fontSize: 14,
    color: '#888',
    textAlign: 'center',
    maxWidth: 260,
  },
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
  cardTop: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  cardSeller: { fontSize: 12, color: '#888', fontWeight: '600' },
  statusBadge: { backgroundColor: GOLD.light, paddingHorizontal: 8, paddingVertical: 3, borderRadius: 6 },
  statusBadgeText: { fontSize: 11, color: GOLD.dark, fontWeight: '700' },
  cardRow: { flexDirection: 'row', alignItems: 'baseline' },
  cardRate: { fontSize: 22, fontWeight: '800', color: '#000' },
  cardRateUnit: { fontSize: 13, color: '#666', fontWeight: '600' },
  cardMeta: { flexDirection: 'row', justifyContent: 'space-between', marginTop: 8 },
  cardMetaText: { fontSize: 12, color: '#666' },
});

export default P2PMarketScreen;
