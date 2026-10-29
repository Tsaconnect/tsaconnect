// components/forms/ListingDetails.tsx
//
// A listing's answers to its category's listing fields ("Institution: UNN",
// "Amenities: Wifi, Water"), labelled from the category's field definitions.
// Renders nothing when the category asks for nothing or nothing was answered.
import React from 'react';
import { View, type ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { Card, Divider, Text } from '@/components/ui';
import { useCategoryListingFields } from '@/hooks/useCategoryListingFields';
import { formatSchemaAnswer } from '@/utils/schemaFields';

interface ListingDetailsProps {
  categoryId?: string | null;
  values?: Record<string, unknown> | null;
  style?: ViewStyle;
}

export function ListingDetails({ categoryId, values, style }: ListingDetailsProps) {
  const { spacing } = useTheme();
  const { fields } = useCategoryListingFields(values ? categoryId : null);
  const rows = fields
    .map((f) => ({ key: f.key, label: f.label, value: formatSchemaAnswer(f, values?.[f.key]) }))
    .filter((r): r is { key: string; label: string; value: string } => r.value !== null);
  if (rows.length === 0) return null;

  return (
    <Card padding="lg" style={style}>
      {rows.map((r, i) => (
        <View key={r.key}>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', gap: spacing.md, paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textMuted">
              {r.label}
            </Text>
            <Text variant="bodySm" weight="600" style={{ flexShrink: 1, textAlign: 'right' }}>
              {r.value}
            </Text>
          </View>
          {i < rows.length - 1 && <Divider />}
        </View>
      ))}
    </Card>
  );
}
