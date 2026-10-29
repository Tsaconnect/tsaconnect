// Shared logic for schema-driven forms: the buyer's booking form and the
// merchant's category listing fields both come from the same field
// vocabulary on the API (field_defs), so they validate and clean answers the
// same way the API does (models.ValidateAnswers).

export type SchemaFieldType =
  | 'text' | 'textarea' | 'number' | 'counter' | 'select' | 'multiselect' | 'boolean'
  | 'date' | 'time' | 'datetime' | 'location';

export interface SchemaField {
  key: string;
  type: SchemaFieldType;
  label: string;
  group?: string;
  required: boolean;
  options?: string[];
  filterable?: boolean;
}

export type SchemaAnswers = Record<string, any>;

export function isEmptyAnswer(v: unknown): boolean {
  return (
    v === undefined ||
    v === null ||
    (typeof v === 'string' && v.trim() === '') ||
    (Array.isArray(v) && v.length === 0)
  );
}

// A number field holds the typed text ("1.", "-") until submit; this is its
// value, or NaN when the text is not a number.
function numberAnswer(v: unknown): number {
  if (typeof v === 'number') return v;
  if (typeof v === 'string' && v.trim() !== '') return Number(v.trim());
  return NaN;
}

// Errors keyed by field key; empty when the answers are acceptable.
export function validateSchemaAnswers(fields: SchemaField[], answers: SchemaAnswers): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const f of fields) {
    const v = answers[f.key];
    if (f.type === 'counter') {
      // 0 is a number but means "none selected" for a required counter.
      if (f.required && !(typeof v === 'number' && v >= 1)) errors[f.key] = `${f.label} is required`;
      continue;
    }
    if (f.type === 'boolean') continue; // a switch always has an answer
    if (isEmptyAnswer(v)) {
      if (f.required) errors[f.key] = `${f.label} is required`;
      continue;
    }
    if (f.type === 'number' && !Number.isFinite(numberAnswer(v))) errors[f.key] = `${f.label} must be a number`;
  }
  return errors;
}

// Only the answers the form asks for, empty ones dropped — the payload the
// API stores. A boolean is sent only when the form has it, as true or false.
export function cleanSchemaAnswers(fields: SchemaField[], answers: SchemaAnswers): SchemaAnswers {
  const out: SchemaAnswers = {};
  for (const f of fields) {
    const raw = answers[f.key];
    if (f.type === 'boolean') {
      out[f.key] = !!raw;
      continue;
    }
    if (isEmptyAnswer(raw)) continue;
    out[f.key] = f.type === 'number' ? numberAnswer(raw) : raw;
  }
  return out;
}

// One answer as display text ("Wifi, Water", "Yes"), or null when unanswered.
export function formatSchemaAnswer(field: SchemaField, value: unknown): string | null {
  if (field.type === 'boolean') return value === true ? 'Yes' : value === false ? 'No' : null;
  if (isEmptyAnswer(value)) return null;
  if (Array.isArray(value)) return value.join(', ');
  return String(value);
}

// Search filters for the filterable fields a buyer can pick from, as the
// API's attr[key]=value query parameters.
export function filterQuery(filters: Record<string, string>): string {
  return Object.entries(filters)
    .filter(([, v]) => v !== '')
    .map(([k, v]) => `attr[${encodeURIComponent(k)}]=${encodeURIComponent(v)}`)
    .join('&');
}
