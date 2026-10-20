// app/(dashboard)/referrals.tsx
//
// Referrals / invite — shows the user's referral code (copy + share), referral
// stats (total / verified / TP earned), and the list of referred users with their
// verification status and TP contribution. Restyled onto the TSA Signature design
// system (theme tokens + @/components/ui primitives).
//
// Visual/structural restyle ONLY: the stats fetch (api.getReferrals + api.getProfile),
// referral-code resolution, clipboard copy, and native Share flow are preserved
// verbatim.
import React, { useCallback, useEffect, useState } from 'react';
import { View, FlatList, Alert, RefreshControl } from 'react-native';
import { referralCodeOf, shareReferralCode } from '@/utils/referralShare';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import api from '../../components/services/api';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Avatar,
  Badge,
  Button,
  Card,
  Divider,
  EmptyState,
  Spinner,
  Text,
} from '@/components/ui';

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
  const { colors, spacing } = useTheme();
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

  const handleShare = async () => {
    const code = referralCodeOf(userProfile);
    if (!code) return;
    try {
      await shareReferralCode(code);
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

  const getStatusBadge = (status: string) => {
    if (status === 'verified') {
      return { icon: 'verified' as const, color: colors.success, bg: colors.successBg, label: 'Verified' };
    }
    if (status === 'in_review') {
      return { icon: 'clock' as const, color: colors.warning, bg: colors.warningBg, label: 'In Review' };
    }
    return { icon: 'clock' as const, color: colors.textMuted, bg: colors.surfaceAlt, label: 'Pending' };
  };

  const referralCode = userProfile?.referralCode || userProfile?.username || 'N/A';

  const renderReferralItem = ({ item }: { item: Referral }) => {
    const badge = getStatusBadge(item.verificationStatus);
    return (
      <Card padding="md" style={{ marginBottom: spacing.sm }}>
        <View style={{ flexDirection: 'row', alignItems: 'center' }}>
          <Avatar name={item.name} size={44} />
          <View style={{ flex: 1, marginLeft: spacing.md }}>
            <Text variant="bodyMed" weight="700" numberOfLines={1}>
              {item.name}
            </Text>
            <Text variant="caption" color="textSecondary" numberOfLines={1}>
              @{item.username}
            </Text>
            <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
              Joined {formatDate(item.createdAt)}
            </Text>
          </View>
          <View style={{ alignItems: 'flex-end', gap: spacing.xs }}>
            <Badge label={badge.label} icon={badge.icon} color={badge.color} background={badge.bg} size="sm" />
            <Text variant="bodySm" weight="700" color="accent" numeric>
              {item.tpContributed.toFixed(4)} TP
            </Text>
          </View>
        </View>
      </Card>
    );
  };

  const renderEmpty = () => (
    <EmptyState
      icon="user"
      title="No referrals yet"
      subtitle="Share your referral code to start earning!"
    />
  );

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading referrals..." />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <FlatList
        data={data?.referrals || []}
        renderItem={renderReferralItem}
        keyExtractor={(item) => item.id}
        ListEmptyComponent={renderEmpty}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.accent} />
        }
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        ListHeaderComponent={
          <View>

            {/* Summary stats */}
            <View style={{ flexDirection: 'row', gap: spacing.sm, marginBottom: spacing.lg }}>
              <Card padding="md" style={{ flex: 1, alignItems: 'center' }}>
                <Text variant="title1" weight="700" numeric>
                  {data?.totalReferrals || 0}
                </Text>
                <Text variant="caption" color="textSecondary">
                  Total
                </Text>
              </Card>
              <Card padding="md" style={{ flex: 1, alignItems: 'center' }}>
                <Text variant="title1" weight="700" numeric>
                  {data?.verifiedReferrals || 0}
                </Text>
                <Text variant="caption" color="textSecondary">
                  Verified
                </Text>
              </Card>
              <Card padding="md" style={{ flex: 1, alignItems: 'center' }}>
                <AmountText
                  value={data?.totalTPFromReferrals || 0}
                  variant="title1"
                  minimumFractionDigits={2}
                  maximumFractionDigits={2}
                />
                <Text variant="caption" color="textSecondary">
                  TP Earned
                </Text>
              </Card>
            </View>

            {/* Referral code */}
            <AccentCard padding="xl" style={{ marginBottom: spacing.lg }}>
              <Text variant="caption" color="textSecondary" align="center">
                Your Referral Code
              </Text>
              <Text
                variant="title1"
                weight="700"
                color="accent"
                align="center"
                numeric
                style={{ marginTop: spacing.xs, marginBottom: spacing.md }}
              >
                {referralCode}
              </Text>
              <View style={{ flexDirection: 'row', gap: spacing.md }}>
                <Button
                  title="Copy"
                  leftIcon="copy"
                  variant="secondary"
                  size="sm"
                  onPress={copyReferralCode}
                  style={{ flex: 1 }}
                />
                <Button
                  title="Share"
                  leftIcon="send"
                  variant="secondary"
                  size="sm"
                  onPress={handleShare}
                  style={{ flex: 1 }}
                />
              </View>
              <Divider spacing="md" />
              <Text variant="caption" color="textMuted" align="center">
                Share your username with others to earn TP when they trade
              </Text>
            </AccentCard>

            {/* Referral list header */}
            {(data?.referrals?.length || 0) > 0 && (
              <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
                Referred Users
              </Text>
            )}
          </View>
        }
      />
    </SafeAreaView>
  );
};

export default ReferralsScreen;
