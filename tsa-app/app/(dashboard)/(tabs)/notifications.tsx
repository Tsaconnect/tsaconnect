// app/(dashboard)/(tabs)/notifications.tsx
//
// Notifications list. Merges realtime (WebSocket) notifications from
// NotificationContext with the paginated REST feed, supports pull-to-refresh,
// infinite scroll, tap-to-mark-read, and mark-all-read.
//
// Restyled onto the TSA Signature design system (tokens + @/components/ui).
// Behavior — fetch / pagination / read & mark-all-read — is unchanged.
import React, { useEffect, useState, useCallback } from 'react';
import { View, FlatList, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import moment from 'moment';
import { useNotifications } from '../../../contexts/NotificationContext';
import api from '@/components/services/api';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  EmptyState,
  Icon,
  IconName,
  PressableScale,
  SkeletonGroup,
  Spinner,
  Text,
} from '@/components/ui';

// Map a notification category (the first segment of `type`) to a registry icon.
// Falls back to a generic info glyph for unmapped categories.
const CATEGORY_ICONS: Record<string, IconName> = {
  transaction: 'swap',
  order: 'list',
  security: 'shield',
  verification: 'checkCircle',
  merchant: 'storefront',
  product: 'card',
  wallet: 'wallet',
};

const NotificationsScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { notifications: realtimeNotifications, setNotifications, refreshUnreadCount } = useNotifications();
  const [notifications, setLocalNotifications] = useState<any[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

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

  const getIcon = (type: string): IconName => {
    const category = type.split('.')[0];
    return CATEGORY_ICONS[category] || 'info';
  };

  const renderItem = ({ item }: { item: any }) => {
    const unread = !item.isRead;
    const iconColor = unread ? colors.accent : colors.textMuted;
    return (
      <PressableScale onPress={() => handlePress(item)} style={{ marginBottom: spacing.md }}>
        <AccentCard
          padding="md"
          railColor={unread ? colors.accentRail : colors.hairline}
          railWidth={unread ? 4 : 0}
        >
          <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.md }}>
            <View
              style={{
                width: 40,
                height: 40,
                borderRadius: 20,
                backgroundColor: unread ? colors.accentMuted : colors.surfaceAlt,
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <Icon name={getIcon(item.type)} size={20} color={iconColor} />
            </View>
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight={unread ? '700' : '600'} numberOfLines={1}>
                {item.title}
              </Text>
              <Text variant="bodySm" color="textSecondary" numberOfLines={2} style={{ marginTop: 2 }}>
                {item.message}
              </Text>
              <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
                {moment(item.createdAt).fromNow()}
              </Text>
            </View>
            {unread && (
              <View
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: 4,
                  backgroundColor: colors.accent,
                  marginTop: spacing.xs,
                }}
              />
            )}
          </View>
        </AccentCard>
      </PressableScale>
    );
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ padding: spacing.lg }}>
          <Text variant="title1" weight="700">
            Notifications
          </Text>
          <View style={{ marginTop: spacing.lg }}>
            <SkeletonGroup shape="tradeRow" count={6} />
          </View>
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <FlatList
        data={mergedNotifications}
        keyExtractor={(item) => item.id}
        renderItem={renderItem}
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl refreshing={refreshing} tintColor={colors.accent} onRefresh={onRefresh} />
        }
        onEndReached={loadMore}
        onEndReachedThreshold={0.3}
        ListHeaderComponent={
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'space-between',
              marginBottom: spacing.lg,
            }}
          >
            <Text variant="title1" weight="700">
              Notifications
            </Text>
            {mergedNotifications.length > 0 && (
              <Button
                title="Mark all read"
                variant="ghost"
                size="sm"
                fullWidth={false}
                onPress={handleMarkAllRead}
              />
            )}
          </View>
        }
        ListFooterComponent={
          !loading && refreshing === false && mergedNotifications.length > 0 && hasMore ? (
            <View style={{ paddingVertical: spacing.lg }}>
              <Spinner />
            </View>
          ) : null
        }
        ListEmptyComponent={
          <View style={{ paddingTop: spacing['4xl'] }}>
            <EmptyState
              icon="empty"
              title="No notifications yet"
              subtitle="Updates about your orders, trades, and account will show up here."
            />
          </View>
        }
      />
    </SafeAreaView>
  );
};

export default NotificationsScreen;
