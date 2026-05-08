import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  StyleSheet,
  FlatList,
  ActivityIndicator,
  Text,
  TouchableOpacity,
  RefreshControl,
  Image,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { getOrders, formatTokenAmount, Order } from '@/services/orderApi';
import {
  listContactPayments,
  ContactPayment,
} from '@/services/serviceContactApi';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';

const GOLD = '#D4AF37';

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

    return (
      <TouchableOpacity
        style={[styles.orderCard, needsAction && styles.orderCardNeedsAction]}
        onPress={() => router.push(`/merchants/orders/${item.id}` as any)}
        activeOpacity={0.7}
      >
        <View style={styles.orderCardHeader}>
          <View style={{ flex: 1, marginRight: 8 }}>
            <Text style={styles.productName} numberOfLines={1}>{productName}</Text>
            <Text style={styles.orderId} numberOfLines={1}>#{item.id.slice(0, 8)} · {buyerLabel}</Text>
          </View>
          <View style={[styles.statusBadge, { backgroundColor: statusColor.bg }]}>
            <Text style={[styles.statusText, { color: statusColor.text }]}>
              {formatStatus(item.status)}
            </Text>
          </View>
        </View>

        <View style={styles.orderCardBody}>
          {item.product?.imageUrl ? (
            <Image source={{ uri: item.product.imageUrl }} style={styles.thumbnail} />
          ) : (
            <View style={[styles.thumbnail, styles.thumbnailPlaceholder]}>
              <Ionicons name="cube-outline" size={24} color="#CCC" />
            </View>
          )}
          <View style={{ flex: 1, marginLeft: 12 }}>
            <View style={styles.orderInfoRow}>
              <Ionicons name="cube-outline" size={14} color={GOLD} />
              <Text style={styles.orderInfoText}>Qty {item.quantity}</Text>
            </View>
            <View style={styles.orderInfoRow}>
              <Ionicons name="wallet-outline" size={14} color={GOLD} />
              <Text style={styles.orderInfoText}>{formatTokenAmount(item.totalAmount, item.token)}</Text>
            </View>
            <View style={styles.orderInfoRow}>
              <Ionicons name="calendar-outline" size={14} color={GOLD} />
              <Text style={styles.orderInfoText}>{formatDate(item.createdAt)}</Text>
            </View>
          </View>
        </View>

        <View style={styles.orderCardFooter}>
          {needsAction ? (
            <Text style={styles.actionHint}>
              {item.status === 'escrowed' && '⚡ Ready to ship'}
              {item.status === 'shipped' && '📦 Awaiting confirmation'}
              {item.status === 'refund_requested' && (item.merchantApprovedRefund ? '⏳ Refund approved · awaiting admin' : '⚠ Refund requested')}
            </Text>
          ) : (
            <Text style={styles.viewDetail}>View Details</Text>
          )}
          <Ionicons name="chevron-forward" size={16} color={GOLD} />
        </View>
      </TouchableOpacity>
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
      <View style={styles.orderCard}>
        <View style={styles.orderCardHeader}>
          <View style={{ flex: 1, marginRight: 8 }}>
            <Text style={styles.productName} numberOfLines={1}>{serviceName}</Text>
            <Text style={styles.orderId} numberOfLines={1}>#{item.id.slice(0, 8)} · {buyerLabel}</Text>
          </View>
          <View style={[styles.statusBadge, { backgroundColor: '#E6F9EE' }]}>
            <Text style={[styles.statusText, { color: '#16A34A' }]}>PAID</Text>
          </View>
        </View>

        <View style={styles.orderCardBody}>
          <View style={[styles.thumbnail, styles.thumbnailPlaceholder]}>
            <Ionicons name="briefcase-outline" size={24} color={GOLD} />
          </View>
          <View style={{ flex: 1, marginLeft: 12 }}>
            <View style={styles.orderInfoRow}>
              <Ionicons name="cash-outline" size={14} color={GOLD} />
              <Text style={styles.orderInfoText}>You earned {earnedDisplay} of {totalDisplay} ({item.token})</Text>
            </View>
            {item.counterparty?.email ? (
              <View style={styles.orderInfoRow}>
                <Ionicons name="mail-outline" size={14} color={GOLD} />
                <Text style={styles.orderInfoText} numberOfLines={1}>{item.counterparty.email}</Text>
              </View>
            ) : null}
            <View style={styles.orderInfoRow}>
              <Ionicons name="calendar-outline" size={14} color={GOLD} />
              <Text style={styles.orderInfoText}>{formatDate(item.createdAt)}</Text>
            </View>
          </View>
        </View>

        <View style={styles.orderCardFooter}>
          <Text style={styles.viewDetail}>Settled on-chain</Text>
          <Ionicons name="checkmark-circle" size={16} color="#16A34A" />
        </View>
      </View>
    );
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <TouchableOpacity onPress={() => router.back()} style={styles.backBtn}>
          <Ionicons name="chevron-back" size={24} color="#1A1A1A" />
        </TouchableOpacity>
        <Text style={styles.title}>Orders</Text>
        <View style={{ width: 40 }} />
      </View>

      {/* Mode tabs: Products (escrow) vs Services (contact-fee) */}
      <View style={styles.modeRow}>
        {(['products', 'services'] as const).map((m) => (
          <TouchableOpacity
            key={m}
            style={[styles.modeTab, mode === m && styles.modeTabActive]}
            onPress={() => setMode(m)}
            activeOpacity={0.7}
          >
            <Text style={[styles.modeTabText, mode === m && styles.modeTabTextActive]}>
              {m === 'products' ? 'Products' : 'Services'}
            </Text>
          </TouchableOpacity>
        ))}
      </View>

      {/* Status filter — only meaningful for the products escrow flow */}
      {mode === 'products' && (
        <FlatList
          horizontal
          showsHorizontalScrollIndicator={false}
          data={STATUS_FILTERS}
          keyExtractor={(item) => item.key}
          contentContainerStyle={styles.filterContainer}
          style={styles.filterList}
          renderItem={({ item: filter }) => (
            <TouchableOpacity
              style={[
                styles.filterPill,
                selectedStatus === filter.key && styles.filterPillActive,
              ]}
              onPress={() => setSelectedStatus(filter.key)}
            >
              <Text
                style={[
                  styles.filterPillText,
                  selectedStatus === filter.key && styles.filterPillTextActive,
                ]}
              >
                {filter.label}
              </Text>
            </TouchableOpacity>
          )}
        />
      )}

      {loading ? (
        <View style={styles.centered}>
          <ActivityIndicator size="large" color={GOLD} />
        </View>
      ) : mode === 'products' ? (
        <FlatList
          data={orders}
          keyExtractor={(item) => item.id}
          renderItem={renderOrderCard}
          contentContainerStyle={orders.length === 0 ? styles.centered : styles.listContent}
          refreshControl={
            <RefreshControl refreshing={refreshing} onRefresh={handleRefresh} tintColor={GOLD} />
          }
          onEndReached={handleLoadMore}
          onEndReachedThreshold={0.3}
          ListEmptyComponent={
            <View style={styles.emptyContainer}>
              <Ionicons name="receipt-outline" size={64} color="#D9B68B" />
              <Text style={styles.emptyTitle}>
                {error ? 'Something went wrong' : 'No orders yet'}
              </Text>
              <Text style={styles.emptySubtitle}>
                {error || "Orders from your customers will appear here"}
              </Text>
              {error && (
                <TouchableOpacity style={styles.retryBtn} onPress={() => fetchOrders(1, false)}>
                  <Text style={styles.retryText}>Try again</Text>
                </TouchableOpacity>
              )}
            </View>
          }
        />
      ) : (
        <FlatList
          data={servicePayments}
          keyExtractor={(item) => item.id}
          renderItem={renderServiceCard}
          contentContainerStyle={servicePayments.length === 0 ? styles.centered : styles.listContent}
          refreshControl={
            <RefreshControl refreshing={refreshing} onRefresh={handleRefresh} tintColor={GOLD} />
          }
          onEndReached={handleLoadMore}
          onEndReachedThreshold={0.3}
          ListEmptyComponent={
            <View style={styles.emptyContainer}>
              <Ionicons name="briefcase-outline" size={64} color="#D9B68B" />
              <Text style={styles.emptyTitle}>
                {error ? 'Something went wrong' : 'No service requests yet'}
              </Text>
              <Text style={styles.emptySubtitle}>
                {error || 'When a buyer unlocks your contact info, the request appears here.'}
              </Text>
              {error && (
                <TouchableOpacity style={styles.retryBtn} onPress={() => fetchServicePayments(1, false)}>
                  <Text style={styles.retryText}>Try again</Text>
                </TouchableOpacity>
              )}
            </View>
          }
        />
      )}
    </View>
  );
};

export default MerchantOrders;

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
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
  title: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },

  modeRow: {
    flexDirection: 'row',
    backgroundColor: '#FFF',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  modeTab: {
    flex: 1,
    paddingVertical: 14,
    alignItems: 'center',
    borderBottomWidth: 2,
    borderBottomColor: 'transparent',
  },
  modeTabActive: { borderBottomColor: GOLD },
  modeTabText: { fontSize: 14, fontWeight: '600', color: '#888' },
  modeTabTextActive: { color: '#1A1A1A', fontWeight: '700' },

  filterList: { maxHeight: 52, flexGrow: 0 },
  filterContainer: { paddingHorizontal: 16, paddingVertical: 12, gap: 8 },
  filterPill: {
    paddingHorizontal: 14,
    paddingVertical: 8,
    borderRadius: 20,
    backgroundColor: '#F5F5F5',
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  filterPillActive: { backgroundColor: '#FFF8E1', borderColor: GOLD },
  filterPillText: { fontSize: 13, color: '#888', fontWeight: '600' },
  filterPillTextActive: { color: '#1A1A1A', fontWeight: '700' },

  listContent: { padding: 16, paddingBottom: 40 },
  centered: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 32 },

  orderCard: {
    backgroundColor: '#FFF',
    borderRadius: 16,
    padding: 16,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  orderCardNeedsAction: { borderColor: GOLD, borderWidth: 1.5 },
  orderCardHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
    marginBottom: 12,
  },
  productName: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  orderId: { fontSize: 11, color: '#888', marginTop: 2 },
  statusBadge: { paddingHorizontal: 10, paddingVertical: 4, borderRadius: 12 },
  statusText: { fontSize: 11, fontWeight: '700', letterSpacing: 0.3 },

  orderCardBody: { flexDirection: 'row', marginBottom: 12 },
  thumbnail: { width: 56, height: 56, borderRadius: 8, backgroundColor: '#F0F0F0' },
  thumbnailPlaceholder: { justifyContent: 'center', alignItems: 'center' },
  orderInfoRow: { flexDirection: 'row', alignItems: 'center', gap: 6, paddingVertical: 2 },
  orderInfoText: { fontSize: 12, color: '#555' },

  orderCardFooter: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingTop: 10,
    borderTopWidth: 1,
    borderTopColor: '#F5F5F5',
  },
  viewDetail: { fontSize: 13, color: GOLD, fontWeight: '600' },
  actionHint: { fontSize: 13, color: GOLD, fontWeight: '700' },

  emptyContainer: { alignItems: 'center', padding: 32 },
  emptyTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginTop: 16 },
  emptySubtitle: { fontSize: 13, color: '#888', marginTop: 6, textAlign: 'center' },
  retryBtn: {
    marginTop: 16,
    paddingHorizontal: 20,
    paddingVertical: 10,
    backgroundColor: GOLD,
    borderRadius: 20,
  },
  retryText: { color: '#FFF', fontWeight: '700', fontSize: 14 },
});
