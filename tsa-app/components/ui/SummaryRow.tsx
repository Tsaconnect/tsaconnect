// Label ↔ value rows for fee breakdowns, order summaries, onboarding deposits.
import React, { useState } from 'react';
import { Pressable, View } from 'react-native';
import { ColorTokens, useTheme } from '@/theme';
import { Text } from './Text';
import { Divider } from './Divider';
import { Icon } from './Icon';

export interface SummaryRowProps {
  label: string;
  value: React.ReactNode; // string or a node (e.g. AmountText)
  /** Emphasize as a total (bold, larger). */
  total?: boolean;
  valueColor?: keyof ColorTokens;
  hint?: string;
  /** Shown under the row when its (i) is tapped: detail kept off the screen
   *  until asked for. */
  tip?: string;
}

export const SummaryRow: React.FC<SummaryRowProps> = ({ label, value, total, valueColor, hint, tip }) => {
  const { colors, spacing } = useTheme();
  const [tipOpen, setTipOpen] = useState(false);
  const labelText = (
    <Text
      variant={total ? 'bodyMed' : 'bodySm'}
      color={total ? 'textPrimary' : 'textSecondary'}
      weight={total ? '700' : '400'}
      style={tip ? { flexShrink: 1 } : { flexShrink: 1, maxWidth: '50%' }}
    >
      {label}
    </Text>
  );
  return (
    <View style={{ paddingVertical: spacing.sm }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.md }}>
        {tip ? (
          <Pressable
            onPress={() => setTipOpen((o) => !o)}
            accessibilityRole="button"
            accessibilityLabel={`${label}. More information`}
            accessibilityState={{ expanded: tipOpen }}
            hitSlop={8}
            style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, flexShrink: 1, maxWidth: '50%' }}
          >
            {labelText}
            <Icon name="info" size={14} color={colors.textMuted} />
          </Pressable>
        ) : (
          // Labels wrap before they crowd the value out; the value keeps at least half the row.
          labelText
        )}
        {typeof value === 'string' ? (
          <Text
            variant={total ? 'title3' : 'bodyMed'}
            weight={total ? '700' : '600'}
            numeric
            color={valueColor ?? 'textPrimary'}
            // Long values (company account names) wrap inside the card instead of pushing past its edge.
            style={{ flexShrink: 1, textAlign: 'right' }}
          >
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
      {tip && tipOpen ? (
        <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
          {tip}
        </Text>
      ) : null}
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
