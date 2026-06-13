// Activity indicator with an optional label ("Matching merchants…", "Releasing…").
import React from 'react';
import { ActivityIndicator, View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from './Text';

export interface SpinnerProps {
  size?: 'small' | 'large';
  color?: string;
  label?: string;
  /** Stack label below (default) or beside the spinner. */
  inline?: boolean;
}

export const Spinner: React.FC<SpinnerProps> = ({ size = 'small', color, label, inline }) => {
  const { colors, spacing } = useTheme();
  const c = color ?? colors.accent;
  if (!label) return <ActivityIndicator size={size} color={c} />;
  return (
    <View
      style={{
        flexDirection: inline ? 'row' : 'column',
        alignItems: 'center',
        gap: spacing.sm,
      }}
    >
      <ActivityIndicator size={size} color={c} />
      <Text variant="bodySm" color="textSecondary" align="center">
        {label}
      </Text>
    </View>
  );
};
