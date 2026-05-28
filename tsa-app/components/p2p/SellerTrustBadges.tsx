// components/p2p/SellerTrustBadges.tsx
//
// Renders a seller's trust signals from SellerStats. Compact row for ad cards;
// shows a "New" chip when the merchant has no completed trades yet.
import React from 'react';
import { StyleSheet, Text, View } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { P2P_GOLD } from '../../constants/p2p';
import type { SellerStats } from '../../services/p2p';

const GOLD = P2P_GOLD;

function relTime(secs?: number): string | null {
  if (secs == null) return null;
  if (secs < 90) return `~${secs}s`;
  return `~${Math.round(secs / 60)}m`;
}

const SellerTrustBadges: React.FC<{ stats?: SellerStats }> = ({ stats }) => {
  if (!stats || stats.isNew) {
    return (
      <View style={styles.row}>
        <View style={[styles.chip, styles.newChip]}>
          <Text style={styles.newChipText}>New</Text>
        </View>
      </View>
    );
  }
  const release = relTime(stats.avgReleaseSecs);
  return (
    <View style={styles.row}>
      {stats.online && <View style={styles.onlineDot} />}
      {stats.trusted ? (
        <View style={[styles.chip, styles.trustedChip]}>
          <Icon name="verified" size={11} color="#fff" />
          <Text style={styles.trustedText}>Trusted</Text>
        </View>
      ) : stats.verified ? (
        <View style={styles.chip}>
          <Icon name="check-circle" size={11} color={GOLD.dark} />
          <Text style={styles.chipText}>Verified</Text>
        </View>
      ) : null}
      <Text style={styles.stat}>{stats.totalOrders} orders</Text>
      {stats.completionRate != null && (
        <Text style={styles.stat}>· {(stats.completionRate * 100).toFixed(0)}%</Text>
      )}
      {release && <Text style={styles.stat}>· releases {release}</Text>}
      {stats.positivePct != null && (
        <View style={styles.thumbStat}>
          <Text style={styles.stat}>· </Text>
          <Icon name="thumb-up" size={11} color="#666" />
          <Text style={styles.stat}> {(stats.positivePct * 100).toFixed(0)}%</Text>
        </View>
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  row: { flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap', gap: 6, marginTop: 6 },
  thumbStat: { flexDirection: 'row', alignItems: 'center' },
  chip: { flexDirection: 'row', alignItems: 'center', gap: 3, paddingHorizontal: 6, paddingVertical: 2, borderRadius: 5, backgroundColor: GOLD.light },
  chipText: { fontSize: 10, fontWeight: '700', color: GOLD.dark },
  trustedChip: { backgroundColor: GOLD.dark },
  trustedText: { fontSize: 10, fontWeight: '700', color: '#fff' },
  newChip: { backgroundColor: '#EEF2FF' },
  newChipText: { fontSize: 10, fontWeight: '700', color: '#3B82F6' },
  onlineDot: { width: 8, height: 8, borderRadius: 4, backgroundColor: '#22C55E' },
  stat: { fontSize: 11, color: '#666', fontWeight: '600' },
});

export default SellerTrustBadges;
