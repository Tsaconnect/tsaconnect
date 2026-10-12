// app/p2p/dispute/[tradeId].tsx
//
// Self-serve dispute WIZARD. A guided, 3-step alternative to the inline
// dispute modal on the trade screen:
//
//   Step 1  Reason    — pick a structured reason (RadioGroup) + free-text when "other".
//   Step 2  Evidence  — optionally attach payment screenshots (PhotoPickerTile).
//   Step 3  Review    — confirm reason + evidence count; submit.
//
// Submit reuses the EXACT raise-dispute flow from app/p2p/trade/[tradeId].tsx:
//   raiseDispute (prepare) → wallet sign + broadcast → confirmRaiseDispute.
// The structured reason enum is mapped to a human-readable reason string that
// goes on the wire (the backend stores `reason` free-text); picked evidence
// URLs ride along as `evidenceUrls`.
//
// Gate: only a participant (buyer/seller) of a trade in pending_payment|paid
// may dispute — anything else routes to a denied/closed state.
import { router, useLocalSearchParams } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Wizard } from '@/components/ui/Wizard';
import { PhotoPickerTile } from '@/components/ui/PhotoPickerTile';
import {
  AccentCard,
  Card,
  EmptyState,
  Icon,
  Input,
  RadioGroup,
  SkeletonGroup,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  useToast,
} from '@/components/ui';
import { useTheme } from '@/theme';
import { useAuth } from '../../../AuthContext/AuthContext';
import { tradesApi, type P2PTrade } from '../../../services/p2p';
import { runP2PTx } from '../../../services/p2pTx';
import { getActiveWallet } from '../../../services/wallet';
import { safeProofUrl } from '../../../utils/payInto';
import { applyFollowUp } from '../../../utils/p2pFollowUp';
import { disputeFollowUp, disputeThrownFollowUp } from '../../../utils/p2pTradeView';

// Structured reasons. The `reason` string on the wire is free-text (the
// backend stores it verbatim and shows it to the admin), so each enum maps to
// a human sentence; "other" defers to the buyer's own words.
type DisputeReason = 'payment_not_received' | 'wrong_amount' | 'no_crypto_released' | 'other';

const REASON_OPTIONS: { value: DisputeReason; label: string; sublabel: string }[] = [
  {
    value: 'payment_not_received',
    label: "I didn't receive the payment",
    sublabel: 'The fiat transfer never arrived in your account.',
  },
  {
    value: 'wrong_amount',
    label: 'Wrong amount was paid',
    sublabel: 'The fiat amount sent does not match the trade.',
  },
  {
    value: 'no_crypto_released',
    label: "Crypto wasn't released",
    sublabel: "You paid, but the seller hasn't released the crypto.",
  },
  {
    value: 'other',
    label: 'Something else',
    sublabel: 'Describe the problem in your own words.',
  },
];

const REASON_SENTENCE: Record<Exclude<DisputeReason, 'other'>, string> = {
  payment_not_received: 'The fiat payment was never received.',
  wrong_amount: 'The wrong fiat amount was paid for this trade.',
  no_crypto_released: 'The fiat was paid but the crypto has not been released.',
};

const STEPS = [
  { key: 'reason', title: 'Reason' },
  { key: 'evidence', title: 'Evidence' },
  { key: 'review', title: 'Review' },
];

const DisputeWizardScreen: React.FC = () => {
  const { tradeId } = useLocalSearchParams<{ tradeId: string }>();
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { currentUser } = useAuth();
  const user = currentUser as { id?: string } | null;

  const [trade, setTrade] = useState<P2PTrade | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);

  const [step, setStep] = useState(0);
  const [reason, setReason] = useState<DisputeReason | null>(null);
  const [otherText, setOtherText] = useState('');
  const [evidence, setEvidence] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);

  // Load the trade so we can gate on role + status and show context. Same
  // endpoint the trade screen uses (tradesApi.get).
  const load = useCallback(async () => {
    if (!tradeId) return;
    setLoadError(false);
    const res = await tradesApi.get(String(tradeId));
    if (res.success && res.data) {
      setTrade(res.data);
    } else {
      setLoadError(true);
    }
    setLoading(false);
  }, [tradeId]);

  useEffect(() => {
    load();
  }, [load]);

  // Role detection mirrors the trade screen: buyer or seller, else not a party.
  const role: 'buyer' | 'seller' | null = useMemo(() => {
    if (!trade || !user) return null;
    if (trade.buyerId === user.id) return 'buyer';
    if (trade.sellerId === user.id) return 'seller';
    return null;
  }, [trade, user]);

  // A dispute can only be raised by a participant on a live, mid-flight trade.
  const canDispute =
    !!role && (trade?.status === 'pending_payment' || trade?.status === 'paid');

  // ─── Loading / gate states ────────────────────────────────────────────────

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ padding: spacing.lg }}>
          <SkeletonGroup shape="line" count={6} />
        </View>
      </SafeAreaView>
    );
  }

  if (loadError || !trade) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="warning"
            title="Couldn't load this trade"
            subtitle="Check your connection and try again."
            actionLabel="Retry"
            onAction={() => {
              setLoading(true);
              load();
            }}
          />
        </View>
      </SafeAreaView>
    );
  }

  if (!canDispute) {
    // Either not a party to the trade, or the trade is no longer in a state
    // where a dispute is meaningful (already terminal / disputed / resolved).
    const subtitle = !role
      ? "You're not a party to this trade."
      : 'This trade is no longer open to disputes.';
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="lock"
            title="Can't open a dispute"
            subtitle={subtitle}
            actionLabel="Go back"
            onAction={() => router.back()}
          />
        </View>
      </SafeAreaView>
    );
  }

  // ─── Wizard wiring ────────────────────────────────────────────────────────

  const wireReason = (): string => {
    if (reason === 'other') return otherText.trim();
    if (reason) return REASON_SENTENCE[reason];
    return '';
  };

  // Step 1: must pick a reason; if "other", the free-text must be non-empty.
  // Step 2: evidence is optional → always advanceable.
  // Step 3: review → submit gate is the same as a valid reason.
  const canAdvance = (() => {
    if (submitting) return false;
    if (step === 0) return reason !== null && (reason !== 'other' || otherText.trim().length > 0);
    if (step === 1) return true;
    return wireReason().length > 0;
  })();

  const onBack = () => setStep((s) => Math.max(0, s - 1));
  const onNext = () => setStep((s) => Math.min(STEPS.length - 1, s + 1));

  const leave = () => {
    // router.back() returns to the trade screen; fall back to a hard route
    // if there's no stack.
    if (router.canGoBack?.()) router.back();
    else router.replace(`/p2p/trade/${trade.id}` as never);
  };

  const onSubmit = async () => {
    if (submitting) return;
    const reasonStr = wireReason();
    if (!reasonStr) return;

    const safeEvidence = evidence.map(safeProofUrl);
    if (safeEvidence.some((u) => u === null)) {
      toast.error('Some evidence images are not secure (https) links. Remove them and add them again.');
      return;
    }
    const evidenceUrls = safeEvidence as string[];

    try {
      setSubmitting(true);

      const callerAddress = await getActiveWallet();
      if (!callerAddress) throw new Error('No wallet found');

      const r = await runP2PTx({
        chainId: trade.chainId,
        walletAddress: callerAddress,
        action: 'dispute',
        tradeId: trade.id,
        prepare: () => tradesApi.raiseDispute(trade.id, reasonStr, callerAddress, evidenceUrls),
        confirm: (h) => tradesApi.confirmRaiseDispute(trade.id, h),
        // A lost confirm: the trade screen shows "Confirming…" with a retry
        // instead of re-offering the dispute, until the trade shows disputed.
        stash: () => ({ mode: 'dispute', tradeId: trade.id, token: trade.token, amount: trade.tokenAmount }),
      });

      applyFollowUp(disputeFollowUp(r, trade.id), { toast, navigate: leave });
    } catch (err) {
      // Only reached before anything is broadcast (no wallet, sign/broadcast failure).
      applyFollowUp(disputeThrownFollowUp(err), { toast });
    } finally {
      setSubmitting(false);
    }
  };

  // ─── Step content ─────────────────────────────────────────────────────────

  const reasonLabel =
    reason === 'other'
      ? otherText.trim() || 'Other'
      : REASON_OPTIONS.find((o) => o.value === reason)?.label ?? '—';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Wizard
        steps={STEPS}
        current={step}
        onBack={onBack}
        onNext={onNext}
        canAdvance={canAdvance}
        onSubmit={onSubmit}
        submitting={submitting}
        submitLabel="Open dispute"
      >
        {step === 0 && (
          <View style={{ gap: spacing.lg }}>
            <View style={{ gap: spacing.xs }}>
              <Text variant="title3" weight="700">
                What went wrong?
              </Text>
              <Text variant="bodySm" color="textSecondary">
                Pick the closest reason. An admin reviews this to decide how the escrowed funds
                are handled.
              </Text>
            </View>

            <RadioGroup<DisputeReason>
              options={REASON_OPTIONS}
              value={reason}
              onChange={setReason}
            />

            {reason === 'other' && (
              <Input
                label="Describe the issue"
                placeholder="e.g. I sent ₦15,000 at 14:32 but the seller hasn't released after 25 minutes."
                value={otherText}
                onChangeText={setOtherText}
                multiline
                maxLength={1000}
                counter={1000}
                textAlignVertical="top"
                inputStyle={{ minHeight: 96 }}
              />
            )}
          </View>
        )}

        {step === 1 && (
          <View style={{ gap: spacing.lg }}>
            <View style={{ gap: spacing.xs }}>
              <Text variant="title3" weight="700">
                Add evidence
              </Text>
              <Text variant="bodySm" color="textSecondary">
                Optional, but it helps. Add screenshots of your payment — bank transfer receipts,
                confirmation pages, or chat history.
              </Text>
            </View>

            <Card>
              <PhotoPickerTile
                value={evidence}
                onChange={setEvidence}
                label="Screenshots of your payment"
              />
            </Card>
          </View>
        )}

        {step === 2 && (
          <View style={{ gap: spacing.lg }}>
            <View style={{ gap: spacing.xs }}>
              <Text variant="title3" weight="700">
                Review your dispute
              </Text>
              <Text variant="bodySm" color="textSecondary">
                Confirm the details below before submitting.
              </Text>
            </View>

            <Card>
              <SummaryCard>
                <SummaryRow label="Reason" value={reasonLabel} />
                <SummaryRow
                  label="Evidence"
                  value={evidence.length === 0 ? 'None attached' : `${evidence.length} screenshot${evidence.length === 1 ? '' : 's'}`}
                  valueColor={evidence.length === 0 ? 'textSecondary' : 'textPrimary'}
                />
                <SummaryRow label="Your role" value={role === 'buyer' ? 'Buyer' : 'Seller'} />
              </SummaryCard>
            </Card>

            {reason === 'other' && otherText.trim().length > 0 && (
              <Card>
                <Text variant="overline" color="textMuted">
                  YOUR DESCRIPTION
                </Text>
                <Text variant="body" style={{ marginTop: spacing.xs }}>
                  {otherText.trim()}
                </Text>
              </Card>
            )}

            <AccentCard railColor={colors.warning} padding="md">
              <View style={{ flexDirection: 'row', gap: spacing.md, alignItems: 'flex-start' }}>
                <Icon name="shield" size={20} color={colors.warning} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">
                    Funds stay frozen
                  </Text>
                  <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>
                    The escrowed crypto remains locked on chain while an admin reviews your dispute.
                    Neither side can move it until the dispute is resolved.
                  </Text>
                </View>
              </View>
            </AccentCard>

            {submitting && (
              <View style={{ alignItems: 'center', paddingVertical: spacing.sm }}>
                <Spinner label="Opening dispute…" inline />
              </View>
            )}
          </View>
        )}
      </Wizard>
    </SafeAreaView>
  );
};

export default DisputeWizardScreen;
