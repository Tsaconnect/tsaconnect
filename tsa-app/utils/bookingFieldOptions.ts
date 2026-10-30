// utils/bookingFieldOptions.ts
// Choices for select-type booking fields the merchant types in themselves —
// needed when the catalogue ships the field without options (Vehicle type,
// Cabin class). The API refuses a select with no options, so the builder and
// the submit guard share these.
import type { BookingField } from '@/services/serviceListingApi';

export const MAX_FIELD_OPTIONS = 30;

export function fieldTakesOptions(field: Pick<BookingField, 'type'>): boolean {
  return field.type === 'select' || field.type === 'multiselect';
}

// "Saloon, SUV ,, suv" → ["Saloon", "SUV"]: trimmed, blanks and
// case-insensitive repeats dropped, capped at MAX_FIELD_OPTIONS.
export function parseOptionList(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(',')) {
    const option = raw.trim();
    const key = option.toLowerCase();
    if (!option || seen.has(key)) continue;
    seen.add(key);
    out.push(option);
    if (out.length === MAX_FIELD_OPTIONS) break;
  }
  return out;
}

// Labels of switched-on choice fields that still have nothing to choose from.
export function fieldsMissingOptions(fields: BookingField[]): string[] {
  return fields.filter((f) => fieldTakesOptions(f) && !(f.options?.length)).map((f) => f.label);
}
