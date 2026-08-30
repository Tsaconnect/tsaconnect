// components/orders/orderList.tsx
//
// Orders list (rendered by the orders screen) — buyer order history with a
// horizontal status filter, pull-to-refresh, and infinite scroll. Restyled onto
// the TSA Signature design system (@/theme tokens + @/components/ui primitives).
//
// VISUAL/STRUCTURAL restyle ONLY: the getOrders fetch (page/limit/status/role),
// pagination + append logic, status filtering, refresh, load-more, and the
// orderdetail navigation route are preserved verbatim. STATUS_COLORS /
// formatStatus / formatDate / formatTokenAmount remain the source of truth for
// status presentation and amount/date formatting.
import React, { useEffect, useState, useCallback } from 'react';
import { View, FlatList, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { getOrders, formatTokenAmount, Order } from '@/services/orderApi';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Chip,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

const STATUS_FILTERS = [
  { key: '', label: 'All' },
  { key: 'pending_payment', label: 'Pending' },
  { key: 'escrowed', label: 'Escrowed' },
  { key: 'delivered', label: 'Delivered' },
  { key: 'completed', label: 'Completed' },
  { key: 'refund_requested', label: 'Refund' },
  { key: 'refunded', label: 'Refunded' },
];

const OrderList = () => {
  const { colors, spacing } = useTheme();
  const [orders, setOrders] = useState<Order[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectedStatus, setSelectedStatus] = useState('');
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);

  const fetchOrders = useCallback(
    async (pageNum: number = 1, append: boolean = false) => {
      try {
        const result = await getOrders({
          page: pageNum,
          limit: 20,
          status: selectedStatus || undefined,
          role: 'buyer',
        });
        if (result.success && result.data) {
          const fetched = result.data.orders || [];
          setOrders(append ? (prev) => [...prev, ...fetched] : fetched);
          const pagination = result.data.pagination;
          setHasMore(pagination ? pageNum < pagination.totalPages : fetched.length === 20);
        } else {
          if (!append) setOrders([]);
          setError(result.message || 'Failed to load orders');
        }
      } catch (err: any) {
        setError(err.message || 'Failed to load orders');
      }
    },
    [selectedStatus]
  );

  useEffect(() => {
    setLoading(true);
    setPage(1);
    fetchOrders(1, false).finally(() => setLoading(false));
  }, [fetchOrders]);

  const handleRefresh = async () => {
    setRefreshing(true);
    setPage(1);
    await fetchOrders(1, false);
    setRefreshing(false);
  };

  const handleLoadMore = () => {
    if (!hasMore || loading) return;
    const nextPage = page + 1;
    setPage(nextPage);
    fetchOrders(nextPage, true);
  };

  const renderOrderCard = ({ item }: { item: Order }) => {
    const statusColor = STATUS_COLORS[item.status] || STATUS_COLORS.cancelled;
    return (
      <PressableScale
        onPress={() => router.push(`/orderdetail?id=${item.id}`)}
        style={{ marginBottom: spacing.md }}
      >
        <AccentCard padding="lg">
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'space-between',
              alignItems: 'center',
            }}
          >
            <Text variant="bodyMed" weight="700" numberOfLines={1} style={{ flex: 1, marginRight: spacing.sm }}>
              Order #{item.id.slice(0, 8)}...
            </Text>
            <Badge label={formatStatus(item.status)} color={statusColor.text} background={statusColor.bg} size="sm" />
          </View>

          <View style={{ gap: spacing.sm, marginTop: spacing.md }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon name="list" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary">
                Qty: {item.quantity}
              </Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon name="wallet" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary" numeric>
                {formatTokenAmount(item.totalAmount, item.token)}
              </Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon name="clock" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary">
                {formatDate(item.createdAt)}
              </Text>
            </View>
          </View>

          <Divider spacing="md" />

          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'flex-end',
              gap: spacing.xs,
            }}
          >
            <Text variant="bodySm" weight="700" color="accent">
              View Details
            </Text>
            <Icon name="chevronRight" size={16} color={colors.accent} />
          </View>
        </AccentCard>
      </PressableScale>
    );
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {/* Status filter tabs */}
      <FlatList
        horizontal
        showsHorizontalScrollIndicator={false}
        data={STATUS_FILTERS}
        keyExtractor={(item) => item.key}
        contentContainerStyle={{
          paddingHorizontal: spacing.lg,
          paddingVertical: spacing.md,
          gap: spacing.sm,
        }}
        style={{ flexGrow: 0 }}
        renderItem={({ item: filter }) => (
          <Chip
            label={filter.label}
            selected={selectedStatus === filter.key}
            onPress={() => setSelectedStatus(filter.key)}
          />
        )}
      />

      {loading ? (
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading orders..." />
        </View>
      ) : (
        <FlatList
          data={orders}
          keyExtractor={(item) => item.id}
          renderItem={renderOrderCard}
          contentContainerStyle={
            orders.length === 0
              ? { flexGrow: 1, justifyContent: 'center' }
              : { padding: spacing.lg, paddingTop: spacing.xs }
          }
          refreshControl={
            <RefreshControl refreshing={refreshing} onRefresh={handleRefresh} tintColor={colors.accent} />
          }
          onEndReached={handleLoadMore}
          onEndReachedThreshold={0.3}
          ListEmptyComponent={
            error ? (
              <ErrorState
                title="Something went wrong"
                subtitle={error}
                onRetry={handleRefresh}
              />
            ) : (
              <EmptyState
                icon="list"
                title="No orders yet"
                subtitle="Your order history will appear here"
              />
            )
          }
        />
      )}
    </SafeAreaView>
  );
};

export default OrderList;
