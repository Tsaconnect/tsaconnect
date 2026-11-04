// components/paycrest/BuyerIdSheet.tsx
//
// Asks for the BVN or NIN and date of birth our bank-transfer provider needs
// on a purchase. Shown only when this phone has none saved (or the provider
// refused the saved one). Kept on the phone; never stored by us.

import React, { useEffect, useState } from 'react';
import { View } from 'react-native';
import { Button, Input, SegmentedControl, Sheet, Text } from '@/components/ui';
import { useTheme } from '@/theme';
import {
  birthDateForInput,
  formatBirthDateInput,
  parseBuyerId,
  type BuyerId,
  type BuyerIdField,
  type BuyerIdType,
} from '@/services/buyerId';

export interface BuyerIdSheetProps {
  visible: boolean;
  /** Prefills the form, e.g. when the provider refused the saved details. */
  initial?: BuyerId | null;
  /** Shown above the form, e.g. why the saved details were refused. */
  notice?: string;
  onClose: () => void;
  onSubmit: (id: BuyerId) => void;
}

const SEGMENTS: { key: BuyerIdType; label: string }[] = [
  { key: 'bvn', label: 'BVN' },
  { key: 'nin', label: 'NIN' },
];

export default function BuyerIdSheet({ visible, initial, notice, onClose, onSubmit }: BuyerIdSheetProps) {
  const { spacing } = useTheme();
  const [type, setType] = useState<BuyerIdType>('bvn');
  const [number, setNumber] = useState('');
  const [birthDate, setBirthDate] = useState('');
  const [error, setError] = useState<{ field: BuyerIdField; message: string } | null>(null);

  useEffect(() => {
    if (!visible) return;
    setType(initial?.type ?? 'bvn');
    setNumber(initial?.number ?? '');
    setBirthDate(initial ? birthDateForInput(initial.birthDate) : '');
    setError(null);
  }, [visible, initial]);

  // Nothing mounted while closed: it is only ever opened by a refused buy.
  if (!visible) return null;

  const submit = () => {
    const parsed = parseBuyerId({ type, number, birthDate });
    if ('field' in parsed) {
      setError(parsed);
      return;
    }
    onSubmit(parsed.id);
  };

  return (
    <Sheet
      visible={visible}
      onClose={onClose}
      title="Confirm your identity"
      scrollable
      footer={<Button title="Save and continue" onPress={submit} fullWidth />}
    >
      <View style={{ gap: spacing.md }}>
        <Text variant="bodySm" color="textSecondary">
          Our bank-transfer partner needs your BVN or NIN and date of birth for each purchase. We keep them only on
          this phone, so you enter them once. Pay from an account in your own name.
        </Text>
        {notice ? (
          <Text variant="bodySm" color="danger">
            {notice}
          </Text>
        ) : null}
        <SegmentedControl
          segments={SEGMENTS}
          value={type}
          onChange={(k) => {
            setType(k);
            setError(null);
          }}
        />
        <Input
          label={type === 'bvn' ? 'BVN' : 'NIN'}
          placeholder="11 digits"
          value={number}
          keyboardType="number-pad"
          maxLength={11}
          autoCorrect={false}
          error={error?.field === 'number' ? error.message : undefined}
          onChangeText={(v) => {
            setNumber(v.replace(/\D/g, ''));
            setError(null);
          }}
        />
        <Input
          label="Date of birth"
          placeholder="DD/MM/YYYY"
          value={birthDate}
          keyboardType="number-pad"
          maxLength={10}
          error={error?.field === 'birthDate' ? error.message : undefined}
          onChangeText={(v) => {
            setBirthDate(formatBirthDateInput(v));
            setError(null);
          }}
        />
      </View>
    </Sheet>
  );
}
