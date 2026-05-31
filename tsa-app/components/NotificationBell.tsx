// components/NotificationBell.tsx
//
// Header notification bell with unread badge.
// Migrated onto the TSA Signature design system: legacy COLORS + raw StyleSheet
// (hardcoded #ef4444 / #fff, magic numbers) are replaced by useTheme() tokens and
// the PressableScale primitive (44px touch target + signature press spring). The
// unread-count subscription and onPress navigation are preserved verbatim.
//
// PREFERRED FOLLOW-UP: add a "notifications" entry to the Icon registry
// (components/ui/Icon.tsx — off-limits here) and swap the inline Ionicons glyph
// for <IconButton icon="notifications" ... />. Reported via iconsNeeded.
import React from 'react';
import { View } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { useRouter } from 'expo-router';
import { useNotifications } from '../contexts/NotificationContext';
import { useTheme } from '@/theme';
import { PressableScale, Text } from '@/components/ui';

const NotificationBell: React.FC = () => {
  const { unreadCount } = useNotifications();
  const router = useRouter();
  const { colors, radius, spacing } = useTheme();

  return (
    <PressableScale
      onPress={() => router.push('/notifications')}
      accessibilityRole="button"
      accessibilityLabel={
        unreadCount > 0 ? `Notifications, ${unreadCount} unread` : 'Notifications'
      }
      style={{
        width: 44,
        height: 44,
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: radius.pill,
      }}
    >
      <Ionicons name="notifications-outline" size={24} color={colors.textPrimary} />
      {unreadCount > 0 && (
        <View
          pointerEvents="none"
          style={{
            position: 'absolute',
            top: spacing.sm,
            right: spacing.sm,
            minWidth: 18,
            height: 18,
            paddingHorizontal: spacing.xs,
            borderRadius: radius.pill,
            backgroundColor: colors.danger,
            borderWidth: 2,
            borderColor: colors.surface,
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          <Text variant="overline" weight="700" style={{ color: colors.textInverse }}>
            {unreadCount > 99 ? '99+' : unreadCount}
          </Text>
        </View>
      )}
    </PressableScale>
  );
};

export default NotificationBell;
