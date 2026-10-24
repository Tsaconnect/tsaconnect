// components/pay/BusinessName.tsx
//
// A merchant's business name: the name in use, a rename waiting for approval,
// and the form that asks for one. The name is the merchant's, so an approved
// rename reaches TSA Pay, P2P and the store together. A rename takes
// effect only once an admin approves it — until then customers and payouts
// keep the current name. The screen owns the guarded send (password, PIN/
// biometric, the emailed code), shared with the payout change.
import React from 'react';
import { useTheme } from '@/theme';
import { Button, Card, Input, Text } from '@/components/ui';
import type { OwnMerchant } from '@/services/payMerchantApi';

/** A business name as the server stores it: surrounding space dropped, inner runs of space made one. */
export const normalizeBusinessName = (name: string): string => name.split(/\s+/).filter(Boolean).join(' ');

/** The server's bounds on a business name, in characters (code points — what it counts, not UTF-16 units). */
export const BUSINESS_NAME_MIN = 2;
export const BUSINESS_NAME_MAX = 100;

/**
 * The rename to send, or what is wrong. Her current name is allowed only while
 * a rename waits: asking for it back withdraws that request. The rest of the
 * rule (hidden characters, reserved names) is the server's, answered as
 * business_name_invalid.
 */
export function businessNameChangeFrom(
  input: string, m: Pick<OwnMerchant, 'businessName' | 'pendingBusinessName'>,
): { name: string } | { missing: string } {
  const name = normalizeBusinessName(input);
  if (!name) return { missing: 'Enter the new business name.' };
  const length = [...name].length;
  if (length < BUSINESS_NAME_MIN || length > BUSINESS_NAME_MAX) {
    return { missing: `Enter a name of ${BUSINESS_NAME_MIN} to ${BUSINESS_NAME_MAX} characters.` };
  }
  if (name === m.pendingBusinessName) return { missing: 'That name is already waiting for approval.' };
  if (name === normalizeBusinessName(m.businessName) && !m.pendingBusinessName) return { missing: 'That is already your business name.' };
  return { name };
}

export const pendingBusinessNameNote = (m: Pick<OwnMerchant, 'businessName' | 'pendingBusinessName'>): string | null =>
  m.pendingBusinessName
    ? `Your new name '${m.pendingBusinessName}' is waiting for approval. Customers and payouts keep '${m.businessName}' until then.`
    : null;

/** The name in use, a rename waiting, and the way to ask for one. */
export function BusinessNameCard({ merchant, canRename, onRename }: {
  merchant: OwnMerchant;
  canRename: boolean;
  onRename: () => void;
}) {
  const { spacing } = useTheme();
  const note = pendingBusinessNameNote(merchant);
  return (
    <Card style={{ gap: spacing.xs }}>
      <Text variant="caption" color="textMuted">Business name</Text>
      <Text variant="bodyMed" weight="700">{merchant.businessName}</Text>
      {note ? <Text variant="caption" color="textSecondary">{note}</Text> : null}
      {canRename ? (
        <Button title="Change business name" variant="ghost" size="sm" fullWidth={false} onPress={onRename} />
      ) : null}
    </Card>
  );
}

/** Asking for a rename: the new name, then the screen's shared password field, error and actions. */
export function BusinessNameForm({
  merchant, name, onChangeName, passwordField, errorLine, saving, onSave, onCancel,
}: {
  merchant: OwnMerchant;
  name: string;
  onChangeName: (v: string) => void;
  passwordField: React.ReactNode;
  errorLine: React.ReactNode;
  saving: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const { spacing } = useTheme();
  return (
    <Card style={{ gap: spacing.sm }}>
      <Text variant="bodyMed" weight="700">Change your business name</Text>
      <Input
        label="New business name"
        value={name}
        onChangeText={onChangeName}
        placeholder={merchant.businessName}
        autoCapitalize="words"
      />
      <Text variant="caption" color="textMuted">
        {`Your new name takes effect once our team approves it. Until then customers and payouts keep '${merchant.businessName}'.`
          + ' It renames your whole merchant account — TSA Pay, P2P and your store — and we email you a code to confirm the request.'}
      </Text>
      {passwordField}
      {errorLine}
      <Button title="Request new name" onPress={onSave} loading={saving} disabled={saving} fullWidth />
      <Button title="Cancel" variant="ghost" onPress={onCancel} fullWidth />
    </Card>
  );
}
