// components/services/BookingConfigBuilder.tsx
//
// Merchant field-config builder for booking services (unified booking engine).
// Renders the server's booking-field catalog as grouped toggles; enabling a
// field adds it to the service's FieldConfig, and a per-field "Required" toggle
// marks it mandatory. The resulting BookingField[] is lifted to the parent
// (AddService) and persisted on the ServiceProfile. The marketplace then
// auto-generates the customer form from exactly these fields.
import React, { useEffect, useMemo, useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Card, Divider, Spinner, Text, Toggle } from '@/components/ui';
import {
  getBookingCatalog,
  type BookingCatalogField,
  type BookingField,
} from '@/services/serviceListingApi';

interface BookingConfigBuilderProps {
  value: BookingField[];
  onChange: (fields: BookingField[]) => void;
}

const GROUP_LABELS: Record<string, string> = {
  location: 'Location',
  datetime: 'Date & time',
  people: 'People',
  serviceOptions: 'Service options',
  other: 'Other',
};

const BookingConfigBuilder: React.FC<BookingConfigBuilderProps> = ({ value, onChange }) => {
  const { spacing } = useTheme();
  const [catalog, setCatalog] = useState<BookingCatalogField[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const res = await getBookingCatalog();
      if (cancelled) return;
      setCatalog(res.success && res.data ? res.data.catalog || [] : []);
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Index the current selection by key for O(1) lookups while rendering.
  const selected = useMemo(() => {
    const m: Record<string, BookingField> = {};
    for (const f of value) m[f.key] = f;
    return m;
  }, [value]);

  const toggleField = (cat: BookingCatalogField, on: boolean) => {
    if (on) {
      onChange([
        ...value,
        { key: cat.key, type: cat.type, label: cat.label, group: cat.group, options: cat.options, required: false },
      ]);
    } else {
      onChange(value.filter((f) => f.key !== cat.key));
    }
  };

  const setRequired = (key: string, required: boolean) => {
    onChange(value.map((f) => (f.key === key ? { ...f, required } : f)));
  };

  const grouped = useMemo(() => {
    const order: string[] = [];
    const byGroup: Record<string, BookingCatalogField[]> = {};
    for (const c of catalog) {
      const g = c.group || 'other';
      if (!byGroup[g]) {
        byGroup[g] = [];
        order.push(g);
      }
      byGroup[g].push(c);
    }
    return order.map((g) => ({ group: g, items: byGroup[g] }));
  }, [catalog]);

  if (loading) {
    return (
      <View style={{ paddingVertical: spacing.lg, alignItems: 'center' }}>
        <Spinner label="Loading fields…" />
      </View>
    );
  }

  return (
    <View style={{ gap: spacing.md }}>
      <View>
        <Text variant="bodyMed" weight="700">
          Customer form fields
        </Text>
        <Text variant="caption" color="textSecondary">
          Enable the details customers must provide when booking. The form is
          generated automatically from your selection.
        </Text>
      </View>

      {grouped.map(({ group, items }) => (
        <Card key={group} padding="md" style={{ gap: spacing.sm }}>
          <Text variant="caption" weight="700" color="textSecondary">
            {GROUP_LABELS[group] ?? group}
          </Text>
          {items.map((cat, i) => {
            const sel = selected[cat.key];
            return (
              <View key={cat.key} style={{ gap: spacing.xs }}>
                {i > 0 ? <Divider /> : null}
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                  <Text variant="bodyMed" style={{ flex: 1 }}>
                    {cat.label}
                  </Text>
                  <Toggle
                    value={!!sel}
                    onValueChange={(on) => toggleField(cat, on)}
                    accessibilityLabel={`Enable ${cat.label}`}
                  />
                </View>
                {sel ? (
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                    <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                      Required
                    </Text>
                    <Toggle
                      value={sel.required}
                      onValueChange={(req) => setRequired(cat.key, req)}
                      accessibilityLabel={`Make ${cat.label} required`}
                    />
                  </View>
                ) : null}
              </View>
            );
          })}
        </Card>
      ))}
    </View>
  );
};

export default BookingConfigBuilder;
