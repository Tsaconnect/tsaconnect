// app/bills/order/[id].tsx
//
// Bill transaction detail — the receipt for one order, with Share (native
// share sheet, plain-text receipt) and Report (one complaint per order,
// reviewed by an admin; its status and resolution show here once filed).
import React, { useCallback, useState } from 'react';
import { RefreshControl, ScrollView, Share, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  disputeStatusLabel,
  fileDispute,
  getDisputeForSubject,
  isDisputeOpen,
  type Dispute,
} from '@/services/disputesApi';
import {
  Badge,
  Button,
  Card,
  Divider,
  Input,
  PhotoPickerTile,
  RadioGroup,
  Sheet,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  useToast,
} from '@/components/ui';
import {
  getBillOrder,
  type BillOrder,
} from '@/services/billsApi';

const CATEGORY_LABEL: Record<string, string> = {
  airtime: 'Airtime',
  data: 'Data',
  electricity: 'Electricity',
  cabletv: 'Cable TV',
  betting: 'Betting',
  epin: 'Exam PIN',
};

type Tone = 'success' | 'warning' | 'danger' | 'neutral';

function statusMeta(status: BillOrder['status']): { label: string; tone: Tone } {
  switch (status) {
    case 'delivered':
      return { label: 'Delivered', tone: 'success' };
    case 'quoted':
      return { label: 'Not paid', tone: 'neutral' };
    case 'vending':
    case 'paid':
    case 'paying':
      return { label: 'Processing', tone: 'warning' };
    case 'refunded':
      return { label: 'Refunded', tone: 'neutral' };
    case 'vend_failed':
    case 'failed':
      return { label: 'Failed', tone: 'danger' };
    case 'expired':
      return { label: 'Expired', tone: 'neutral' };
    default:
      return { label: 'Pending', tone: 'warning' };
  }
}

// Money moved, so there is something to complain about. Mirrors the backend's
// reportable set — unpaid/expired quotes are excluded.
const REPORTABLE: BillOrder['status'][] = [
  'paying',
  'paid',
  'vending',
  'delivered',
  'vend_failed',
  'refunded',
  'failed',
];

type ReportReason = 'not_received' | 'partial' | 'wrong_recipient' | 'other';

const REASON_OPTIONS: { value: ReportReason; label: string; sublabel?: string }[] = [
  { value: 'not_received', label: 'Nothing arrived', sublabel: 'I paid but did not receive value' },
  { value: 'partial', label: 'Wrong amount', sublabel: 'I received less than I paid for' },
  { value: 'wrong_recipient', label: 'Wrong recipient', sublabel: 'Delivered to the wrong number or account' },
  { value: 'other', label: 'Something else' },
];

// The backend stores reason as free text; the enum is a UI convenience.
const REASON_SENTENCE: Record<ReportReason, string> = {
  not_received: 'I paid but did not receive value.',
  partial: 'I received less than I paid for.',
  wrong_recipient: 'It was delivered to the wrong number or account.',
  other: '',
};

const fmtMcgp = (base?: string) => {
  try {
    return Number(ethers.formatUnits(base || '0', 18)).toLocaleString(undefined, {
      maximumFractionDigits: 2,
    });
  } catch {
    return '0';
  }
};

const fmtRate = (rateKobo?: string) =>
  (Number(rateKobo || '0') / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });

const shortDate = (iso?: string) => {
  if (!iso) return '—';
  try {
    return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  } catch {
    return iso;
  }
};

function receiptText(o: BillOrder): string {
  const label = CATEGORY_LABEL[o.category] ?? 'Bill';
  const lines = [
    'TSA Connect — bill receipt',
    `${label}${o.providerName ? ` · ${o.providerName}` : ''}${o.planName ? ` · ${o.planName}` : ''}`,
    `Amount: ₦${Number(o.ngnAmount ?? 0).toLocaleString()}`,
    `Status: ${statusMeta(o.status).label}`,
  ];
  const target = o.customerId || o.phoneNumber;
  if (target) lines.push(`To: ${target}${o.customerName ? ` (${o.customerName})` : ''}`);
  if (o.deliveryPayload) lines.push(`${o.category === 'epin' ? 'PIN' : 'Token'}: ${o.deliveryPayload}`);
  lines.push(`Date: ${shortDate(o.createdAt)}`);
  lines.push(`Ref: ${o.id}`);
  return lines.join('\n');
}

export default function BillOrderDetail() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { id } = useLocalSearchParams<{ id: string }>();
  const [order, setOrder] = useState<BillOrder | null>(null);
  const [report, setReport] = useState<Dispute | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [reporting, setReporting] = useState(false);
  const [reason, setReason] = useState<ReportReason | null>(null);
  const [details, setDetails] = useState('');
  const [evidence, setEvidence] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);

  const load = useCallback(async () => {
    if (!id) {
      setError('Missing order id');
      setLoading(false);
      return;
    }
    setError('');
    // A bill report is a dispute now (one operator queue across every order
    // type), so this reads the shared endpoint. The /bills report routes still
    // exist and still work — they are what app builds already installed call.
    const [o, r] = await Promise.all([
      getBillOrder(id),
      getDisputeForSubject('bill_order', id),
    ]);
    if (o.success && o.data) setOrder(o.data);
    else setError(o.message || 'Order not found');
    // "No dispute yet" is the normal case and resolves to null; only a real
    // failure leaves the previous value alone.
    if (r.success) setReport(r.data ?? null);
    setLoading(false);
  }, [id]);

  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  const toneColor = (tone: Tone) =>
    tone === 'success'
      ? colors.success
      : tone === 'danger'
        ? colors.danger
        : tone === 'warning'
          ? colors.warning
          : colors.textSecondary;

  const share = useCallback(async () => {
    if (!order) return;
    try {
      await Share.share({ message: receiptText(order) });
    } catch {
      /* user dismissed the sheet */
    }
  }, [order]);

  const submitReport = useCallback(async () => {
    if (!order || !reason) return;
    const text = [REASON_SENTENCE[reason], details.trim()].filter(Boolean).join(' ');
    if (!text) return;
    setSubmitting(true);
    const r = await fileDispute({
      subjectType: 'bill_order',
      subjectId: order.id,
      reason: text,
      ...(evidence.length > 0 ? { evidenceUrls: evidence } : {}),
    });
    setSubmitting(false);
    if (r.success && r.data) {
      setReport(r.data);
      setReporting(false);
      setReason(null);
      setDetails('');
      setEvidence([]);
      toast.success('Report submitted — we’ll look into it');
    } else {
      toast.error(r.message || 'Could not submit the report');
    }
  }, [order, reason, details, evidence, toast]);

  if (loading && !order) {
    return (
      <SafeAreaView
        style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}
      >
        <Spinner />
      </SafeAreaView>
    );
  }

  if (error && !order) {
    return (
      <SafeAreaView
        style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}
      >
        <Text variant="body" style={{ color: colors.danger, textAlign: 'center' }}>
          {error}
        </Text>
      </SafeAreaView>
    );
  }

  if (!order) return null;

  const meta = statusMeta(order.status);
  const label = CATEGORY_LABEL[order.category] ?? 'Bill';
  const target = order.customerId || order.phoneNumber;
  const paid = order.status !== 'quoted' && order.status !== 'expired';
  const canReport = REPORTABLE.includes(order.status) && !report;
  const canSubmit = !!reason && (reason !== 'other' ? true : details.trim().length > 0);

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={loading} onRefresh={load} tintColor={colors.accent} />}
      >
        <Card>
          <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
                {label}
                {order.providerName ? ` · ${order.providerName}` : ''}
              </Text>
              {order.planName ? (
                <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                  {order.planName}
                </Text>
              ) : null}
            </View>
            <Badge label={meta.label} color={toneColor(meta.tone)} />
          </View>

          <Divider spacing="sm" />

          <SummaryCard>
            <SummaryRow label="Amount" value={`₦${Number(order.ngnAmount ?? 0).toLocaleString()}`} />
            {paid ? (
              <>
                <Divider spacing="xs" />
                <SummaryRow label="Paid with" value={`${fmtMcgp(order.mcgpAmount)} MCGP`} />
                <Divider spacing="xs" />
                <SummaryRow label="Rate" value={`₦${fmtRate(order.rateKobo)} / MCGP`} />
              </>
            ) : null}
            {target ? (
              <>
                <Divider spacing="xs" />
                <SummaryRow label="Recipient" value={target} />
              </>
            ) : null}
            {order.customerName ? (
              <>
                <Divider spacing="xs" />
                <SummaryRow label="Account name" value={order.customerName} />
              </>
            ) : null}
            <Divider spacing="xs" />
            <SummaryRow label="Date" value={shortDate(order.createdAt)} />
            {order.vendorOrderId ? (
              <>
                <Divider spacing="xs" />
                <SummaryRow label="Provider ref" value={order.vendorOrderId} />
              </>
            ) : null}
            <Divider spacing="xs" />
            <SummaryRow label="Order ID" value={`${order.id.slice(0, 8)}…${order.id.slice(-4)}`} />
          </SummaryCard>

          {order.status === 'delivered' && order.deliveryPayload ? (
            <View style={{ marginTop: spacing.md }}>
              <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
                {order.category === 'epin' ? 'Your PIN' : 'Token'}
              </Text>
              <Text variant="body" weight="700" selectable style={{ color: colors.textPrimary }}>
                {order.deliveryPayload}
              </Text>
            </View>
          ) : null}

          {(order.status === 'vend_failed' || order.status === 'failed') && order.failureReason ? (
            <View style={{ marginTop: spacing.md }}>
              <Text variant="bodySm" style={{ color: colors.danger }}>
                {order.failureReason}
              </Text>
            </View>
          ) : null}

          {order.status === 'refunded' ? (
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.md }}>
              Your MCGP has been returned to your balance.
            </Text>
          ) : null}
        </Card>

        {report ? (
          <Card>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <View style={{ flex: 1 }}>
                <Text variant="body" weight="700" style={{ color: colors.textPrimary }}>
                  Report
                </Text>
              </View>
              <Badge
                label={disputeStatusLabel(report.status)}
                color={
                  isDisputeOpen(report.status)
                    ? colors.warning
                    : report.status === 'resolved'
                      ? colors.success
                      : colors.danger
                }
              />
            </View>
            <Text variant="bodySm" style={{ color: colors.textSecondary, marginTop: spacing.xs }}>
              {report.reason}
            </Text>
            {!isDisputeOpen(report.status) && report.resolution ? (
              <>
                <Divider spacing="sm" />
                <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
                  Response
                </Text>
                <Text variant="bodySm" style={{ color: colors.textPrimary }}>
                  {report.resolution}
                </Text>
              </>
            ) : null}
            {isDisputeOpen(report.status) ? (
              <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
                {report.status === 'escalated'
                  ? 'We have taken this to the payment provider. The outcome will appear here.'
                  : 'Our team is looking into this. The outcome will appear here.'}
              </Text>
            ) : null}
          </Card>
        ) : null}

        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          <Button
            title="Share"
            variant="secondary"
            leftIcon="send"
            style={{ flex: 1 }}
            onPress={share}
          />
          {canReport ? (
            <Button
              title="Report"
              variant="ghost"
              leftIcon="dispute"
              style={{ flex: 1 }}
              onPress={() => setReporting(true)}
            />
          ) : null}
        </View>
      </ScrollView>

      <Sheet visible={reporting} onClose={() => setReporting(false)} title="Report this transaction">
        <View style={{ gap: spacing.md }}>
          <Text variant="bodySm" style={{ color: colors.textSecondary }}>
            What went wrong?
          </Text>
          <RadioGroup<ReportReason> options={REASON_OPTIONS} value={reason} onChange={setReason} />
          <Input
            label={reason === 'other' ? 'Tell us what happened' : 'Add details (optional)'}
            value={details}
            onChangeText={setDetails}
            multiline
            maxLength={500}
            counter
            placeholder="Anything that helps us trace it"
          />
          <PhotoPickerTile
            label="Evidence (optional)"
            value={evidence}
            onChange={setEvidence}
            max={4}
          />
          <Button
            title="Submit report"
            variant="primary"
            fullWidth
            loading={submitting}
            disabled={!canSubmit}
            onPress={submitReport}
          />
        </View>
      </Sheet>
    </SafeAreaView>
  );
}
