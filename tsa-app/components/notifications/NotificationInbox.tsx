// components/notifications/NotificationInbox.tsx
//
// Reusable notifications list. Merges realtime (WebSocket) notifications from
// NotificationContext with the paginated REST feed; supports pull-to-refresh,
// infinite scroll, tap-to-mark-read + deep-link, and mark-all-read.
//
// Rendered by the dashboard route app/(dashboard)/(tabs)/notifications.tsx.
// That route is a Tabs.Screen with `href: null` — it is hidden from the tab bar
// and only reached by pushing from the header bell (components/NotificationBell)
// — and the Tabs screenOptions hide the header. So this screen has to draw its
// own back affordance or the bell is a one-way trip. ScreenHeader is
// canGoBack()-guarded and falls back to /home, which also covers arriving here
// cold from a push notification.
import React, { useEffect, useMemo, useRef, useState, useCallback } from 'react';
import { View, FlatList, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import moment from 'moment';
import { useNotifications } from '../../contexts/NotificationContext';
import { mergeInbox } from '../../hooks/notificationFilter';
import { useAuth } from '../../AuthContext/AuthContext';
import { routeForNotification } from '../../hooks/notificationRouting';
import api from '@/components/services/api';
import { useTheme } from '@/theme';
import { HOME_TAB } from '@/constants/routes';
import {
  AccentCard,
  Button,
  EmptyState,
  Icon,
  IconName,
  PressableScale,
  ScreenHeader,
  SkeletonGroup,
  Spinner,
  Text,
} from '@/components/ui';

// Map a notification category (the first segment of `type`) to a registry icon.
// Falls back to a generic info glyph for unmapped categories.
const CATEGORY_ICONS: Record<string, IconName> = {
  transaction: 'swap',
  order: 'list',
  p2p: 'swap',
  security: 'shield',
  verification: 'checkCircle',
  merchant: 'storefront',
  product: 'card',
  wallet: 'wallet',
};

const NotificationInbox: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { notifications: realtimeNotifications, setNotifications, refreshUnreadCount } = useNotifications();
  const { currentUser } = useAuth();
  const [notifications, setLocalNotifications] = useState<any[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const loadingMoreRef = useRef(false);

  const fetchNotifications = useCallback(async (pageNum: number, refresh = false) => {
    try {
      const res = await api.getNotifications(pageNum);
      if (res.success) {
        const items = res.data.notifications || [];
        if (refresh) {
          setLocalNotifications(items);
        } else {
          // De-dup on append: offset pagination can re-serve a row if a new
          // notification arrives between page fetches (the window shifts).
          setLocalNotifications((prev) => {
            const seen = new Set(prev.map((n) => n.id));
            const fresh = items.filter((i: any) => !seen.has(i.id));
            return fresh.length ? [...prev, ...fresh] : prev;
          });
        }
        setHasMore(items.length >= 20);
      }
    } catch {} finally {
      setLoading(false);
      setRefreshing(false);
      loadingMoreRef.current = false;
    }
  }, []);

  useEffect(() => {
    fetchNotifications(1, true);
  }, [fetchNotifications]);

  // Memoised: the merge used to rerun (and rescan the feed per realtime row)
  // on every render, including each refresh/loading toggle.
  const userId = currentUser?.id;
  const mergedNotifications = useMemo(
    () => mergeInbox(realtimeNotifications, notifications, userId),
    [realtimeNotifications, notifications, userId],
  );

  const onRefresh = useCallback(() => {
    setRefreshing(true);
    setPage(1);
    setNotifications([]);
    fetchNotifications(1, true);
    refreshUnreadCount();
  }, [fetchNotifications, refreshUnreadCount, setNotifications]);

  const loadMore = useCallback(() => {
    // loadingMoreRef guards against overlapping onEndReached fires (the `loading`
    // state only covers the first page, never gets reset true for page>1).
    if (!hasMore || loading || loadingMoreRef.current) return;
    loadingMoreRef.current = true;
    const nextPage = page + 1;
    setPage(nextPage);
    fetchNotifications(nextPage);
  }, [page, hasMore, loading, fetchNotifications]);

  const handlePress = useCallback(async (item: any) => {
    if (!item.isRead) {
      await api.markAsRead(item.id);
      // Mark read in BOTH lists. A freshly WS-delivered row lives only in the
      // realtime context list (not the REST-backed local list) until a refresh,
      // so updating only the local list would leave the tapped row styled unread.
      setLocalNotifications((prev) =>
        prev.map((n) => (n.id === item.id ? { ...n, isRead: true } : n))
      );
      setNotifications((prev) =>
        prev.map((n) => (n.id === item.id ? { ...n, isRead: true } : n))
      );
      refreshUnreadCount();
    }
    // Deep-link to the relevant screen. The persisted `data` carries tradeId
    // etc. but not always `type` (that's a separate column), so fold the
    // notification's own type in. Unmapped types just mark-read with no nav.
    const route = routeForNotification({ type: item.type, ...(item.data || {}) });
    if (route) router.push(route as never);
  }, [refreshUnreadCount, setNotifications]);

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
        <ScreenHeader title="Notifications" fallbackRoute={HOME_TAB} />
        <View style={{ padding: spacing.lg }}>
          <SkeletonGroup shape="tradeRow" count={6} />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader
        title="Notifications"
        fallbackRoute={HOME_TAB}
        right={
          mergedNotifications.length > 0 ? (
            <Button
              title="Mark all read"
              variant="ghost"
              size="sm"
              fullWidth={false}
              onPress={handleMarkAllRead}
            />
          ) : undefined
        }
      />
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

export default NotificationInbox;
