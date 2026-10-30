// components/services/BookingForm.tsx
//
// Schema-driven customer booking form (unified booking engine). Renders the
// merchant's enabled fields — fetched as a BookingFormSchema — through the
// shared SchemaFields renderer, collects the buyer's answers keyed by field
// key, validates them, and hands the answers to the caller for the inline
// escrow checkout. No per-service-type branching: the form is generated
// entirely from `fields`.
//
// With `hideSubmit` the parent owns the CTA (pinned sheet footer) and triggers
// validation + submit through the BookingFormHandle ref instead.
import React, { forwardRef, useImperativeHandle, useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Button } from '@/components/ui';
import { SchemaFields } from '@/components/forms/SchemaFields';
import { toDateString } from '@/utils/dateString';
import { cleanSchemaAnswers, validateSchemaAnswers } from '@/utils/schemaFields';
import type { BookingField } from '../../services/serviceListingApi';

interface BookingFormProps {
  fields: BookingField[];
  submitting?: boolean;
  submitLabel?: string;
  onSubmit: (answers: Record<string, any>) => void;
  /** Hide the internal submit button — the parent renders the CTA in a pinned
   *  sheet footer and submits via the BookingFormHandle ref. */
  hideSubmit?: boolean;
  /** Called with the raw answers on every change (the sheet prices them live). */
  onChange?: (answers: Record<string, any>) => void;
}

export interface BookingFormHandle {
  /** Validate and submit — same path as the internal button. */
  submit: () => void;
}

const BookingForm = forwardRef<BookingFormHandle, BookingFormProps>(function BookingForm(
  { fields, submitting, submitLabel = 'Book & Pay', onSubmit, hideSubmit, onChange },
  ref,
) {
  const { spacing } = useTheme();
  const [answers, setAnswers] = useState<Record<string, any>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});

  const setAnswer = (key: string, value: any) => {
    const next = { ...answers, [key]: value };
    setAnswers(next);
    onChange?.(next);
    setErrors((prev) => (prev[key] ? { ...prev, [key]: '' } : prev));
  };

  const handleSubmit = () => {
    const next = validateSchemaAnswers(fields, answers);
    setErrors(next);
    if (Object.keys(next).length > 0) return;
    // Only the form's own, non-empty answers go out.
    onSubmit(cleanSchemaAnswers(fields, answers));
  };

  useImperativeHandle(ref, () => ({ submit: handleSubmit }));

  return (
    <View style={{ gap: spacing.lg }}>
      {/* A booking is never for a day already gone. */}
      <SchemaFields fields={fields} answers={answers} errors={errors} onChange={setAnswer} minDate={toDateString(new Date())} />
      {!hideSubmit && (
        <Button
          title={submitLabel}
          size="lg"
          leftIcon="shield"
          onPress={handleSubmit}
          loading={submitting}
          disabled={submitting}
        />
      )}
    </View>
  );
});

export default BookingForm;
