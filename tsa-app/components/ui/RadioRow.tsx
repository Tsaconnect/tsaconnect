// Single-select rows (payment-method pick, dispute reason, onboarding choices).
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { PressableScale } from './PressableScale';
import { Text } from './Text';
import { Icon, IconName } from './Icon';

export interface RadioRowProps {
  label: string;
  sublabel?: string;
  selected: boolean;
  onPress: () => void;
  leftIcon?: IconName;
  disabled?: boolean;
}

export const RadioRow: React.FC<RadioRowProps> = ({ label, sublabel, selected, onPress, leftIcon, disabled }) => {
  const { colors, radius, spacing } = useTheme();
  return (
    <PressableScale
      onPress={onPress}
      disabled={disabled}
      accessibilityRole="radio"
      accessibilityState={{ selected, disabled: !!disabled }}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        padding: spacing.md,
        borderRadius: radius.md,
        borderWidth: 1.5,
        borderColor: selected ? colors.accent : colors.border,
        backgroundColor: selected ? colors.accentMuted : colors.surface,
        opacity: disabled ? 0.5 : 1,
      }}
    >
      {leftIcon && <Icon name={leftIcon} size={20} color={selected ? colors.accent : colors.textSecondary} />}
      <View style={{ flex: 1 }}>
        <Text variant="bodyMed" weight="600">
          {label}
        </Text>
        {sublabel && (
          <Text variant="caption" color="textSecondary">
            {sublabel}
          </Text>
        )}
      </View>
      <View
        style={{
          width: 22,
          height: 22,
          borderRadius: 11,
          borderWidth: 2,
          borderColor: selected ? colors.accent : colors.border,
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        {selected && <View style={{ width: 12, height: 12, borderRadius: 6, backgroundColor: colors.accent }} />}
      </View>
    </PressableScale>
  );
};

export interface RadioOption<T extends string = string> {
  value: T;
  label: string;
  sublabel?: string;
  icon?: IconName;
}

export interface RadioGroupProps<T extends string = string> {
  options: RadioOption<T>[];
  value: T | null;
  onChange: (value: T) => void;
  disabled?: boolean;
}

export function RadioGroup<T extends string = string>({ options, value, onChange, disabled }: RadioGroupProps<T>) {
  const { spacing } = useTheme();
  return (
    <View style={{ gap: spacing.sm }}>
      {options.map((o) => (
        <RadioRow
          key={o.value}
          label={o.label}
          sublabel={o.sublabel}
          leftIcon={o.icon}
          selected={value === o.value}
          onPress={() => onChange(o.value)}
          disabled={disabled}
        />
      ))}
    </View>
  );
}
