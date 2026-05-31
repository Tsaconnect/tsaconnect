// app/p2p/payment-methods/new.tsx
//
// Form to add a payment method, driven by a selected provider + currency.
// Restyled onto the TSA Signature design system (@/theme + @/components/ui);
// the per-provider dynamic field logic, validation, name-enquiry, and submit
// flow are unchanged — this is a visual/UX restyle only.
//
// Two paths:
//   • NG bank (provider.type === 'bank' AND currency === 'NGN'): keeps the
//     rich Nigerian-bank dropdown + NUBAN name enquiry (Nomba) auto-fill.
//   • Generic (everything else): a single identifier field (labelled by the
//     provider, e.g. "Email" / "Tag/phone") + a manual account-name field.
//
// The method currency is a LOCAL form field (seeded from the P2P market
// currency); traders can add methods in other currencies so they can post
// ads in those currencies. Server persists `verified: false` until a later
// onboarding flow verifies it.
import { router } from 'expo-router';
import React, { useEffect, useMemo, useState } from 'react';
import { Alert, KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { paymentMethodsApi, PaymentProvider } from '../../../services/p2p';
import {
  getNigerianBanks,
  NgBank,
  resolveAccountName,
} from '../../../services/ngBanks';
import { useP2pCurrency } from '../../../hooks/useP2pCurrency';
import CurrencySelector from '../../../components/currency/CurrencySelector';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  BottomActionBar,
  Card,
  Icon,
  Input,
  PickerSheet,
  PressableScale,
  SelectRow,
  Spinner,
  Text,
  Toggle,
} from '@/components/ui';
import type { IconName } from '@/components/ui';

// Map a provider type to a semantic registry icon for the picker rows.
function providerIcon(type: PaymentProvider['type']): IconName {
  if (type === 'bank') return 'bank';
  if (type === 'mobile_money') return 'mobile';
  return 'wallet';
}

function providerTypeLabel(type: PaymentProvider['type']): string {
  if (type === 'bank') return 'Bank';
  if (type === 'mobile_money') return 'Mobile money';
  return 'Wallet';
}

const NewPaymentMethodScreen: React.FC = () => {
  const { colors, spacing } = useTheme();

  // Currency for THIS method is a local form field. Seed it from the P2P
  // market currency (do NOT mutate the hook's setCurrency here). The first
  // two letters of an ISO-4217 currency code are almost always the ISO-3166
  // country code — good enough; the backend just needs a non-empty country.
  const { currency: marketCurrency } = useP2pCurrency();
  const [pickedCurrency, setPickedCurrency] = useState<string | null>(null);
  const currencyCode = (pickedCurrency ?? marketCurrency.code).toUpperCase();
  const country = currencyCode.slice(0, 2);

  // Provider catalog + the selected provider drives which inputs we show.
  const [providers, setProviders] = useState<PaymentProvider[]>([]);
  const [providersLoading, setProvidersLoading] = useState(true);
  const [provider, setProvider] = useState<PaymentProvider | null>(null);
  const [showProviderPicker, setShowProviderPicker] = useState(false);

  const [bank, setBank] = useState<NgBank | null>(null);
  const [showBankPicker, setShowBankPicker] = useState(false);
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [banksLoading, setBanksLoading] = useState(true);
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');
  const [isDefault, setIsDefault] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  // The rich NG-bank experience (bank directory + NUBAN name enquiry) only
  // applies to bank providers settling in NGN. Everything else uses a simple
  // manual-entry path.
  const isNgBankPath = provider?.type === 'bank' && currencyCode === 'NGN';

  // Name-enquiry state. `resolved` flags an auto-fill from Nomba so we can
  // lock the input + show a check; `resolveError` lets the user know to
  // either fix the digits or fall back to manual entry on outage.
  const [resolving, setResolving] = useState(false);
  const [resolved, setResolved] = useState(false);
  const [resolveError, setResolveError] = useState<
    null | 'not_found' | 'unavailable'
  >(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const res = await paymentMethodsApi.providers();
      if (cancelled) return;
      if (res.success && res.data) {
        setProviders(res.data);
      } else {
        console.warn('[new-payment-method] provider catalog load failed', res.message);
      }
      setProvidersLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const list = await getNigerianBanks();
      if (!cancelled) {
        setBanks(list);
        setBanksLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Debounced account-name resolution. Fires only on the NG-bank path when
  // the user has both a bank selected and a 10-digit account number. Re-firing
  // on any dependency change cancels the in-flight call via `cancelled`.
  useEffect(() => {
    if (!isNgBankPath || !bank || accountNumber.length !== 10) {
      setResolving(false);
      setResolved(false);
      setResolveError(null);
      return;
    }
    let cancelled = false;
    setResolving(true);
    setResolved(false);
    setResolveError(null);
    const timer = setTimeout(async () => {
      const res = await resolveAccountName(bank.code, accountNumber);
      if (cancelled) return;
      setResolving(false);
      if (res.ok) {
        setAccountName(res.accountName);
        setResolved(true);
      } else {
        setAccountName('');
        setResolveError(res.reason);
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [isNgBankPath, bank, accountNumber]);

  const valid =
    !!provider &&
    accountNumber.trim().length > 0 &&
    accountName.trim().length >= 2 &&
    // NG-bank path keeps the 10-digit NUBAN expectation + a selected bank;
    // the generic path just needs a non-empty identifier.
    (!isNgBankPath || (!!bank && accountNumber.trim().length >= 10));

  const handleSubmit = async () => {
    if (!valid || submitting || !provider) return;
    setSubmitting(true);
    const bankName = isNgBankPath && bank ? bank.name : provider.label;
    const bankCode = isNgBankPath && bank ? bank.code : undefined;
    const res = await paymentMethodsApi.create({
      provider: provider.slug,
      country,
      currency: currencyCode,
      bankName,
      bankCode,
      accountNumber: accountNumber.trim(),
      accountName: accountName.trim(),
      isDefault,
    });
    setSubmitting(false);
    if (res.success) {
      router.back();
    } else {
      Alert.alert('Could not save', res.message || 'Server rejected the request');
    }
  };

  // ─── Picker item adapters ─────────────────────────────────────────────────
  // PickerSheet works on { id, label, ... } items; map the provider/bank
  // catalogs to that shape and resolve the selection back by id on choose.
  const providerItems = useMemo(
    () =>
      providers.map((p) => ({
        id: p.slug,
        label: p.label,
        sublabel: providerTypeLabel(p.type),
        icon: providerIcon(p.type),
      })),
    [providers],
  );
  const bankItems = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name, icon: 'bank' as IconName })),
    [banks],
  );

  // Identifier label for the generic path (e.g. "Email" / "Tag/phone").
  const identifierLabel = provider?.identifierLabel || 'Identifier';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="title1" weight="700">
            Add payment method
          </Text>
          <Text
            variant="bodySm"
            color="textSecondary"
            style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}
          >
            Buyers see this account when they open a trade. Double-check the details before saving.
          </Text>

          <View style={{ gap: spacing.lg }}>
            <View>
              <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
                Currency
              </Text>
              <CurrencySelector
                variant="card"
                selectedCode={currencyCode}
                onSelectCode={(code) => {
                  setPickedCurrency(code);
                  // Switching currency can change which path applies; clear the
                  // entered identifier/name and NG-bank-specific state so we don't
                  // carry stale digits across into the generic field (or vice versa).
                  setBank(null);
                  setAccountNumber('');
                  setAccountName('');
                  setResolved(false);
                  setResolveError(null);
                }}
              />
            </View>

            <SelectRow
              label="Provider"
              placeholder="Choose a provider"
              value={provider?.label}
              leftIcon={provider ? providerIcon(provider.type) : 'card'}
              onPress={() => setShowProviderPicker(true)}
            />

            {!provider ? (
              <AccentCard padding="md">
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Icon name="info" size={18} color={colors.accent} />
                  <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                    Choose a provider to continue.
                  </Text>
                </View>
              </AccentCard>
            ) : isNgBankPath ? (
              <>
                <SelectRow
                  label="Bank"
                  placeholder="Choose a bank"
                  value={bank?.name}
                  leftIcon="bank"
                  onPress={() => setShowBankPicker(true)}
                />

                <Input
                  label="Account number"
                  keyboardType="number-pad"
                  maxLength={20}
                  placeholder="0123456789"
                  value={accountNumber}
                  onChangeText={(v) => setAccountNumber(v.replace(/\D/g, ''))}
                />

                <Input
                  label="Account name"
                  autoCapitalize="words"
                  placeholder="Auto-fills when you enter the account number"
                  value={accountName}
                  onChangeText={(v) => {
                    setAccountName(v);
                    if (resolved) setResolved(false);
                  }}
                  editable={!resolving && !resolved}
                  error={
                    resolveError === 'not_found'
                      ? "We couldn’t find this account at the selected bank. Check the digits or bank."
                      : undefined
                  }
                  helper={
                    resolveError === 'unavailable'
                      ? 'Account verification is offline. You can type the name manually.'
                      : undefined
                  }
                  rightAdornment={
                    resolving ? (
                      <Spinner size="small" />
                    ) : resolved ? (
                      // Tap the check to clear the auto-filled name and edit it
                      // manually (the field is locked while `resolved`).
                      <PressableScale
                        onPress={() => {
                          setResolved(false);
                          setAccountName('');
                        }}
                        hitSlop={10}
                        accessibilityLabel="Clear and edit name"
                      >
                        <Icon name="checkCircle" size={20} color={colors.success} />
                      </PressableScale>
                    ) : undefined
                  }
                />
              </>
            ) : (
              <>
                <Input
                  label={identifierLabel}
                  autoCapitalize="none"
                  autoCorrect={false}
                  placeholder={`Your ${identifierLabel.toLowerCase()}`}
                  value={accountNumber}
                  onChangeText={setAccountNumber}
                />

                <Input
                  label="Account name"
                  autoCapitalize="words"
                  placeholder="As registered with the provider"
                  value={accountName}
                  onChangeText={setAccountName}
                />
              </>
            )}

            <Card padding="md">
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="600">
                    Set as default
                  </Text>
                  <Text variant="caption" color="textSecondary" style={{ marginTop: 2 }}>
                    Pre-selected when you open a new trade.
                  </Text>
                </View>
                <Toggle
                  value={isDefault}
                  onValueChange={setIsDefault}
                  accessibilityLabel="Set as default payment method"
                />
              </View>
            </Card>
          </View>
        </ScrollView>

        <BottomActionBar>
          <Button
            title={submitting ? 'Saving…' : 'Save payment method'}
            onPress={handleSubmit}
            loading={submitting}
            disabled={!valid || submitting}
          />
        </BottomActionBar>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={showProviderPicker}
        onClose={() => setShowProviderPicker(false)}
        title="Choose a provider"
        items={providerItems}
        loading={providersLoading && providers.length === 0}
        selectedId={provider?.slug}
        emptyText="No providers available right now."
        onSelect={(item) => {
          const p = providers.find((x) => x.slug === item.id);
          if (!p) return;
          setProvider(p);
          setShowProviderPicker(false);
          // Reset path-specific fields when the provider changes so we don't
          // carry a bank/identifier across an incompatible switch.
          setBank(null);
          setAccountNumber('');
          setAccountName('');
          setResolved(false);
          setResolveError(null);
        }}
      />

      <PickerSheet
        visible={showBankPicker}
        onClose={() => setShowBankPicker(false)}
        title="Choose a bank"
        items={bankItems}
        loading={banksLoading && banks.length === 0}
        selectedId={bank?.code}
        emptyText="No banks found."
        onSelect={(item) => {
          const b = banks.find((x) => x.code === item.id);
          if (!b) return;
          setBank(b);
          setShowBankPicker(false);
        }}
      />
    </SafeAreaView>
  );
};

export default NewPaymentMethodScreen;
