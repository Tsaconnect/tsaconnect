// app/wallet/it/intent/[id].tsx
//
// Instant Transfer order detail — status progress for a single buy/sell intent.
import React, { useCallback, useState } from 'react';
import { ScrollView, RefreshControl, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { Card, Divider, Spinner, SummaryCard, SummaryRow, Text } from '@/components/ui';
import { getITIntent, type ITIntent } from '@/services/itApi';

const fmtMcgp = (wei?: string) => {
  try {
    return Number(ethers.formatUnits(wei || '0', 18)).toLocaleString(undefined, { maximumFractionDigits: 4 });
  } catch {
    return '0';
  }
};
const fmtNaira = (kobo?: string) => {
  try {
    return (Number(kobo || '0') / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });
  } catch {
    return '0';
  }
};
const fmtRate = (kobo?: string) => {
  try {
    return (Number(kobo || '0') / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });
  } catch {
    return '0';
  }
};

function statusLabel(status: string): string {
  switch (status) {
    case 'pending_payment':
      return 'Awaiting transfer';
    case 'processing':
      return 'Processing';
    case 'credited':
      return 'Complete';
    case 'debited':
      return 'Payout in progress';
    case 'settled':
      return 'Complete';
    case 'awaiting_rate':
      return 'Awaiting rate';
    case 'failed':
      return 'Failed';
    case 'cancelled':
      return 'Cancelled';
    case 'reversed':
      return 'Reversed';
    default:
      return status || '—';
  }
}

function shortDate(iso?: string): string {
  if (!iso) return '—';
  try {
    return new Date(iso).toLocaleString(undefined, {
      dateStyle: 'medium',
      timeStyle: 'short',
    });
  } catch {
    return iso;
  }
}

export default function ITIntentDetail() {
  const { colors, spacing } = useTheme();
  const { id } = useLocalSearchParams<{ id: string }>();
  const [intent, setIntent] = useState<ITIntent | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    if (!id) {
      setError('Missing order id');
      setLoading(false);
      return;
    }
    setError('');
    const r = await getITIntent(id);
    if (r.success && r.data?.intent) setIntent(r.data.intent);
    else setError(r.message || 'Order not found');
    setLoading(false);
  }, [id]);

  useFocusEffect(useCallback(() => { load(); }, [load]));

  if (loading && !intent) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner />
      </SafeAreaView>
    );
  }

  if (error && !intent) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Text variant="body" style={{ color: colors.danger, textAlign: 'center' }}>{error}</Text>
      </SafeAreaView>
    );
  }

  if (!intent) return null;

  const isBuy = intent.direction === 'buy';
  const title = isBuy ? 'Buy' : 'Sell';

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={loading} onRefresh={load} tintColor={colors.accent} />}
      >
        <Card>
          <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
            {title}
          </Text>
          <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
            {statusLabel(intent.status)}
          </Text>
          <SummaryCard>
            <SummaryRow label="Status" value={statusLabel(intent.status)} />
            <Divider spacing="xs" />
            {isBuy ? (
              <>
                <SummaryRow label="You pay" value={`₦${fmtNaira(intent.ngnKobo)}`} />
                <Divider spacing="xs" />
                <SummaryRow label="You get" value={`${fmtMcgp(intent.mcgpAmount)} MCGP`} />
              </>
            ) : (
              <>
                <SummaryRow label="You sell" value={`${fmtMcgp(intent.mcgpAmount)} MCGP`} />
                <Divider spacing="xs" />
                <SummaryRow label="You receive" value={`₦${fmtNaira(intent.ngnKobo)}`} />
              </>
            )}
            <Divider spacing="xs" />
            <SummaryRow label="Rate" value={`₦${fmtRate(intent.rateKobo)} / MCGP`} />
            <Divider spacing="xs" />
            <SummaryRow label="Created" value={shortDate(intent.createdAt)} />
            {intent.txHash ? (
              <>
                <Divider spacing="xs" />
                <SummaryRow label="Tx" value={`${intent.txHash.slice(0, 10)}…${intent.txHash.slice(-6)}`} />
              </>
            ) : null}
          </SummaryCard>
          {intent.failureReason ? (
            <View style={{ marginTop: spacing.md }}>
              <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
                Note
              </Text>
              <Text variant="bodySm" style={{ color: colors.textPrimary }}>
                {intent.failureReason}
              </Text>
            </View>
          ) : null}
          {intent.status === 'pending_payment' ? (
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.md }}>
              Transfer any Naira amount into your Instant Transfer account. This order updates when
              payment is confirmed at the live rate.
            </Text>
          ) : null}
        </Card>
      </ScrollView>
    </SafeAreaView>
  );
}
