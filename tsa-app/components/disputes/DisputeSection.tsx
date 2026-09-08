// components/disputes/DisputeSection.tsx
//
// The dispute affordance, in one place, for every order screen that has one:
// a button while nothing is filed, a status row once something is, and the
// sheet in between.
//
// It lives here rather than in each order screen because the flow is identical
// whatever was ordered — only the subject differs — and three copies would
// drift the moment one of them gained a field the others did not.
import React, { useCallback, useEffect, useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Button, Card, Input, PhotoPickerTile, Sheet, Text } from '@/components/ui';
import {
  disputeAffordance,
  disputeStatusLabel,
  fileDispute,
  getDisputeForSubject,
  isDisputeOpen,
  type Dispute,
  type DisputeLookup,
  type DisputeSubjectType,
} from '@/services/disputesApi';

/** Matches the server's cap — no point letting someone attach a fifth image
 *  only to be refused after four uploads have already gone to Cloudinary. */
const MAX_EVIDENCE = 4;
const MAX_REASON = 1000;

export interface DisputeSectionProps {
  subjectType: DisputeSubjectType;
  subjectId: string;
  /**
   * Whether this order is far enough along to dispute. The server decides for
   * real; this only keeps the button off screens where it would always fail —
   * an order still awaiting payment wants Cancel, not a dispute.
   */
  disputable: boolean;
  /** Label for the button. Defaults to the neutral wording. */
  title?: string;
  /** Prompt above the reason box, so each order type can ask for what helps. */
  prompt?: string;
}

export const DisputeSection: React.FC<DisputeSectionProps> = ({
  subjectType,
  subjectId,
  disputable,
  title = 'Report a problem',
  prompt = 'Tell us what went wrong. Attach a receipt or screenshot if you have one — it is the fastest way for us to chase this up.',
}) => {
  const { colors, spacing } = useTheme();
  const [dispute, setDispute] = useState<Dispute | null>(null);
  const [lookup, setLookup] = useState<DisputeLookup>('pending');
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState('');
  const [evidence, setEvidence] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!subjectId) return;
    let cancelled = false;
    getDisputeForSubject(subjectType, subjectId).then((res) => {
      if (cancelled) return;
      if (!res.success) {
        // The form is still offered — see disputeAffordance for why hiding it
        // was the worse failure. A duplicate is caught by the server.
        setLookup('failed');
        return;
      }
      setDispute(res.data ?? null);
      setLookup(res.data ? 'found' : 'none');
    });
    return () => {
      cancelled = true;
    };
  }, [subjectType, subjectId]);

  const submit = useCallback(async () => {
    const text = reason.trim();
    if (!text) {
      setError('Please tell us what went wrong.');
      return;
    }
    setSubmitting(true);
    setError(null);
    const res = await fileDispute({
      subjectType,
      subjectId,
      reason: text,
      ...(evidence.length > 0 ? { evidenceUrls: evidence } : {}),
    });
    setSubmitting(false);
    if (!res.success || !res.data) {
      setError(res.message ?? 'Could not open your dispute. Please try again.');
      return;
    }
    setDispute(res.data);
    setLookup('found');
    setOpen(false);
    setReason('');
    setEvidence([]);
  }, [reason, evidence, subjectType, subjectId]);

  const affordance = disputeAffordance({ lookup, disputable });

  if (affordance === 'hidden') return null;

  if (affordance === 'show-dispute' && dispute) {
    return (
      <Card style={{ borderWidth: 1, borderColor: colors.border }}>
        <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
          Your report
        </Text>
        <Text
          variant="bodyMed"
          weight="700"
          style={{
            color: isDisputeOpen(dispute.status) ? colors.warning : colors.textPrimary,
            marginTop: spacing.xs,
          }}
        >
          {disputeStatusLabel(dispute.status)}
        </Text>
        <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
          {dispute.reason}
        </Text>
        {/* The outcome is the whole point of having filed — show it, rather
            than making the user infer it from a status word. */}
        {!!dispute.resolution && (
          <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.sm }}>
            {dispute.resolution}
          </Text>
        )}
      </Card>
    );
  }

  return (
    <>
      <Button title={title} variant="ghost" onPress={() => setOpen(true)} fullWidth />

      <Sheet
        visible={open}
        onClose={() => setOpen(false)}
        title={title}
        scrollable
        footer={
          <Button
            title="Submit report"
            onPress={submit}
            loading={submitting}
            disabled={submitting}
            fullWidth
          />
        }
      >
        <View style={{ gap: spacing.md }}>
          <Text variant="bodySm" style={{ color: colors.textSecondary }}>
            {prompt}
          </Text>
          <Input
            label="What happened?"
            value={reason}
            onChangeText={(v: string) => setReason(v.slice(0, MAX_REASON))}
            placeholder="I transferred ₦50,000 but nothing arrived…"
            multiline
            numberOfLines={4}
          />
          <PhotoPickerTile
            label="Evidence (optional)"
            value={evidence}
            onChange={setEvidence}
            max={MAX_EVIDENCE}
          />
          {error && (
            <Text variant="bodySm" style={{ color: colors.danger }}>
              {error}
            </Text>
          )}
        </View>
      </Sheet>
    </>
  );
};
