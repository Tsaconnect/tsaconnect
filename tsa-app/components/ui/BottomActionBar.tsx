// Sticky CTA container docked above the safe area. Use at the bottom of forms,
// wizards and the Express order screen so the primary action never scrolls away.
import React from 'react';
import { View, ViewStyle } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';

export interface BottomActionBarProps {
  children: React.ReactNode;
  /** Hairline top border (default true). */
  bordered?: boolean;
  style?: ViewStyle;
}

export const BottomActionBar: React.FC<BottomActionBarProps> = ({ children, bordered = true, style }) => {
  const { colors, spacing } = useTheme();
  const insets = useSafeAreaInsets();
  return (
    <View
      style={[
        {
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: Math.max(insets.bottom, spacing.md),
          backgroundColor: colors.surface,
          borderTopWidth: bordered ? 1 : 0,
          borderTopColor: colors.hairline,
          gap: spacing.sm,
        },
        style,
      ]}
    >
      {children}
    </View>
  );
};
