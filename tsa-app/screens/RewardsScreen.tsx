// screens/RewardsScreen.tsx
//
// SP8 — first-class TP / Rewards dashboard (the "TP-levels" screen).
//
// Data: GET /api/rewards/summary (rewardsApi.summary) is the single source of
// truth for the hero, the fixed 15-level ladder, the per-generation breakdown,
// the global-pool share, and the claimable MCGP. GET /api/rewards/prepare-claim
// (rewardsApi.prepareClaim) returns the proof + a backend-prepared unsigned
// claim(account, cumulative, proof) tx; the app completes (nonce/gas), signs,
// and broadcasts it with the user's wallet (services/rewardsClaim.ts), then
// refreshes the summary so the new claimed amount shows.
//
// TP earning history reuses the existing /users/tp-earnings endpoint
// (api.getTPEarnings) — the summary endpoint intentionally returns aggregates,
// not a ledger.
//
// Claim states handled: claimable / nothing-to-claim / pending-next-epoch
// (root not yet published) / ineligible (KYC) / no-gas / on-chain error.
//
// Styling is 100% TSA Signature (@/theme tokens + @/components/ui primitives);
// no hand-rolled colors/fonts. The P2P screens are the reference.

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View, ScrollView, RefreshControl, Linking } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme, formatFiat, formatNumber } from '@/theme';
import {
  AccentCard,
  AmountText,
  Badge,
  Button,
  Card,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  IconButton,
  Spinner,
  SummaryRow,
  Text,
  useToast,
} from '@/components/ui';
import type { IconName } from '@/components/ui';
import api, {
  rewardsApi,
  type LadderLevel,
  type LadderStatus,
  type PrizeDueItem,
  type RewardSummary,
} from '../components/services/api';
import { getChainByChainId } from '../constants/chains';
import {
  broadcastRewardClaim,
  ClaimWouldRevertError,
  WrongWalletError,
} from '../services/rewardsClaim';

// ── Local types ──────────────────────────────────────────────────────────────

interface TPEarningRow {
  id: string;
  tpEarned: number;
  sourceType: string;
  generation: number;
  feeAmountUsd: number;
  createdAt: string;
}

/** UX state machine for the claim CTA. */
type ClaimState =
  | 'loading' // resolving claim eligibility
  | 'claimable' // unsignedTx present — show Claim button
  | 'nothing' // included:false, no pending — nothing owed
  | 'pending_epoch' // root not yet published on-chain — check back shortly
  | 'ineligible' // user not eligible (e.g. KYC) — show why + CTA
  | 'error'; // prepare-claim failed (network)

// ── Helpers ──────────────────────────────────────────────────────────────────

const STATUS_VISUAL: Record<
  LadderStatus,
  { label: string; icon: IconName }
> = {
  earned: { label: 'Earned', icon: 'checkCircle' },
  in_progress: { label: 'In Progress', icon: 'bolt' },
  locked: { label: 'Locked', icon: 'lock' },
};

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
}

/** Human label for a TP earning's source. */
function sourceLabel(sourceType: string): string {
  const map: Record<string, string> = {
    order: 'Marketplace order',
    checkout: 'Marketplace order',
    service: 'Service order',
    swap: 'Swap',
    instant_pay: 'Instant Pay',
    p2p: 'P2P trade',
    p2p_trade: 'P2P trade',
    global_pool: 'Global pool',
    level_reward: 'Level reward',
  };
  return map[sourceType] || sourceType.replace(/_/g, ' ');
}

/** "gen 0 (you)" / "gen N upline". */
function genLabel(generation: number): string {
  if (generation <= 0) return 'You (gen 0)';
  return `Gen ${generation} upline`;
}

/** "0x1234…abcd" — compact address for claim-destination copy. */
function shortAddr(addr: string): string {
  if (!addr || addr.length < 10) return addr;
  return `${addr.slice(0, 6)}…${addr.slice(-4)}`;
}

// ── Sub-components ────────────────────────────────────────────────────────────

/** A grouped number followed by a " TP" unit (and optional "+" sign), since
 *  AmountText has no prefix/suffix slots. Keeps tabular alignment via numeric. */
const TpAmount: React.FC<{
  value: number;
  weight?: '600' | '700' | '800';
  color?: 'textPrimary' | 'textSecondary' | 'success' | 'accent';
  signed?: boolean;
  maxFrac?: number;
}> = ({ value, weight = '700', color = 'textPrimary', signed = false, maxFrac = 4 }) => (
  <Text variant="bodySm" weight={weight} color={color} numeric>
    {signed ? '+' : ''}
    {formatNumber(value, { maximumFractionDigits: maxFrac })} TP
  </Text>
);

const ProgressBar: React.FC<{ progress: number }> = ({ progress }) => {
  const { colors, radius } = useTheme();
  const pct = Math.max(0, Math.min(1, progress));
  return (
    <View
      style={{
        height: 8,
        borderRadius: radius.pill,
        backgroundColor: colors.surfaceAlt,
        overflow: 'hidden',
      }}
    >
      <View
        style={{
          width: `${pct * 100}%`,
          height: '100%',
          borderRadius: radius.pill,
          backgroundColor: colors.accent,
        }}
      />
    </View>
  );
};

const LadderRow: React.FC<{ level: LadderLevel; isLast: boolean }> = ({ level, isLast }) => {
  const { colors, spacing, radius } = useTheme();
  const visual = STATUS_VISUAL[level.status];
  const tone =
    level.status === 'earned'
      ? { fg: colors.success, bg: colors.successBg }
      : level.status === 'in_progress'
        ? { fg: colors.accent, bg: colors.accentMuted }
        : { fg: colors.textMuted, bg: colors.surfaceAlt };

  return (
    <View>
      <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.md }}>
        {/* Level badge */}
        <View
          style={{
            width: 36,
            height: 36,
            borderRadius: radius.pill,
            backgroundColor: tone.bg,
            justifyContent: 'center',
            alignItems: 'center',
            marginRight: spacing.md,
          }}
        >
          <Text variant="bodySm" weight="800" style={{ color: tone.fg }}>
            {level.level}
          </Text>
        </View>

        {/* Threshold + prize */}
        <View style={{ flex: 1 }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
            <Text variant="bodyMed" weight="700">
              {formatNumber(level.thresholdTP)} TP
            </Text>
            {!!level.prize && (
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: 2 }}>
                <Icon name="gift" size={13} color={colors.accent} />
                <Text variant="caption" weight="700" color="accent">
                  {level.prize}
                </Text>
              </View>
            )}
          </View>
          <Text variant="caption" color="textSecondary" style={{ marginTop: 2 }}>
            Reward {formatFiat(level.rewardUsd, 'USD', { maximumFractionDigits: level.rewardUsd < 1 ? 4 : 2 })}
          </Text>
        </View>

        {/* Status */}
        <Badge label={visual.label} icon={visual.icon} color={tone.fg} background={tone.bg} size="sm" />
      </View>
      {!isLast && <Divider inset={52} />}
    </View>
  );
};

const PrizeDueRow: React.FC<{ item: PrizeDueItem }> = ({ item }) => {
  const { colors, spacing } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.sm,
        paddingVertical: spacing.sm,
      }}
    >
      <Icon name="gift" size={18} color={colors.accent} />
      <View style={{ flex: 1 }}>
        <Text variant="bodySm" weight="700">
          {item.prize}
        </Text>
        <Text variant="caption" color="textMuted">
          Level {item.level}
        </Text>
      </View>
      <Badge
        label={item.status === 'pending' ? 'Pending' : item.status}
        icon="clock"
        color={colors.warning}
        background={colors.warningBg}
        size="sm"
      />
    </View>
  );
};

// ── Screen ────────────────────────────────────────────────────────────────────

const RewardsScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const toast = useToast();

  const [summary, setSummary] = useState<RewardSummary | null>(null);
  const [earnings, setEarnings] = useState<TPEarningRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadError, setLoadError] = useState(false);

  // Claim state
  const [claimState, setClaimState] = useState<ClaimState>('loading');
  const [claimReason, setClaimReason] = useState<string>('');
  const [claimTx, setClaimTx] = useState<RewardSummaryClaimTx | null>(null);
  const [claiming, setClaiming] = useState(false);
  const [claimStage, setClaimStage] = useState('');
  const [lastClaimHash, setLastClaimHash] = useState<{ hash: string; chainId: number } | null>(null);

  // ── Data loading ────────────────────────────────────────────────────────────

  const loadSummary = useCallback(async () => {
    const [summaryRes, earningsRes] = await Promise.all([
      rewardsApi.summary(),
      api.getTPEarnings(1, 15),
    ]);

    if (summaryRes.success && summaryRes.data) {
      setSummary(summaryRes.data);
      setLoadError(false);
    } else {
      setLoadError(true);
    }

    if (earningsRes.success && earningsRes.data?.earnings) {
      setEarnings(earningsRes.data.earnings as TPEarningRow[]);
    }
  }, []);

  // Resolve the claim CTA state from prepare-claim + the summary's claimable.
  const loadClaimState = useCallback(async (claimableMcgp: number) => {
    setClaimState('loading');
    const res = await rewardsApi.prepareClaim();

    if (!res.success || !res.data) {
      // Network/transport error resolving the claim — distinct from "nothing".
      setClaimState('error');
      setClaimReason(res.message || 'Could not check your claim right now.');
      return;
    }

    const { claimData, unsignedTx } = res.data;

    if (unsignedTx) {
      // Carry the registered claim account so the broadcast helper signs + pays
      // gas with it (the leaf is bound to it regardless of msg.sender).
      setClaimTx({ ...unsignedTx, account: claimData?.account });
      setClaimState('claimable');
      return;
    }

    // No unsigned tx — figure out *why* so the UX can be specific.
    setClaimTx(null);
    if (claimData?.pendingNextEpoch) {
      setClaimState('pending_epoch');
      return;
    }
    // Backend accrues TP for everyone, but the on-chain claim requires the user
    // to be in a published merkle root. If they have ~zero accrued, there's
    // simply nothing to claim.
    if (claimableMcgp <= 0) {
      setClaimState('nothing');
      return;
    }

    // They HAVE accrued claimable MCGP but aren't in a live root. Two reasons:
    //   1) unverified KYC — a published root excludes flagged/unverified users,
    //      so tell them to complete verification (actionable).
    //   2) the epoch with their reward just hasn't been published yet — tell
    //      them to check back shortly.
    // Distinguish via the existing KYC-status endpoint (best-effort; on any
    // failure we fall back to the softer pending-epoch copy).
    try {
      const kyc = await api.getKYCStatus();
      const status = kyc.success ? kyc.data?.verificationStatus : undefined;
      if (status && status !== 'verified' && status !== 'approved') {
        setClaimReason(
          'Your rewards are accruing, but on-chain claims require a verified identity. Complete verification to claim.',
        );
        setClaimState('ineligible');
        return;
      }
    } catch {
      // ignore — fall through to pending-epoch
    }
    setClaimState('pending_epoch');
  }, []);

  const refreshAll = useCallback(async () => {
    const summaryRes = await rewardsApi.summary();
    const earningsRes = await api.getTPEarnings(1, 15);
    if (summaryRes.success && summaryRes.data) {
      setSummary(summaryRes.data);
      setLoadError(false);
      await loadClaimState(summaryRes.data.claimableMcgp);
    } else {
      setLoadError(true);
    }
    if (earningsRes.success && earningsRes.data?.earnings) {
      setEarnings(earningsRes.data.earnings as TPEarningRow[]);
    }
  }, [loadClaimState]);

  useEffect(() => {
    (async () => {
      setLoading(true);
      await loadSummary();
      setLoading(false);
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Once the summary is in, resolve the claim state from its claimable figure.
  useEffect(() => {
    if (summary) {
      loadClaimState(summary.claimableMcgp);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [summary?.claimableMcgp]);

  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await refreshAll();
    setRefreshing(false);
  }, [refreshAll]);

  // ── Claim action ──────────────────────────────────────────────────────────

  const handleClaim = useCallback(async () => {
    if (!claimTx || claiming) return;
    setClaiming(true);
    setClaimStage('');
    try {
      const result = await broadcastRewardClaim(
        claimTx,
        // Pay gas + sign with the registered claim account, not the active wallet.
        claimTx.account,
        (stage) => setClaimStage(stage),
        (title, message) => {
          // Gas guard fired — surface and bail (result will be null).
          toast.error(`${title}: ${message}`);
        },
      );

      if (!result) {
        // Gas guard returned false — user needs to fund their wallet first.
        return;
      }

      setLastClaimHash({ hash: result.txHash, chainId: Number(claimTx.chainId) });
      // Be precise: the MCGP is paid to the registered claim account. Only say
      // "your wallet" when that account is the user's active wallet.
      toast.success(
        claimTx.account
          ? `Reward claimed — MCGP is on its way to ${shortAddr(claimTx.account)}.`
          : 'Reward claimed — MCGP is on its way to your wallet.',
      );
      // Refresh so claimable/claimed reflect the new on-chain state.
      await refreshAll();
    } catch (err: any) {
      // Wrong wallet — the claim pays the registered account; tell the user to
      // switch to it (no broadcast happened).
      if (err instanceof WrongWalletError) {
        toast.error(err.message);
        return;
      }
      // estimateGas reverted — nothing left to claim. Do NOT broadcast a
      // guaranteed-revert tx; refresh so the CTA reflects the true state.
      if (err instanceof ClaimWouldRevertError) {
        toast.error('Nothing left to claim — this reward was already claimed.');
        await refreshAll();
        return;
      }
      const msg = String(err?.message || err);
      // Map a few common chain errors to friendlier copy.
      if (/insufficient funds/i.test(msg)) {
        toast.error('Not enough gas to claim. Fund your wallet and try again.');
      } else if (/no active wallet|no private key/i.test(msg)) {
        toast.error('Set up your wallet first to claim rewards.');
      } else if (/bad proof|already claimed|nothing/i.test(msg)) {
        toast.error('This reward was already claimed or is no longer available.');
        await refreshAll();
      } else {
        toast.error(msg);
      }
    } finally {
      setClaiming(false);
      setClaimStage('');
    }
  }, [claimTx, claiming, toast, refreshAll]);

  const openTxExplorer = useCallback(() => {
    if (!lastClaimHash) return;
    const chain = getChainByChainId(lastClaimHash.chainId);
    if (!chain) return;
    Linking.openURL(`${chain.explorerUrl}/tx/${lastClaimHash.hash}`).catch(() =>
      toast.error('Could not open the explorer'),
    );
  }, [lastClaimHash, toast]);

  // ── Derived ───────────────────────────────────────────────────────────────

  const atTop = useMemo(
    () => !!summary && summary.nextLevelThresholdTP === 0,
    [summary],
  );

  const nonZeroGenerations = useMemo(
    () => (summary?.perGeneration || []).filter((g) => g.earnedTp > 0),
    [summary],
  );

  // ── Render: loading / error ─────────────────────────────────────────────────

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading your rewards…" />
        </View>
      </SafeAreaView>
    );
  }

  if (loadError || !summary) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flexDirection: 'row', alignItems: 'center', padding: spacing.lg }}>
          <IconButton
            icon="chevronLeft"
            onPress={() => router.back()}
            accessibilityLabel="Go back"
            style={{ marginLeft: -spacing.sm }}
          />
          <Text variant="title2" weight="700">
            Rewards
          </Text>
        </View>
        <View style={{ flex: 1, justifyContent: 'center', padding: spacing.lg }}>
          <ErrorState
            title="Couldn't load rewards"
            subtitle="Check your connection and try again."
            onRetry={async () => {
              setLoading(true);
              await loadSummary();
              setLoading(false);
            }}
          />
        </View>
      </SafeAreaView>
    );
  }

  // ── Render: main ────────────────────────────────────────────────────────────

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
            Rewards & TP Levels
          </Text>
        </View>

        {/* ── Hero ── */}
        <AccentCard padding="xl">
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start' }}>
            <View style={{ flex: 1 }}>
              <Text variant="bodySm" color="textSecondary">
                Trade Points
              </Text>
              <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: spacing.xs }}>
                <AmountText
                  value={summary.tpBalance}
                  variant="display"
                  minimumFractionDigits={0}
                  maximumFractionDigits={2}
                  style={{ marginTop: spacing.xs }}
                />
                <Text variant="title3" weight="700" color="accent">
                  TP
                </Text>
              </View>
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
              <Icon name="star" size={30} color={colors.accent} />
            </View>
          </View>

          <Divider spacing="lg" />

          {/* Level + progress */}
          <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginBottom: spacing.sm }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
              <Icon name="trendingUp" size={16} color={colors.accent} />
              <Text variant="bodyMed" weight="700">
                Level {summary.currentLevel}
              </Text>
            </View>
            <Text variant="caption" color="textSecondary">
              {atTop
                ? 'Top level reached'
                : `Next: ${formatNumber(summary.nextLevelThresholdTP)} TP`}
            </Text>
          </View>
          <ProgressBar progress={summary.progressToNext} />
          <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
            {atTop
              ? 'You have reached the highest TP level.'
              : `${Math.round(summary.progressToNext * 100)}% to Level ${summary.currentLevel + 1}`}
          </Text>
        </AccentCard>

        {/* ── Reward earned (MCGP + claim) ── */}
        <Card padding="xl" style={{ marginTop: spacing.lg }}>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start' }}>
            <View>
              <Text variant="bodySm" color="textSecondary">
                Reward earned
              </Text>
              <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: spacing.xs, marginTop: spacing.xs }}>
                <AmountText
                  value={summary.claimableMcgp}
                  variant="title1"
                  minimumFractionDigits={0}
                  maximumFractionDigits={4}
                />
                <Text variant="bodyMed" weight="700" color="accent">
                  MCGP
                </Text>
              </View>
            </View>
            <Icon name="wallet" size={24} color={colors.accent} />
          </View>

          {summary.totalClaimedMcgp > 0 && (
            <>
              <Divider spacing="md" />
              <SummaryRow
                label="Already claimed"
                value={`${formatNumber(summary.totalClaimedMcgp, { maximumFractionDigits: 4 })} MCGP`}
              />
            </>
          )}

          <View style={{ marginTop: spacing.lg }}>
            {renderClaimCta()}
          </View>

          {lastClaimHash && (
            <Button
              title="View transaction"
              leftIcon="arrowRight"
              variant="ghost"
              size="sm"
              onPress={openTxExplorer}
              style={{ marginTop: spacing.sm }}
            />
          )}
        </Card>

        {/* ── Prizes due ── */}
        {summary.prizesDue.length > 0 && (
          <Card padding="lg" style={{ marginTop: spacing.lg }}>
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.xs }}>
              Prizes to be awarded
            </Text>
            {summary.prizesDue.map((p, i) => (
              <React.Fragment key={`${p.level}-${i}`}>
                <PrizeDueRow item={p} />
                {i < summary.prizesDue.length - 1 && <Divider />}
              </React.Fragment>
            ))}
          </Card>
        )}

        {/* ── 15-level ladder ── */}
        <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
          TP Levels
        </Text>
        <Card padding="lg">
          {summary.levels.map((lvl, i) => (
            <LadderRow key={lvl.level} level={lvl} isLast={i === summary.levels.length - 1} />
          ))}
        </Card>

        {/* ── Per-generation breakdown ── */}
        <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.xs }}>
          Earnings by Generation
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.md }}>
          TP you have earned from your own activity (gen 0) and 10 generations of downline.
        </Text>
        <Card padding="lg">
          {nonZeroGenerations.length === 0 ? (
            <EmptyState
              icon="user"
              title="No generation earnings yet"
              subtitle="Earn TP from your own trades and your network's activity."
            />
          ) : (
            summary.perGeneration.map((g, i) => (
              <View key={g.generation}>
                <View
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    paddingVertical: spacing.sm,
                    opacity: g.earnedTp > 0 ? 1 : 0.45,
                  }}
                >
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                    <View
                      style={{
                        width: 28,
                        height: 28,
                        borderRadius: radius.pill,
                        backgroundColor: g.generation === 0 ? colors.accentMuted : colors.surfaceAlt,
                        justifyContent: 'center',
                        alignItems: 'center',
                      }}
                    >
                      <Text variant="caption" weight="800" color={g.generation === 0 ? 'accent' : 'textSecondary'}>
                        {g.generation}
                      </Text>
                    </View>
                    <Text variant="bodySm" weight={g.generation === 0 ? '700' : '600'}>
                      {genLabel(g.generation)}
                    </Text>
                  </View>
                  <TpAmount value={g.earnedTp} color={g.earnedTp > 0 ? 'textPrimary' : 'textSecondary'} />
                </View>
                {i < summary.perGeneration.length - 1 && <Divider inset={36} />}
              </View>
            ))
          )}
        </Card>

        {/* ── Global pool share ── */}
        <Card padding="lg" style={{ marginTop: spacing.lg }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
            <View
              style={{
                width: 44,
                height: 44,
                borderRadius: radius.md,
                backgroundColor: colors.infoBg,
                justifyContent: 'center',
                alignItems: 'center',
              }}
            >
              <Icon name="pool" size={22} color={colors.info} />
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700">
                Global pool share
              </Text>
              <Text variant="caption" color="textSecondary">
                TP from the platform-wide reward pool
              </Text>
            </View>
            <TpAmount value={summary.globalPoolShareTp} weight="700" />
          </View>
        </Card>

        {/* ── TP earning history ── */}
        <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
          TP Earning History
        </Text>
        <Card padding="lg">
          {earnings.length === 0 ? (
            <EmptyState
              icon="list"
              title="No TP earned yet"
              subtitle="Your TP credits will appear here as you trade."
            />
          ) : (
            earnings.map((e, i) => (
              <View key={e.id}>
                <View
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    paddingVertical: spacing.sm,
                  }}
                >
                  <View style={{ flex: 1, marginRight: spacing.md }}>
                    <Text variant="bodySm" weight="700" numberOfLines={1}>
                      {sourceLabel(e.sourceType)}
                    </Text>
                    <Text variant="caption" color="textMuted">
                      {genLabel(e.generation)} · {formatDate(e.createdAt)}
                    </Text>
                  </View>
                  <TpAmount value={e.tpEarned} color="success" signed />
                </View>
                {i < earnings.length - 1 && <Divider />}
              </View>
            ))
          )}
        </Card>
      </ScrollView>
    </SafeAreaView>
  );

  // ── Claim CTA renderer (uses closure over state) ─────────────────────────────

  function renderClaimCta() {
    switch (claimState) {
      case 'loading':
        return (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Spinner size="small" />
            <Text variant="bodySm" color="textSecondary">
              Checking your claim…
            </Text>
          </View>
        );

      case 'claimable':
        return (
          <Button
            title={claiming ? claimStage || 'Working…' : 'Claim MCGP'}
            leftIcon="wallet"
            variant="primary"
            loading={claiming}
            onPress={handleClaim}
          />
        );

      case 'nothing':
        return (
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              gap: spacing.sm,
              backgroundColor: colors.surfaceAlt,
              borderRadius: radius.sm,
              padding: spacing.md,
            }}
          >
            <Icon name="info" size={16} color={colors.textMuted} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              Nothing to claim yet. Keep trading to earn claimable MCGP rewards.
            </Text>
          </View>
        );

      case 'pending_epoch':
        return (
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'flex-start',
              gap: spacing.sm,
              backgroundColor: colors.warningBg,
              borderRadius: radius.sm,
              padding: spacing.md,
            }}
          >
            <Icon name="clock" size={16} color={colors.warning} />
            <View style={{ flex: 1 }}>
              <Text variant="bodySm" weight="700" style={{ color: colors.warning }}>
                Reward pending
              </Text>
              <Text variant="caption" color="textSecondary" style={{ marginTop: 2 }}>
                Your reward is being finalized on-chain. Check back shortly to claim.
              </Text>
            </View>
          </View>
        );

      case 'ineligible':
        return (
          <View
            style={{
              backgroundColor: colors.dangerBg,
              borderRadius: radius.sm,
              padding: spacing.md,
            }}
          >
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.sm }}>
              <Icon name="shield" size={16} color={colors.danger} />
              <Text variant="bodySm" weight="700" style={{ color: colors.danger }}>
                Verification required
              </Text>
            </View>
            <Text variant="caption" color="textSecondary" style={{ marginBottom: spacing.md }}>
              {claimReason || 'Complete identity verification to be eligible to claim rewards.'}
            </Text>
            <Button
              title="Complete KYC"
              leftIcon="shield"
              variant="secondary"
              size="sm"
              onPress={() => router.push('/profile/kyc')}
            />
          </View>
        );

      case 'error':
      default:
        return (
          <View
            style={{
              backgroundColor: colors.surfaceAlt,
              borderRadius: radius.sm,
              padding: spacing.md,
            }}
          >
            <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.sm }}>
              {claimReason || 'Could not check your claim right now.'}
            </Text>
            <Button
              title="Retry"
              leftIcon="refresh"
              variant="secondary"
              size="sm"
              onPress={() => summary && loadClaimState(summary.claimableMcgp)}
            />
          </View>
        );
    }
  }
};

// Local alias for the prepared tx we hold in state (subset of RewardUnsignedTx)
// plus the registered claim `account` — the wallet the MCGP is paid to and that
// must sign + pay gas (claimData.account). We thread it through so the broadcast
// helper resolves gas/nonce/signing from it rather than the active wallet.
type RewardSummaryClaimTx = {
  to: string;
  value: string;
  data: string;
  chainId: string;
  /** Registered wallet the claim pays + must be signed by. */
  account?: string;
};

export default RewardsScreen;
