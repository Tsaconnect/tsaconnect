// screens/RewardProgramScreen.tsx
//
// The "Reward" drawer hub: Signup form | Onboarding form | Automated earnings.
//
// DISTINCT from screens/RewardsScreen.tsx (route /rewards), which is the TP
// ladder + on-chain MCGP claim dashboard. This screen is programme ENROLMENT.
// The third section deliberately links out to the existing Rewards and Cashback
// screens rather than restating mechanics that already ship.
import React, { useCallback, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useFocusEffect, type Href } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Badge,
  Button,
  Card,
  Icon,
  PressableScale,
  ScreenHeader,
  Spinner,
  Text,
} from '@/components/ui';
import {
  getMyRewardClaims,
  type MyRewardClaims,
  type RewardClaimStatus,
  type RewardFormState,
} from '@/services/rewardProgramApi';

// StatusPill's kinds are trade/ad/dispute only, so claim statuses map to Badge
// colours directly.
const STATUS_LABEL: Record<RewardClaimStatus, string> = {
  paid: 'Paid',
  approved: 'Approved',
  pending: 'Under review',
  rejected: 'Not approved',
};

const RewardProgramScreen = () => {
  const { colors, spacing, radius } = useTheme();

  const statusColor = (s: RewardClaimStatus) =>
    s === 'rejected' ? colors.danger : s === 'pending' ? colors.warning : colors.success;
  const statusBg = (s: RewardClaimStatus) =>
    s === 'rejected' ? colors.dangerBg : s === 'pending' ? colors.warningBg : colors.successBg;
  const [data, setData] = useState<MyRewardClaims | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    const r = await getMyRewardClaims();
    if (r.success && r.data) {
      setData(r.data);
      setError('');
    } else {
      setError(r.message || 'Could not load your reward status');
    }
    setLoading(false);
    setRefreshing(false);
  }, []);

  // Re-check on focus so returning from a form reflects the new submission.
  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  const formCard = (
    title: string,
    blurb: string,
    footnote: string,
    state: RewardFormState | undefined,
    route: string,
  ) => {
    const claim = state?.claim ?? null;
    const elig = state?.eligibility;
    const submitted = !!claim;
    // Already-submitted is not a blocker worth showing as a reason — the
    // submitted pill already says it.
    const blockers = (elig?.reasons ?? []).filter((r) => !r.toLowerCase().includes('already submitted'));

    return (
      <Card padding="lg" style={{ marginBottom: spacing.lg }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginBottom: spacing.sm }}>
          <Text variant="title3" weight="700">
            {title}
          </Text>
          {submitted && (
            <Badge
              label={STATUS_LABEL[claim!.status] ?? claim!.status}
              color={statusColor(claim!.status)}
              background={statusBg(claim!.status)}
              size="sm"
            />
          )}
        </View>

        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.md }}>
          {blurb}
        </Text>

        {submitted ? (
          <View style={{ gap: spacing.xs }}>
            <Text variant="bodySm" color="textMuted">
              You submitted this form. We&apos;ll be in touch once it&apos;s reviewed.
            </Text>
            {!!claim!.reviewNotes && (
              <Text variant="caption" color="textMuted">
                Note: {claim!.reviewNotes}
              </Text>
            )}
          </View>
        ) : (
          <>
            {blockers.length > 0 && (
              <View
                style={{
                  gap: spacing.xs,
                  padding: spacing.md,
                  borderRadius: radius.md,
                  backgroundColor: colors.surfaceAlt,
                  marginBottom: spacing.md,
                }}
              >
                {blockers.map((reason) => (
                  <View key={reason} style={{ flexDirection: 'row', gap: spacing.xs, alignItems: 'flex-start' }}>
                    <Icon name="closeCircle" size={14} color={colors.textMuted} />
                    <Text variant="caption" color="textMuted" style={{ flex: 1 }}>
                      {reason}
                    </Text>
                  </View>
                ))}
              </View>
            )}
            <Button
              title="Fill form"
              onPress={() => router.push(route as Href)}
              // Fail OPEN when eligibility is unknown (load failed): the server
              // re-checks on submit, so a network blip shouldn't permanently
              // disable the form with no way to retry.
              disabled={elig ? !elig.eligible : false}
            />
            <Text variant="caption" color="textMuted" style={{ marginTop: spacing.sm }}>
              {footnote}
            </Text>
          </>
        )}
      </Card>
    );
  };

  const linkRow = (label: string, sublabel: string, route: string) => (
    <PressableScale
      onPress={() => router.push(route as Href)}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        paddingVertical: spacing.md,
      }}
    >
      <View style={{ flex: 1 }}>
        <Text variant="bodyMed" weight="600">
          {label}
        </Text>
        <Text variant="caption" color="textMuted">
          {sublabel}
        </Text>
      </View>
      <Icon name="chevronRight" size={18} color={colors.textMuted} />
    </PressableScale>
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader title="Reward" fallbackRoute="/home" />
      {loading ? (
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner label="Loading rewards…" />
        </View>
      ) : (
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          refreshControl={
            <RefreshControl
              refreshing={refreshing}
              onRefresh={() => {
                setRefreshing(true);
                load();
              }}
              tintColor={colors.accent}
            />
          }
        >
          {!!error && (
            <Card padding="md" style={{ marginBottom: spacing.md }}>
              <Text variant="bodySm" color="danger" style={{ marginBottom: spacing.xs }}>
                {error}
              </Text>
              <PressableScale onPress={load} style={{ paddingVertical: spacing.xs }}>
                <Text variant="bodySm" weight="600" color="accent">
                  Tap to retry
                </Text>
              </PressableScale>
            </Card>
          )}

          {formCard(
            'Signup form',
            'A one-time reward for joining — paid as airtime or TradePoints.',
            'Ensure to have done your KYC to be eligible.',
            data?.signup,
            '/reward-program/signup',
          )}

          {formCard(
            'Onboarding form',
            'A one-time cash reward for bringing people onto TSA Connect.',
            `Ensure you onboard a minimum of ${data?.onboarding?.eligibility?.requiredReferrals ?? 10} users and also make a transaction to be eligible.`,
            data?.onboarding,
            '/reward-program/onboarding',
          )}

          <Card padding="lg">
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
              Automated earnings
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.sm }}>
              You earn continuously as you use the marketplace — instant cashback plus
              TradePoints on product and service transactions, and TradePoints on daily
              spending and trading across P2P, Instant Transfer, spot, All Swap and OTC.
            </Text>
            {linkRow('TradePoints & rewards', 'Your level, claimable rewards and TP history', '/rewards')}
            {linkRow('Cashback', 'What you earned back on transactions', '/cashback')}
            {linkRow('Referrals', 'Who you onboarded and TP earned from them', '/referrals')}
          </Card>
        </ScrollView>
      )}
    </SafeAreaView>
  );
};

export default RewardProgramScreen;
