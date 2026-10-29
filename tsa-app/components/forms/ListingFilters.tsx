// components/forms/ListingFilters.tsx
//
// Search filters built from the category's filterable listing fields. The
// browse screen gets one compact row — a "Filters" chip plus a chip per active
// filter (tap to clear it) — and the choices live in a sheet: one chip row per
// field ("Any" plus its options, or Yes/No for a switch). The sheet edits a
// draft and applies it on "Show results", so the list reloads once, not per tap.
// The chosen values go to the API as attr[key]=value. Free-text and number
// fields are not offered here — chips can only pick from a known set.
import React, { useState } from 'react';
import { ScrollView, View } from 'react-native';
import { useTheme } from '@/theme';
import { Button, Chip, Sheet, Text } from '@/components/ui';
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
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const filterable = fields.filter(
    (f) => f.filterable && (f.type === 'boolean' || ((f.type === 'select' || f.type === 'multiselect') && f.options?.length)),
  );
  if (filterable.length === 0) return null;

  const active = filterable.filter((f) => (value[f.key] ?? '') !== '');
  const labelFor = (f: SchemaField) =>
    choicesFor(f).find((c) => c.value === value[f.key])?.label ?? value[f.key];

  const openSheet = () => {
    setDraft(value);
    setOpen(true);
  };
  const apply = () => {
    onChange(draft);
    setOpen(false);
  };

  return (
    <>
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        style={{ flexGrow: 0 }}
        contentContainerStyle={{ gap: spacing.xs, paddingHorizontal: spacing.lg, paddingBottom: spacing.md }}
      >
        <Chip
          label={active.length > 0 ? `Filters (${active.length})` : 'Filters'}
          leftIcon="filter"
          selected={active.length > 0}
          onPress={openSheet}
        />
        {active.map((f) => (
          <Chip
            key={f.key}
            label={`${f.label}: ${labelFor(f)}`}
            leftIcon="close"
            onPress={() => onChange({ ...value, [f.key]: '' })}
          />
        ))}
      </ScrollView>

      <Sheet
        visible={open}
        onClose={() => setOpen(false)}
        title="Filters"
        scrollable
        footer={
          <View style={{ flexDirection: 'row', gap: spacing.sm }}>
            <Button title="Clear all" variant="secondary" onPress={() => setDraft({})} style={{ flex: 1 }} />
            <Button title="Show results" onPress={apply} style={{ flex: 1 }} />
          </View>
        }
      >
        <View style={{ gap: spacing.lg, paddingHorizontal: spacing.lg, paddingVertical: spacing.sm }}>
          {filterable.map((f) => {
            const current = draft[f.key] ?? '';
            const set = (v: string) => setDraft((prev) => ({ ...prev, [f.key]: v }));
            return (
              <View key={f.key} style={{ gap: spacing.xs }}>
                <Text variant="bodySm" weight="600">
                  {f.label}
                </Text>
                <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs }}>
                  <Chip label="Any" selected={current === ''} onPress={() => set('')} />
                  {choicesFor(f).map((c) => (
                    <Chip
                      key={c.value}
                      label={c.label}
                      selected={current === c.value}
                      onPress={() => set(current === c.value ? '' : c.value)}
                    />
                  ))}
                </View>
              </View>
            );
          })}
        </View>
      </Sheet>
    </>
  );
}
