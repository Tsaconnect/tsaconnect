// Label ↔ value rows for fee breakdowns, order summaries, onboarding deposits.
import React from 'react';
import { View } from 'react-native';
import { ColorTokens, useTheme } from '@/theme';
import { Text } from './Text';
import { Divider } from './Divider';

export interface SummaryRowProps {
  label: string;
  value: React.ReactNode; // string or a node (e.g. AmountText)
  /** Emphasize as a total (bold, larger). */
  total?: boolean;
  valueColor?: keyof ColorTokens;
  hint?: string;
}

export const SummaryRow: React.FC<SummaryRowProps> = ({ label, value, total, valueColor, hint }) => {
  const { spacing } = useTheme();
  return (
    <View style={{ paddingVertical: spacing.sm }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.md }}>
        <Text variant={total ? 'bodyMed' : 'bodySm'} color={total ? 'textPrimary' : 'textSecondary'} weight={total ? '700' : '400'}>
          {label}
        </Text>
        {typeof value === 'string' ? (
          <Text variant={total ? 'title3' : 'bodyMed'} weight={total ? '700' : '600'} numeric color={valueColor ?? 'textPrimary'}>
            {value}
          </Text>
        ) : (
          value
        )}
      </View>
      {hint && (
        <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
          {hint}
        </Text>
      )}
    </View>
  );
};

/** Card-wrapped group of rows with a divider before a `total` row. */
export const SummaryCard: React.FC<{ children: React.ReactNode; dividerBeforeLast?: boolean }> = ({
  children,
  dividerBeforeLast,
}) => {
  const items = React.Children.toArray(children);
  return (
    <View>
      {items.map((child, i) => (
        <View key={i}>
          {dividerBeforeLast && i === items.length - 1 && <Divider spacing="xs" />}
          {child}
        </View>
      ))}
    </View>
  );
};
