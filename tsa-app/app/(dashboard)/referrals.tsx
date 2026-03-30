import React, { useCallback, useEffect, useState } from 'react';
import {
  View,
  Text,
  FlatList,
  TouchableOpacity,
  StyleSheet,
  ActivityIndicator,
  Alert,
  RefreshControl,
  Share,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import Icon from 'react-native-vector-icons/MaterialIcons';
import * as Clipboard from 'expo-clipboard';
import { router } from 'expo-router';
import api from '../../components/services/api';

const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  success: '#28A745',
};

interface Referral {
  id: string;
  name: string;
  username: string;
  createdAt: string;
  verificationStatus: string;
  tpContributed: number;
}

interface ReferralData {
  totalReferrals: number;
  verifiedReferrals: number;
  pendingReferrals: number;
  totalTPFromReferrals: number;
  referrals: Referral[];
}

const ReferralsScreen: React.FC = () => {
  const [data, setData] = useState<ReferralData | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [userProfile, setUserProfile] = useState<any>(null);

  const fetchData = async () => {
    try {
      const [referralsRes, profileRes] = await Promise.all([
        api.getReferrals(),
        api.getProfile(),
      ]);

      if (referralsRes.success && referralsRes.data) {
        setData(referralsRes.data);
      }
      if (profileRes.success && profileRes.data) {
        setUserProfile(profileRes.data);
      }
    } catch (error) {
      console.error('Error fetching referral data:', error);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  };

  useEffect(() => {
    fetchData();
  }, []);

  const onRefresh = useCallback(() => {
    setRefreshing(true);
    fetchData();
  }, []);

  const copyReferralCode = () => {
    const code = userProfile?.referralCode || userProfile?.username;
    if (!code) {
      Alert.alert('Error', 'No referral code available');
      return;
    }
    Clipboard.setStringAsync(code);
    Alert.alert('Copied!', 'Referral code copied to clipboard');
  };

  const shareReferralCode = async () => {
    const code = userProfile?.referralCode || userProfile?.username;
    if (!code) return;
    try {
      await Share.share({
        message: `Join TSA Connect using my referral code: ${code}`,
        title: 'TSA Connect Referral',
      });
    } catch (error) {
      Alert.alert('Error', 'Failed to share referral code');
    }
  };

  const formatDate = (dateStr: string) => {
    const date = new Date(dateStr);
    return date.toLocaleDateString('en-US', {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
    });
  };

  const getInitial = (name: string) => {
    return name ? name.charAt(0).toUpperCase() : '?';
  };

  const getStatusBadge = (status: string) => {
    if (status === 'verified') {
      return { icon: 'verified', color: GOLD_COLORS.success, label: 'Verified' };
    }
    if (status === 'in_review') {
      return { icon: 'hourglass-empty', color: '#F57C00', label: 'In Review' };
    }
    return { icon: 'schedule', color: '#999', label: 'Pending' };
  };

  const renderReferralItem = ({ item }: { item: Referral }) => {
    const badge = getStatusBadge(item.verificationStatus);
    return (
      <View style={styles.referralItem}>
        <View style={styles.avatarCircle}>
          <Text style={styles.avatarText}>{getInitial(item.name)}</Text>
        </View>
        <View style={styles.referralInfo}>
          <Text style={styles.referralName}>{item.name}</Text>
          <Text style={styles.referralUsername}>@{item.username}</Text>
          <Text style={styles.referralDate}>Joined {formatDate(item.createdAt)}</Text>
        </View>
        <View style={styles.referralRight}>
          <View style={styles.statusBadge}>
            <Icon name={badge.icon} size={14} color={badge.color} />
            <Text style={[styles.statusText, { color: badge.color }]}>{badge.label}</Text>
          </View>
          <Text style={styles.tpContributed}>{item.tpContributed.toFixed(4)} TP</Text>
        </View>
      </View>
    );
  };

  const renderEmpty = () => (
    <View style={styles.emptyContainer}>
      <Icon name="people-outline" size={64} color="#ccc" />
      <Text style={styles.emptyTitle}>No referrals yet</Text>
      <Text style={styles.emptySubtitle}>
        Share your referral code to start earning!
      </Text>
    </View>
  );

  if (loading) {
    return (
      <SafeAreaView style={styles.container}>
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={GOLD_COLORS.dark} />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={styles.container}>
      <FlatList
        data={data?.referrals || []}
        renderItem={renderReferralItem}
        keyExtractor={(item) => item.id}
        ListEmptyComponent={renderEmpty}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} />
        }
        ListHeaderComponent={
          <View>
            {/* Header */}
            <View style={styles.header}>
              <TouchableOpacity onPress={() => router.back()} style={styles.backButton}>
                <Icon name="arrow-back" size={24} color="#000" />
              </TouchableOpacity>
              <Text style={styles.headerTitle}>My Referrals</Text>
            </View>

            {/* Summary Cards */}
            <View style={styles.summaryRow}>
              <View style={styles.summaryCard}>
                <Text style={styles.summaryValue}>{data?.totalReferrals || 0}</Text>
                <Text style={styles.summaryLabel}>Total</Text>
              </View>
              <View style={styles.summaryCard}>
                <Text style={styles.summaryValue}>{data?.verifiedReferrals || 0}</Text>
                <Text style={styles.summaryLabel}>Verified</Text>
              </View>
              <View style={styles.summaryCard}>
                <Text style={styles.summaryValue}>
                  {(data?.totalTPFromReferrals || 0).toFixed(2)}
                </Text>
                <Text style={styles.summaryLabel}>TP Earned</Text>
              </View>
            </View>

            {/* Referral Code Section */}
            <View style={styles.codeSection}>
              <Text style={styles.codeLabel}>Your Referral Code</Text>
              <Text style={styles.codeValue}>
                {userProfile?.referralCode || userProfile?.username || 'N/A'}
              </Text>
              <View style={styles.codeActions}>
                <TouchableOpacity style={styles.codeButton} onPress={copyReferralCode}>
                  <Icon name="content-copy" size={18} color={GOLD_COLORS.dark} />
                  <Text style={styles.codeButtonText}>Copy</Text>
                </TouchableOpacity>
                <TouchableOpacity style={styles.codeButton} onPress={shareReferralCode}>
                  <Icon name="share" size={18} color={GOLD_COLORS.dark} />
                  <Text style={styles.codeButtonText}>Share</Text>
                </TouchableOpacity>
              </View>
              <Text style={styles.codeHint}>
                Share your username with others to earn TP when they trade
              </Text>
            </View>

            {/* Referral List Header */}
            {(data?.referrals?.length || 0) > 0 && (
              <Text style={styles.listHeader}>Referred Users</Text>
            )}
          </View>
        }
        contentContainerStyle={styles.listContent}
      />
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
  },
  backButton: {
    padding: 8,
    marginRight: 12,
  },
  headerTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000',
  },
  summaryRow: {
    flexDirection: 'row',
    paddingHorizontal: 16,
    gap: 10,
    marginBottom: 16,
  },
  summaryCard: {
    flex: 1,
    backgroundColor: '#fff',
    borderRadius: 12,
    padding: 16,
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.05,
    shadowRadius: 3,
    elevation: 2,
  },
  summaryValue: {
    fontSize: 22,
    fontWeight: '900',
    color: '#000',
    marginBottom: 4,
  },
  summaryLabel: {
    fontSize: 12,
    color: '#666',
  },
  codeSection: {
    backgroundColor: '#fff',
    marginHorizontal: 16,
    borderRadius: 12,
    padding: 20,
    alignItems: 'center',
    marginBottom: 16,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.05,
    shadowRadius: 3,
    elevation: 2,
  },
  codeLabel: {
    fontSize: 14,
    color: '#666',
    marginBottom: 8,
  },
  codeValue: {
    fontSize: 24,
    fontWeight: '800',
    color: GOLD_COLORS.dark,
    marginBottom: 12,
  },
  codeActions: {
    flexDirection: 'row',
    gap: 12,
    marginBottom: 12,
  },
  codeButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 16,
    paddingVertical: 10,
    borderRadius: 8,
    gap: 6,
  },
  codeButtonText: {
    fontSize: 14,
    fontWeight: '600',
    color: GOLD_COLORS.dark,
  },
  codeHint: {
    fontSize: 12,
    color: '#999',
    textAlign: 'center',
  },
  listHeader: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000',
    paddingHorizontal: 16,
    marginBottom: 12,
  },
  listContent: {
    paddingBottom: 32,
  },
  referralItem: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff',
    marginHorizontal: 16,
    marginBottom: 8,
    borderRadius: 12,
    padding: 14,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.03,
    shadowRadius: 2,
    elevation: 1,
  },
  avatarCircle: {
    width: 44,
    height: 44,
    borderRadius: 22,
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  avatarText: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000',
  },
  referralInfo: {
    flex: 1,
  },
  referralName: {
    fontSize: 15,
    fontWeight: '700',
    color: '#000',
  },
  referralUsername: {
    fontSize: 13,
    color: '#666',
  },
  referralDate: {
    fontSize: 12,
    color: '#999',
    marginTop: 2,
  },
  referralRight: {
    alignItems: 'flex-end',
  },
  statusBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    marginBottom: 4,
  },
  statusText: {
    fontSize: 11,
    fontWeight: '600',
  },
  tpContributed: {
    fontSize: 13,
    fontWeight: '700',
    color: GOLD_COLORS.dark,
  },
  emptyContainer: {
    alignItems: 'center',
    paddingVertical: 48,
  },
  emptyTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: '#333',
    marginTop: 16,
  },
  emptySubtitle: {
    fontSize: 14,
    color: '#999',
    marginTop: 8,
    textAlign: 'center',
    paddingHorizontal: 48,
  },
});

export default ReferralsScreen;
