// Themed switch wrapper (set-default PM, auto-release ⚡ opt-in).
import React from 'react';
import { Switch } from 'react-native';
import { useTheme } from '@/theme';

export interface ToggleProps {
  value: boolean;
  onValueChange: (v: boolean) => void;
  disabled?: boolean;
  accessibilityLabel?: string;
}

export const Toggle: React.FC<ToggleProps> = ({ value, onValueChange, disabled, accessibilityLabel }) => {
  const { colors } = useTheme();
  return (
    <Switch
      value={value}
      onValueChange={onValueChange}
      disabled={disabled}
      accessibilityLabel={accessibilityLabel}
      trackColor={{ false: colors.surfaceAlt, true: colors.accentMuted }}
      thumbColor={value ? colors.accent : colors.surface}
      ios_backgroundColor={colors.surfaceAlt}
      style={{ opacity: disabled ? 0.5 : 1 }}
    />
  );
};
