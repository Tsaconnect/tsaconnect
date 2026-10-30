// utils/bookingQuoteRefusal.ts
// What the booking sheet tells the buyer when the live quote is refused. The
// quote also fails while the form is still being filled ("From is required"),
// which the form already shows, so the refusal is only news once every
// required answer — and, on a slotted listing, the slot — is in: then it is
// about the booking itself ("sold out on 31 Oct 2026", "Only 2 left").
import { validateSchemaAnswers, type SchemaAnswers, type SchemaField } from '@/utils/schemaFields';

export function bookingQuoteRefusal(
  res: { success: boolean; message?: string },
  form: { fields: SchemaField[]; answers: SchemaAnswers; slotMissing: boolean },
): string | null {
  if (res.success || !res.message) return null;
  if (form.slotMissing) return null;
  if (Object.keys(validateSchemaAnswers(form.fields, form.answers)).length > 0) return null;
  return res.message;
}
