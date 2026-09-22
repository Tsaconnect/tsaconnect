// The buyer's saved delivery addresses for checkout, plus which one is picked.
// The pick survives reloads while it still exists and otherwise falls back to
// the default, so deleting the picked address never leaves checkout pointing
// at nothing.
import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  listDeliveryAddresses,
  type DeliveryAddress,
  type DeliveryAddressInput,
} from '@/services/deliveryAddressApi';

export function pickAddress(addresses: DeliveryAddress[], currentId: string | null): string | null {
  if (currentId && addresses.some((a) => a.id === currentId)) return currentId;
  return (addresses.find((a) => a.isDefault) ?? addresses[0])?.id ?? null;
}

type RequiredField = 'recipientName' | 'phone' | 'street' | 'city' | 'state' | 'country';
export type AddressFormErrors = Partial<Record<RequiredField, string>>;

const REQUIRED: Record<RequiredField, string> = {
  recipientName: 'Enter the recipient’s name',
  phone: 'Enter a phone number',
  street: 'Enter a street address',
  city: 'Select a city',
  state: 'Select a state',
  country: 'Select a country',
};

export function validateAddressForm(form: Pick<DeliveryAddressInput, RequiredField>): AddressFormErrors {
  const errors: AddressFormErrors = {};
  for (const field of Object.keys(REQUIRED) as RequiredField[]) {
    if (!form[field]?.trim()) errors[field] = REQUIRED[field];
  }
  if (!errors.phone && form.phone.replace(/\D/g, '').length < 7) {
    errors.phone = 'Enter a valid phone number';
  }
  return errors;
}

export function useDeliveryAddresses() {
  const [addresses, setAddresses] = useState<DeliveryAddress[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  // A failed load is NOT an empty address book: showing "Add address" then
  // invites duplicates, so the error is surfaced for a retry instead.
  const [error, setError] = useState<string | null>(null);

  /**
   * Reloads the list; `select` picks a just-saved address. Resolves to the new
   * list, or null when the load failed (the previous list is kept).
   */
  const reload = useCallback(async (select?: string): Promise<DeliveryAddress[] | null> => {
    setLoading(true);
    const res = await listDeliveryAddresses();
    setLoading(false);
    if (!res.success || !res.data) {
      setError(res.message || 'Could not load your addresses');
      return null;
    }
    const rows = res.data;
    setError(null);
    setAddresses(rows);
    setSelectedId((current) => pickAddress(rows, select ?? current));
    return rows;
  }, []);

  useEffect(() => {
    reload();
  }, [reload]);

  const selected = useMemo(() => addresses.find((a) => a.id === selectedId) ?? null, [addresses, selectedId]);

  return { addresses, selected, select: setSelectedId, loading, error, reload };
}
