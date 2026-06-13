// Selectable filter chip (payment-method / token filters, multi-select tags).
import React from 'react';
import { ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { PressableScale } from './PressableScale';
import { Text } from './Text';
import { Icon, IconName } from './Icon';

export interface ChipProps {
  label: string;
  selected?: boolean;
  onPress?: () => void;
  leftIcon?: IconName;
  disabled?: boolean;
  style?: ViewStyle;
}

export const Chip: React.FC<ChipProps> = ({ label, selected, onPress, leftIcon, disabled, style }) => {
  const { colors, radius, spacing } = useTheme();
  return (
    <PressableScale
      onPress={onPress}
      disabled={disabled}
      accessibilityRole="button"
      accessibilityState={{ selected: !!selected, disabled: !!disabled }}
      style={[
        {
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.xs,
          minHeight: 36,
          paddingVertical: spacing.xs,
          paddingHorizontal: spacing.md,
          borderRadius: radius.pill,
          borderWidth: 1,
          borderColor: selected ? colors.accent : colors.border,
          backgroundColor: selected ? colors.accentMuted : colors.surface,
          opacity: disabled ? 0.5 : 1,
        },
        style,
      ]}
    >
      {leftIcon && <Icon name={leftIcon} size={14} color={selected ? colors.accent : colors.textSecondary} />}
      <Text variant="bodySm" weight={selected ? '600' : '400'} style={{ color: selected ? colors.accent : colors.textSecondary }}>
        {label}
      </Text>
    </PressableScale>
  );
};
