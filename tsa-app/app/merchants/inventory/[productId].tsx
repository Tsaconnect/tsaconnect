// app/merchants/inventory/[productId].tsx
//
// Inventory product detail — a merchant's view of a single product: hero image,
// price/status, stock/views/sales stats, per-token revenue, recent orders,
// shipping rates, location, plus an Edit + activate/deactivate action bar.
// Migrated onto the TSA Signature design system (@/theme tokens + @/components/ui
// primitives). This is a VISUAL/STRUCTURAL restyle only — every API call
// (api.getProductById, getOrders, api.updateProduct), all state/effects, the
// wei→number revenue math (weiToNumber / sumByToken / USD_PEGGED bucketing),
// the status-toggle confirm flow, role/status gating, navigation routes/params,
// and money/token formatting are preserved verbatim from the legacy screen.
import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  ScrollView,
  Image,
  Dimensions,
  RefreshControl,
  Alert,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import api, { Product } from '@/components/services/api';
import { getOrders, formatTokenAmount, Order } from '@/services/orderApi';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Badge,
  Button,
  Card,
  Divider,
  EmptyState,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

const { width: SCREEN_WIDTH } = Dimensions.get('window');

// Wei conversion per token (matches frontend `formatTokenAmount` decimals)
const TOKEN_DECIMALS: Record<string, number> = {
  MCGP: 18,
  USDC: 6,
  USDT: 6,
};

// Tokens that we treat as USD-pegged for revenue display.
// Non-pegged tokens (MCGP) keep their native unit so we don't fabricate USD numbers.
const USD_PEGGED = new Set(['USDC', 'USDT']);

// Convert a wei-encoded BigInt string to a number (with decimals applied).
// Safe for typical order amounts; large amounts lose precision past ~15 digits.
function weiToNumber(weiStr: string, decimals: number): number {
  if (!weiStr) return 0;
  try {
    const value = BigInt(weiStr);
    const divisor = BigInt(10) ** BigInt(decimals);
    const whole = value / divisor;
    const frac = value % divisor;
    // Build a decimal string: "<whole>.<frac padded to decimals>"
    const fracStr = frac.toString().padStart(decimals, '0').slice(0, 6);
    return parseFloat(`${whole}.${fracStr}`);
  } catch {
    return 0;
  }
}

// Sum order amounts grouped by token, returning per-token totals.
function sumByToken(orders: Order[]): Record<string, number> {
  const totals: Record<string, number> = {};
  for (const o of orders) {
    const decimals = TOKEN_DECIMALS[o.token] ?? 18;
    const amount = weiToNumber(o.totalAmount, decimals);
    totals[o.token] = (totals[o.token] || 0) + amount;
  }
  return totals;
}

const MerchantProductDetail = () => {
  const { colors, spacing, radius } = useTheme();
  const { productId } = useLocalSearchParams<{ productId: string }>();
  const [product, setProduct] = useState<Product | null>(null);
  const [orders, setOrders] = useState<Order[]>([]);
  const [ordersError, setOrdersError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [togglingStatus, setTogglingStatus] = useState(false);

  const fetchData = useCallback(async () => {
    if (!productId) return;
    try {
      setError(null);
      setOrdersError(null);
      const [productRes, ordersRes] = await Promise.all([
        api.getProductById(productId as string),
        getOrders({ role: 'seller', limit: 50 }),
      ]);
      if (productRes.success && productRes.data) {
        setProduct(productRes.data);
      } else {
        setError(productRes.message || 'Failed to load product');
      }
      if (ordersRes.success && ordersRes.data?.orders) {
        setOrders(ordersRes.data.orders.filter((o) => o.productId === productId));
      } else {
        setOrders([]);
        setOrdersError(ordersRes.message || 'Failed to load orders');
      }
    } catch (err: any) {
      setError(err.message || 'Failed to load product');
    }
  }, [productId]);

  useEffect(() => {
    setLoading(true);
    fetchData().finally(() => setLoading(false));
  }, [fetchData]);

  const handleRefresh = async () => {
    setRefreshing(true);
    await fetchData();
    setRefreshing(false);
  };

  const handleToggleStatus = async () => {
    if (!product) return;
    const currentlyActive = product.status === 'active';
    const next = currentlyActive ? 'inactive' : 'active';
    Alert.alert(
      currentlyActive ? 'Deactivate product?' : 'Activate product?',
      currentlyActive
        ? 'Buyers will no longer see this product in the marketplace until you reactivate it.'
        : 'This product will be visible in the marketplace again.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: currentlyActive ? 'Deactivate' : 'Activate',
          style: currentlyActive ? 'destructive' : 'default',
          onPress: async () => {
            setTogglingStatus(true);
            const fd = new FormData();
            fd.append('status', next);
            const res = await api.updateProduct(product.id || (product as any)._id, fd);
            setTogglingStatus(false);
            if (res.success) {
              await fetchData();
            } else {
              Alert.alert('Error', res.message || 'Failed to update status');
            }
          },
        },
      ]
    );
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <Spinner size="large" label="Loading product…" />
        </View>
      </SafeAreaView>
    );
  }

  if (error || !product) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View
          style={{
            flex: 1,
            justifyContent: 'center',
            alignItems: 'center',
            padding: spacing['3xl'],
            gap: spacing.lg,
          }}
        >
          <View
            style={{
              width: 64,
              height: 64,
              borderRadius: 32,
              backgroundColor: colors.dangerBg,
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            <Icon name="warning" size={30} color={colors.danger} />
          </View>
          <Text variant="title3" weight="700" align="center">
            {error || 'Product not found'}
          </Text>
          <View style={{ flexDirection: 'row', gap: spacing.sm }}>
            <Button
              title="Retry"
              variant="primary"
              size="sm"
              fullWidth={false}
              onPress={() => {
                setLoading(true);
                fetchData().finally(() => setLoading(false));
              }}
            />
            <Button
              title="Go back"
              variant="secondary"
              size="sm"
              fullWidth={false}
              onPress={() => router.back()}
            />
          </View>
        </View>
      </SafeAreaView>
    );
  }

  const primaryImage = product.images?.find((i) => i.url)?.url;
  const completedOrders = orders.filter(
    (o) => o.status === 'completed' || o.status === 'delivered' || o.status === 'shipped'
  );
  const revenueByToken = sumByToken(completedOrders);
  const tokenEntries = Object.entries(revenueByToken).filter(([, v]) => v > 0);
  const usdRevenue = tokenEntries
    .filter(([t]) => USD_PEGGED.has(t))
    .reduce((sum, [, v]) => sum + v, 0);
  const otherTokens = tokenEntries.filter(([t]) => !USD_PEGGED.has(t));

  const pendingCount = orders.filter(
    (o) => o.status === 'escrowed' || o.status === 'shipped' || o.status === 'refund_requested'
  ).length;
  const isActive = product.status === 'active';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {/* Header */}
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          justifyContent: 'space-between',
          paddingHorizontal: spacing.sm,
          paddingVertical: spacing.sm,
          borderBottomWidth: 1,
          borderBottomColor: colors.hairline,
          backgroundColor: colors.surface,
        }}
      >
        <IconButton icon="chevronLeft" accessibilityLabel="Go back" onPress={() => router.back()} />
        <Text variant="title3" weight="700" numberOfLines={1} style={{ flex: 1, textAlign: 'center', marginHorizontal: spacing.sm }}>
          Product Details
        </Text>
        <IconButton
          icon="edit"
          color={colors.accent}
          accessibilityLabel="Edit product"
          onPress={() => router.push(`/merchants/inventory/edit/${product.id}`)}
        />
      </View>

      <ScrollView
        contentContainerStyle={{ paddingBottom: spacing['4xl'] * 2 }}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={handleRefresh} tintColor={colors.accent} colors={[colors.accent]} />
        }
      >
        {primaryImage ? (
          <Image
            source={{ uri: primaryImage }}
            style={{ width: SCREEN_WIDTH, height: SCREEN_WIDTH * 0.7, backgroundColor: colors.surfaceAlt }}
          />
        ) : (
          <View
            style={{
              width: SCREEN_WIDTH,
              height: SCREEN_WIDTH * 0.7,
              backgroundColor: colors.surfaceAlt,
              justifyContent: 'center',
              alignItems: 'center',
            }}
          >
            <Icon name="image" size={48} color={colors.textMuted} />
          </View>
        )}

        {/* Identity */}
        <Card style={{ marginHorizontal: spacing.lg, marginTop: spacing.md }}>
          <Text variant="title2" weight="700">
            {product.name}
          </Text>
          <View style={{ marginTop: spacing.xs, marginBottom: spacing.sm }}>
            <AmountText value={product.price} currency="USD" variant="title1" color="accent" minimumFractionDigits={2} maximumFractionDigits={2} />
          </View>
          <Badge
            label={formatStatus(product.status || 'active')}
            color={isActive ? colors.success : colors.textSecondary}
            background={isActive ? colors.successBg : colors.surfaceAlt}
          />
          {product.description ? (
            <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm }}>
              {product.description}
            </Text>
          ) : null}
        </Card>

        {/* Stats */}
        <View style={{ flexDirection: 'row', marginHorizontal: spacing.lg, marginTop: spacing.md, gap: spacing.sm }}>
          <Card padding="md" style={{ flex: 1, alignItems: 'center' }}>
            <Text variant="overline" color="textMuted">
              STOCK
            </Text>
            <Text variant="title2" weight="700" numeric color={product.stock === 0 ? 'danger' : 'textPrimary'} style={{ marginTop: spacing.xs }}>
              {product.stock}
            </Text>
          </Card>
          <Card padding="md" style={{ flex: 1, alignItems: 'center' }}>
            <Text variant="overline" color="textMuted">
              VIEWS
            </Text>
            <Text variant="title2" weight="700" numeric style={{ marginTop: spacing.xs }}>
              {product.views ?? 0}
            </Text>
          </Card>
          <Card padding="md" style={{ flex: 1, alignItems: 'center' }}>
            <Text variant="overline" color="textMuted">
              SALES
            </Text>
            <Text variant="title2" weight="700" numeric style={{ marginTop: spacing.xs }}>
              {product.sales ?? 0}
            </Text>
          </Card>
        </View>

        {/* Revenue */}
        <Card style={{ marginHorizontal: spacing.lg, marginTop: spacing.md }}>
          <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
            REVENUE
          </Text>
          {tokenEntries.length === 0 ? (
            <Text variant="bodySm" color="textMuted">
              No revenue yet
            </Text>
          ) : (
            <>
              {usdRevenue > 0 && (
                <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
                  <Text variant="bodySm" color="textSecondary">
                    USD-pegged (USDC + USDT)
                  </Text>
                  <AmountText value={usdRevenue} currency="USD" variant="bodyMed" color="accent" minimumFractionDigits={2} maximumFractionDigits={2} />
                </View>
              )}
              {otherTokens.map(([token, amount]) => (
                <View
                  key={token}
                  style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}
                >
                  <Text variant="bodySm" color="textSecondary">
                    {token}
                  </Text>
                  <Text variant="bodySm" weight="700" numeric>
                    {amount.toFixed(4)} {token}
                  </Text>
                </View>
              ))}
              <Text variant="caption" color="textMuted" style={{ marginTop: spacing.sm, fontStyle: 'italic' }}>
                From {completedOrders.length} shipped/delivered/completed order(s)
              </Text>
            </>
          )}
        </Card>

        {/* Orders */}
        <Card style={{ marginHorizontal: spacing.lg, marginTop: spacing.md }}>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: spacing.sm }}>
            <View>
              <Text variant="overline" color="textMuted">
                ORDERS ({orders.length})
              </Text>
              {pendingCount > 0 && (
                <Text variant="caption" weight="700" color="accent" style={{ marginTop: 2 }}>
                  {pendingCount} awaiting action
                </Text>
              )}
            </View>
            {orders.length > 0 && (
              <PressableScale onPress={() => router.push('/merchants/orders' as any)}>
                <Text variant="bodySm" weight="600" color="accent">
                  View all
                </Text>
              </PressableScale>
            )}
          </View>

          {ordersError ? (
            <AccentCard railColor={colors.warning} padding="md" style={{ backgroundColor: colors.warningBg }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="warning" size={16} color={colors.warning} />
                <Text variant="caption" color="warning" style={{ flex: 1 }}>
                  {ordersError}. Pull to refresh.
                </Text>
              </View>
            </AccentCard>
          ) : orders.length === 0 ? (
            <EmptyState icon="list" title="No orders yet for this product" />
          ) : (
            orders.slice(0, 5).map((order, idx) => {
              const statusColor = STATUS_COLORS[order.status] || STATUS_COLORS.cancelled;
              const buyerLabel =
                order.buyer?.name?.trim() ||
                (order.buyer?.username ? `@${order.buyer.username}` : '') ||
                'Buyer';
              return (
                <View key={order.id}>
                  {idx > 0 && <Divider />}
                  <PressableScale
                    onPress={() => router.push(`/merchants/orders/${order.id}` as any)}
                    style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm }}
                  >
                    <View style={{ flex: 1 }}>
                      <Text variant="bodySm" weight="700" numberOfLines={1}>
                        #{order.id.slice(0, 8)} · {buyerLabel}
                      </Text>
                      <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                        {formatDate(order.createdAt)}
                      </Text>
                    </View>
                    <View style={{ alignItems: 'flex-end', marginRight: spacing.sm }}>
                      <Text variant="bodySm" weight="700" numeric>
                        {formatTokenAmount(order.totalAmount, order.token)}
                      </Text>
                      <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                        Qty {order.quantity}
                      </Text>
                    </View>
                    <Badge label={formatStatus(order.status)} color={statusColor.text} background={statusColor.bg} size="sm" />
                  </PressableScale>
                </View>
              );
            })
          )}
        </Card>

        {/* Shipping rates */}
        <Card style={{ marginHorizontal: spacing.lg, marginTop: spacing.md }}>
          <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
            SHIPPING RATES
          </Text>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">
              Same Residential Area
            </Text>
            <Text variant="bodySm" weight="700">
              Free
            </Text>
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">
              Same City
            </Text>
            <AmountText value={product.shippingSameCity ?? 0} currency="USD" variant="bodySm" weight="400" minimumFractionDigits={2} maximumFractionDigits={2} />
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">
              Same State
            </Text>
            <AmountText value={product.shippingSameState ?? 0} currency="USD" variant="bodySm" weight="400" minimumFractionDigits={2} maximumFractionDigits={2} />
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">
              Same Country
            </Text>
            <AmountText value={product.shippingSameCountry ?? 0} currency="USD" variant="bodySm" weight="400" minimumFractionDigits={2} maximumFractionDigits={2} />
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">
              International
            </Text>
            <AmountText value={product.shippingInternational ?? 0} currency="USD" variant="bodySm" weight="400" minimumFractionDigits={2} maximumFractionDigits={2} />
          </View>
        </Card>

        {product.location && (
          <Card style={{ marginHorizontal: spacing.lg, marginTop: spacing.md }}>
            <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
              LOCATION
            </Text>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
              <Icon name="nearMe" size={16} color={colors.accent} />
              <Text variant="bodySm">{product.location}</Text>
            </View>
          </Card>
        )}
      </ScrollView>

      {/* Action bar — Edit + Toggle Active */}
      <View
        style={{
          position: 'absolute',
          bottom: 0,
          left: 0,
          right: 0,
          flexDirection: 'row',
          gap: spacing.sm,
          padding: spacing.lg,
          backgroundColor: colors.surface,
          borderTopWidth: 1,
          borderTopColor: colors.hairline,
        }}
      >
        <PressableScale
          onPress={handleToggleStatus}
          disabled={togglingStatus}
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            justifyContent: 'center',
            gap: spacing.xs,
            height: 56,
            paddingHorizontal: spacing.lg,
            borderRadius: radius.md,
            borderWidth: 1.5,
            borderColor: isActive ? colors.danger : colors.success,
            backgroundColor: isActive ? colors.dangerBg : colors.successBg,
            opacity: togglingStatus ? 0.5 : 1,
          }}
        >
          {togglingStatus ? (
            <Spinner size="small" color={isActive ? colors.danger : colors.success} />
          ) : (
            <>
              <Icon name={isActive ? 'pause' : 'play'} size={18} color={isActive ? colors.danger : colors.success} />
              <Text variant="bodySm" weight="700" style={{ color: isActive ? colors.danger : colors.success }}>
                {isActive ? 'Deactivate' : 'Activate'}
              </Text>
            </>
          )}
        </PressableScale>
        <Button
          title="Edit Product"
          leftIcon="edit"
          size="lg"
          style={{ flex: 1 }}
          onPress={() => router.push(`/merchants/inventory/edit/${product.id}`)}
        />
      </View>
    </SafeAreaView>
  );
};

export default MerchantProductDetail;
