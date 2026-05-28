// app/p2p/seller/[sellerId].tsx — merchant profile: badges, stats, recent feedback, active ads.
import { router, useLocalSearchParams } from 'expo-router';
import React, { useEffect, useState } from 'react';
import { ActivityIndicator, ScrollView, StyleSheet, Text, TouchableOpacity, View } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import { sellersApi, formatFiat, type MerchantProfile } from '../../../services/p2p';
import SellerTrustBadges from '../../../components/p2p/SellerTrustBadges';
import { P2P_GOLD } from '../../../constants/p2p';

const GOLD = P2P_GOLD;

const SellerProfileScreen: React.FC = () => {
  const { sellerId } = useLocalSearchParams<{ sellerId: string }>();
  const [profile, setProfile] = useState<MerchantProfile | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    (async () => {
      if (!sellerId) return;
      const res = await sellersApi.profile(String(sellerId));
      if (res.success && res.data) setProfile(res.data);
      setLoading(false);
    })();
  }, [sellerId]);

  if (loading) {
    return (
      <SafeAreaView style={styles.container}>
        <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 80 }} />
      </SafeAreaView>
    );
  }
  if (!profile) {
    return (
      <SafeAreaView style={styles.container}>
        <Text style={styles.empty}>Seller not found.</Text>
      </SafeAreaView>
    );
  }
  const up = profile.recentRatings.filter((r) => r.value === 1).length;
  const down = profile.recentRatings.filter((r) => r.value === -1).length;

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView contentContainerStyle={styles.scroll}>
        <Text style={styles.heading}>Merchant</Text>
        <View style={styles.card}>
          <SellerTrustBadges stats={profile.stats} />
        </View>

        <Text style={styles.sectionLabel}>Recent feedback</Text>
        <View style={styles.card}>
          <View style={styles.feedbackRow}>
            <Icon name="thumb-up" size={16} color="#2E8B57" />
            <Text style={styles.feedbackNum}>{up}</Text>
            <Icon name="thumb-down" size={16} color={GOLD.error} style={{ marginLeft: 16 }} />
            <Text style={styles.feedbackNum}>{down}</Text>
          </View>
          {profile.recentRatings.length === 0 && <Text style={styles.muted}>No feedback yet.</Text>}
        </View>

        <Text style={styles.sectionLabel}>Active ads ({profile.activeAds.length})</Text>
        {profile.activeAds.map((ad) => (
          <TouchableOpacity key={ad.id} style={styles.adRow} onPress={() => router.push(`/p2p/ad/${ad.id}` as any)} activeOpacity={0.85}>
            <Text style={styles.adToken}>{ad.token}</Text>
            <Text style={styles.adRate}>{ad.fiatCurrency} {formatFiat(ad.rate)}</Text>
          </TouchableOpacity>
        ))}
        {profile.activeAds.length === 0 && <Text style={styles.muted}>No active ads.</Text>}
      </ScrollView>
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 22, fontWeight: '800', color: '#000', marginBottom: 12 },
  card: { backgroundColor: '#FFF', borderRadius: 12, padding: 14, borderWidth: 1, borderColor: GOLD.muted, marginBottom: 16 },
  sectionLabel: { fontSize: 13, fontWeight: '700', color: '#333', marginBottom: 8 },
  feedbackRow: { flexDirection: 'row', alignItems: 'center' },
  feedbackNum: { fontSize: 15, fontWeight: '700', color: '#000', marginLeft: 6 },
  adRow: { flexDirection: 'row', justifyContent: 'space-between', backgroundColor: '#FFF', borderRadius: 10, padding: 14, borderWidth: 1, borderColor: GOLD.muted, marginBottom: 8 },
  adToken: { fontSize: 15, fontWeight: '700', color: '#000' },
  adRate: { fontSize: 14, color: GOLD.dark, fontWeight: '700' },
  muted: { fontSize: 13, color: '#888', fontStyle: 'italic' },
  empty: { textAlign: 'center', marginTop: 60, color: '#888' },
});

export default SellerProfileScreen;
