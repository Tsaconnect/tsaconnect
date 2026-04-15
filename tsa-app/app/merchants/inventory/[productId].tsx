import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  Image,
  ActivityIndicator,
  Dimensions,
  RefreshControl,
  Alert,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router, useLocalSearchParams } from 'expo-router';
import api, { Product } from '@/components/services/api';
import { getOrders, formatTokenAmount, Order } from '@/services/orderApi';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';

const GOLD = '#D4AF37';
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
      <View style={styles.centered}>
        <ActivityIndicator size="large" color={GOLD} />
      </View>
    );
  }

  if (error || !product) {
    return (
      <View style={styles.centered}>
        <Ionicons name="alert-circle-outline" size={64} color="#D9B68B" />
        <Text style={styles.errorTitle}>{error || 'Product not found'}</Text>
        <View style={{ flexDirection: 'row', gap: 10, marginTop: 16 }}>
          <TouchableOpacity style={styles.retryBtn} onPress={() => { setLoading(true); fetchData().finally(() => setLoading(false)); }}>
            <Text style={styles.retryText}>Retry</Text>
          </TouchableOpacity>
          <TouchableOpacity style={[styles.retryBtn, { backgroundColor: '#888' }]} onPress={() => router.back()}>
            <Text style={styles.retryText}>Go back</Text>
          </TouchableOpacity>
        </View>
      </View>
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
    <View style={{ flex: 1, backgroundColor: '#FAFAFA' }}>
      <View style={styles.header}>
        <TouchableOpacity onPress={() => router.back()} style={styles.backBtn}>
          <Ionicons name="chevron-back" size={24} color="#1A1A1A" />
        </TouchableOpacity>
        <Text style={styles.title} numberOfLines={1}>Product Details</Text>
        <TouchableOpacity
          onPress={() => router.push(`/merchants/inventory/edit/${product.id}`)}
          style={styles.editBtn}
        >
          <Ionicons name="create-outline" size={22} color={GOLD} />
        </TouchableOpacity>
      </View>

      <ScrollView
        contentContainerStyle={styles.content}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={handleRefresh} tintColor={GOLD} />}
      >
        {primaryImage ? (
          <Image source={{ uri: primaryImage }} style={styles.image} />
        ) : (
          <View style={[styles.image, styles.imagePlaceholder]}>
            <Ionicons name="image-outline" size={48} color="#CCC" />
          </View>
        )}

        <View style={styles.card}>
          <Text style={styles.name}>{product.name}</Text>
          <Text style={styles.price}>${product.price.toFixed(2)}</Text>
          <View style={[styles.statusPill, isActive ? styles.statusActive : styles.statusInactive]}>
            <Text style={[styles.statusPillText, !isActive && { color: '#6B7280' }]}>
              {formatStatus(product.status || 'active')}
            </Text>
          </View>
          {product.description ? <Text style={styles.desc}>{product.description}</Text> : null}
        </View>

        {/* Stats */}
        <View style={styles.statsRow}>
          <View style={styles.statCard}>
            <Text style={styles.statLabel}>Stock</Text>
            <Text style={[styles.statValue, product.stock === 0 && { color: '#DC2626' }]}>
              {product.stock}
            </Text>
          </View>
          <View style={styles.statCard}>
            <Text style={styles.statLabel}>Views</Text>
            <Text style={styles.statValue}>{product.views ?? 0}</Text>
          </View>
          <View style={styles.statCard}>
            <Text style={styles.statLabel}>Sales</Text>
            <Text style={styles.statValue}>{product.sales ?? 0}</Text>
          </View>
        </View>

        {/* Revenue */}
        <View style={styles.card}>
          <Text style={styles.cardLabel}>REVENUE</Text>
          {tokenEntries.length === 0 ? (
            <Text style={styles.muted}>No revenue yet</Text>
          ) : (
            <>
              {usdRevenue > 0 && (
                <View style={styles.rowBetween}>
                  <Text style={styles.muted}>USD-pegged (USDC + USDT)</Text>
                  <Text style={[styles.bold, { color: GOLD }]}>${usdRevenue.toFixed(2)}</Text>
                </View>
              )}
              {otherTokens.map(([token, amount]) => (
                <View key={token} style={styles.rowBetween}>
                  <Text style={styles.muted}>{token}</Text>
                  <Text style={styles.bold}>{amount.toFixed(4)} {token}</Text>
                </View>
              ))}
              <Text style={[styles.muted, { fontSize: 11, marginTop: 6, fontStyle: 'italic' }]}>
                From {completedOrders.length} shipped/delivered/completed order(s)
              </Text>
            </>
          )}
        </View>

        {/* Orders header */}
        <View style={styles.card}>
          <View style={styles.ordersHeader}>
            <View>
              <Text style={styles.cardLabel}>ORDERS ({orders.length})</Text>
              {pendingCount > 0 && (
                <Text style={styles.pendingText}>{pendingCount} awaiting action</Text>
              )}
            </View>
            {orders.length > 0 && (
              <TouchableOpacity onPress={() => router.push('/merchants/orders' as any)}>
                <Text style={styles.viewAll}>View all</Text>
              </TouchableOpacity>
            )}
          </View>

          {ordersError ? (
            <View style={styles.errorBanner}>
              <Ionicons name="warning-outline" size={16} color="#92400E" />
              <Text style={styles.errorBannerText}>{ordersError}. Pull to refresh.</Text>
            </View>
          ) : orders.length === 0 ? (
            <View style={styles.emptyOrders}>
              <Ionicons name="receipt-outline" size={40} color="#D9B68B" />
              <Text style={styles.emptyOrdersText}>No orders yet for this product</Text>
            </View>
          ) : (
            orders.slice(0, 5).map((order) => {
              const statusColor = STATUS_COLORS[order.status] || STATUS_COLORS.cancelled;
              const buyerLabel =
                order.buyer?.name?.trim() ||
                (order.buyer?.username ? `@${order.buyer.username}` : '') ||
                'Buyer';
              return (
                <TouchableOpacity
                  key={order.id}
                  style={styles.orderItem}
                  onPress={() => router.push(`/merchants/orders/${order.id}` as any)}
                  activeOpacity={0.7}
                >
                  <View style={{ flex: 1 }}>
                    <Text style={styles.orderItemId}>#{order.id.slice(0, 8)} · {buyerLabel}</Text>
                    <Text style={styles.orderItemDate}>{formatDate(order.createdAt)}</Text>
                  </View>
                  <View style={{ alignItems: 'flex-end', marginRight: 8 }}>
                    <Text style={styles.orderItemAmount}>{formatTokenAmount(order.totalAmount, order.token)}</Text>
                    <Text style={styles.orderItemQty}>Qty {order.quantity}</Text>
                  </View>
                  <View style={[styles.statusBadge, { backgroundColor: statusColor.bg }]}>
                    <Text style={[styles.statusText, { color: statusColor.text }]}>{formatStatus(order.status)}</Text>
                  </View>
                </TouchableOpacity>
              );
            })
          )}
        </View>

        {/* Shipping rates */}
        <View style={styles.card}>
          <Text style={styles.cardLabel}>SHIPPING RATES</Text>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>Same Residential Area</Text>
            <Text style={styles.bold}>Free</Text>
          </View>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>Same City</Text>
            <Text>${(product.shippingSameCity ?? 0).toFixed(2)}</Text>
          </View>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>Same State</Text>
            <Text>${(product.shippingSameState ?? 0).toFixed(2)}</Text>
          </View>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>Same Country</Text>
            <Text>${(product.shippingSameCountry ?? 0).toFixed(2)}</Text>
          </View>
          <View style={styles.rowBetween}>
            <Text style={styles.muted}>International</Text>
            <Text>${(product.shippingInternational ?? 0).toFixed(2)}</Text>
          </View>
        </View>

        {product.location && (
          <View style={styles.card}>
            <Text style={styles.cardLabel}>LOCATION</Text>
            <View style={styles.rowStart}>
              <Ionicons name="location-outline" size={16} color={GOLD} />
              <Text style={{ marginLeft: 6 }}>{product.location}</Text>
            </View>
          </View>
        )}
      </ScrollView>

      {/* Action bar — Edit + Toggle Active */}
      <View style={styles.actionBar}>
        <TouchableOpacity
          style={[styles.toggleBtn, isActive ? styles.toggleBtnActive : styles.toggleBtnInactive, togglingStatus && { opacity: 0.5 }]}
          onPress={handleToggleStatus}
          disabled={togglingStatus}
        >
          {togglingStatus ? (
            <ActivityIndicator color={isActive ? '#DC2626' : '#16A34A'} size="small" />
          ) : (
            <>
              <Ionicons
                name={isActive ? 'pause-circle-outline' : 'play-circle-outline'}
                size={18}
                color={isActive ? '#DC2626' : '#16A34A'}
              />
              <Text style={[styles.toggleBtnText, { color: isActive ? '#DC2626' : '#16A34A' }]}>
                {isActive ? 'Deactivate' : 'Activate'}
              </Text>
            </>
          )}
        </TouchableOpacity>
        <TouchableOpacity
          style={styles.editActionBtn}
          onPress={() => router.push(`/merchants/inventory/edit/${product.id}`)}
        >
          <Ionicons name="create-outline" size={18} color="#FFF" />
          <Text style={styles.editActionText}>Edit Product</Text>
        </TouchableOpacity>
      </View>
    </View>
  );
};

export default MerchantProductDetail;

const styles = StyleSheet.create({
  centered: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 32, backgroundColor: '#FAFAFA' },
  errorTitle: { fontSize: 16, fontWeight: '700', color: '#1A1A1A', marginTop: 16, textAlign: 'center' },

  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 16,
    paddingVertical: 12,
    backgroundColor: '#FFF',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  backBtn: { width: 40, height: 40, justifyContent: 'center', alignItems: 'center' },
  editBtn: { width: 40, height: 40, justifyContent: 'center', alignItems: 'center' },
  title: { fontSize: 17, fontWeight: '700', color: '#1A1A1A', flex: 1, textAlign: 'center', marginHorizontal: 8 },

  content: { paddingBottom: 110 },

  image: { width: SCREEN_WIDTH, height: SCREEN_WIDTH * 0.7, backgroundColor: '#F0F0F0' },
  imagePlaceholder: { justifyContent: 'center', alignItems: 'center' },

  card: {
    backgroundColor: '#FFF',
    borderRadius: 16,
    padding: 16,
    marginHorizontal: 16,
    marginTop: 12,
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  cardLabel: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1, marginBottom: 10 },

  name: { fontSize: 20, fontWeight: '700', color: '#1A1A1A', marginBottom: 4 },
  price: { fontSize: 22, fontWeight: '700', color: GOLD, marginBottom: 8 },
  desc: { fontSize: 14, color: '#555', lineHeight: 20, marginTop: 8 },

  statusPill: {
    alignSelf: 'flex-start',
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: 10,
  },
  statusActive: { backgroundColor: '#D1FAE5' },
  statusInactive: { backgroundColor: '#F5F5F5' },
  statusPillText: { fontSize: 11, fontWeight: '700', color: '#065F46' },

  statsRow: { flexDirection: 'row', marginHorizontal: 16, marginTop: 12, gap: 10 },
  statCard: {
    flex: 1,
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 14,
    alignItems: 'center',
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  statLabel: { fontSize: 11, color: '#888', fontWeight: '600', letterSpacing: 0.5, marginBottom: 4 },
  statValue: { fontSize: 22, fontWeight: '700', color: '#1A1A1A' },

  ordersHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
    marginBottom: 10,
  },
  pendingText: { fontSize: 11, color: GOLD, fontWeight: '700', marginTop: 2 },
  viewAll: { fontSize: 13, color: GOLD, fontWeight: '600' },

  orderItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 10,
    borderTopWidth: 1,
    borderTopColor: '#F5F5F5',
  },
  orderItemId: { fontSize: 13, fontWeight: '700', color: '#1A1A1A' },
  orderItemDate: { fontSize: 11, color: '#888', marginTop: 2 },
  orderItemAmount: { fontSize: 13, fontWeight: '700', color: '#1A1A1A' },
  orderItemQty: { fontSize: 11, color: '#888', marginTop: 2 },
  statusBadge: { paddingHorizontal: 8, paddingVertical: 3, borderRadius: 10 },
  statusText: { fontSize: 10, fontWeight: '700' },

  emptyOrders: { alignItems: 'center', paddingVertical: 20 },
  emptyOrdersText: { fontSize: 13, color: '#888', marginTop: 8 },
  errorBanner: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    backgroundColor: '#FEF3C7',
    paddingVertical: 10,
    paddingHorizontal: 12,
    borderRadius: 10,
  },
  errorBannerText: { fontSize: 12, color: '#92400E', flex: 1 },

  rowBetween: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingVertical: 5,
  },
  rowStart: { flexDirection: 'row', alignItems: 'center' },
  muted: { color: '#888', fontSize: 13 },
  bold: { fontWeight: '700', color: '#1A1A1A', fontSize: 13 },

  actionBar: {
    position: 'absolute',
    bottom: 0, left: 0, right: 0,
    backgroundColor: '#FFF',
    padding: 16,
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
    flexDirection: 'row',
    gap: 10,
  },
  toggleBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 6,
    paddingVertical: 14,
    paddingHorizontal: 16,
    borderRadius: 14,
    borderWidth: 1.5,
  },
  toggleBtnActive: { borderColor: '#DC2626', backgroundColor: '#FEE2E2' },
  toggleBtnInactive: { borderColor: '#16A34A', backgroundColor: '#D1FAE5' },
  toggleBtnText: { fontWeight: '700', fontSize: 14 },
  editActionBtn: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 8,
    paddingVertical: 14,
    borderRadius: 14,
    backgroundColor: GOLD,
  },
  editActionText: { color: '#FFF', fontWeight: '700', fontSize: 15 },

  retryBtn: {
    paddingHorizontal: 20,
    paddingVertical: 10,
    backgroundColor: GOLD,
    borderRadius: 20,
  },
  retryText: { color: '#FFF', fontWeight: '700', fontSize: 14 },
});
