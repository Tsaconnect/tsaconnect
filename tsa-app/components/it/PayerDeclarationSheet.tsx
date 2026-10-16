// components/it/PayerDeclarationSheet.tsx
//
// "Paid from another account?" on a naira buy. The automatic match pairs a
// payment with an order by amount and the buyer's own verified name; money from
// a spouse's, friend's or business account carries someone else's name and
// waits. Here the buyer names the account that paid: the server asks the bank
// for that account's name, requires it to agree with the name typed here, and
// then matches the payment by that name, bank and amount. The name is typed,
// never auto-filled, so the bank's record has something to confirm.
import React, { useEffect, useState } from 'react';
import { View } from 'react-native';
import { Button, Input, PickerSheet, SelectRow, Sheet, Text } from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import { useTheme } from '@/theme';
import { getNigerianBanks, type NgBank } from '@/services/ngBanks';
import { declarePayer, type PayerDeclaration } from '@/services/itApi';

export interface PayerDeclarationSheetProps {
  visible: boolean;
  orderId: string;
  onClose: () => void;
  /** Called after a declaration is saved, with what became of it. */
  onDeclared: (declaration: PayerDeclaration) => void;
}

const OUTCOME_COPY: Record<PayerDeclaration['status'], string> = {
  matched: 'Payment found and confirmed.',
  waiting: "Saved. We'll confirm your order as soon as the payment arrives.",
  review: 'Saved. Our team will check the payment and confirm your order shortly.',
};

export function PayerDeclarationSheet({ visible, orderId, onClose, onDeclared }: PayerDeclarationSheetProps) {
  const { colors, spacing } = useTheme();
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [bankPickerOpen, setBankPickerOpen] = useState(false);
  const [bank, setBank] = useState<NgBank | null>(null);
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [done, setDone] = useState<PayerDeclaration | null>(null);

  useEffect(() => {
    if (!visible) return;
    let live = true;
    getNigerianBanks()
      .then((list) => live && setBanks(list))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [visible]);

  // Each opening starts clean: a previous attempt's errors are not this one's.
  useEffect(() => {
    if (visible) {
      setNameError(null);
      setFormError(null);
      setDone(null);
    }
  }, [visible]);

  const ready = !!bank && accountNumber.length === 10 && accountName.trim().length >= 3;

  const submit = async () => {
    if (!bank || !ready) return;
    setSubmitting(true);
    setNameError(null);
    setFormError(null);
    const r = await declarePayer(orderId, bank.code, accountNumber, accountName.trim());
    setSubmitting(false);
    if (r.success && r.data) {
      setDone(r.data);
      onDeclared(r.data);
      return;
    }
    if (r.code === 'payer_name_mismatch') {
      setNameError(r.message || "That name doesn't match the bank's record for this account.");
      return;
    }
    setFormError(r.message || 'Something went wrong. Try again.');
  };

  const bankItems: PickerItem[] = banks.map((b) => ({ id: b.code, label: b.name, icon: 'bank' as const }));

  return (
    <>
      <Sheet
        visible={visible && !bankPickerOpen}
        onClose={() => !submitting && onClose()}
        title="Paid from another account?"
        scrollable
        footer={
          done ? (
            <Button title="Done" onPress={onClose} />
          ) : (
            <Button title="Confirm account" loading={submitting} disabled={!ready || submitting} onPress={submit} />
          )
        }
      >
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, gap: spacing.md }}>
          {done ? (
            <Text variant="body" style={{ color: colors.textPrimary }}>
              {OUTCOME_COPY[done.status]}
            </Text>
          ) : (
            <>
              <Text variant="bodySm" style={{ color: colors.textMuted }}>
                If the transfer came from an account that isn&apos;t in your name, tell us which one. We check
                the name with the bank, then match the payment to your order.
              </Text>
              <SelectRow
                label="Bank that sent the money"
                placeholder={banks.length ? 'Select bank' : 'Loading banks…'}
                value={bank?.name}
                leftIcon="bank"
                onPress={() => setBankPickerOpen(true)}
              />
              <Input
                label="Account number"
                placeholder="10-digit account number"
                keyboardType="number-pad"
                maxLength={10}
                value={accountNumber}
                onChangeText={(v) => setAccountNumber(v.replace(/\D/g, '').slice(0, 10))}
              />
              <Input
                label="Account name"
                placeholder="Name on the account, as the bank shows it"
                autoCapitalize="words"
                value={accountName}
                onChangeText={(v) => {
                  setAccountName(v);
                  if (nameError) setNameError(null);
                }}
                error={nameError ?? undefined}
              />
              {formError ? (
                <Text variant="bodySm" style={{ color: colors.danger }}>
                  {formError}
                </Text>
              ) : null}
            </>
          )}
        </View>
      </Sheet>
      <PickerSheet
        visible={bankPickerOpen}
        onClose={() => setBankPickerOpen(false)}
        title="Select bank"
        items={bankItems}
        loading={banks.length === 0}
        selectedId={bank?.code}
        emptyText="No banks available right now."
        onSelect={(item) => {
          setBank(banks.find((b) => b.code === item.id) ?? null);
          setBankPickerOpen(false);
        }}
      />
    </>
  );
}
