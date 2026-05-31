// Input-looking field that opens a picker (caller wires the Sheet/PickerSheet).
import React from 'react';
import { View, ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { PressableScale } from './PressableScale';
import { Text } from './Text';
import { Icon, IconName } from './Icon';

export interface SelectRowProps {
  label?: string;
  value?: string;
  placeholder?: string;
  onPress: () => void;
  leftIcon?: IconName;
  /** Custom left node (e.g. a remote image / color dot) — takes precedence over leftIcon. */
  leftElement?: React.ReactNode;
  error?: string;
  disabled?: boolean;
  style?: ViewStyle;
}

export const SelectRow: React.FC<SelectRowProps> = ({
  label,
  value,
  placeholder = 'Select',
  onPress,
  leftIcon,
  leftElement,
  error,
  disabled,
  style,
}) => {
  const { colors, radius, spacing } = useTheme();
  return (
    <View style={style}>
      {label && (
        <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
          {label}
        </Text>
      )}
      <PressableScale
        onPress={onPress}
        disabled={disabled}
        accessibilityRole="button"
        accessibilityLabel={label ? `${label}: ${value ?? placeholder}` : value ?? placeholder}
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.sm,
          minHeight: 48,
          paddingHorizontal: spacing.md,
          borderRadius: radius.md,
          borderWidth: 1.5,
          borderColor: error ? colors.danger : colors.border,
          backgroundColor: colors.surface,
          opacity: disabled ? 0.5 : 1,
        }}
      >
        {leftElement ?? (leftIcon && <Icon name={leftIcon} size={20} color={colors.textSecondary} />)}
        <Text variant="body" color={value ? 'textPrimary' : 'textMuted'} style={{ flex: 1 }} numberOfLines={1}>
          {value || placeholder}
        </Text>
        <Icon name="chevronDown" size={18} color={colors.textMuted} />
      </PressableScale>
      {error && (
        <Text variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
          {error}
        </Text>
      )}
    </View>
  );
};
