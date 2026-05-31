// app/p2p/seller/[sellerId].tsx
//
// Merchant public profile — trust badges, completion/feedback stats, and the
// seller's active ads. Restyled onto the TSA Signature design system; data
// fetching (sellersApi.profile), state, effect deps, and navigation routes are
// unchanged — this is a visual/UX restyle only.
import { router, useLocalSearchParams } from 'expo-router';
import React, { useEffect, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { sellersApi, type MerchantProfile } from '../../../services/p2p';
import SellerTrustBadges from '../../../components/p2p/SellerTrustBadges';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Avatar,
  Card,
  EmptyState,
  Icon,
  PressableScale,
  SkeletonGroup,
  Text,
} from '@/components/ui';

const SellerProfileScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
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
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ padding: spacing.lg }}>
          <SkeletonGroup shape="line" count={6} />
        </View>
      </SafeAreaView>
    );
  }

  if (!profile) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="user"
            title="Seller not found"
            subtitle="This merchant profile isn't available right now."
            actionLabel="Go back"
            onAction={() => router.back()}
          />
        </View>
      </SafeAreaView>
    );
  }

  const up = profile.recentRatings.filter((r) => r.value === 1).length;
  const down = profile.recentRatings.filter((r) => r.value === -1).length;
  const sellerLabel = `seller-${profile.userId.slice(0, 4)}`;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <Text variant="title1" weight="800">
          Merchant
        </Text>

        {/* Identity + trust signals */}
        <AccentCard style={{ marginTop: spacing.md }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
            <Avatar name={sellerLabel} size={48} online={profile.stats.online} />
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700" numberOfLines={1}>
                @{sellerLabel}
              </Text>
              <Text variant="caption" color="textMuted">
                {profile.stats.totalOrders} {profile.stats.totalOrders === 1 ? 'order' : 'orders'}
              </Text>
            </View>
          </View>
          <SellerTrustBadges stats={profile.stats} />
        </AccentCard>

        {/* Recent feedback */}
        <Text variant="overline" color="textMuted" style={{ marginTop: spacing.lg, marginBottom: spacing.sm }}>
          RECENT FEEDBACK
        </Text>
        <Card>
          {profile.recentRatings.length === 0 ? (
            <Text variant="bodySm" color="textSecondary">
              No feedback yet.
            </Text>
          ) : (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.lg }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="thumbUp" size={16} color={colors.success} />
                <Text variant="bodyMed" weight="700" numeric>
                  {up}
                </Text>
              </View>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="thumbDown" size={16} color={colors.danger} />
                <Text variant="bodyMed" weight="700" numeric>
                  {down}
                </Text>
              </View>
            </View>
          )}
        </Card>

        {/* Active ads */}
        <Text variant="overline" color="textMuted" style={{ marginTop: spacing.lg, marginBottom: spacing.sm }}>
          ACTIVE ADS ({profile.activeAds.length})
        </Text>
        {profile.activeAds.length === 0 ? (
          <Card>
            <Text variant="bodySm" color="textSecondary">
              No active ads.
            </Text>
          </Card>
        ) : (
          <View style={{ gap: spacing.sm }}>
            {profile.activeAds.map((ad) => (
              <PressableScale key={ad.id} onPress={() => router.push(`/p2p/ad/${ad.id}` as any)}>
                <Card>
                  <View
                    style={{
                      flexDirection: 'row',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: spacing.sm,
                    }}
                  >
                    <Text variant="bodyMed" weight="700">
                      {ad.token}
                    </Text>
                    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                      <AmountText value={ad.rate} currency={ad.fiatCurrency} variant="bodyMed" weight="700" />
                      <Icon name="chevronRight" size={18} color={colors.textMuted} />
                    </View>
                  </View>
                </Card>
              </PressableScale>
            ))}
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

export default SellerProfileScreen;
