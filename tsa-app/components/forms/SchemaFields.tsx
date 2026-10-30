// components/forms/SchemaFields.tsx
//
// Renders schema-driven fields — the buyer's booking form and the merchant's
// category listing fields — by mapping each field `type` to a TSA Signature
// primitive. Controlled: the parent owns the answers and errors, and validates
// with utils/schemaFields.
import React, { useMemo, useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Chip, CounterControl, Input, PickerSheet, SelectRow, Text, Toggle } from '@/components/ui';
import type { SchemaAnswers, SchemaField } from '@/utils/schemaFields';

interface SchemaFieldsProps {
  fields: SchemaField[];
  answers: SchemaAnswers;
  errors?: Record<string, string>;
  onChange: (key: string, value: any) => void;
}

// A select with more options than this opens a picker sheet instead of chips.
const MAX_CHIP_OPTIONS = 6;

const placeholderFor = (type: SchemaField['type']): string | undefined => {
  switch (type) {
    case 'date':
      return 'YYYY-MM-DD';
    case 'time':
      return 'HH:MM';
    case 'datetime':
      return 'YYYY-MM-DD HH:MM';
    case 'location':
      return 'Enter a location';
    default:
      return undefined;
  }
};

export function SchemaFields({ fields, answers, errors = {}, onChange }: SchemaFieldsProps) {
  const { spacing } = useTheme();
  // The long select whose picker sheet is open, if any.
  const [pickerKey, setPickerKey] = useState<string | null>(null);
  const pickerField = fields.find((f) => f.key === pickerKey);

  const labelled = (f: SchemaField, control: React.ReactNode) => (
    <View key={f.key} style={{ gap: spacing.xs }}>
      <Text variant="bodyMed" weight="600">
        {f.required ? `${f.label} *` : f.label}
      </Text>
      {control}
      {errors[f.key] ? (
        <Text variant="caption" color="danger">
          {errors[f.key]}
        </Text>
      ) : null}
    </View>
  );

  const renderField = (f: SchemaField) => {
    if (f.type === 'select') {
      const options = f.options ?? [];
      const current: string = answers[f.key] ?? '';
      if (options.length > MAX_CHIP_OPTIONS) {
        return (
          <SelectRow
            key={f.key}
            label={f.required ? `${f.label} *` : f.label}
            value={current || undefined}
            placeholder={`Select ${f.label.toLowerCase()}`}
            error={errors[f.key]}
            onPress={() => setPickerKey(f.key)}
          />
        );
      }
      return labelled(
        f,
        <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs }}>
          {options.map((o) => (
            <Chip
              key={o}
              label={o}
              selected={current === o}
              // An optional choice can be tapped again to clear it.
              onPress={() => onChange(f.key, current === o && !f.required ? '' : o)}
            />
          ))}
        </View>,
      );
    }

    if (f.type === 'multiselect') {
      const picked: string[] = Array.isArray(answers[f.key]) ? answers[f.key] : [];
      return labelled(
        f,
        <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs }}>
          {(f.options ?? []).map((o) => (
            <Chip
              key={o}
              label={o}
              selected={picked.includes(o)}
              onPress={() =>
                onChange(f.key, picked.includes(o) ? picked.filter((p) => p !== o) : [...picked, o])
              }
            />
          ))}
        </View>,
      );
    }

    if (f.type === 'boolean') {
      return (
        <View key={f.key} style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
          <Text variant="bodyMed" weight="600">
            {f.label}
          </Text>
          <Toggle value={!!answers[f.key]} onValueChange={(v) => onChange(f.key, v)} accessibilityLabel={f.label} />
        </View>
      );
    }

    if (f.type === 'counter') {
      const val = typeof answers[f.key] === 'number' ? answers[f.key] : 0;
      return labelled(f, <CounterControl value={val} onChange={(n) => onChange(f.key, n)} label={f.label} />);
    }

    // text | textarea | number | date | time | datetime | location → Input.
    // A number stays as typed text; utils/schemaFields converts it on submit.
    const numeric = f.type === 'number';
    return (
      <Input
        key={f.key}
        label={f.required ? `${f.label} *` : f.label}
        error={errors[f.key]}
        value={answers[f.key] != null ? String(answers[f.key]) : ''}
        placeholder={placeholderFor(f.type)}
        multiline={f.type === 'textarea'}
        keyboardType={numeric ? 'decimal-pad' : 'default'}
        onChangeText={(t) => onChange(f.key, t)}
      />
    );
  };

  const grouped = useMemo(() => {
    // Preserve config order but keep grouped fields visually adjacent.
    const order: string[] = [];
    const byGroup: Record<string, SchemaField[]> = {};
    for (const f of fields) {
      const g = f.group || 'other';
      if (!byGroup[g]) {
        byGroup[g] = [];
        order.push(g);
      }
      byGroup[g].push(f);
    }
    return order.map((g) => ({ group: g, items: byGroup[g] }));
  }, [fields]);

  return (
    <View style={{ gap: spacing.lg }}>
      {grouped.map(({ group, items }) => (
        <View key={group} style={{ gap: spacing.md }}>
          {items.map(renderField)}
        </View>
      ))}
      <PickerSheet
        visible={!!pickerField}
        onClose={() => setPickerKey(null)}
        title={pickerField?.label ?? ''}
        items={(pickerField?.options ?? []).map((o) => ({ id: o, label: o }))}
        selectedId={pickerField ? answers[pickerField.key] : undefined}
        searchable
        onSelect={(item) => {
          if (pickerField) onChange(pickerField.key, item.id);
          setPickerKey(null);
        }}
      />
    </View>
  );
}
