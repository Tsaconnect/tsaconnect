// components/p2p/NotificationBell.tsx
//
// Header bell + unread badge. Lives in the P2P screen headers so notifications
// are actually reachable (the dashboard notifications tab is href:null). Reads
// the live unread count from NotificationContext and refreshes it on mount;
// tapping opens the P2P-stack notifications inbox.
import React, { useEffect } from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Icon, PressableScale, Text } from '@/components/ui';
import { useNotifications } from '../../contexts/NotificationContext';

export const NotificationBell: React.FC<{ to?: string }> = ({ to = '/p2p/notifications' }) => {
  const { colors } = useTheme();
  const { unreadCount, refreshUnreadCount } = useNotifications();

  // Re-sync the count when a header mounts (e.g. returning from the inbox after
  // marking items read) so the badge doesn't show a stale number.
  useEffect(() => {
    refreshUnreadCount();
  }, [refreshUnreadCount]);

  const badge = unreadCount > 99 ? '99+' : String(unreadCount);

  return (
    <PressableScale
      onPress={() => router.push(to as never)}
      accessibilityLabel={unreadCount ? `Notifications, ${unreadCount} unread` : 'Notifications'}
      hitSlop={8}
      style={{ width: 40, height: 40, alignItems: 'center', justifyContent: 'center' }}
    >
      <Icon name="notifications" size={24} color={colors.textPrimary} />
      {unreadCount > 0 && (
        <View
          style={{
            position: 'absolute',
            top: 2,
            right: 2,
            minWidth: 18,
            height: 18,
            borderRadius: 9,
            paddingHorizontal: 4,
            backgroundColor: colors.danger,
            alignItems: 'center',
            justifyContent: 'center',
            borderWidth: 1.5,
            borderColor: colors.surface,
          }}
        >
          {/* White on the red danger fill is correct in both themes (the fill
              is red in both), so this is a deliberate hardcode rather than a
              token whose value flips with the theme. */}
          <Text variant="caption" weight="700" style={{ color: '#FFFFFF', fontSize: 10, lineHeight: 12 }}>
            {badge}
          </Text>
        </View>
      )}
    </PressableScale>
  );
};

export default NotificationBell;
