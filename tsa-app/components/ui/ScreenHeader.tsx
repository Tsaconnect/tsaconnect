// components/ui/ScreenHeader.tsx
//
// The back-header for screens that hide the native Stack header
// (headerShown: false). Before this, back was hand-rolled nine different ways —
// icon-only vs icon+"Back" label, chevron at size 20/22/24, accent vs
// textPrimary tint, and assorted negative margins to optically align. Use this
// instead of a new inline header row.
//
// It renders the same IconButton as HeaderBackButton (the native headers'
// headerLeft) and routes through the same goBack() action, so a screen reads
// identically whether its chrome is native or hand-drawn.
import React from 'react';
import { View, ViewStyle } from 'react-native';
import { type Href } from 'expo-router';
import { useTheme } from '@/theme';
import { goBack, hasHistory } from '@/utils/back';
import { IconButton } from './IconButton';
import { Text } from './Text';

export interface ScreenHeaderProps {
  title?: string;
  /** 'center' pairs the title with a spacer so it stays optically centred. */
  align?: 'center' | 'left';
  /** Overrides the default back action — use for intra-screen step-back. */
  onBack?: () => void;
  /**
   * Where to land when there is no history to pop — a cold start from a deep
   * link or a push notification. Required, and it should name the SECTION this
   * screen belongs to: defaulting every screen to Home is what produced the
   * "back takes me to main" reports. Typed so a bad route literal is a compile
   * error — this is exactly where /reward vs /rewards would otherwise slip
   * through. Not needed when `onBack` is supplied, since that never pops.
   */
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
  fallbackRoute,
  right,
  showBack = true,
  style,
}) => {
  const { colors, spacing } = useTheme();

  // A chevron with nowhere to go is worse than no chevron: it reads as a broken
  // button. Cold-started screens (notification taps, deep links) with no
  // declared fallback get the spacer instead.
  const canBack = showBack && (!!onBack || !!fallbackRoute || hasHistory());

  const handleBack = () => {
    if (onBack) return onBack();
    goBack(fallbackRoute ?? '/home');
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
      {canBack ? (
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
