// app/merchants/payout-method.tsx
//
// Where a seller is paid when a buyer confirms an order: a bank account in
// naira, or their wallet. The account must be in their own name (or their
// verified business's); the change is guarded by the account password, the
// device PIN/biometric and a code emailed to them.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { Button, Card, ErrorState, Input, PickerSheet, SelectRow, Spinner, Text, useToast } from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import { usePayAuthorization } from '@/hooks/usePayAuthorization';
import { getNigerianBanks, resolveAccountName, type NgBank } from '@/services/ngBanks';
import {
  clearPayoutMethod, confirmPayoutMethod, getPayoutMethod, requestPayoutMethod, PAYOUT_CODES,
  type PayoutConfirmation, type SellerPayout,
} from '@/services/sellerPayoutApi';
import { errorCode, errorMessage } from '@/utils/payCheckout';

const masked = (n?: string) => (n && n.length > 4 ? `••••${n.slice(-4)}` : n ?? '');

export default function PayoutMethod() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [current, setCurrent] = useState<SellerPayout | null>(null);
  const [loadError, setLoadError] = useState(false);

  const [banks, setBanks] = useState<NgBank[]>([]);
  const [bankPickerOpen, setBankPickerOpen] = useState(false);
  const [bank, setBank] = useState<NgBank | null>(null);
  const [accountNumber, setAccountNumber] = useState('');
  const [resolvedName, setResolvedName] = useState<string | null>(null);
  const [resolving, setResolving] = useState(false);
  const [password, setPassword] = useState('');
  const [formError, setFormError] = useState<string | undefined>();
  const [saving, setSaving] = useState(false);
  const [pending, setPending] = useState<PayoutConfirmation | null>(null);
  const [code, setCode] = useState('');
  // A double biometric success must not send the change twice.
  const submitting = useRef(false);

  const refresh = useCallback(async () => {
    try {
      setCurrent(await getPayoutMethod());
      setLoadError(false);
    } catch {
      setLoadError(true);
    }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  useEffect(() => { getNigerianBanks().then(setBanks).catch(() => {}); }, []);

  // The bank's own name for the account, shown before it is saved: it is what
  // the server holds against the seller's verified name.
  useEffect(() => {
    setResolvedName(null);
    if (!bank || accountNumber.length !== 10) { setResolving(false); return; }
    let cancelled = false;
    setResolving(true);
    const t = setTimeout(async () => {
      const res = await resolveAccountName(bank.code, accountNumber);
      if (cancelled) return;
      setResolving(false);
      setResolvedName(res.ok ? res.accountName : null);
    }, 400);
    return () => { cancelled = true; clearTimeout(t); };
  }, [bank, accountNumber]);

  const bankItems: PickerItem[] = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name, icon: 'bank' as const })),
    [banks],
  );

  const send = async () => {
    if (submitting.current || !bank) return;
    submitting.current = true;
    setSaving(true);
    setFormError(undefined);
    try {
      setPending(await requestPayoutMethod(bank.code, accountNumber, password));
      setPassword('');
    } catch (e) {
      const why = errorCode(e);
      setFormError(why === PAYOUT_CODES.NAME_MISMATCH
        ? 'The account name must match your verified name or your verified business name.'
        : errorMessage(e, 'Could not save your payout account.'));
    } finally {
      submitting.current = false;
      setSaving(false);
    }
  };

  const confirm = async () => {
    if (submitting.current || !pending) return;
    if (!/^\d{6}$/.test(code.trim())) { setFormError('Enter the 6-digit code from your email.'); return; }
    submitting.current = true;
    setSaving(true);
    setFormError(undefined);
    try {
      setCurrent(await confirmPayoutMethod(pending.id, code));
      setPending(null);
      setCode('');
      setAccountNumber('');
      setBank(null);
      toast.success('Payout account saved');
    } catch (e) {
      setFormError(errorMessage(e, 'Could not confirm the change.'));
      const why = errorCode(e);
      if (why === PAYOUT_CODES.NO_PENDING) { setPending(null); setCode(''); }
    } finally {
      submitting.current = false;
      setSaving(false);
    }
  };

  const useWallet = async () => {
    setSaving(true);
    try {
      setCurrent(await clearPayoutMethod());
      setPending(null);
      toast.success('You will be paid to your wallet');
    } catch (e) {
      setFormError(errorMessage(e, 'Could not change your payout account.'));
    } finally {
      setSaving(false);
    }
  };

  const { request, authorization } = usePayAuthorization<true>({
    title: () => 'Authorize changing where you are paid',
    onAuthorized: () => { void send(); },
  });

  const onSave = () => {
    if (!bank || accountNumber.length !== 10) { setFormError('Choose your bank and enter the 10-digit account number.'); return; }
    if (!password) { setFormError('Enter your account password to confirm.'); return; }
    setFormError(undefined);
    request(true);
  };

  if (loadError) return <ErrorState title="Could not load your payout account." onRetry={refresh} />;
  if (!current) return <Spinner />;

  const onBank = current.kind === 'bank';
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }} keyboardShouldPersistTaps="handled">
        <Card style={{ gap: spacing.xs }}>
          <Text variant="caption" color="textMuted">Your sales are paid to</Text>
          <Text variant="bodyMed" weight="700">
            {onBank ? `${current.institutionName} ${masked(current.accountNumber)} · ${current.accountName}` : 'Your wallet, in USDC or USDT'}
          </Text>
          {onBank ? (
            <Text variant="caption" color="textMuted">
              Orders paid in USDC or USDT are sent to this account in naira when the buyer confirms. Buyers see a small payout fee.
            </Text>
          ) : null}
        </Card>

        {pending ? (
          <Card style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700">Check your email</Text>
            <Text variant="bodySm">{`We sent a 6-digit code to ${pending.sentTo}. Enter it to save this account — nothing changes until you do.`}</Text>
            <Input label="Code" value={code} onChangeText={(v) => setCode(v.replace(/\D/g, ''))} keyboardType="number-pad" maxLength={6} placeholder="123456" />
            {formError ? <Text variant="bodySm" style={{ color: colors.danger }}>{formError}</Text> : null}
            <Button title="Confirm" onPress={confirm} loading={saving} disabled={saving} fullWidth />
            <Button title="Cancel" variant="ghost" onPress={() => { setPending(null); setCode(''); setFormError(undefined); }} fullWidth />
          </Card>
        ) : (
          <Card style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700">{onBank ? 'Change your payout account' : 'Get paid in naira'}</Text>
            <SelectRow label="Bank" placeholder="Choose your bank" value={bank?.name} leftIcon="bank" onPress={() => setBankPickerOpen(true)} />
            <Input
              label="Account number"
              value={accountNumber}
              onChangeText={(v) => setAccountNumber(v.replace(/\D/g, ''))}
              placeholder="10-digit account number"
              keyboardType="number-pad"
              maxLength={10}
            />
            {resolving ? <Text variant="caption" color="textMuted">Checking the account…</Text> : null}
            {resolvedName ? <Text variant="caption" weight="600">Account name: {resolvedName}</Text> : null}
            <Text variant="caption" color="textMuted">
              The account must be in your own name or your verified business's. We email you a code to confirm.
            </Text>
            <Input label="Password" value={password} onChangeText={setPassword} secureTextEntry autoCapitalize="none" placeholder="To confirm it's you" />
            {formError ? <Text variant="bodySm" style={{ color: colors.danger }}>{formError}</Text> : null}
            <Button title="Continue" onPress={onSave} loading={saving} disabled={saving} fullWidth />
            {onBank ? <Button title="Pay me to my wallet instead" variant="ghost" onPress={useWallet} disabled={saving} fullWidth /> : null}
          </Card>
        )}
      </ScrollView>

      <PickerSheet
        visible={bankPickerOpen}
        onClose={() => setBankPickerOpen(false)}
        title="Choose your bank"
        items={bankItems}
        loading={banks.length === 0}
        selectedId={bank?.code}
        emptyText="No banks found."
        onSelect={(item) => {
          const b = banks.find((x) => x.code === item.id);
          if (!b) return;
          setBank(b);
          setBankPickerOpen(false);
          setAccountNumber('');
        }}
      />
      <AuthorizationModal {...authorization} />
    </SafeAreaView>
  );
}
