// components/services/BookingForm.tsx
//
// Schema-driven customer booking form (unified booking engine). Renders the
// merchant's enabled fields — fetched as a BookingFormSchema — by mapping each
// field `type` to a TSA Signature primitive, collects the buyer's answers keyed
// by field key, validates required fields, and hands the answers to the caller
// for the inline escrow checkout. No per-service-type branching: the form is
// generated entirely from `fields`.
import React, { useMemo, useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Button, IconButton, Input, RadioGroup, Text } from '@/components/ui';
import type { BookingField } from '../../services/serviceListingApi';

interface BookingFormProps {
  fields: BookingField[];
  submitting?: boolean;
  submitLabel?: string;
  onSubmit: (answers: Record<string, any>) => void;
}

// Field types that store a numeric answer.
const NUMERIC_TYPES = new Set(['counter', 'number']);

const placeholderFor = (type: BookingField['type']): string | undefined => {
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

const BookingForm: React.FC<BookingFormProps> = ({
  fields,
  submitting,
  submitLabel = 'Book & Pay',
  onSubmit,
}) => {
  const { spacing } = useTheme();
  const [answers, setAnswers] = useState<Record<string, any>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});

  const setAnswer = (key: string, value: any) => {
    setAnswers((prev) => ({ ...prev, [key]: value }));
    setErrors((prev) => (prev[key] ? { ...prev, [key]: '' } : prev));
  };

  const isEmpty = (v: any) => v === undefined || v === null || (typeof v === 'string' && v.trim() === '');

  const validate = (): boolean => {
    const next: Record<string, string> = {};
    for (const f of fields) {
      if (!f.required) continue;
      // A required counter must be at least 1 (0 is a valid number but means
      // "none selected"); other required fields just need a non-empty value.
      if (f.type === 'counter') {
        if (!(typeof answers[f.key] === 'number' && answers[f.key] >= 1)) {
          next[f.key] = `${f.label} is required`;
        }
      } else if (isEmpty(answers[f.key])) {
        next[f.key] = `${f.label} is required`;
      }
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  };

  const handleSubmit = () => {
    if (!validate()) return;
    // Drop empty optional answers so the payload only carries real values.
    const cleaned: Record<string, any> = {};
    for (const f of fields) {
      const v = answers[f.key];
      if (!isEmpty(v)) cleaned[f.key] = v;
    }
    onSubmit(cleaned);
  };

  const renderField = (f: BookingField) => {
    const label = f.required ? `${f.label} *` : f.label;
    const error = errors[f.key];

    if (f.type === 'select') {
      const options = (f.options ?? []).map((o) => ({ value: o, label: o }));
      return (
        <View key={f.key} style={{ gap: spacing.xs }}>
          <Text variant="bodyMed" weight="600">
            {label}
          </Text>
          <RadioGroup
            options={options}
            value={answers[f.key] ?? ''}
            onChange={(v) => setAnswer(f.key, v)}
          />
          {error ? (
            <Text variant="caption" color="danger">
              {error}
            </Text>
          ) : null}
        </View>
      );
    }

    if (f.type === 'counter') {
      const val = typeof answers[f.key] === 'number' ? answers[f.key] : 0;
      return (
        <View key={f.key} style={{ gap: spacing.xs }}>
          <Text variant="bodyMed" weight="600">
            {label}
          </Text>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
            <IconButton
              icon="minus"
              variant="surface"
              onPress={() => setAnswer(f.key, Math.max(0, val - 1))}
              accessibilityLabel={`Decrease ${f.label}`}
            />
            <Text variant="title3" weight="700" style={{ minWidth: 32, textAlign: 'center' }}>
              {val}
            </Text>
            <IconButton
              icon="plus"
              variant="surface"
              onPress={() => setAnswer(f.key, val + 1)}
              accessibilityLabel={`Increase ${f.label}`}
            />
          </View>
          {error ? (
            <Text variant="caption" color="danger">
              {error}
            </Text>
          ) : null}
        </View>
      );
    }

    // text | textarea | number | date | time | datetime | location → Input
    const numeric = NUMERIC_TYPES.has(f.type);
    return (
      <Input
        key={f.key}
        label={label}
        error={error}
        value={answers[f.key] != null ? String(answers[f.key]) : ''}
        placeholder={placeholderFor(f.type)}
        multiline={f.type === 'textarea'}
        keyboardType={numeric ? 'numeric' : 'default'}
        onChangeText={(t) => setAnswer(f.key, numeric ? (t === '' ? '' : Number(t)) : t)}
      />
    );
  };

  const grouped = useMemo(() => {
    // Preserve config order but keep grouped fields visually adjacent.
    const order: string[] = [];
    const byGroup: Record<string, BookingField[]> = {};
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
      <Button
        title={submitLabel}
        size="lg"
        leftIcon="shield"
        onPress={handleSubmit}
        loading={submitting}
        disabled={submitting}
      />
    </View>
  );
};

export default BookingForm;
