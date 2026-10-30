// A date answer picked from the platform date picker, held as YYYY-MM-DD — the
// format the API reads booking dates in. Typed dates were the only way before,
// and a check-in of "next friday" reached the server.
import React, { useState } from 'react';
import DateTimePickerModal from 'react-native-modal-datetime-picker';
import { SelectRow } from '@/components/ui';
import { fromDateString, toDateString } from '@/utils/dateString';

export interface DateFieldProps {
  label: string;
  value?: string;
  onChange: (value: string) => void;
  error?: string;
  /** Earliest pickable day, YYYY-MM-DD. */
  minimumDate?: string;
}

export function DateField({ label, value, onChange, error, minimumDate }: DateFieldProps) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <SelectRow
        label={label}
        value={value || undefined}
        placeholder="Pick a date"
        leftIcon="calendar"
        error={error}
        onPress={() => setOpen(true)}
      />
      <DateTimePickerModal
        isVisible={open}
        mode="date"
        date={fromDateString(value) ?? fromDateString(minimumDate) ?? new Date()}
        minimumDate={fromDateString(minimumDate)}
        onConfirm={(d) => {
          setOpen(false);
          onChange(toDateString(d));
        }}
        onCancel={() => setOpen(false)}
      />
    </>
  );
}
