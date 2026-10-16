// components/it/PayerDeclarationSheet.tsx
//
// "Paid from another account?" on a naira buy. The automatic match pairs a
// payment with an order by amount and the buyer's own verified name; money from
// a spouse's, friend's or business account carries someone else's name and
// waits. Here the buyer names the account that paid — bank and number only. The
// name is the bank's own (looked up as they type, shown read-only), and the
// server matches the payment by that name, bank and amount.
import React, { useEffect, useState } from 'react';
import { View } from 'react-native';
import { Button, Input, PickerSheet, SelectRow, Sheet, Spinner, Text } from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import { useTheme } from '@/theme';
import { getNigerianBanks, resolveAccountName, type NgBank } from '@/services/ngBanks';
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
  // The bank's name for bank + number: null until looked up.
  const [accountName, setAccountName] = useState<string | null>(null);
  const [lookingUp, setLookingUp] = useState(false);
  const [lookupError, setLookupError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
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
      setFormError(null);
      setDone(null);
    }
  }, [visible]);

  // Name enquiry once the bank and all ten digits are in, debounced; a later
  // edit drops the earlier answer so a stale name is never shown.
  useEffect(() => {
    setAccountName(null);
    setLookupError(null);
    if (!bank || accountNumber.length !== 10) return;
    let live = true;
    setLookingUp(true);
    const timer = setTimeout(() => {
      resolveAccountName(bank.code, accountNumber)
        .then((r) => {
          if (!live) return;
          if (r.ok) setAccountName(r.accountName);
          else
            setLookupError(
              r.reason === 'not_found'
                ? "The bank couldn't find this account. Check the number and the bank."
                : "We couldn't reach the bank to check this account. Try again in a moment."
            );
        })
        .finally(() => live && setLookingUp(false));
    }, 400);
    return () => {
      live = false;
      clearTimeout(timer);
      setLookingUp(false);
    };
  }, [bank, accountNumber]);

  const ready = !!bank && accountNumber.length === 10 && !!accountName && !lookingUp;

  const submit = async () => {
    if (!bank || !ready) return;
    setSubmitting(true);
    setFormError(null);
    const r = await declarePayer(orderId, bank.code, accountNumber);
    setSubmitting(false);
    if (r.success && r.data) {
      setDone(r.data);
      onDeclared(r.data);
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
                If the transfer came from an account that isn&apos;t in your name, tell us which one. We get
                the account name from the bank and match the payment to your order.
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
                placeholder={bank && accountNumber.length === 10 ? 'Checking with the bank…' : 'Filled in by the bank'}
                value={accountName ?? ''}
                editable={false}
                error={lookupError ?? undefined}
                helper={accountName ? 'Verified with the bank' : undefined}
                rightAdornment={lookingUp ? <Spinner size="small" /> : undefined}
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
