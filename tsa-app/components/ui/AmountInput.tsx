// Numeric amount field with a currency/token symbol affordance + optional "Max"
// and a balance/limit hint. Sanitizes to a valid decimal string.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Input, InputProps } from './Input';
import { Text } from './Text';
import { PressableScale } from './PressableScale';

export interface AmountInputProps
  extends Omit<InputProps, 'value' | 'onChangeText' | 'keyboardType' | 'rightAdornment' | 'leftAdornment'> {
  value: string;
  onChangeText: (v: string) => void;
  symbol?: string;
  symbolPosition?: 'prefix' | 'suffix';
  onMax?: () => void;
  maxLabel?: string;
}

// Keep only digits and a single decimal point.
function sanitize(input: string): string {
  const cleaned = input.replace(/[^0-9.]/g, '');
  const firstDot = cleaned.indexOf('.');
  if (firstDot === -1) return cleaned;
  return cleaned.slice(0, firstDot + 1) + cleaned.slice(firstDot + 1).replace(/\./g, '');
}

export const AmountInput: React.FC<AmountInputProps> = ({
  value,
  onChangeText,
  symbol,
  symbolPosition = 'prefix',
  onMax,
  maxLabel = 'Max',
  ...rest
}) => {
  const { colors, spacing } = useTheme();

  const symbolNode = symbol ? (
    <Text variant="bodyMed" weight="700" color="textSecondary">
      {symbol}
    </Text>
  ) : undefined;

  const maxNode = onMax ? (
    <PressableScale
      onPress={onMax}
      accessibilityRole="button"
      accessibilityLabel={maxLabel}
      style={{ paddingHorizontal: spacing.sm, paddingVertical: spacing.xs }}
    >
      <Text variant="bodySm" weight="700" style={{ color: colors.accent }}>
        {maxLabel}
      </Text>
    </PressableScale>
  ) : undefined;

  return (
    <Input
      {...rest}
      value={value}
      onChangeText={(t) => onChangeText(sanitize(t))}
      keyboardType="decimal-pad"
      inputMode="decimal"
      leftAdornment={symbolPosition === 'prefix' ? symbolNode : undefined}
      rightAdornment={
        symbolPosition === 'suffix' || onMax ? (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
            {symbolPosition === 'suffix' ? symbolNode : null}
            {maxNode}
          </View>
        ) : undefined
      }
    />
  );
};
