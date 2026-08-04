// app/bills/orders.tsx
//
// Bill history. This is where an order lands when the purchase screen stops
// polling: ClubKonnect accepts a vend before it confirms it, and the backend
// reconciler can take minutes to settle the final state — longer than anyone
// will sit on a spinner. Without this screen a delivered bill was
// unreachable, which is exactly the dead end users hit.
//
// Pulls to refresh, and auto-refreshes while anything is still pending so a
// user watching the list sees it resolve without interaction.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useFocusEffect } from '@react-navigation/native';
import { useTheme } from '@/theme';
import { Badge, Card, Divider, Icon, Spinner, Text } from '@/components/ui';
import { listBillOrders, type BillOrder } from '@/services/billsApi';

const CATEGORY_LABEL: Record<string, string> = {
  airtime: 'Airtime',
  data: 'Data',
  electricity: 'Electricity',
  cabletv: 'Cable TV',
  betting: 'Betting',
  epin: 'Exam PIN',
};

const PENDING: BillOrder['status'][] = ['quoted', 'paying', 'paid', 'vending'];

type Tone = 'success' | 'warning' | 'danger' | 'neutral';

function statusMeta(status: BillOrder['status']): { label: string; tone: Tone } {
  switch (status) {
    case 'delivered':
      return { label: 'Delivered', tone: 'success' };
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

export default function BillOrders() {
  const { colors, spacing } = useTheme();
  const [orders, setOrders] = useState<BillOrder[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState('');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const load = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true);
    const r = await listBillOrders();
    if (r.success && Array.isArray(r.data)) {
      setOrders(r.data);
      setError('');
    } else if (!isRefresh) {
      setError(r.message || 'Could not load your bills');
    }
    setLoading(false);
    setRefreshing(false);
  }, []);

  useFocusEffect(
    useCallback(() => {
      load();
      return () => {
        if (timer.current) clearTimeout(timer.current);
      };
    }, [load]),
  );

  // Poll while anything is unresolved; stop as soon as everything is terminal
  // so the screen isn't hitting the API forever in the background.
  useEffect(() => {
    if (timer.current) clearTimeout(timer.current);
    if (!orders.some((o) => PENDING.includes(o.status))) return;
    timer.current = setTimeout(() => load(), 10000);
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, [orders, load]);

  const toneColor = (tone: Tone) =>
    tone === 'success'
      ? colors.success
      : tone === 'danger'
        ? colors.danger
        : tone === 'warning'
          ? colors.warning
          : colors.textSecondary;

  if (loading) {
    return (
      <SafeAreaView
        style={{ flex: 1, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.background }}
      >
        <Spinner />
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => load(true)} />}
      >
        {error ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error}
          </Text>
        ) : null}

        {orders.length === 0 && !error ? (
          <View style={{ alignItems: 'center', paddingVertical: spacing.xl, gap: spacing.sm }}>
            <Icon name="bolt" size={40} color={colors.textMuted} />
            <Text variant="body" style={{ color: colors.textSecondary }}>
              No bills yet
            </Text>
            <Text variant="bodySm" style={{ color: colors.textMuted, textAlign: 'center' }}>
              Airtime, data, electricity and more will appear here.
            </Text>
          </View>
        ) : null}

        {orders.map((o) => {
          const meta = statusMeta(o.status);
          const label = CATEGORY_LABEL[o.category] ?? 'Bill';
          const target = o.customerId || o.phoneNumber;
          return (
            <Card key={o.id}>
              <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.md }}>
                <View style={{ flex: 1 }}>
                  <Text variant="body" weight="700" style={{ color: colors.textPrimary }}>
                    {label}
                    {o.providerName ? ` · ${o.providerName}` : ''}
                  </Text>
                  {o.planName ? (
                    <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                      {o.planName}
                    </Text>
                  ) : null}
                  {target ? (
                    <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                      {target}
                    </Text>
                  ) : null}
                  <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
                    {new Date(o.createdAt).toLocaleString()}
                  </Text>
                </View>
                <View style={{ alignItems: 'flex-end', gap: spacing.xs }}>
                  <Text variant="body" weight="700" style={{ color: colors.textPrimary }}>
                    ₦{Number(o.ngnAmount ?? 0).toLocaleString()}
                  </Text>
                  <Badge label={meta.label} color={toneColor(meta.tone)} />
                </View>
              </View>

              {o.status === 'delivered' && o.deliveryPayload ? (
                <>
                  <Divider spacing="sm" />
                  <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
                    {o.category === 'epin' ? 'Your PIN' : 'Token'}
                  </Text>
                  <Text variant="body" weight="700" selectable style={{ color: colors.textPrimary }}>
                    {o.deliveryPayload}
                  </Text>
                </>
              ) : null}

              {PENDING.includes(o.status) ? (
                <>
                  <Divider spacing="sm" />
                  <Text variant="bodySm" style={{ color: colors.textMuted }}>
                    Still with the provider. This updates automatically — you don’t need to pay again.
                  </Text>
                </>
              ) : null}

              {(o.status === 'vend_failed' || o.status === 'failed') && o.failureReason ? (
                <>
                  <Divider spacing="sm" />
                  <Text variant="bodySm" style={{ color: colors.danger }}>
                    {o.failureReason}
                  </Text>
                </>
              ) : null}

              {o.status === 'refunded' ? (
                <>
                  <Divider spacing="sm" />
                  <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                    Your MCGP has been returned to your balance.
                  </Text>
                </>
              ) : null}
            </Card>
          );
        })}
      </ScrollView>
    </SafeAreaView>
  );
}
