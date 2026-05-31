// app/merchants/orders/index.tsx
//
// Merchant orders list — escrow product orders (with status filter + lifecycle
// actions) and contact-fee service requests, paginated and pull-to-refresh.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). Data fetching (orderApi.getOrders / serviceContactApi.listContactPayments),
// state, effects, pagination, status gating, money/amount rendering, and all
// navigation routes are preserved verbatim — this is a visual/structural restyle only.
//
// Note: STATUS_COLORS / formatStatus / formatDate come from constants/orderStatus
// (domain order-lifecycle helpers, not the legacy COLORS/SIZES/FONTS/SHADOWS theme)
// and are kept intact so per-status badge coloring matches the buyer-side order UI.
import React, { useEffect, useState, useCallback } from 'react';
import { View, FlatList, RefreshControl, Image } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { getOrders, formatTokenAmount, Order } from '@/services/orderApi';
import {
  listContactPayments,
  ContactPayment,
} from '@/services/serviceContactApi';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Card,
  Chip,
  Divider,
  EmptyState,
  ErrorState,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

type Mode = 'products' | 'services';

const STATUS_FILTERS = [
  { key: '', label: 'All' },
  { key: 'pending_payment', label: 'Pending' },
  { key: 'escrowed', label: 'Paid' },
  { key: 'shipped', label: 'Shipped' },
  { key: 'delivered', label: 'Delivered' },
  { key: 'completed', label: 'Completed' },
  { key: 'refund_requested', label: 'Refund' },
  { key: 'refunded', label: 'Refunded' },
];

const MerchantOrders = () => {
  const { colors, spacing, radius } = useTheme();
  const [mode, setMode] = useState<Mode>('products');
  const [orders, setOrders] = useState<Order[]>([]);
  const [servicePayments, setServicePayments] = useState<ContactPayment[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectedStatus, setSelectedStatus] = useState('');
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);

  const fetchOrders = useCallback(
    async (pageNum: number = 1, append: boolean = false) => {
      try {
        setError(null);
        const result = await getOrders({
          page: pageNum,
          limit: 20,
          status: selectedStatus || undefined,
          role: 'seller',
        });
        if (result.success && result.data) {
          const fetched = result.data.orders || [];
          setOrders((prev) => (append ? [...prev, ...fetched] : fetched));
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

  // Service requests have no escrow lifecycle (the on-chain split settles
  // when the contact fee is paid), so the status filter doesn't apply here
  // — we just paginate by createdAt DESC.
  const fetchServicePayments = useCallback(
    async (pageNum: number = 1, append: boolean = false) => {
      try {
        setError(null);
        const result = await listContactPayments({
          role: 'provider',
          page: pageNum,
          limit: 20,
        });
        if (result.success && result.data) {
          const fetched = result.data.items || [];
          setServicePayments((prev) => (append ? [...prev, ...fetched] : fetched));
          const pagination = result.data.pagination;
          setHasMore(pagination ? pageNum < pagination.totalPages : fetched.length === 20);
        } else {
          if (!append) setServicePayments([]);
          setError(result.message || 'Failed to load service requests');
        }
      } catch (err: any) {
        setError(err.message || 'Failed to load service requests');
      }
    },
    []
  );

  // Re-fetch when the active tab changes, or when the status filter
  // changes while on the products tab. The service tab doesn't depend on
  // selectedStatus so it only re-runs on tab switch.
  useEffect(() => {
    setLoading(true);
    setPage(1);
    setHasMore(true);
    const run = mode === 'products' ? fetchOrders(1, false) : fetchServicePayments(1, false);
    run.finally(() => setLoading(false));
  }, [mode, fetchOrders, fetchServicePayments]);

  const handleRefresh = async () => {
    setRefreshing(true);
    setPage(1);
    setHasMore(true);
    if (mode === 'products') await fetchOrders(1, false);
    else await fetchServicePayments(1, false);
    setRefreshing(false);
  };

  const handleLoadMore = () => {
    if (!hasMore || loading) return;
    const nextPage = page + 1;
    setPage(nextPage);
    if (mode === 'products') fetchOrders(nextPage, true);
    else fetchServicePayments(nextPage, true);
  };

  const renderOrderCard = ({ item }: { item: Order }) => {
    const statusColor = STATUS_COLORS[item.status] || STATUS_COLORS.cancelled;
    const needsAction = item.status === 'escrowed' || item.status === 'shipped' || item.status === 'refund_requested';
    const buyerLabel =
      item.buyer?.name?.trim() ||
      (item.buyer?.username ? `@${item.buyer.username}` : '') ||
      item.buyer?.email ||
      'Buyer';
    const productName = item.product?.name || `Product ${item.productId.slice(0, 6)}`;

    const CardComponent = needsAction ? AccentCard : Card;
    const cardProps = needsAction ? { railColor: colors.accentRail } : {};

    return (
      <PressableScale
        onPress={() => router.push(`/merchants/orders/${item.id}` as any)}
        style={{ marginBottom: spacing.md }}
      >
        <CardComponent padding="lg" {...cardProps}>
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'space-between',
              alignItems: 'flex-start',
              gap: spacing.sm,
            }}
          >
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="700" numberOfLines={1}>
                {productName}
              </Text>
              <Text variant="caption" color="textMuted" numberOfLines={1}>
                #{item.id.slice(0, 8)} · {buyerLabel}
              </Text>
            </View>
            <Badge
              label={formatStatus(item.status)}
              color={statusColor.text}
              background={statusColor.bg}
              size="sm"
            />
          </View>

          <View style={{ flexDirection: 'row', marginTop: spacing.md }}>
            {item.product?.imageUrl ? (
              <Image
                source={{ uri: item.product.imageUrl }}
                style={{ width: 56, height: 56, borderRadius: radius.sm, backgroundColor: colors.surfaceAlt }}
              />
            ) : (
              <View
                style={{
                  width: 56,
                  height: 56,
                  borderRadius: radius.sm,
                  backgroundColor: colors.surfaceAlt,
                  justifyContent: 'center',
                  alignItems: 'center',
                }}
              >
                <Icon name="cart" size={24} color={colors.textMuted} />
              </View>
            )}
            <View style={{ flex: 1, marginLeft: spacing.md, gap: spacing.xs }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="cart" size={14} color={colors.accent} />
                <Text variant="caption" color="textSecondary">
                  Qty {item.quantity}
                </Text>
              </View>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="wallet" size={14} color={colors.accent} />
                <Text variant="caption" color="textSecondary" numeric>
                  {formatTokenAmount(item.totalAmount, item.token)}
                </Text>
              </View>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="clock" size={14} color={colors.accent} />
                <Text variant="caption" color="textSecondary">
                  {formatDate(item.createdAt)}
                </Text>
              </View>
            </View>
          </View>

          <Divider spacing="md" />

          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
            {needsAction ? (
              <Text variant="bodySm" weight="700" color="accent">
                {item.status === 'escrowed' && '⚡ Ready to ship'}
                {item.status === 'shipped' && '📦 Awaiting confirmation'}
                {item.status === 'refund_requested' && (item.merchantApprovedRefund ? '⏳ Refund approved · awaiting admin' : '⚠ Refund requested')}
              </Text>
            ) : (
              <Text variant="bodySm" weight="600" color="accent">
                View Details
              </Text>
            )}
            <Icon name="chevronRight" size={16} color={colors.accent} />
          </View>
        </CardComponent>
      </PressableScale>
    );
  };

  const renderServiceCard = ({ item }: { item: ContactPayment }) => {
    const buyerLabel =
      item.counterparty?.name?.trim() ||
      (item.counterparty?.username ? `@${item.counterparty.username}` : '') ||
      item.counterparty?.email ||
      'Buyer';
    const serviceName = item.service?.name || `Service ${item.serviceId.slice(0, 6)}`;
    const earnedDisplay = `$${item.providerUSD.toFixed(2)}`;
    const totalDisplay = `$${item.feeUSD.toFixed(2)}`;

    return (
      <Card padding="lg" style={{ marginBottom: spacing.md }}>
        <View
          style={{
            flexDirection: 'row',
            justifyContent: 'space-between',
            alignItems: 'flex-start',
            gap: spacing.sm,
          }}
        >
          <View style={{ flex: 1 }}>
            <Text variant="bodyMed" weight="700" numberOfLines={1}>
              {serviceName}
            </Text>
            <Text variant="caption" color="textMuted" numberOfLines={1}>
              #{item.id.slice(0, 8)} · {buyerLabel}
            </Text>
          </View>
          <Badge label="PAID" color={colors.success} background={colors.successBg} size="sm" />
        </View>

        <View style={{ flexDirection: 'row', marginTop: spacing.md }}>
          <View
            style={{
              width: 56,
              height: 56,
              borderRadius: radius.sm,
              backgroundColor: colors.surfaceAlt,
              justifyContent: 'center',
              alignItems: 'center',
            }}
          >
            <Icon name="storefront" size={24} color={colors.accent} />
          </View>
          <View style={{ flex: 1, marginLeft: spacing.md, gap: spacing.xs }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
              <Icon name="wallet" size={14} color={colors.accent} />
              <Text variant="caption" color="textSecondary">
                You earned {earnedDisplay} of {totalDisplay} ({item.token})
              </Text>
            </View>
            {item.counterparty?.email ? (
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="send" size={14} color={colors.accent} />
                <Text variant="caption" color="textSecondary" numberOfLines={1}>
                  {item.counterparty.email}
                </Text>
              </View>
            ) : null}
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
              <Icon name="clock" size={14} color={colors.accent} />
              <Text variant="caption" color="textSecondary">
                {formatDate(item.createdAt)}
              </Text>
            </View>
          </View>
        </View>

        <Divider spacing="md" />

        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <Text variant="bodySm" weight="600" color="textSecondary">
            Settled on-chain
          </Text>
          <Icon name="checkCircle" size={16} color={colors.success} />
        </View>
      </Card>
    );
  };

  const renderHeader = () => (
    <>
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          justifyContent: 'space-between',
          paddingHorizontal: spacing.lg,
          paddingVertical: spacing.sm,
        }}
      >
        <IconButton icon="chevronLeft" accessibilityLabel="Go back" onPress={() => router.back()} />
        <Text variant="title3" weight="700">
          Orders
        </Text>
        <View style={{ width: 44 }} />
      </View>

      {/* Mode tabs: Products (escrow) vs Services (contact-fee) */}
      <View
        style={{
          flexDirection: 'row',
          paddingHorizontal: spacing.lg,
          gap: spacing.sm,
          marginBottom: spacing.md,
        }}
      >
        {(['products', 'services'] as const).map((m) => (
          <PressableScale
            key={m}
            onPress={() => setMode(m)}
            style={{
              flex: 1,
              paddingVertical: spacing.md,
              alignItems: 'center',
              borderBottomWidth: 2,
              borderBottomColor: mode === m ? colors.accent : 'transparent',
            }}
          >
            <Text
              variant="bodySm"
              weight={mode === m ? '700' : '600'}
              color={mode === m ? 'textPrimary' : 'textMuted'}
            >
              {m === 'products' ? 'Products' : 'Services'}
            </Text>
          </PressableScale>
        ))}
      </View>

      {/* Status filter — only meaningful for the products escrow flow */}
      {mode === 'products' && (
        <FlatList
          horizontal
          showsHorizontalScrollIndicator={false}
          data={STATUS_FILTERS}
          keyExtractor={(item) => item.key}
          contentContainerStyle={{ paddingHorizontal: spacing.lg, gap: spacing.sm, paddingBottom: spacing.md }}
          style={{ flexGrow: 0 }}
          renderItem={({ item: filter }) => (
            <Chip
              label={filter.label}
              selected={selectedStatus === filter.key}
              onPress={() => setSelectedStatus(filter.key)}
            />
          )}
        />
      )}
    </>
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {renderHeader()}

      {loading ? (
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" />
        </View>
      ) : mode === 'products' ? (
        <FlatList
          data={orders}
          keyExtractor={(item) => item.id}
          renderItem={renderOrderCard}
          contentContainerStyle={
            orders.length === 0
              ? { flexGrow: 1, justifyContent: 'center' }
              : { padding: spacing.lg, paddingBottom: spacing['4xl'] }
          }
          refreshControl={
            <RefreshControl refreshing={refreshing} onRefresh={handleRefresh} tintColor={colors.accent} />
          }
          onEndReached={handleLoadMore}
          onEndReachedThreshold={0.3}
          ListEmptyComponent={
            error ? (
              <ErrorState subtitle={error} onRetry={() => fetchOrders(1, false)} />
            ) : (
              <EmptyState
                icon="list"
                title="No orders yet"
                subtitle="Orders from your customers will appear here"
              />
            )
          }
        />
      ) : (
        <FlatList
          data={servicePayments}
          keyExtractor={(item) => item.id}
          renderItem={renderServiceCard}
          contentContainerStyle={
            servicePayments.length === 0
              ? { flexGrow: 1, justifyContent: 'center' }
              : { padding: spacing.lg, paddingBottom: spacing['4xl'] }
          }
          refreshControl={
            <RefreshControl refreshing={refreshing} onRefresh={handleRefresh} tintColor={colors.accent} />
          }
          onEndReached={handleLoadMore}
          onEndReachedThreshold={0.3}
          ListEmptyComponent={
            error ? (
              <ErrorState subtitle={error} onRetry={() => fetchServicePayments(1, false)} />
            ) : (
              <EmptyState
                icon="storefront"
                title="No service requests yet"
                subtitle="When a buyer unlocks your contact info, the request appears here."
              />
            )
          }
        />
      )}
    </SafeAreaView>
  );
};

export default MerchantOrders;
