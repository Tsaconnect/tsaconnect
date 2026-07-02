// components/services/PolicyFields.tsx
//
// Booking-type policy text fields (cancellation / reschedule / refund / booking
// rules). Controlled — the parent owns the values and persists them in the
// ServiceProfile via upsertServiceProfile.
//
// TSA Signature design system only (@/theme + @/components/ui).
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Card, Input } from '@/components/ui';

export interface PolicyValues {
  cancellationPolicy: string;
  reschedulePolicy: string;
  refundPolicy: string;
  bookingRules: string;
}

export const emptyPolicyValues = (): PolicyValues => ({
  cancellationPolicy: '',
  reschedulePolicy: '',
  refundPolicy: '',
  bookingRules: '',
});

interface PolicyFieldsProps {
  value: PolicyValues;
  onChange: (patch: Partial<PolicyValues>) => void;
}

const PolicyFields: React.FC<PolicyFieldsProps> = ({ value, onChange }) => {
  const { spacing } = useTheme();

  const field = (
    label: string,
    key: keyof PolicyValues,
    placeholder: string
  ) => (
    <Input
      label={label}
      value={value[key]}
      onChangeText={(t) => onChange({ [key]: t })}
      placeholder={placeholder}
      multiline
      numberOfLines={3}
      textAlignVertical="top"
      inputStyle={{ minHeight: 72 }}
    />
  );

  return (
    <Card padding="lg" style={{ gap: spacing.lg }}>
      {field('Cancellation policy', 'cancellationPolicy', 'e.g. Free cancellation up to 24h before')}
      {field('Reschedule policy', 'reschedulePolicy', 'e.g. One free reschedule allowed')}
      {field('Refund policy', 'refundPolicy', 'e.g. 50% refund within 48h')}
      {field('Booking rules', 'bookingRules', 'Any other rules buyers should know')}
    </Card>
  );
};

export default PolicyFields;
