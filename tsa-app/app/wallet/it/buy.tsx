// app/wallet/it/buy.tsx
//
// Buy MCGP with Naira (Spec 1). Currency / method pickers offer NGN + bank
// transfer. Continue quotes an indicative amount, then shows the user's
// permanent standing VA (any amount accepted). First-time users complete a
// one-time BVN/NIN gate to mint the VA. Settlement happens when funds land —
// no payment reference to type, no per-intent expiry.
import React, { useMemo, useState } from 'react';
import { View, ScrollView, TextInput, KeyboardAvoidingView, Platform } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput,
  Button,
  Card,
  Chip,
  CopyableField,
  Divider,
  Icon,
  PickerSheet,
  SelectRow,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import {
  activateITVA,
  itBuy,
  type ITBuyQuote,
  type ITIntent,
  type ITPayTo,
} from '@/services/itApi';
import { useCurrency } from '@/contexts/CurrencyContext';
import { ngnToUsd, shouldShowNgnSecondary } from './currency-display';

type Screen = 'form' | 'activate' | 'submitting' | 'instructions' | 'error';
type IdType = 'BVN' | 'NIN';

const CURRENCY_OPTIONS: PickerItem[] = [
  { id: 'NGN', label: 'NGN · Nigerian Naira', icon: 'wallet' },
];
const METHOD_OPTIONS: PickerItem[] = [
  { id: 'bank', label: 'Bank transfer', icon: 'bank' },
];
const ID_NUMBER_RE = /^\d{11}$/;

export default function ITBuy() {
  const { colors, spacing, radius, fonts, type } = useTheme();
  const { formatPrice, allRates, currency } = useCurrency();
  const [screen, setScreen] = useState<Screen>('form');
  const [naira, setNaira] = useState('');
  const [error, setError] = useState('');
  const [quote, setQuote] = useState<ITBuyQuote | null>(null);
  const [payTo, setPayTo] = useState<ITPayTo | null>(null);
  const [intent, setIntent] = useState<ITIntent | null>(null);
  const [estMcgp, setEstMcgp] = useState('');

  const [paymentCurrency, setPaymentCurrency] = useState('NGN');
  const [paymentMethod, setPaymentMethod] = useState('bank');
  const [showCurrencyPicker, setShowCurrencyPicker] = useState(false);
  const [showMethodPicker, setShowMethodPicker] = useState(false);

  const [idType, setIdType] = useState<IdType>('BVN');
  const [idNumber, setIdNumber] = useState('');
  const [idFocused, setIdFocused] = useState(false);
  const idValid = useMemo(() => ID_NUMBER_RE.test(idNumber.trim()), [idNumber]);

  const currencyLabel =
    CURRENCY_OPTIONS.find((o) => o.id === paymentCurrency)?.label ?? paymentCurrency;
  const methodLabel =
    METHOD_OPTIONS.find((o) => o.id === paymentMethod)?.label ?? paymentMethod;

  const applyQuote = (q: ITBuyQuote, to: ITPayTo | null, nextIntent?: ITIntent | null) => {
    setQuote(q);
    setPayTo(to);
    if (nextIntent !== undefined) setIntent(nextIntent);
    try {
      setEstMcgp(
        Number(ethers.formatUnits(q.mcgpAmount, 18)).toLocaleString(undefined, {
          maximumFractionDigits: 4,
        }),
      );
    } catch {
      setEstMcgp('');
    }
  };

  const submit = async () => {
    setError('');
    const amt = Number(naira);
    if (!amt || amt <= 0) {
      setError('Enter an amount');
      return;
    }
    setScreen('submitting');
    const kobo = String(Math.round(amt * 100));
    const r = await itBuy(kobo);
    if (!r.success || !r.data) {
      setError(r.message || 'Could not open the buy order');
      setScreen('error');
      return;
    }
    if (r.data.needsActivation) {
      applyQuote(r.data.quote, null, null);
      setScreen('activate');
      return;
    }
    applyQuote(r.data.quote, r.data.payTo, r.data.intent ?? null);
    setScreen('instructions');
  };

  const activate = async () => {
    setError('');
    if (!idValid) {
      setError(`${idType} must be exactly 11 digits`);
      return;
    }
    setScreen('submitting');
    const act = await activateITVA(idType, idNumber);
    if (!act.success || !act.data) {
      setError(act.message || 'Activation failed');
      setScreen('activate');
      return;
    }
    // Re-quote so payTo is attached; quote may already be set from Continue.
    const kobo = String(Math.round(Number(naira) * 100));
    const r = await itBuy(kobo);
    if (!r.success || !r.data || r.data.needsActivation) {
      applyQuote(quote ?? { ngnKobo: kobo, mcgpAmount: '0', rateKobo: '0' }, act.data.payTo, null);
      setScreen('instructions');
      return;
    }
    applyQuote(r.data.quote, r.data.payTo, r.data.intent ?? null);
    setScreen('instructions');
  };

  const ngnRate = allRates.NGN?.midRate ?? null;
  const payUsd = ngnToUsd(Number(naira) || 0, ngnRate);
  const showPaySecondary = payUsd != null && shouldShowNgnSecondary(currency.code);

  if (screen === 'submitting') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Text variant="body" style={{ color: colors.textSecondary }}>One moment…</Text>
      </SafeAreaView>
    );
  }

  if (screen === 'activate') {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
          <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }} keyboardShouldPersistTaps="handled">
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
              Activate Instant Transfer
            </Text>
            <Text variant="bodySm" style={{ color: colors.textSecondary }}>
              One-time identity check to issue your permanent Naira account. Your {idType} is
              sent to our payment partner and is not stored by TSA.
            </Text>

            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              ID type
            </Text>
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              {(['BVN', 'NIN'] as IdType[]).map((t) => (
                <Chip key={t} label={t} selected={idType === t} onPress={() => setIdType(t)} />
              ))}
            </View>

            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              {idType} (11 digits)
            </Text>
            <TextInput
              value={idNumber}
              onChangeText={(v) => setIdNumber(v.replace(/[^\d]/g, ''))}
              keyboardType="number-pad"
              maxLength={11}
              placeholder={idType === 'BVN' ? 'Bank Verification Number' : 'National Identification Number'}
              placeholderTextColor={colors.textMuted}
              onFocus={() => setIdFocused(true)}
              onBlur={() => setIdFocused(false)}
              style={{
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.md,
                borderRadius: radius.md,
                borderWidth: idFocused ? 2 : 1.5,
                borderColor: idFocused ? colors.accent : colors.border,
                backgroundColor: colors.surface,
                color: colors.textPrimary,
                fontFamily: fonts['400'],
                fontSize: type.body.fontSize,
                letterSpacing: 2,
              }}
            />
            {error ? (
              <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text>
            ) : null}
            <Button title="Activate" onPress={activate} disabled={!idValid} />
            <Button title="Back" variant="secondary" onPress={() => setScreen('form')} />
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    );
  }

  if (screen === 'instructions' && quote) {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Card>
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
              Transfer any amount to this account
            </Text>
            {showPaySecondary ? (
              <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.sm }}>
                For ₦{Number(naira).toLocaleString()} ≈ {formatPrice(payUsd as number)}
              </Text>
            ) : (
              <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.sm }}>
                For ₦{Number(naira).toLocaleString()} — rate locks when payment arrives
              </Text>
            )}
            <SummaryCard>
              <SummaryRow label="You get" value={`${estMcgp} MCGP`} />
              <Divider spacing="xs" />
              <SummaryRow
                label="Status"
                value={intent?.status === 'pending_payment' ? 'Awaiting transfer' : (intent?.status || 'Awaiting transfer')}
              />
            </SummaryCard>

            {payTo ? (
              <>
                <View style={{ marginTop: spacing.md }}>
                  <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
                    Pay into this account
                  </Text>
                  <SummaryCard>
                    <SummaryRow label="Bank" value={payTo.bank || '—'} />
                    <SummaryRow label="Account name" value={payTo.accountName || '—'} />
                  </SummaryCard>
                  <View style={{ marginTop: spacing.xs }}>
                    <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
                      Account number
                    </Text>
                    <CopyableField value={payTo.accountNumber || ''} />
                  </View>
                </View>
                <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.md }}>
                  This is your permanent Instant Transfer account — no reference to type, no
                  expiry. Transfer any Naira amount; your spendable MCGP appears when the
                  payment is confirmed at the live rate.
                </Text>
              </>
            ) : (
              <Text variant="bodySm" style={{ color: colors.danger, marginTop: spacing.md }}>
                Account details unavailable. Please try again.
              </Text>
            )}
          </Card>
          <Button title="Done" onPress={() => router.replace('/wallet/it')} />
        </ScrollView>
      </SafeAreaView>
    );
  }

  if (screen === 'error') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="closeCircle" size={64} color={colors.danger} />
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Couldn’t continue</Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>{error}</Text>
        <View style={{ height: spacing.xl }} />
        <Button title="Back" variant="secondary" onPress={() => setScreen('form')} />
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }} keyboardShouldPersistTaps="handled">
        <Text variant="bodySm" style={{ color: colors.textMuted }}>
          Buy spendable MCGP with a Naira bank transfer into your permanent account. More payment
          currencies will open later.
        </Text>

        <SelectRow
          label="Payment currency"
          value={currencyLabel}
          leftIcon="wallet"
          onPress={() => setShowCurrencyPicker(true)}
        />
        <SelectRow
          label="Method of payment"
          value={methodLabel}
          leftIcon="bank"
          onPress={() => setShowMethodPicker(true)}
        />

        <AmountInput
          label="Amount"
          value={naira}
          onChangeText={setNaira}
          symbol="₦"
          symbolPosition="prefix"
          error={error || undefined}
        />
        <Button title="Continue" onPress={submit} />
      </ScrollView>

      <PickerSheet
        visible={showCurrencyPicker}
        onClose={() => setShowCurrencyPicker(false)}
        title="Payment currency"
        items={CURRENCY_OPTIONS}
        selectedId={paymentCurrency}
        onSelect={(item) => {
          setPaymentCurrency(item.id);
          setShowCurrencyPicker(false);
        }}
      />
      <PickerSheet
        visible={showMethodPicker}
        onClose={() => setShowMethodPicker(false)}
        title="Method of payment"
        items={METHOD_OPTIONS}
        selectedId={paymentMethod}
        onSelect={(item) => {
          setPaymentMethod(item.id);
          setShowMethodPicker(false);
        }}
      />
    </SafeAreaView>
  );
}
