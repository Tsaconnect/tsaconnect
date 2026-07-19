// app/(dashboard)/contact-requests.tsx
//
// Contact requests — the pay-per-contact reveal history (ServiceContact) in the
// booking-requests two-segment layout. "My requests" (caller) lists providers
// whose contact the user paid to reveal; "Received" (provider) lists buyers who
// paid to reveal the user's contact. Both come from
// GET /services/contact-payments?role=caller|provider. Every row is a completed
// on-chain fee payment, so status is always "Revealed" — caller rows open the
// service detail, where the unlocked contact is shown.
import React, { useCallback, useEffect, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Badge, Button, Card, EmptyState, Icon, PressableScale, Spinner, Text } from '@/components/ui';
import { listContactPayments, type ContactPayment } from '@/services/serviceContactApi';
import { formatDate } from '@/constants/orderStatus';

type Tab = 'caller' | 'provider';

export default function ContactRequestsScreen() {
  const { colors, spacing } = useTheme();
  const [tab, setTab] = useState<Tab>('caller');
  const [rows, setRows] = useState<ContactPayment[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    const res = await listContactPayments({ role: tab, page: 1, limit: 50 });
    setRows(res.success && res.data ? res.data.items || [] : []);
    setLoading(false);
    setRefreshing(false);
  }, [tab]);

  useEffect(() => {
    setLoading(true);
    load();
  }, [load]);

  const onRefresh = () => {
    setRefreshing(true);
    load();
  };

  const openService = (payment: ContactPayment) => {
    if (!payment.serviceId) return;
    router.push({
      pathname: '/servicedetail',
      params: { id: payment.serviceId, title: payment.service?.name ?? '' },
    });
  };

  const Segment = ({ value, label }: { value: Tab; label: string }) => {
    const active = tab === value;
    return (
      <Button
        title={label}
        variant={active ? 'primary' : 'secondary'}
        size="sm"
        onPress={() => setTab(value)}
        style={{ flex: 1 }}
      />
    );
  };

  return (
    <View style={{ flex: 1, backgroundColor: colors.background }}>
      <View style={{ flexDirection: 'row', gap: spacing.sm, padding: spacing.lg }}>
        <Segment value="caller" label="My requests" />
        <Segment value="provider" label="Received" />
      </View>

      {loading ? (
        <View style={{ paddingVertical: spacing['2xl'], alignItems: 'center' }}>
          <Spinner label="Loading contact requests…" />
        </View>
      ) : (
        <ScrollView
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingBottom: spacing['2xl'], gap: spacing.md }}
          refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
        >
          {rows.length === 0 ? (
            <EmptyState
              icon="list"
              title="No contact requests"
              subtitle={
                tab === 'caller'
                  ? 'Provider contacts you unlock will show here.'
                  : 'Buyers who unlock your contact will show here.'
              }
            />
          ) : (
            rows.map((payment) => {
              const counterpartyName =
                payment.counterparty?.name || payment.counterparty?.username || '';
              const amountLabel =
                tab === 'caller'
                  ? `Paid $${(payment.feeUSD ?? 0).toFixed(2)} ${payment.token || ''}`.trim()
                  : `Earned $${(payment.providerUSD ?? 0).toFixed(2)}`;
              const card = (
                <Card padding="lg" style={{ gap: spacing.sm }}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                    <Text variant="bodyMed" weight="700" style={{ flex: 1 }} numberOfLines={1}>
                      {payment.service?.name || 'Service'}
                    </Text>
                    <Badge label="Revealed" background={colors.success} color={colors.onAccent} size="sm" />
                  </View>

                  {counterpartyName ? (
                    <Text variant="caption" color="textSecondary" numberOfLines={1}>
                      {tab === 'caller' ? 'Provider' : 'From'}: {counterpartyName}
                    </Text>
                  ) : null}

                  <View
                    style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}
                  >
                    <Text variant="bodySm" color="textSecondary" numeric>
                      {amountLabel}
                    </Text>
                    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                      <Text variant="caption" color="textMuted">
                        {formatDate(payment.createdAt)}
                      </Text>
                      {tab === 'caller' ? (
                        <Icon name="chevronRight" size={14} color={colors.textMuted} />
                      ) : null}
                    </View>
                  </View>
                </Card>
              );
              // Only caller rows navigate — the point is re-opening the service
              // whose contact you already unlocked. Provider rows are a record.
              return tab === 'caller' ? (
                <PressableScale key={payment.id} onPress={() => openService(payment)}>
                  {card}
                </PressableScale>
              ) : (
                <View key={payment.id}>{card}</View>
              );
            })
          )}
        </ScrollView>
      )}
    </View>
  );
}
