// components/pay/NairaField.tsx
//
// A naira amount field for TSA Pay links: ₦ prefix, decimal keypad, text kept as
// typed. Deliberately not AmountInput — that one strips a comma, and "5000,50"
// must be refused (utils/payLinks koboFromNairaInput), not read as 500,050.
import React from 'react';
import { Input, Text } from '@/components/ui';

export function NairaField(props: {
  label: string;
  value: string;
  onChangeText: (v: string) => void;
  error?: string;
  helper?: string;
  placeholder?: string;
}) {
  return (
    <Input
      {...props}
      placeholder={props.placeholder ?? '0.00'}
      keyboardType="decimal-pad"
      leftAdornment={<Text variant="bodyMed" weight="700" color="textSecondary">₦</Text>}
    />
  );
}
