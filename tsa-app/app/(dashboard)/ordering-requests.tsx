// app/(dashboard)/ordering-requests.tsx
//
// Ordering requests — product/service orders in the booking-requests two-segment
// layout. "My orders" (buyer) lists orders the user has placed; "Incoming"
// (visible to merchants only, same AsyncStorage role gate as the drawer's
// MERCHANT section) lists orders received for the merchant's own listings.
// Both come from GET /orders?role=buyer|seller. Rows are summaries — tapping
// one opens the shared order-detail screen, which owns every order action
// (escrow, ship, confirm, cancel, dispute…).
import React, { useCallback, useEffect, useState } from 'react';
import { RefreshControl, ScrollView, View } from 'react-native';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useTheme } from '@/theme';
import { useIsPaymentMerchant } from '@/hooks/useIsPaymentMerchant';
import { isMerchantAccount } from '@/utils/isMerchantAccount';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Icon,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';
import { getOrders, formatTokenAmount, type Order } from '@/services/orderApi';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';

type Tab = 'buyer' | 'seller';

export default function OrderingRequestsScreen() {
  const { colors, spacing } = useTheme();
  const [tab, setTab] = useState<Tab>('buyer');
  const [isMerchant, setIsMerchant] = useState(false);
  const [rows, setRows] = useState<Order[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  // Same merchant gate as the drawer's MERCHANT section: role stored at login.
  const pm = useIsPaymentMerchant();
  useEffect(() => {
    AsyncStorage.getItem('role').then(
      (role) => setIsMerchant(isMerchantAccount(role, pm)),
      () => setIsMerchant(false)
    );
  }, [pm.isPm, pm.kind]);

  const load = useCallback(async () => {
    const res = await getOrders({ role: tab, page: 1, limit: 50 });
    setRows(res.success && res.data ? res.data.orders || [] : []);
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
        <Segment value="buyer" label="My orders" />
        {isMerchant ? <Segment value="seller" label="Incoming" /> : null}
      </View>

      {loading ? (
        <View style={{ paddingVertical: spacing['2xl'], alignItems: 'center' }}>
          <Spinner label="Loading orders…" />
        </View>
      ) : (
        <ScrollView
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingBottom: spacing['2xl'], gap: spacing.md }}
          refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
        >
          {rows.length === 0 ? (
            <EmptyState
              icon="list"
              title="No orders"
              subtitle={
                tab === 'buyer'
                  ? 'Orders you place will show here.'
                  : 'Incoming orders for your products will show here.'
              }
            />
          ) : (
            rows.map((order) => {
              const statusColor = STATUS_COLORS[order.status] || STATUS_COLORS.cancelled;
              // Buyer view shows who you bought from; incoming view shows who ordered.
              const counterparty = tab === 'buyer' ? order.seller : order.buyer;
              const counterpartyName = counterparty?.name || counterparty?.username || '';
              return (
                <PressableScale
                  key={order.id}
                  onPress={() => router.push(`/orderdetail?id=${order.id}`)}
                >
                  <Card padding="lg" style={{ gap: spacing.sm }}>
                    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                      <Text variant="bodyMed" weight="700" style={{ flex: 1 }} numberOfLines={1}>
                        {order.product?.name || `Order #${order.id.slice(0, 8)}`}
                      </Text>
                      <Badge
                        label={formatStatus(order.status)}
                        color={statusColor.text}
                        background={statusColor.bg}
                        size="sm"
                      />
                    </View>

                    {counterpartyName ? (
                      <Text variant="caption" color="textSecondary" numberOfLines={1}>
                        {tab === 'buyer' ? 'Seller' : 'Buyer'}: {counterpartyName}
                      </Text>
                    ) : null}

                    <View
                      style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}
                    >
                      <Text variant="bodySm" color="textSecondary" numeric>
                        {formatTokenAmount(order.totalAmount, order.token)}
                      </Text>
                      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                        <Text variant="caption" color="textMuted">
                          {formatDate(order.createdAt)}
                        </Text>
                        <Icon name="chevronRight" size={14} color={colors.textMuted} />
                      </View>
                    </View>
                  </Card>
                </PressableScale>
              );
            })
          )}
        </ScrollView>
      )}
    </View>
  );
}
