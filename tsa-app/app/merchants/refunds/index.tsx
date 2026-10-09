// app/merchants/refunds/index.tsx
//
// Her refunds, in the mode she is in (live once she can take live payments; `?mode=` names
// another): the ones waiting for her to send first, then those under review or on their
// way, then the rest, newest first. Refunds are asked for from a payment on the web; she
// sends each from its own screen. Kept fresh every 15 s while she looks.
//
// The newest page alone could hide an old refund still waiting for her, so the ones waiting
// are fetched on their own and merged in.
import React, { useCallback, useState } from 'react';
import { FlatList, RefreshControl, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import { Card, EmptyState, ErrorState, getStatusVisual, PressableScale, StatusPill, Text } from '@/components/ui';
import { OwnPayMerchantGate, ScreenSpinner } from '@/components/pay/OwnPayMerchantGate';
import { PAY_REFUNDS } from '@/constants/routes';
import { useFocusPolling } from '@/hooks/useFocusPolling';
import {
  listPayRefunds, payLinkModeOf, payLinkModeParam, type PayLinkMode, type PayRefundRow,
} from '@/services/payMerchantApi';
import { errorMessage } from '@/utils/payCheckout';
import { formatLinkTime } from '@/utils/payLinks';
import { refundAmountLabel, refundBroadcastNote, sortRefundsOpenFirst } from '@/utils/payRefunds';

const PAGE = 50;
const POLL_MS = 15_000;

export default function PayRefunds() {
  const modeParam = payLinkModeParam(useLocalSearchParams<{ mode?: string }>().mode);
  return (
    <OwnPayMerchantGate>
      {(merchant) => {
        const mode = modeParam ?? payLinkModeOf(merchant);
        // A new mode is a different list: nothing of the old one may stay on screen.
        return <RefundList key={mode} mode={mode} />;
      }}
    </OwnPayMerchantGate>
  );
}

function RefundList({ mode }: { mode: PayLinkMode }) {
  const { colors, spacing } = useTheme();
  const [rows, setRows] = useState<PayRefundRow[] | null>(null);
  const [error, setError] = useState<string | undefined>();
  const [refreshing, setRefreshing] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const [newest, waiting] = await Promise.all([
        listPayRefunds(mode, 1, PAGE),
        listPayRefunds(mode, 1, PAGE, 'requested'),
      ]);
      const seen = new Set(newest.refunds.map((r) => r.id));
      setRows(sortRefundsOpenFirst([...newest.refunds, ...waiting.refunds.filter((r) => !seen.has(r.id))]));
      setError(undefined);
    } catch (e) {
      setError(errorMessage(e, 'Could not load your refunds.'));
    } finally {
      setRefreshing(false);
    }
  }, [mode]);

  useFocusPolling(refresh, POLL_MS);

  if (rows === null) {
    if (error) return <ErrorState title="Could not load your refunds" subtitle={error} onRetry={refresh} />;
    return <ScreenSpinner />;
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <FlatList
        data={rows}
        keyExtractor={(r) => r.id}
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.sm }}
        ListHeaderComponent={
          <View style={{ gap: spacing.sm, marginBottom: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              Refunds are asked for from a payment on the web. Send each one here, from your TSA wallet.
            </Text>
            {error ? <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text> : null}
          </View>
        }
        ListEmptyComponent={
          <EmptyState icon="refresh" title="No refunds yet" subtitle="Refunds you ask for on the web appear here for you to send." />
        }
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => { setRefreshing(true); void refresh(); }} />}
        renderItem={({ item }) => <RefundRow refund={item} />}
      />
    </SafeAreaView>
  );
}

function RefundRow({ refund: r }: { refund: PayRefundRow }) {
  const { spacing, colors } = useTheme();
  const who = r.customerName || 'Customer';
  return (
    <PressableScale
      onPress={() => router.push(`${PAY_REFUNDS}/${r.id}`)}
      accessibilityRole="button"
      accessibilityLabel={`${who}, ${refundAmountLabel(r)}, ${getStatusVisual('payRefund', r.status).label}`}
      testID={`refund-${r.id}`}
    >
      <Card style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
        <View style={{ flex: 1, gap: spacing.xs }}>
          <Text variant="bodyMed" weight="700" numberOfLines={1}>{who}</Text>
          <Text variant="caption" color="textSecondary" numberOfLines={1}>
            {[r.description, formatLinkTime(r.createdAt)].filter(Boolean).join(' · ')}
          </Text>
          {r.status === 'submitted' && r.lastBroadcastError ? (
            <Text variant="caption" style={{ color: colors.danger }}>{refundBroadcastNote(r.lastBroadcastError)}</Text>
          ) : null}
        </View>
        <View style={{ alignItems: 'flex-end', gap: spacing.xs }}>
          <Text variant="bodyMed" weight="700" numeric>{refundAmountLabel(r)}</Text>
          <StatusPill kind="payRefund" status={r.status} size="sm" />
        </View>
      </Card>
    </PressableScale>
  );
}
