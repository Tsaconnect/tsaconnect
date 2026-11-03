// components/it/PayoutDetailsForm.tsx
//
// Recipient form for a foreign payout corridor (USD, EUR, GBP, GHS, KES, ZAR…).
// The server's withdraw-destinations catalog says which fields a corridor
// needs and validates the same list before the debit, so this component only
// renders what it is given: text inputs, a choice for `select`, the country's
// bank list for `bank`, and that bank's branches for `branch`.
//
// Controlled: `values` is the single source of truth, keyed by field key.
// `invalid` marks fields the server refused (payout_details_invalid).
import React, { useEffect, useState } from 'react';
import { View } from 'react-native';
import { Input, PickerSheet, SelectRow, Text } from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import { useTheme } from '@/theme';
import { getBanksForCountry, type NgBank } from '@/services/ngBanks';
import { banksApi } from '@/services/p2p';
import type { PayoutField } from '@/services/itApi';

export interface PayoutDetailsFormProps {
  fields: PayoutField[];
  /** ISO-2 country whose bank list a `bank` field loads. */
  country?: string;
  values: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
  invalid?: string[];
}

// The bank's provider id travels with its code so the branch list can load;
// the server keeps it only as a lookup aid.
const BANK_ID_KEY = 'bankId';

const bankKey = (b: NgBank) => b.id || b.code;

/** True when every field the corridor asks for has a value. */
export function payoutDetailsComplete(fields: PayoutField[], values: Record<string, string>): boolean {
  return fields.every((f) => (values[f.key] ?? '').trim() !== '');
}

function keyboardFor(field: PayoutField): 'default' | 'email-address' | 'phone-pad' | 'number-pad' {
  if (field.kind === 'email') return 'email-address';
  if (field.kind === 'phone') return 'phone-pad';
  if (field.key === 'routingNumber' || field.key === 'phoneCountryCode') return 'number-pad';
  return 'default';
}

function capitalizeFor(field: PayoutField): 'none' | 'words' | 'characters' {
  if (field.kind === 'email') return 'none';
  if (field.kind === 'country' || field.key === 'swiftCode') return 'characters';
  if (field.key === 'accountNumber' || field.key === 'sortCode' || field.key === 'postalCode') return 'characters';
  return 'words';
}

export default function PayoutDetailsForm({ fields, country, values, onChange, invalid = [] }: PayoutDetailsFormProps) {
  const { spacing } = useTheme();
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [banksLoading, setBanksLoading] = useState(false);
  const [branches, setBranches] = useState<NgBank[]>([]);
  const [branchesLoading, setBranchesLoading] = useState(false);
  // The bank whose branch list is in `branches`, so an empty list is only read
  // as "this bank has none" once it has actually loaded.
  const [branchesFor, setBranchesFor] = useState('');
  const [openPicker, setOpenPicker] = useState<string | null>(null);

  const needsBank = fields.some((f) => f.kind === 'bank');
  const needsBranch = fields.some((f) => f.kind === 'branch');
  const bankId = values[BANK_ID_KEY] ?? '';

  useEffect(() => {
    if (!needsBank || !country) return;
    let cancelled = false;
    setBanksLoading(true);
    (async () => {
      const list = await getBanksForCountry(country);
      if (!cancelled) {
        setBanks(list);
        setBanksLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [needsBank, country]);

  useEffect(() => {
    if (!needsBranch || !bankId) {
      setBranches([]);
      setBranchesFor('');
      return;
    }
    let cancelled = false;
    setBranchesLoading(true);
    (async () => {
      const res = await banksApi.branches(bankId);
      if (!cancelled) {
        setBranches(res.success && res.data ? [...res.data].sort((a, b) => a.name.localeCompare(b.name)) : []);
        setBranchesFor(bankId);
        setBranchesLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [needsBranch, bankId]);

  const set = (patch: Record<string, string>) => onChange({ ...values, ...patch });

  // Flutterwave lists no branches for some Ghanaian banks (or the lookup
  // fails). The branch is still required, so the user types the code their
  // bank gives them instead of facing an empty picker.
  const typeBranch = needsBranch && !!bankId && branchesFor === bankId && branches.length === 0;
  const branchKey = fields.find((f) => f.kind === 'branch')?.key;
  const onlyBranch = branchesFor === bankId && branches.length === 1 ? branches[0].code : '';

  // A bank with a single branch needs no choice.
  useEffect(() => {
    if (branchKey && onlyBranch && !values[branchKey]) onChange({ ...values, [branchKey]: onlyBranch });
  }, [branchKey, onlyBranch, values, onChange]);
  const errorFor = (key: string) => (invalid.includes(key) ? 'Check this field' : undefined);

  const pickerItems = (field: PayoutField): PickerItem[] => {
    // Keyed by the provider id when there is one: two banks in a directory
    // can share a code, and the id is what the branch lookup needs.
    if (field.kind === 'bank') return banks.map((b) => ({ id: bankKey(b), label: b.name, icon: 'bank' as const }));
    if (field.kind === 'branch') return branches.map((b) => ({ id: b.code, label: b.name, icon: 'bank' as const }));
    return (field.options ?? []).map((o) => ({ id: o, label: o.charAt(0).toUpperCase() + o.slice(1) }));
  };

  const selectedLabel = (field: PayoutField): string | undefined => {
    const v = values[field.key];
    if (!v) return undefined;
    if (field.kind === 'bank') {
      // By id first (codes can repeat), then by code if the id went stale.
      return (banks.find((b) => b.code === v && b.id === bankId) ?? banks.find((b) => b.code === v))?.name ?? v;
    }
    return pickerItems(field).find((i) => i.id === v)?.label ?? v;
  };

  const activeField = fields.find((f) => f.key === openPicker);

  return (
    <View style={{ gap: spacing.md }}>
      {fields.map((field) => {
        if (field.kind === 'branch' && typeBranch) {
          return (
            <Input
              key={field.key}
              label={field.label}
              placeholder="Branch code from your bank"
              value={values[field.key] ?? ''}
              autoCapitalize="characters"
              autoCorrect={false}
              maxLength={40}
              error={errorFor(field.key)}
              onChangeText={(v) => set({ [field.key]: v })}
            />
          );
        }
        if (field.kind === 'bank' || field.kind === 'branch' || field.kind === 'select') {
          const loading = field.kind === 'bank' ? banksLoading : field.kind === 'branch' ? branchesLoading : false;
          const blocked = field.kind === 'branch' && !bankId;
          return (
            <View key={field.key}>
              <SelectRow
                label={field.label}
                placeholder={
                  blocked ? 'Select a bank first' : loading ? 'Loading…' : `Select ${field.label.toLowerCase()}`
                }
                value={selectedLabel(field)}
                leftIcon={field.kind === 'select' ? undefined : 'bank'}
                disabled={blocked}
                onPress={() => setOpenPicker(field.key)}
              />
              {errorFor(field.key) ? (
                <Text variant="caption" color="danger">{errorFor(field.key)}</Text>
              ) : null}
            </View>
          );
        }
        return (
          <Input
            key={field.key}
            label={field.label}
            placeholder={field.hint}
            value={values[field.key] ?? ''}
            keyboardType={keyboardFor(field)}
            autoCapitalize={capitalizeFor(field)}
            autoCorrect={false}
            maxLength={field.kind === 'country' ? 2 : 120}
            error={errorFor(field.key)}
            onChangeText={(v) => set({ [field.key]: v })}
          />
        );
      })}

      <PickerSheet
        visible={!!activeField}
        onClose={() => setOpenPicker(null)}
        title={activeField ? `Select ${activeField.label.toLowerCase()}` : ''}
        items={activeField ? pickerItems(activeField) : []}
        loading={activeField?.kind === 'bank' ? banksLoading : activeField?.kind === 'branch' ? branchesLoading : false}
        selectedId={
          activeField?.kind === 'bank'
            ? bankId || values[activeField.key] || undefined
            : activeField
              ? values[activeField.key] || undefined
              : undefined
        }
        emptyText="Nothing available right now."
        onSelect={(item) => {
          if (!activeField) return;
          setOpenPicker(null);
          if (activeField.kind === 'bank') {
            const bank = banks.find((b) => bankKey(b) === item.id);
            // A new bank invalidates the branch chosen for the old one.
            set({ [activeField.key]: bank?.code || item.id, [BANK_ID_KEY]: bank?.id ?? '', branch: '' });
            return;
          }
          set({ [activeField.key]: item.id });
        }}
      />
    </View>
  );
}
