// app/screens/TradeAndEarnScreen.tsx
//
// Trade & Earn landing — Trade-Points balance + referral hero, earning-method
// shortcuts, earning details, TP explainer, and the "how it works" steps plus
// the trade / merchant CTAs. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives). All data fetching, state,
// effects, navigation routes, clipboard/share behavior, and number formatting
// are unchanged — this is a visual/structural restyle only.
import { router } from 'expo-router';
import React, { useEffect, useState } from 'react';
import { View, ScrollView, Share, Alert, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Card,
  Divider,
  Icon,
  IconButton,
  PressableScale,
  Surface,
  Text,
} from '@/components/ui';
import api from '../components/services/api';

const TradeAndEarnScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const [referralCode, setReferralCode] = useState('');
  const [tpBalance, setTpBalance] = useState(0);
  const [cashbackBalance, setCashbackBalance] = useState(0);
  const [totalReferrals, setTotalReferrals] = useState(0);
  const [refreshing, setRefreshing] = useState(false);

  const fetchData = async () => {
    try {
      const [profileRes, tpRes, referralsRes, cashbackRes] = await Promise.all([
        api.getProfile(),
        api.getTPBalance(),
        api.getReferrals(),
        api.getCashbackBalance(),
      ]);

      if (profileRes.success && profileRes.data) {
        setReferralCode(profileRes.data.referralCode || profileRes.data.username || '');
      }
      if (tpRes.success && tpRes.data) {
        setTpBalance(tpRes.data.tpBalance || 0);
      }
      if (referralsRes.success && referralsRes.data) {
        setTotalReferrals(referralsRes.data.totalReferrals || 0);
      }
      if (cashbackRes.success && cashbackRes.data) {
        setCashbackBalance(cashbackRes.data.cashbackBalance || 0);
      }
    } catch (error) {
      console.error('Error fetching trade & earn data:', error);
    } finally {
      setRefreshing(false);
    }
  };

  useEffect(() => {
    fetchData();
  }, []);

  const onRefresh = () => {
    setRefreshing(true);
    fetchData();
  };

  const copyReferralCode = () => {
    if (!referralCode) {
      Alert.alert('Error', 'No referral code available');
      return;
    }
    Clipboard.setStringAsync(referralCode);
    Alert.alert('Copied!', 'Referral code copied to clipboard');
  };

  const shareReferralCode = async () => {
    if (!referralCode) return;
    try {
      await Share.share({
        message: `Join TSA Connect using my referral code: ${referralCode}`,
        title: 'TSA Connect Referral',
      });
    } catch (error) {
      Alert.alert('Error', 'Failed to share referral code');
    }
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        showsVerticalScrollIndicator={false}
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={onRefresh}
            tintColor={colors.accent}
            colors={[colors.accent]}
          />
        }
      >
        {/* Hero Card — TP Balance + Referral Code */}
        <AccentCard padding="xl">
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start' }}>
            <View>
              <Text variant="bodySm" color="textSecondary">
                Trade Points
              </Text>
              <AmountText
                value={tpBalance}
                variant="display"
                minimumFractionDigits={2}
                maximumFractionDigits={2}
                style={{ marginTop: spacing.xs }}
              />
              <Text variant="caption" weight="700" color="accent" style={{ marginTop: 2 }}>
                TP
              </Text>
            </View>
            <View
              style={{
                width: 56,
                height: 56,
                borderRadius: radius.pill,
                backgroundColor: colors.accentMuted,
                justifyContent: 'center',
                alignItems: 'center',
              }}
            >
              <Icon name="star" size={32} color={colors.accent} />
            </View>
          </View>

          <Divider spacing="lg" />

          <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
            <View style={{ flex: 1 }}>
              <Text variant="caption" color="textMuted">
                Your Referral Code
              </Text>
              <Text variant="title3" weight="700" style={{ marginTop: 2 }}>
                {referralCode || 'N/A'}
              </Text>
            </View>
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              <IconButton
                icon="copy"
                size={18}
                color={colors.accent}
                accessibilityLabel="Copy referral code"
                onPress={copyReferralCode}
              />
              <IconButton
                icon="send"
                size={18}
                color={colors.accent}
                accessibilityLabel="Share referral code"
                onPress={shareReferralCode}
              />
            </View>
          </View>
        </AccentCard>

        {/* Stats Row */}
        <Card padding="lg" style={{ marginTop: spacing.lg, marginBottom: spacing['2xl'] }}>
          <View style={{ flexDirection: 'row' }}>
            <PressableScale
              style={{ flex: 1, alignItems: 'center' }}
              onPress={() => router.push('/marketplace')}
            >
              <View
                style={{
                  width: 32,
                  height: 32,
                  borderRadius: radius.pill,
                  backgroundColor: colors.warningBg,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginBottom: spacing.sm,
                }}
              >
                <Icon name="card" size={16} color={colors.warning} />
              </View>
              <AmountText
                value={cashbackBalance}
                currency="USD"
                variant="bodyMed"
                weight="700"
                minimumFractionDigits={2}
                maximumFractionDigits={2}
              />
              <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                Cashback
              </Text>
            </PressableScale>

            <PressableScale
              style={{ flex: 1, alignItems: 'center' }}
              onPress={() => router.push('/referrals')}
            >
              <View
                style={{
                  width: 32,
                  height: 32,
                  borderRadius: radius.pill,
                  backgroundColor: colors.successBg,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginBottom: spacing.sm,
                }}
              >
                <Icon name="user" size={16} color={colors.success} />
              </View>
              <Text variant="bodyMed" weight="700">
                {totalReferrals}
              </Text>
              <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                Referrals
              </Text>
            </PressableScale>

            <View style={{ flex: 1, alignItems: 'center' }}>
              <View
                style={{
                  width: 32,
                  height: 32,
                  borderRadius: radius.pill,
                  backgroundColor: colors.infoBg,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginBottom: spacing.sm,
                }}
              >
                <Icon name="star" size={16} color={colors.info} />
              </View>
              <AmountText
                value={tpBalance}
                variant="bodyMed"
                weight="700"
                minimumFractionDigits={2}
                maximumFractionDigits={2}
              />
              <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                TP Earned
              </Text>
            </View>
          </View>
        </Card>

        {/* Earning Methods */}
        <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
          Ways to Earn
        </Text>

        <Surface elevation="sm" style={{ marginBottom: spacing.lg, overflow: 'hidden' }}>
          <PressableScale
            onPress={() => router.push('/marketplace')}
            style={{ flexDirection: 'row', alignItems: 'center', padding: spacing.lg }}
          >
            <View
              style={{
                width: 44,
                height: 44,
                borderRadius: radius.md,
                backgroundColor: colors.warningBg,
                justifyContent: 'center',
                alignItems: 'center',
                marginRight: spacing.md,
              }}
            >
              <Icon name="card" size={22} color={colors.warning} />
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700">
                Cashback
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Earn instant dollar cashback on purchases &amp; services
              </Text>
            </View>
            <Icon name="chevronRight" size={22} color={colors.textMuted} />
          </PressableScale>

          <Divider inset={74} />

          <PressableScale
            onPress={() => router.push('/referrals')}
            style={{ flexDirection: 'row', alignItems: 'center', padding: spacing.lg }}
          >
            <View
              style={{
                width: 44,
                height: 44,
                borderRadius: radius.md,
                backgroundColor: colors.successBg,
                justifyContent: 'center',
                alignItems: 'center',
                marginRight: spacing.md,
              }}
            >
              <Icon name="user" size={22} color={colors.success} />
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700">
                Trade Points
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Earn TP from fees across 10 generations of your network
              </Text>
            </View>
            <Icon name="chevronRight" size={22} color={colors.textMuted} />
          </PressableScale>

          <Divider inset={74} />

          <PressableScale
            onPress={() => router.push('/easyswap')}
            style={{ flexDirection: 'row', alignItems: 'center', padding: spacing.lg }}
          >
            <View
              style={{
                width: 44,
                height: 44,
                borderRadius: radius.md,
                backgroundColor: colors.infoBg,
                justifyContent: 'center',
                alignItems: 'center',
                marginRight: spacing.md,
              }}
            >
              <Icon name="swap" size={22} color={colors.info} />
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700">
                Swap &amp; Instant Pay
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Earn TP &amp; cashback on every swap and instant transfer
              </Text>
            </View>
            <Icon name="chevronRight" size={22} color={colors.textMuted} />
          </PressableScale>
        </Surface>

        {/* Earning Details */}
        <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
          Earning Details
        </Text>

        <Card padding="xl" style={{ marginBottom: spacing['2xl'] }}>
          <View style={{ paddingVertical: spacing.xs }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.sm }}>
              <Icon name="card" size={18} color={colors.warning} />
              <Text variant="bodyMed" weight="700">
                Cashback (Trade &amp; Earn)
              </Text>
            </View>
            <Text variant="bodySm" color="textSecondary">
              Paid to you and your direct upline in dollars instantly for every purchase, service order, swap, and Instant Pay — debited from system fees even if it&apos;s $0.00001.
            </Text>
          </View>

          <Divider spacing="lg" />

          <View style={{ paddingVertical: spacing.xs }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.sm }}>
              <Icon name="user" size={18} color={colors.success} />
              <Text variant="bodyMed" weight="700">
                Trade Points (TP) Network
              </Text>
            </View>
            <Text variant="bodySm" color="textSecondary">
              System fees from every transaction are split across 10 generations. You earn 35.4% of fees directly, your direct upline earns 17.7%, and it cascades down to 0.4% at the 10th generation.
            </Text>
          </View>

          <Divider spacing="lg" />

          <View style={{ paddingVertical: spacing.xs }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.sm }}>
              <Icon name="swap" size={18} color={colors.info} />
              <Text variant="bodyMed" weight="700">
                Swap &amp; Instant Pay TP
              </Text>
            </View>
            <Text variant="bodySm" color="textSecondary">
              Every swap (MCGP/USDC) and Instant Pay transfer now earns TP from system fees. TP is credited instantly and visible on your dashboard.
            </Text>
          </View>
        </Card>

        {/* TP Conversion Rates */}
        <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
          How TP Is Earned
        </Text>

        <Card padding="xl" style={{ marginBottom: spacing['2xl'] }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, marginBottom: spacing.md }}>
            <Icon name="info" size={16} color={colors.accent} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              TP is earned from system fees, not trade volume
            </Text>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, marginBottom: spacing.md }}>
            <Icon name="user" size={16} color={colors.accent} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              Split across you + 10 generations of upline
            </Text>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, marginBottom: spacing.md }}>
            <Icon name="bolt" size={16} color={colors.accent} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              Credited instantly after each transaction
            </Text>
          </View>
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'flex-start',
              backgroundColor: colors.accentMuted,
              padding: spacing.md,
              borderRadius: radius.sm,
              marginTop: spacing.xs,
              gap: spacing.sm,
            }}
          >
            <Icon name="info" size={14} color={colors.accent} />
            <Text variant="caption" color="accent" style={{ flex: 1 }}>
              Every time you trade, swap, purchase products/services, or send via Instant Pay, the system fees are split into TP and distributed to you and your upline network.
            </Text>
          </View>
        </Card>

        {/* How It Works */}
        <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
          How It Works
        </Text>

        <Card padding="xl" style={{ marginBottom: spacing['2xl'] }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.lg }}>
            <View
              style={{
                width: 32,
                height: 32,
                borderRadius: radius.pill,
                backgroundColor: colors.accent,
                justifyContent: 'center',
                alignItems: 'center',
                marginRight: spacing.md,
              }}
            >
              <Text variant="bodySm" weight="800" style={{ color: colors.onAccent }}>
                1
              </Text>
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodySm" weight="700">
                Start Trading
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Buy or sell products/services in the marketplace
              </Text>
            </View>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.lg }}>
            <View
              style={{
                width: 32,
                height: 32,
                borderRadius: radius.pill,
                backgroundColor: colors.accent,
                justifyContent: 'center',
                alignItems: 'center',
                marginRight: spacing.md,
              }}
            >
              <Text variant="bodySm" weight="800" style={{ color: colors.onAccent }}>
                2
              </Text>
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodySm" weight="700">
                Earn Rewards
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Automatically earn cashback, TPs, and commissions
              </Text>
            </View>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.lg }}>
            <View
              style={{
                width: 32,
                height: 32,
                borderRadius: radius.pill,
                backgroundColor: colors.accent,
                justifyContent: 'center',
                alignItems: 'center',
                marginRight: spacing.md,
              }}
            >
              <Text variant="bodySm" weight="800" style={{ color: colors.onAccent }}>
                3
              </Text>
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodySm" weight="700">
                Invite &amp; Earn More
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Share your referral code to earn from your network
              </Text>
            </View>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center' }}>
            <View
              style={{
                width: 32,
                height: 32,
                borderRadius: radius.pill,
                backgroundColor: colors.accent,
                justifyContent: 'center',
                alignItems: 'center',
                marginRight: spacing.md,
              }}
            >
              <Text variant="bodySm" weight="800" style={{ color: colors.onAccent }}>
                4
              </Text>
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodySm" weight="700">
                Join P2P
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Become a merchant for additional commission
              </Text>
            </View>
          </View>
        </Card>

        {/* CTA */}
        <PressableScale
          onPress={() => router.push('/marketplace')}
          style={{
            backgroundColor: colors.accent,
            flexDirection: 'row',
            alignItems: 'center',
            justifyContent: 'center',
            borderRadius: radius.md,
            paddingVertical: spacing.lg,
            marginBottom: spacing.md,
            gap: spacing.sm,
          }}
        >
          <Icon name="cart" size={22} color={colors.onAccent} />
          <Text variant="bodyMed" weight="700" style={{ color: colors.onAccent }}>
            Start Trading Now
          </Text>
        </PressableScale>

        <PressableScale
          onPress={() => router.push('/services')}
          style={{
            backgroundColor: colors.surface,
            flexDirection: 'row',
            alignItems: 'center',
            justifyContent: 'center',
            borderRadius: radius.md,
            paddingVertical: spacing.lg,
            gap: spacing.sm,
            borderWidth: 1.5,
            borderColor: colors.accent,
          }}
        >
          <Icon name="storefront" size={22} color={colors.accent} />
          <Text variant="bodyMed" weight="700" color="accent">
            Become a P2P Merchant
          </Text>
        </PressableScale>
      </ScrollView>
    </SafeAreaView>
  );
};

export default TradeAndEarnScreen;
