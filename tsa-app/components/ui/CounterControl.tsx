import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { IconButton } from './IconButton';
import { Text } from './Text';

export interface CounterControlProps {
  value: number;
  onChange: (next: number) => void;
  /** What is being counted, for the buttons' accessibility labels. */
  label: string;
  min?: number;
  max?: number;
}

/** A − value + integer counter (guests, seats, tickets). */
export const CounterControl: React.FC<CounterControlProps> = ({ value, onChange, label, min = 0, max }) => {
  const { spacing } = useTheme();
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
      <IconButton
        icon="minus"
        variant="surface"
        onPress={() => onChange(Math.max(min, value - 1))}
        disabled={value <= min}
        accessibilityLabel={`Decrease ${label}`}
      />
      <Text variant="title3" weight="700" style={{ minWidth: 32, textAlign: 'center' }}>
        {value}
      </Text>
      <IconButton
        icon="plus"
        variant="surface"
        onPress={() => onChange(max == null ? value + 1 : Math.min(max, value + 1))}
        disabled={max != null && value >= max}
        accessibilityLabel={`Increase ${label}`}
      />
    </View>
  );
};
