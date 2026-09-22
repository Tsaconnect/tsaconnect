// Pick, add, edit or delete a saved delivery address at checkout. Two steps in
// one sheet: the list (pick one) and the form (add or edit). A buyer with no
// saved address lands straight on the form, prefilled from their profile.
import React, { useEffect, useState } from 'react';
import { Alert, View } from 'react-native';
import { useTheme } from '@/theme';
import LocationPicker from '@/components/common/LocationPicker';
import { Button, Icon, IconButton, Input, PressableScale, Sheet, Text, Toggle } from '@/components/ui';
import {
  createDeliveryAddress,
  deleteDeliveryAddress,
  formatDeliveryAddress,
  updateDeliveryAddress,
  type DeliveryAddress,
  type DeliveryAddressInput,
} from '@/services/deliveryAddressApi';
import { validateAddressForm, type AddressFormErrors } from '@/hooks/useDeliveryAddresses';

export interface DeliveryAddressSheetProps {
  visible: boolean;
  onClose: () => void;
  addresses: DeliveryAddress[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  /**
   * Reload the list after a save or delete; `select` picks the saved address.
   * Resolves to the new list, or null when the reload failed.
   */
  onChanged: (select?: string) => Promise<DeliveryAddress[] | null>;
  /** Prefill for a buyer's first address (profile name, phone and location). */
  prefill: Partial<DeliveryAddressInput>;
}

const EMPTY: DeliveryAddressInput = {
  label: '',
  recipientName: '',
  phone: '',
  street: '',
  city: '',
  state: '',
  country: '',
  isDefault: false,
};

export const DeliveryAddressSheet = ({
  visible,
  onClose,
  addresses,
  selectedId,
  onSelect,
  onChanged,
  prefill,
}: DeliveryAddressSheetProps) => {
  const { colors, spacing, radius } = useTheme();
  const [mode, setMode] = useState<'list' | 'form'>('list');
  const [editing, setEditing] = useState<DeliveryAddress | null>(null);
  const [form, setForm] = useState<DeliveryAddressInput>(EMPTY);
  const [errors, setErrors] = useState<AddressFormErrors>({});
  const [saving, setSaving] = useState(false);

  /** `savedCount` is passed, not read from props, so it is right straight after a delete. */
  const openForm = (address: DeliveryAddress | null, savedCount = addresses.length) => {
    setEditing(address);
    setErrors({});
    setForm(
      address
        ? { ...EMPTY, ...address, label: address.label ?? '' }
        : { ...EMPTY, ...prefill, isDefault: savedCount === 0 },
    );
    setMode('form');
  };

  // Each opening starts on the list, or on the form when there is nothing to pick.
  useEffect(() => {
    if (!visible) return;
    if (addresses.length === 0) openForm(null, 0);
    else setMode('list');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible]);

  const set = (patch: Partial<DeliveryAddressInput>) => setForm((f) => ({ ...f, ...patch }));

  const save = async () => {
    const found = validateAddressForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setSaving(true);
    const res = editing ? await updateDeliveryAddress(editing.id, form) : await createDeliveryAddress(form);
    setSaving(false);
    if (!res.success || !res.data) {
      Alert.alert('Could not save address', res.message || 'Please try again.');
      return;
    }
    // A new address becomes the pick; an edit only keeps the pick it already had.
    const keepPick = editing !== null && editing.id !== selectedId;
    await onChanged(keepPick ? undefined : res.data.id);
    onClose();
  };

  const remove = (address: DeliveryAddress) => {
    Alert.alert('Delete address?', formatDeliveryAddress(address), [
      { text: 'Cancel', style: 'cancel' },
      {
        text: 'Delete',
        style: 'destructive',
        onPress: async () => {
          const res = await deleteDeliveryAddress(address.id);
          if (!res.success) {
            Alert.alert('Could not delete address', res.message || 'Please try again.');
            return;
          }
          const rows = await onChanged();
          // Deleting the last address leaves nothing to pick: go to the form.
          if (rows?.length === 0) openForm(null, 0);
          else setMode('list');
        },
      },
    ]);
  };

  if (mode === 'form') {
    return (
      <Sheet
        visible={visible}
        onClose={onClose}
        title={editing ? 'Edit address' : 'New address'}
        onBack={addresses.length > 0 ? () => setMode('list') : undefined}
        maxHeightRatio={0.92}
        scrollable
        footer={
          <>
            <Button title="Save address" onPress={save} loading={saving} disabled={saving} />
            {editing ? (
              <Button title="Delete address" variant="ghost" onPress={() => remove(editing)} disabled={saving} />
            ) : null}
          </>
        }
      >
        <View style={{ padding: spacing.lg, gap: spacing.md }}>
          <Input
            label="Recipient name *"
            value={form.recipientName}
            onChangeText={(recipientName) => set({ recipientName })}
            error={errors.recipientName}
            autoComplete="name"
            maxLength={120}
          />
          <Input
            label="Phone number *"
            value={form.phone}
            onChangeText={(phone) => set({ phone })}
            error={errors.phone}
            keyboardType="phone-pad"
            autoComplete="tel"
            maxLength={32}
          />
          <Input
            label="Street address *"
            placeholder="House number, street, landmark"
            value={form.street}
            onChangeText={(street) => set({ street })}
            error={errors.street}
            autoComplete="street-address"
            maxLength={255}
            multiline
          />
          <LocationPicker
            value={{ country: form.country, state: form.state, city: form.city }}
            onChange={(loc) => set(loc)}
            required={['country', 'state', 'city']}
            errors={{ country: errors.country, state: errors.state, city: errors.city }}
          />
          <Input
            label="Label (optional)"
            placeholder="Home, Office…"
            value={form.label}
            onChangeText={(label) => set({ label })}
            maxLength={40}
          />
          {editing?.isDefault ? null : (
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
              <Text variant="bodyMed">Use as my default address</Text>
              <Toggle
                value={!!form.isDefault}
                onValueChange={(isDefault) => set({ isDefault })}
                accessibilityLabel="Use as my default address"
              />
            </View>
          )}
        </View>
      </Sheet>
    );
  }

  return (
    <Sheet
      visible={visible}
      onClose={onClose}
      title="Deliver to"
      scrollable
      footer={<Button title="Add new address" leftIcon="plus" variant="secondary" onPress={() => openForm(null)} />}
    >
      <View style={{ padding: spacing.lg, gap: spacing.sm }}>
        {addresses.map((a) => {
          const selected = a.id === selectedId;
          return (
            <PressableScale
              key={a.id}
              onPress={() => {
                onSelect(a.id);
                onClose();
              }}
              accessibilityRole="radio"
              accessibilityState={{ selected }}
              style={{
                flexDirection: 'row',
                alignItems: 'flex-start',
                gap: spacing.md,
                padding: spacing.md,
                borderRadius: radius.md,
                borderWidth: 1.5,
                borderColor: selected ? colors.accent : colors.border,
                backgroundColor: selected ? colors.accentMuted : colors.surface,
              }}
            >
              <Icon name={selected ? 'checkCircle' : 'location'} size={20} color={selected ? colors.accent : colors.textMuted} />
              <View style={{ flex: 1, gap: 2 }}>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                  <Text variant="bodyMed" weight="600" numberOfLines={1} style={{ flexShrink: 1 }}>
                    {a.label || a.recipientName}
                  </Text>
                  {a.isDefault ? (
                    <Text variant="caption" weight="600" color="accent">
                      · Default
                    </Text>
                  ) : null}
                </View>
                {a.label ? (
                  <Text variant="bodySm" color="textSecondary">
                    {a.recipientName}
                  </Text>
                ) : null}
                <Text variant="bodySm" color="textSecondary">
                  {formatDeliveryAddress(a)}
                </Text>
                <Text variant="caption" color="textMuted">
                  {a.phone}
                </Text>
              </View>
              <IconButton icon="edit" accessibilityLabel={`Edit ${a.label || 'address'}`} size={18} onPress={() => openForm(a)} />
            </PressableScale>
          );
        })}
      </View>
    </Sheet>
  );
};
