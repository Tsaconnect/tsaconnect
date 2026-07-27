// screens/CashbackScreen.tsx
//
// Cashback summary screen — what a user sees when they tap "Cashback".
//
// Two halves:
//   1. The user's ACTUAL cashback — total balance (claimable as MCGP) plus a
//      per-activity breakdown from GET /api/users/cashback-summary
//      (grouped by SourceType; exact totals, not page-limited). If that
//      endpoint isn't available on the connected backend yet, we fall back to
//      the cashback balance alone and still show the rules below.
//   2. The cashback RULES (Trade & Earn) — the exact per-activity rates, and
//      the note that every fee-generating action ALSO earns Trade Points
//      toward cash rewards + gifts at each level.
//
// Cashback (and TP) is paid as claimable MCGP — see TradeAndEarnScreen and
// RewardsScreen for the claim flow. Styling is 100% TSA Signature.

import React, { useCallback, useEffect, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme, formatFiat } from '@/theme';
import {
  AccentCard,
  AmountText,
  Card,
  Divider,
  EmptyState,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  Surface,
  Text,
} from '@/components/ui';
import type { IconName } from '@/components/ui';
import api from '../components/services/api';
import { RAILS, railLabel } from '@/constants/rails';

// ── Types ──────────────────────────────────────────────────────────────────

interface ActivityRow {
  sourceType: string;
  totalUsd: number;
  count: number;
}

// ── Activity label / icon mapping (SourceType → user-facing) ────────────────

// Both counterparty rails settle through the same P2P escrow, so cashback
// reports them as one line.
const PM_P2P_TRADE_LABEL = `${RAILS.p2p.name} & ${RAILS.pm.name} trades`;

const ACTIVITY_META: Record<string, { label: string; icon: IconName }> = {
  checkout: { label: 'Product purchases', icon: 'cart' },
  order: { label: 'Product purchases', icon: 'cart' },
  service_contact: { label: 'Service orders', icon: 'construction' },
  service: { label: 'Service orders', icon: 'construction' },
  p2p_release: { label: PM_P2P_TRADE_LABEL, icon: 'send' },
  p2p_trade: { label: PM_P2P_TRADE_LABEL, icon: 'send' },
  p2p: { label: PM_P2P_TRADE_LABEL, icon: 'send' },
  instant_transfer: { label: railLabel('it'), icon: 'bolt' },
};

function activityMeta(sourceType: string): { label: string; icon: IconName } {
  return (
    ACTIVITY_META[sourceType] || {
      label: sourceType.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase()),
      icon: 'wallet',
    }
  );
}

// ── The cashback rules (exactly as defined by the fee engine) ────────────────
// Rate strings mirror the per-feature split table in the backend
// (fee_distribution_service.go featureSplits) — kept in sync by review.

interface CashbackRule {
  icon: IconName;
  title: string;
  you: string;
  upline: string;
  extra?: string;
}

const CASHBACK_RULES: CashbackRule[] = [
  {
    icon: 'cart',
    title: 'Product purchases',
    you: '0.3% cashback on every product you buy',
    upline: 'Your direct upline also earns 0.3%',
  },
  {
    icon: 'construction',
    title: 'Service orders',
    you: '$0.01 cashback on every contact-fee payment',
    upline: 'Your direct upline also earns $0.01',
    extra: '+ $0.01 extra if you are the service provider',
  },
  {
    icon: 'send',
    title: PM_P2P_TRADE_LABEL,
    you: '0.15% cashback on every trade you complete',
    upline: 'Your direct upline also earns 0.15%',
  },
  {
    icon: 'bolt',
    title: railLabel('it'),
    you: '0.125% cashback on every transfer you complete',
    upline: 'Your direct upline also earns 0.125%',
  },
];

// ── Screen ───────────────────────────────────────────────────────────────────

const CashbackScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();

  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [cashbackBalance, setCashbackBalance] = useState(0);
  const [totalEarned, setTotalEarned] = useState(0);
  const [byActivity, setByActivity] = useState<ActivityRow[]>([]);

  const load = useCallback(async () => {
    // Prefer the grouped summary endpoint; gracefully fall back to the balance
    // alone if the connected backend doesn't expose it yet.
    const res = await api.getCashbackSummary();
    if (res.success && res.data) {
      setCashbackBalance(res.data.cashbackBalance || 0);
      setTotalEarned(res.data.totalEarnedUsd || 0);
      const rows: ActivityRow[] = (res.data.byActivity || []).filter(
        (r: ActivityRow) => r.totalUsd > 0,
      );
      setByActivity(rows);
      return;
    }
    // Fallback: balance only (rules below still render).
    const bal = await api.getCashbackBalance();
    if (bal.success && bal.data) {
      setCashbackBalance(bal.data.cashbackBalance || 0);
    }
    setByActivity([]);
  }, []);

  useEffect(() => {
    (async () => {
      setLoading(true);
      await load();
      setLoading(false);
    })();
  }, [load]);

  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await load();
    setRefreshing(false);
  }, [load]);

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading your cashback…" />
        </View>
      </SafeAreaView>
    );
  }

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
        {/* Header */}
        <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.lg }}>
          <IconButton
            icon="chevronLeft"
            onPress={() => router.back()}
            accessibilityLabel="Go back"
            style={{ marginLeft: -spacing.sm }}
          />
          <Text variant="title2" weight="700">
            Cashback
          </Text>
        </View>

        {/* ── Hero: total cashback ── */}
        <AccentCard padding="xl">
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start' }}>
            <View style={{ flex: 1 }}>
              <Text variant="bodySm" color="textSecondary">
                Cashback earned
              </Text>
              <AmountText
                value={cashbackBalance}
                currency="USD"
                variant="display"
                minimumFractionDigits={2}
                maximumFractionDigits={2}
                style={{ marginTop: spacing.xs }}
              />
              <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
                Paid as claimable MCGP — claim it from Rewards.
              </Text>
            </View>
            <View
              style={{
                width: 56,
                height: 56,
                borderRadius: radius.pill,
                backgroundColor: colors.successBg,
                justifyContent: 'center',
                alignItems: 'center',
              }}
            >
              <Icon name="wallet" size={28} color={colors.success} />
            </View>
          </View>
        </AccentCard>

        {/* ── Your earnings by activity ── */}
        <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
          Your earnings by activity
        </Text>
        <Card padding="lg">
          {byActivity.length === 0 ? (
            <EmptyState
              icon="gift"
              title="No cashback yet"
              subtitle="Complete a purchase, service order, P2P trade or transfer to start earning."
            />
          ) : (
            byActivity.map((a, i) => {
              const meta = activityMeta(a.sourceType);
              return (
                <View key={a.sourceType}>
                  <View
                    style={{
                      flexDirection: 'row',
                      alignItems: 'center',
                      paddingVertical: spacing.sm,
                    }}
                  >
                    <View
                      style={{
                        width: 36,
                        height: 36,
                        borderRadius: radius.pill,
                        backgroundColor: colors.successBg,
                        justifyContent: 'center',
                        alignItems: 'center',
                        marginRight: spacing.md,
                      }}
                    >
                      <Icon name={meta.icon} size={18} color={colors.success} />
                    </View>
                    <View style={{ flex: 1 }}>
                      <Text variant="bodyMed" weight="700" numberOfLines={1}>
                        {meta.label}
                      </Text>
                      <Text variant="caption" color="textMuted">
                        {a.count} {a.count === 1 ? 'reward' : 'rewards'}
                      </Text>
                    </View>
                    <AmountText
                      value={a.totalUsd}
                      currency="USD"
                      variant="bodyMed"
                      weight="700"
                      minimumFractionDigits={2}
                      maximumFractionDigits={2}
                    />
                  </View>
                  {i < byActivity.length - 1 && <Divider inset={52} />}
                </View>
              );
            })
          )}
        </Card>

        {/* ── How you earn cashback (the rules) ── */}
        <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.xs }}>
          How you earn cashback
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.md }}>
          Trade &amp; Earn — cashback rewards on every transaction you complete, carved from the
          platform fee (even on the smallest fees).
        </Text>
        <Surface elevation="sm" style={{ overflow: 'hidden' }}>
          {CASHBACK_RULES.map((rule, i) => (
            <View key={rule.title}>
              <View style={{ flexDirection: 'row', padding: spacing.lg }}>
                <View
                  style={{
                    width: 40,
                    height: 40,
                    borderRadius: radius.md,
                    backgroundColor: colors.accentMuted,
                    justifyContent: 'center',
                    alignItems: 'center',
                    marginRight: spacing.md,
                  }}
                >
                  <Icon name={rule.icon} size={20} color={colors.accent} />
                </View>
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    {rule.title}
                  </Text>
                  <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.xs, marginTop: 4 }}>
                    <Icon name="user" size={13} color={colors.success} />
                    <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                      {rule.you}
                    </Text>
                  </View>
                  <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.xs, marginTop: 2 }}>
                    <Icon name="user" size={13} color={colors.accent} />
                    <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                      {rule.upline}
                    </Text>
                  </View>
                  {!!rule.extra && (
                    <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.xs, marginTop: 2 }}>
                      <Icon name="star" size={13} color={colors.warning} />
                      <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                        {rule.extra}
                      </Text>
                    </View>
                  )}
                </View>
              </View>
              {i < CASHBACK_RULES.length - 1 && <Divider inset={72} />}
            </View>
          ))}
        </Surface>

        {/* ── TP note ── */}
        <Card padding="lg" style={{ marginTop: spacing.lg }}>
          <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.md }}>
            <View
              style={{
                width: 40,
                height: 40,
                borderRadius: radius.md,
                backgroundColor: colors.infoBg,
                justifyContent: 'center',
                alignItems: 'center',
              }}
            >
              <Icon name="star" size={20} color={colors.info} />
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700">
                You also earn Trade Points
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                Every transaction above — plus OTC swaps, Spot trades, AllSwap and Instant Payment —
                also earns you Trade Points (TP) toward cash rewards and gifts at each level you unlock.
              </Text>
            </View>
          </View>
        </Card>

        {/* ── Go to Rewards / TP ── */}
        <PressableScale onPress={() => router.push('/rewards')} style={{ marginTop: spacing.lg }}>
          <Card padding="lg">
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <View
                style={{
                  width: 44,
                  height: 44,
                  borderRadius: radius.md,
                  backgroundColor: colors.accentMuted,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginRight: spacing.md,
                }}
              >
                <Icon name="gift" size={22} color={colors.accent} />
              </View>
              <View style={{ flex: 1 }}>
                <Text variant="bodyMed" weight="700">
                  Rewards &amp; TP Levels
                </Text>
                <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                  View your TP balance, level progress and claim rewards
                </Text>
              </View>
              <Icon name="chevronRight" size={22} color={colors.textMuted} />
            </View>
          </Card>
        </PressableScale>

        {totalEarned > 0 && (
          <Text variant="caption" color="textMuted" style={{ textAlign: 'center', marginTop: spacing.lg }}>
            Lifetime cashback earned: {formatFiat(totalEarned, 'USD', { maximumFractionDigits: 2 })}
          </Text>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

export default CashbackScreen;
