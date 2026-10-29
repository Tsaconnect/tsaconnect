// components/forms/ListingFilters.tsx
//
// Search filters built from the category's filterable listing fields: one
// chip row per field ("Any" plus its options, or Yes/No for a switch). The
// chosen values go to the API as attr[key]=value. Free-text and number fields
// are not offered here — chips can only pick from a known set.
import React from 'react';
import { ScrollView, View } from 'react-native';
import { useTheme } from '@/theme';
import { Chip, Text } from '@/components/ui';
import { useCategoryListingFields } from '@/hooks/useCategoryListingFields';
import type { SchemaField } from '@/utils/schemaFields';

interface ListingFiltersProps {
  categoryId?: string | null;
  value: Record<string, string>;
  onChange: (value: Record<string, string>) => void;
}

function choicesFor(f: SchemaField): { value: string; label: string }[] {
  if (f.type === 'boolean') return [{ value: 'true', label: 'Yes' }, { value: 'false', label: 'No' }];
  return (f.options ?? []).map((o) => ({ value: o, label: o }));
}

export function ListingFilters({ categoryId, value, onChange }: ListingFiltersProps) {
  const { spacing } = useTheme();
  const { fields } = useCategoryListingFields(categoryId);
  const filterable = fields.filter(
    (f) => f.filterable && (f.type === 'boolean' || ((f.type === 'select' || f.type === 'multiselect') && f.options?.length)),
  );
  if (filterable.length === 0) return null;

  const set = (key: string, v: string) => onChange({ ...value, [key]: v });

  return (
    <View style={{ gap: spacing.sm, paddingHorizontal: spacing.lg, paddingBottom: spacing.md }}>
      {filterable.map((f) => {
        const current = value[f.key] ?? '';
        return (
          <View key={f.key} style={{ gap: spacing.xs }}>
            <Text variant="caption" color="textMuted">
              {f.label}
            </Text>
            <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: spacing.xs }}>
              <Chip label="Any" selected={current === ''} onPress={() => set(f.key, '')} />
              {choicesFor(f).map((c) => (
                <Chip
                  key={c.value}
                  label={c.label}
                  selected={current === c.value}
                  onPress={() => set(f.key, current === c.value ? '' : c.value)}
                />
              ))}
            </ScrollView>
          </View>
        );
      })}
    </View>
  );
}
