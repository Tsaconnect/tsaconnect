// app/merchants/pay-settings.tsx
//
// Where a TSA Pay merchant is paid, read and changed by the merchant. Changing
// it is the one thing a stolen phone or login would do to redirect a merchant's
// money, so it asks for the account password (checked by the server, counted
// toward the login lockout) and then PIN or biometric (AuthorizationModal — the
// app's one unlock path). The server only accepts a bank account in the
// business's or the owner's verified name, and applies any change — bank or
// wallet — only once she enters the code it emails her. She is told of every
// change by email and push. Her payment links start here too (PayLinkEntry).
//
// She asks for a business rename the same guarded way (components/pay/
// BusinessName); it takes effect only once an admin approves it.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { getChainByChainId } from '@/constants/chains';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import AssetPicker from '@/components/assets/AssetPicker';
import {
  Button, Card, ErrorState, Input, SegmentedControl, SelectRow, Spinner, Text, useToast,
} from '@/components/ui';
import { PayLinkEntry } from '@/components/pay/PayLinkEntry';
import { BusinessNameCard, BusinessNameForm, businessNameChangeFrom } from '@/components/pay/BusinessName';
import { usePayAuthorization } from '@/hooks/usePayAuthorization';
import { loadOwnMerchant, type OwnMerchantLoad } from '@/hooks/useOwnPayMerchant';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import type { LiFiToken } from '@/services/lifi';
import { getNigerianBanks, type NgBank } from '@/services/ngBanks';
import {
  confirmOwnerAction, OWNER_CODES, updateBusinessName, updateSettlement,
  type OwnMerchant, type PendingConfirmation, type SettlementChange, type SettlementMode,
} from '@/services/payMerchantApi';
import { errorCode, errorMessage } from '@/utils/payCheckout';
import { describeSettlement, payoutBankOf, paysIntoPayoutAccount, settlementChangeFrom, settlesIn } from '@/utils/paySettlement';
import { getPayoutMethod, type SellerPayout } from '@/services/sellerPayoutApi';
import { SELLER_PAYOUT } from '@/constants/routes';

/** A guarded owner change this screen sends: where she is paid, or her business name. */
type OwnerChange =
  | { kind: 'settlement'; change: SettlementChange }
  | { kind: 'business_name'; name: string };

export default function PaySettings() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { address } = useSignableWallet();
  const [load, setLoad] = useState<OwnMerchantLoad>({ kind: 'loading' });
  const [mode, setMode] = useState<SettlementMode>('fiat');

  const [banks, setBanks] = useState<NgBank[]>([]);
  // The merchant's one payout account: a bank-mode TSA Pay account is paid into it, not into a second one.
  const [payoutAccount, setPayoutAccount] = useState<SellerPayout | null | undefined>(undefined);

  const [tokenPickerOpen, setTokenPickerOpen] = useState(false);
  const [token, setToken] = useState<(LiFiToken & { chainId: number }) | undefined>();
  const [wallet, setWallet] = useState('');

  // Renaming the business, instead of changing where she is paid.
  const [renaming, setRenaming] = useState(false);
  const [newName, setNewName] = useState('');
  const [password, setPassword] = useState('');
  const [formError, setFormError] = useState<string | undefined>();
  const [saving, setSaving] = useState(false);
  // A change waiting on the code emailed to her.
  const [pending, setPending] = useState<PendingConfirmation | null>(null);
  const [code, setCode] = useState('');
  // A double biometric success must not send the change twice.
  const submitting = useRef(false);
  // The wallet field is filled once — never again after she clears it.
  const walletFilled = useRef(false);

  const refresh = useCallback(async () => {
    const next = await loadOwnMerchant();
    setLoad(next);
    if (next.kind !== 'ready') return;
    const { merchant } = next;
    if (merchant.settlementMode) setMode(merchant.settlementMode);
    if (!walletFilled.current && merchant.walletAddress) {
      walletFilled.current = true;
      setWallet(merchant.walletAddress);
    }
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);
  useEffect(() => { getNigerianBanks().then(setBanks).catch(() => {}); }, []);
  useEffect(() => { getPayoutMethod().then(setPayoutAccount).catch(() => setPayoutAccount(null)); }, []);
  // With no crypto destination yet, her own wallet on this device is the natural
  // place to be paid; she can type another.
  useEffect(() => {
    if (address && !walletFilled.current && load.kind === 'ready') {
      walletFilled.current = true;
      setWallet(address);
    }
  }, [address, load.kind]);

  const bankName = useCallback((code: string) => banks.find((b) => b.code === code)?.name, [banks]);

  /**
   * The merchant as the server now has it. `confirmed`: an emailed code filed
   * the change. A rename is only ever filed that way — and even then the name
   * in use stays until an admin approves it — so a rename answered without a
   * code (her current name, withdrawing a request) had nothing to confirm and
   * says nothing: the card shows where it stands.
   */
  const applied = (merchant: OwnMerchant, kind: string | undefined, confirmed: boolean) => {
    setLoad({ kind: 'ready', merchant });
    setPassword('');
    setPending(null);
    setCode('');
    if (kind !== 'business_name') { toast.success('Payment settings saved'); return; }
    setRenaming(false);
    setNewName('');
    if (confirmed) toast.success('New business name sent for approval');
  };

  const save = async (owner: OwnerChange) => {
    if (submitting.current || load.kind !== 'ready') return;
    submitting.current = true;
    setSaving(true);
    setFormError(undefined);
    try {
      const { version } = load.merchant;
      const answer = owner.kind === 'business_name'
        ? await updateBusinessName(owner.name, password, version)
        : await updateSettlement(owner.change, password, version);
      setPassword('');
      if (answer.confirmation) setPending(answer.confirmation);
      else if (answer.merchant) applied(answer.merchant, owner.kind, false);
    } catch (e) {
      const why = errorCode(e);
      // The code says the name was refused; its message is only the reason.
      setFormError(why === OWNER_CODES.BUSINESS_NAME_INVALID
        ? `That name can’t be used: ${errorMessage(e, 'it breaks the business name rules')}`
        : errorMessage(e, owner.kind === 'business_name' ? 'Could not request the new business name.' : 'Could not save your payment settings.'));
      if (why === OWNER_CODES.STALE) void refresh();
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
      const answer = await confirmOwnerAction(pending.id, code);
      if (answer.merchant) applied(answer.merchant, pending.kind, true);
    } catch (e) {
      const why = errorCode(e);
      setFormError(errorMessage(e, 'Could not confirm the change.'));
      // Expired, used up, or overtaken by another edit: this code is done.
      if (why === OWNER_CODES.NO_PENDING || why === OWNER_CODES.STALE) { setPending(null); setCode(''); }
      if (why === OWNER_CODES.STALE) void refresh();
    } finally {
      submitting.current = false;
      setSaving(false);
    }
  };

  const { request, authorization } = usePayAuthorization<OwnerChange>({
    title: (owner) => (owner?.kind === 'business_name'
      ? 'Authorize changing your business name'
      : 'Authorize changing where you are paid'),
    onAuthorized: (owner) => { void save(owner); },
  });

  const onSave = () => {
    const payoutBank = payoutBankOf(payoutAccount);
    if (mode === 'fiat' && !payoutBank) { setFormError('Set your naira bank payout account first.'); return; }
    const out = settlementChangeFrom(mode === 'fiat'
      ? { mode, bankCode: payoutBank!.institutionCode, accountNumber: payoutBank!.accountNumber }
      : { mode, token, walletAddress: wallet });
    if ('missing' in out) { setFormError(out.missing); return; }
    if (!password) { setFormError('Enter your account password to confirm.'); return; }
    setFormError(undefined);
    request({ kind: 'settlement', change: out.change });
  };

  const onRename = () => {
    if (load.kind !== 'ready') return;
    const out = businessNameChangeFrom(newName, load.merchant);
    if ('missing' in out) { setFormError(out.missing); return; }
    if (!password) { setFormError('Enter your account password to confirm.'); return; }
    setFormError(undefined);
    request({ kind: 'business_name', name: out.name });
  };

  const stopRenaming = () => { setRenaming(false); setNewName(''); setPassword(''); setFormError(undefined); };

  if (load.kind === 'loading') {
    return <SafeAreaView style={{ flex: 1, justifyContent: 'center' }} edges={['bottom']}><Spinner /></SafeAreaView>;
  }
  if (load.kind === 'error') {
    return <ErrorState title="Could not load your payment settings" subtitle={load.message} onRetry={refresh} />;
  }
  if (load.kind === 'none') {
    return <ErrorState title="No TSA Pay account" subtitle="Apply to accept payments with TSA Pay first." />;
  }
  const { merchant } = load;
  const suspended = merchant.status === 'suspended';
  // A rename can be asked for only while the feature is on (the server says so).
  const canRename = !suspended && merchant.businessNameChangeEnabled === true;
  const codeFor = pending?.kind === 'business_name'
    ? 'send your new business name for approval'
    : `confirm the new ${mode === 'fiat' ? 'bank account' : 'wallet'}`;

  const payoutBank = payoutBankOf(payoutAccount);
  const alreadyPaidThere = !!payoutBank && paysIntoPayoutAccount(merchant, payoutBank);
  // A bank-mode change has nothing to send without a payout account, or when TSA Pay already uses it.
  const hideSave = mode === 'fiat' && (payoutAccount === undefined || !payoutBank || alreadyPaidThere);

  const passwordField = (
    <Input
      label="Account password"
      value={password}
      onChangeText={setPassword}
      secureTextEntry
      autoCapitalize="none"
      placeholder="To confirm it's you"
    />
  );
  const formErrorLine = formError ? <Text variant="bodySm" style={{ color: colors.danger }}>{formError}</Text> : null;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }} keyboardShouldPersistTaps="handled">
        <Card style={{ gap: spacing.xs }}>
          <Text variant="caption" color="textMuted">{merchant.businessName} is paid to</Text>
          <Text variant="bodyMed" weight="700">{describeSettlement(merchant, bankName)}</Text>
        </Card>

        {pending || (renaming && canRename) ? null : (
          <BusinessNameCard
            merchant={merchant}
            canRename={canRename}
            onRename={() => { setRenaming(true); setPassword(''); setFormError(undefined); }}
          />
        )}

        {pending ? null : (
          <Card style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700">Get paid with a link</Text>
            <Text variant="bodySm" color="textSecondary">
              Send a customer a link or QR code for an amount — no website needed.
            </Text>
            <PayLinkEntry canRequest={!suspended} />
          </Card>
        )}

        {pending ? (
          <Card style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700">Check your email</Text>
            <Text variant="bodySm">
              {`We sent a 6-digit code to ${pending.sentTo}. Enter it to ${codeFor} — nothing changes until you do.`}
            </Text>
            <Input
              label="Code"
              value={code}
              onChangeText={(v) => setCode(v.replace(/\D/g, ''))}
              keyboardType="number-pad"
              maxLength={6}
              placeholder="123456"
            />
            {formErrorLine}
            <Button title="Confirm" onPress={confirm} loading={saving} disabled={saving} fullWidth />
            <Button title="Cancel" variant="ghost" onPress={() => { setPending(null); setCode(''); setFormError(undefined); }} fullWidth />
          </Card>
        ) : suspended ? (
          <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
            <Text variant="bodySm">Your merchant account is suspended. Contact support to change where you are paid.</Text>
          </Card>
        ) : renaming && canRename ? (
          <BusinessNameForm
            merchant={merchant}
            name={newName}
            onChangeName={setNewName}
            passwordField={passwordField}
            errorLine={formErrorLine}
            saving={saving}
            onSave={onRename}
            onCancel={stopRenaming}
          />
        ) : (
          <>
            <Text variant="bodyMed" weight="700">Change where you are paid</Text>
            <SegmentedControl<SettlementMode>
              segments={[{ key: 'fiat', label: 'Bank account' }, { key: 'crypto', label: 'Crypto wallet' }]}
              value={mode}
              onChange={(m) => { setMode(m); setFormError(undefined); }}
            />

            {mode === 'fiat' ? (
              <View style={{ gap: spacing.sm }}>
                {payoutAccount === undefined ? (
                  <Text variant="caption" color="textMuted">Loading your payout account…</Text>
                ) : payoutBank ? (
                  <>
                    <Card style={{ gap: spacing.xs }}>
                      <Text variant="caption" color="textMuted">Your payout account</Text>
                      <Text variant="bodyMed" weight="700">{payoutBank.institutionName} · {payoutBank.accountNumber}</Text>
                      <Text variant="bodySm" color="textSecondary">{payoutBank.accountName}</Text>
                    </Card>
                    <Text variant="caption" color="textMuted">
                      {alreadyPaidThere
                        ? 'TSA Pay pays into this account. Change it in Payout settings and TSA Pay follows.'
                        : 'TSA Pay is paid into your payout account, so you only set it once. We email you a code to confirm the change.'}
                    </Text>
                  </>
                ) : (
                  <>
                    <Text variant="bodySm">Set a naira bank payout account first. TSA Pay is paid into it.</Text>
                    <Button title="Set payout account" variant="secondary" onPress={() => router.push(SELLER_PAYOUT as never)} fullWidth />
                  </>
                )}
              </View>
            ) : (
              <View style={{ gap: spacing.sm }}>
                <SelectRow
                  label="Paid in"
                  placeholder="Choose a token"
                  value={token ? `${token.symbol} on ${getChainByChainId(token.chainId)?.name ?? `chain ${token.chainId}`}` : undefined}
                  leftIcon="wallet"
                  onPress={() => setTokenPickerOpen(true)}
                />
                <Input
                  label="Wallet address"
                  value={wallet}
                  onChangeText={setWallet}
                  placeholder="0x…"
                  autoCapitalize="none"
                  autoCorrect={false}
                />
                <Text variant="caption" color="textMuted">
                  Payments arrive in this wallet on that network. Only use a wallet you control.
                </Text>
              </View>
            )}

            {hideSave ? null : (
              <>
                {passwordField}
                {formErrorLine}
                <Button
                  title={mode === 'fiat' ? 'Use my payout account' : 'Save'}
                  onPress={onSave}
                  loading={saving}
                  disabled={saving}
                  fullWidth
                />
              </>
            )}
          </>
        )}
      </ScrollView>

      <AssetPicker
        visible={tokenPickerOpen}
        side="to"
        title="Choose the token you are paid in"
        allow={settlesIn}
        onSelect={(t, chainId) => { setToken({ ...t, chainId }); setTokenPickerOpen(false); }}
        onClose={() => setTokenPickerOpen(false)}
      />
      <AuthorizationModal {...authorization} />
    </SafeAreaView>
  );
}
