// components/forms/SchemaFields.tsx
//
// Renders schema-driven fields — the buyer's booking form and the merchant's
// category listing fields — by mapping each field `type` to a TSA Signature
// primitive. Controlled: the parent owns the answers and errors, and validates
// with utils/schemaFields.
import React, { useMemo } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Chip, IconButton, Input, RadioGroup, Text, Toggle } from '@/components/ui';
import type { SchemaAnswers, SchemaField } from '@/utils/schemaFields';

interface SchemaFieldsProps {
  fields: SchemaField[];
  answers: SchemaAnswers;
  errors?: Record<string, string>;
  onChange: (key: string, value: any) => void;
}

// Field types that store a numeric answer.
const NUMERIC_TYPES = new Set(['counter', 'number']);

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
      const options = (f.options ?? []).map((o) => ({ value: o, label: o }));
      return labelled(
        f,
        <RadioGroup options={options} value={answers[f.key] ?? ''} onChange={(v) => onChange(f.key, v)} />,
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
      return labelled(
        f,
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
          <IconButton
            icon="minus"
            variant="surface"
            onPress={() => onChange(f.key, Math.max(0, val - 1))}
            accessibilityLabel={`Decrease ${f.label}`}
          />
          <Text variant="title3" weight="700" style={{ minWidth: 32, textAlign: 'center' }}>
            {val}
          </Text>
          <IconButton
            icon="plus"
            variant="surface"
            onPress={() => onChange(f.key, val + 1)}
            accessibilityLabel={`Increase ${f.label}`}
          />
        </View>,
      );
    }

    // text | textarea | number | date | time | datetime | location → Input
    const numeric = NUMERIC_TYPES.has(f.type);
    return (
      <Input
        key={f.key}
        label={f.required ? `${f.label} *` : f.label}
        error={errors[f.key]}
        value={answers[f.key] != null ? String(answers[f.key]) : ''}
        placeholder={placeholderFor(f.type)}
        multiline={f.type === 'textarea'}
        keyboardType={numeric ? 'numeric' : 'default'}
        onChangeText={(t) => onChange(f.key, numeric ? (t === '' ? '' : Number(t)) : t)}
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
    </View>
  );
}
