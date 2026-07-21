// components/ui/ScreenHeader.tsx
//
// The one back-header for screens that hide the native Stack header
// (headerShown: false). Before this, back was hand-rolled nine different ways —
// icon-only vs icon+"Back" label, chevron at size 20/22/24, accent vs
// textPrimary tint, and assorted negative margins to optically align. Use this
// instead of a new inline header row.
//
// The chevron is tinted `accent` to match the native headers' headerTintColor,
// so both tiers read the same. Back is canGoBack()-guarded — several screens are
// reachable as a deep link's first route, where an unguarded router.back() is a
// no-op and traps the user.
import React from 'react';
import { View, ViewStyle } from 'react-native';
import { router, type Href } from 'expo-router';
import { useTheme } from '@/theme';
import { IconButton } from './IconButton';
import { Text } from './Text';

export interface ScreenHeaderProps {
  title?: string;
  /** 'center' pairs the title with a spacer so it stays optically centred. */
  align?: 'center' | 'left';
  /** Overrides the default router.back() — use for intra-screen step-back. */
  onBack?: () => void;
  /** Where to go when there is nothing to pop back to. Typed so a bad route
   *  literal is a compile error — this is exactly where /reward vs /rewards
   *  would otherwise slip through. */
  fallbackRoute?: Href;
  /** Trailing slot (actions). Reserves the spacer when centred. */
  right?: React.ReactNode;
  showBack?: boolean;
  style?: ViewStyle;
}

const TOUCH = 44; // IconButton dimension — the spacer must match to stay centred

export const ScreenHeader: React.FC<ScreenHeaderProps> = ({
  title,
  align = 'center',
  onBack,
  fallbackRoute = '/home',
  right,
  showBack = true,
  style,
}) => {
  const { colors, spacing } = useTheme();

  const handleBack = () => {
    if (onBack) return onBack();
    if (router.canGoBack()) router.back();
    else router.replace(fallbackRoute);
  };

  return (
    <View
      style={[
        {
          flexDirection: 'row',
          alignItems: 'center',
          justifyContent: 'space-between',
          paddingHorizontal: spacing.lg,
          paddingVertical: spacing.sm,
        },
        style,
      ]}
    >
      {showBack ? (
        <IconButton
          icon="chevronLeft"
          onPress={handleBack}
          color={colors.accent}
          accessibilityLabel="Go back"
          // Cancels the IconButton's internal padding so the chevron lines up
          // with the screen's content edge rather than floating inset.
          style={{ marginLeft: -spacing.sm }}
        />
      ) : (
        <View style={{ width: TOUCH }} />
      )}

      {title ? (
        <Text
          variant="title3"
          weight="700"
          numberOfLines={1}
          style={
            align === 'center'
              ? { flex: 1, textAlign: 'center' }
              : { flex: 1, marginLeft: spacing.xs }
          }
        >
          {title}
        </Text>
      ) : (
        <View style={{ flex: 1 }} />
      )}

      {right ?? <View style={{ width: TOUCH }} />}
    </View>
  );
};
