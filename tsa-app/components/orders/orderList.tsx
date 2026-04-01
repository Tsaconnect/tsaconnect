import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  StyleSheet,
  FlatList,
  ActivityIndicator,
  Text,
  TouchableOpacity,
  RefreshControl,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { getOrders, formatTokenAmount, Order } from '@/services/orderApi';
import { STATUS_COLORS, formatStatus, formatDate } from '@/constants/orderStatus';

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
      <TouchableOpacity
        style={styles.orderCard}
        onPress={() => router.push(`/(dashboard)/orderdetail?id=${item.id}`)}
        activeOpacity={0.7}
      >
        <View style={styles.orderCardHeader}>
          <Text style={styles.orderId} numberOfLines={1}>
            Order #{item.id.slice(0, 8)}...
          </Text>
          <View style={[styles.statusBadge, { backgroundColor: statusColor.bg }]}>
            <Text style={[styles.statusText, { color: statusColor.text }]}>
              {formatStatus(item.status)}
            </Text>
          </View>
        </View>

        <View style={styles.orderCardBody}>
          <View style={styles.orderInfoRow}>
            <Ionicons name="cube-outline" size={16} color="#A67C52" />
            <Text style={styles.orderInfoText}>Qty: {item.quantity}</Text>
          </View>
          <View style={styles.orderInfoRow}>
            <Ionicons name="wallet-outline" size={16} color="#A67C52" />
            <Text style={styles.orderInfoText}>
              {formatTokenAmount(item.totalAmount, item.token)}
            </Text>
          </View>
          <View style={styles.orderInfoRow}>
            <Ionicons name="calendar-outline" size={16} color="#A67C52" />
            <Text style={styles.orderInfoText}>{formatDate(item.createdAt)}</Text>
          </View>
        </View>

        <View style={styles.orderCardFooter}>
          <Text style={styles.viewDetail}>View Details</Text>
          <Ionicons name="chevron-forward" size={16} color="#8B5A2B" />
        </View>
      </TouchableOpacity>
    );
  };

  return (
    <View style={styles.container}>
      {/* Status filter tabs */}
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

      {loading ? (
        <View style={styles.centered}>
          <ActivityIndicator size="large" color="#8B5A2B" />
        </View>
      ) : (
        <FlatList
          data={orders}
          keyExtractor={(item) => item.id}
          renderItem={renderOrderCard}
          contentContainerStyle={orders.length === 0 ? styles.centered : styles.listContent}
          refreshControl={
            <RefreshControl
              refreshing={refreshing}
              onRefresh={handleRefresh}
              tintColor="#8B5A2B"
            />
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
                {error || 'Your order history will appear here'}
              </Text>
            </View>
          }
        />
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#FDF8F3',
  },
  centered: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },
  filterList: {
    flexGrow: 0,
  },
  filterContainer: {
    paddingHorizontal: 16,
    paddingVertical: 12,
    gap: 8,
  },
  filterPill: {
    paddingHorizontal: 16,
    paddingVertical: 8,
    borderRadius: 20,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: '#E8D5C0',
    marginRight: 8,
  },
  filterPillActive: {
    backgroundColor: '#8B5A2B',
    borderColor: '#8B5A2B',
  },
  filterPillText: {
    fontSize: 13,
    fontWeight: '500',
    color: '#8B5A2B',
  },
  filterPillTextActive: {
    color: '#FFF',
  },
  listContent: {
    padding: 16,
    paddingTop: 4,
  },
  orderCard: {
    backgroundColor: '#FFF',
    borderRadius: 16,
    padding: 16,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#E8D5C0',
    shadowColor: '#8B5A2B',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  orderCardHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  orderId: {
    fontSize: 15,
    fontWeight: '600',
    color: '#4A2C1A',
    flex: 1,
    marginRight: 8,
  },
  statusBadge: {
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: 12,
  },
  statusText: {
    fontSize: 11,
    fontWeight: '600',
  },
  orderCardBody: {
    gap: 6,
  },
  orderInfoRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
  },
  orderInfoText: {
    fontSize: 13,
    color: '#4A2C1A',
  },
  orderCardFooter: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'flex-end',
    marginTop: 12,
    paddingTop: 12,
    borderTopWidth: 1,
    borderTopColor: '#F0E0D0',
  },
  viewDetail: {
    fontSize: 13,
    fontWeight: '600',
    color: '#8B5A2B',
    marginRight: 4,
  },
  emptyContainer: {
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 80,
    paddingHorizontal: 32,
  },
  emptyTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: '#4A2C1A',
    marginTop: 16,
  },
  emptySubtitle: {
    fontSize: 14,
    color: '#A67C52',
    textAlign: 'center',
    marginTop: 8,
    lineHeight: 20,
  },
});

export default OrderList;
