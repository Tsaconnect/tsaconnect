// components/forms/ListingFilters.tsx
//
// Search filters for a category's listings: where (country/state/city from the
// shared LocationPicker, plus an area) and the category's filterable listing
// fields. The browse screen gets one compact row — a "Filters" chip plus a chip
// per active filter (tap to clear it) — and the choices live in a sheet that
// edits a draft and applies it on "Show results", so the list reloads once, not
// per tap. Listing fields go to the API as attr[key]=value and the place as
// country/state/city/area. Free-text and number listing fields are not offered —
// chips can only pick from a known set.
import React, { useState } from 'react';
import { ScrollView, View } from 'react-native';
import { useTheme } from '@/theme';
import { Button, Chip, Input, Sheet, Text } from '@/components/ui';
import LocationPicker from '@/components/common/LocationPicker';
import { useCategoryListingFields } from '@/hooks/useCategoryListingFields';
import type { SchemaField } from '@/utils/schemaFields';
import { formatLocation, type LocationValue } from '@/utils/location';

export interface ListingFilterValue {
  attr: Record<string, string>;
  place: LocationValue;
  area: string;
}

export const EMPTY_LISTING_FILTERS: ListingFilterValue = {
  attr: {},
  place: { country: '', state: '', city: '' },
  area: '',
};

export function isFiltering(value: ListingFilterValue): boolean {
  return (
    Object.values(value.attr).some((v) => v !== '') ||
    formatLocation(value.place) !== '' ||
    value.area.trim() !== ''
  );
}

// The filters as api.getProductsByCategory takes them (blank parts left out).
export function listingFilterParams(value: ListingFilterValue) {
  const params: { attr: Record<string, string>; country?: string; state?: string; city?: string; area?: string } = {
    attr: value.attr,
  };
  const put = (key: 'country' | 'state' | 'city' | 'area', v: string) => {
    if (v.trim()) params[key] = v.trim();
  };
  put('country', value.place.country);
  put('state', value.place.state);
  put('city', value.place.city);
  put('area', value.area);
  return params;
}

interface ListingFiltersProps {
  categoryId?: string | null;
  value: ListingFilterValue;
  onChange: (value: ListingFilterValue) => void;
}

function choicesFor(f: SchemaField): { value: string; label: string }[] {
  if (f.type === 'boolean') return [{ value: 'true', label: 'Yes' }, { value: 'false', label: 'No' }];
  return (f.options ?? []).map((o) => ({ value: o, label: o }));
}

export function ListingFilters({ categoryId, value, onChange }: ListingFiltersProps) {
  const { spacing } = useTheme();
  const { fields } = useCategoryListingFields(categoryId);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<ListingFilterValue>(EMPTY_LISTING_FILTERS);
  const filterable = fields.filter(
    (f) => f.filterable && (f.type === 'boolean' || ((f.type === 'select' || f.type === 'multiselect') && f.options?.length)),
  );

  const activeFields = filterable.filter((f) => (value.attr[f.key] ?? '') !== '');
  const placeLabel = formatLocation(value.place);
  const activeCount = activeFields.length + (placeLabel ? 1 : 0) + (value.area.trim() ? 1 : 0);
  const labelFor = (f: SchemaField) =>
    choicesFor(f).find((c) => c.value === value.attr[f.key])?.label ?? value.attr[f.key];

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
          label={activeCount > 0 ? `Filters (${activeCount})` : 'Filters'}
          leftIcon="filter"
          selected={activeCount > 0}
          onPress={openSheet}
        />
        {placeLabel ? (
          <Chip
            label={placeLabel}
            leftIcon="close"
            onPress={() => onChange({ ...value, place: EMPTY_LISTING_FILTERS.place })}
          />
        ) : null}
        {value.area.trim() ? (
          <Chip label={`Area: ${value.area.trim()}`} leftIcon="close" onPress={() => onChange({ ...value, area: '' })} />
        ) : null}
        {activeFields.map((f) => (
          <Chip
            key={f.key}
            label={`${f.label}: ${labelFor(f)}`}
            leftIcon="close"
            onPress={() => onChange({ ...value, attr: { ...value.attr, [f.key]: '' } })}
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
            <Button
              title="Clear all"
              variant="secondary"
              onPress={() => setDraft(EMPTY_LISTING_FILTERS)}
              style={{ flex: 1 }}
            />
            <Button title="Show results" onPress={apply} style={{ flex: 1 }} />
          </View>
        }
      >
        <View style={{ gap: spacing.lg, paddingHorizontal: spacing.lg, paddingVertical: spacing.sm }}>
          <View style={{ gap: spacing.sm }}>
            <Text variant="bodySm" weight="600">
              Location
            </Text>
            <LocationPicker value={draft.place} onChange={(place) => setDraft((prev) => ({ ...prev, place }))} />
            <Input
              label="Area or neighbourhood"
              value={draft.area}
              onChangeText={(area) => setDraft((prev) => ({ ...prev, area }))}
              placeholder="e.g. Odenigbo, Kubwa, Yaba"
            />
          </View>

          {filterable.map((f) => {
            const current = draft.attr[f.key] ?? '';
            const set = (v: string) => setDraft((prev) => ({ ...prev, attr: { ...prev.attr, [f.key]: v } }));
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
