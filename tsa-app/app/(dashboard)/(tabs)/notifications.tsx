import React, { useEffect, useState, useCallback } from 'react';
import {
  View,
  Text,
  FlatList,
  TouchableOpacity,
  RefreshControl,
  StyleSheet,
  ActivityIndicator,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useRouter } from 'expo-router';
import moment from 'moment';
import { useNotifications } from '../../../contexts/NotificationContext';
import { COLORS } from '../../../constants/theme';
import api from '@/components/services/api';

const CATEGORY_ICONS: Record<string, string> = {
  transaction: 'swap-horizontal',
  order: 'cart',
  security: 'shield',
  verification: 'checkmark-circle',
  merchant: 'storefront',
  product: 'pricetag',
  wallet: 'wallet',
};

const NotificationsScreen: React.FC = () => {
  const { notifications: realtimeNotifications, setNotifications, refreshUnreadCount } = useNotifications();
  const [notifications, setLocalNotifications] = useState<any[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const router = useRouter();

  const fetchNotifications = useCallback(async (pageNum: number, refresh = false) => {
    try {
      const res = await api.getNotifications(pageNum);
      if (res.success) {
        const items = res.data.notifications || [];
        if (refresh) {
          setLocalNotifications(items);
        } else {
          setLocalNotifications((prev) => [...prev, ...items]);
        }
        setHasMore(items.length >= 20);
      }
    } catch {} finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    fetchNotifications(1, true);
  }, [fetchNotifications]);

  const mergedNotifications = [
    ...realtimeNotifications.filter(
      (rt) => !notifications.find((n) => n.id === rt.id)
    ),
    ...notifications,
  ];

  const onRefresh = useCallback(() => {
    setRefreshing(true);
    setPage(1);
    setNotifications([]);
    fetchNotifications(1, true);
    refreshUnreadCount();
  }, [fetchNotifications, refreshUnreadCount, setNotifications]);

  const loadMore = useCallback(() => {
    if (!hasMore || loading) return;
    const nextPage = page + 1;
    setPage(nextPage);
    fetchNotifications(nextPage);
  }, [page, hasMore, loading, fetchNotifications]);

  const handlePress = useCallback(async (item: any) => {
    if (!item.isRead) {
      await api.markAsRead(item.id);
      setLocalNotifications((prev) =>
        prev.map((n) => (n.id === item.id ? { ...n, isRead: true } : n))
      );
      refreshUnreadCount();
    }
  }, [refreshUnreadCount]);

  const handleMarkAllRead = useCallback(async () => {
    await api.markAllAsRead();
    setLocalNotifications((prev) => prev.map((n) => ({ ...n, isRead: true })));
    setNotifications([]);
    refreshUnreadCount();
  }, [refreshUnreadCount, setNotifications]);

  const getIcon = (type: string) => {
    const category = type.split('.')[0];
    return CATEGORY_ICONS[category] || 'notifications';
  };

  const renderItem = ({ item }: { item: any }) => (
    <TouchableOpacity
      style={[styles.item, !item.isRead && styles.unread]}
      onPress={() => handlePress(item)}
    >
      <View style={[styles.iconContainer, !item.isRead && styles.iconUnread]}>
        <Ionicons name={getIcon(item.type) as any} size={20} color={!item.isRead ? COLORS.primary : COLORS.gray} />
      </View>
      <View style={styles.content}>
        <Text style={[styles.title, !item.isRead && styles.titleUnread]}>{item.title}</Text>
        <Text style={styles.message} numberOfLines={2}>{item.message}</Text>
        <Text style={styles.time}>{moment(item.createdAt).fromNow()}</Text>
      </View>
    </TouchableOpacity>
  );

  if (loading) {
    return (
      <View style={styles.center}>
        <ActivityIndicator size="large" color={COLORS.primary} />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      {mergedNotifications.length > 0 && (
        <TouchableOpacity style={styles.markAllBtn} onPress={handleMarkAllRead}>
          <Text style={styles.markAllText}>Mark all as read</Text>
        </TouchableOpacity>
      )}
      <FlatList
        data={mergedNotifications}
        keyExtractor={(item) => item.id}
        renderItem={renderItem}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} colors={[COLORS.primary]} />
        }
        onEndReached={loadMore}
        onEndReachedThreshold={0.3}
        ListEmptyComponent={
          <View style={styles.center}>
            <Ionicons name="notifications-off-outline" size={48} color={COLORS.lightGray} />
            <Text style={styles.emptyText}>No notifications yet</Text>
          </View>
        }
      />
    </View>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f9fafb' },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', paddingTop: 100 },
  markAllBtn: { padding: 12, alignItems: 'flex-end' },
  markAllText: { color: COLORS.primary, fontSize: 14, fontWeight: '600' },
  item: {
    flexDirection: 'row',
    padding: 16,
    backgroundColor: '#ffffff',
    borderBottomWidth: 1,
    borderBottomColor: '#f3f4f6',
  },
  unread: { backgroundColor: '#fef9f3' },
  iconContainer: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: '#f3f4f6',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  iconUnread: { backgroundColor: '#fde8cd' },
  content: { flex: 1 },
  title: { fontSize: 14, color: COLORS.dark, marginBottom: 2 },
  titleUnread: { fontWeight: '700' },
  message: { fontSize: 13, color: COLORS.gray, lineHeight: 18 },
  time: { fontSize: 11, color: '#9ca3af', marginTop: 4 },
  emptyText: { marginTop: 12, color: COLORS.gray, fontSize: 15 },
});

export default NotificationsScreen;
