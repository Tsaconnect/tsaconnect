// app/wallet/it/buy.tsx
//
// Buy MCGP with Naira. Creates an intent + payment reference; the user pays the
// company merchant via Nomba including the reference, and the balance updates
// once the payment is confirmed (no client signing).
import React, { useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { AmountInput, Button, Card, CopyableField, Divider, Icon, SummaryCard, SummaryRow, Text } from '@/components/ui';
import { itBuy, type ITIntent, type ITPayTo } from '@/services/itApi';
import { useCurrency } from '@/contexts/CurrencyContext';
import { ngnToUsd, shouldShowNgnSecondary } from './currency-display';

type Screen = 'form' | 'submitting' | 'instructions' | 'error';

export default function ITBuy() {
  const { colors, spacing } = useTheme();
  const { formatPrice, allRates, currency } = useCurrency();
  const [screen, setScreen] = useState<Screen>('form');
  const [naira, setNaira] = useState('');
  const [error, setError] = useState('');
  const [intent, setIntent] = useState<ITIntent | null>(null);
  const [payTo, setPayTo] = useState<ITPayTo | null>(null);
  const [estMcgp, setEstMcgp] = useState('');

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
      setError(r.message || 'Could not create the buy');
      setScreen('error');
      return;
    }
    setIntent(r.data.intent);
    setPayTo(r.data.payTo ?? null);
    try {
      setEstMcgp(Number(ethers.formatUnits(r.data.intent.mcgpAmount, 18)).toLocaleString(undefined, { maximumFractionDigits: 4 }));
    } catch {
      setEstMcgp('');
    }
    setScreen('instructions');
  };

  // Payment is always a real Naira bank transfer (Nomba only settles NGN) —
  // the "Pay ₦X" instruction itself must stay in Naira. This is only a
  // secondary, informational conversion in the user's selected display
  // currency, shown alongside (never instead of) the Naira amount.
  const ngnRate = allRates.NGN?.midRate ?? null;
  const payUsd = ngnToUsd(Number(naira) || 0, ngnRate);
  const showPaySecondary = payUsd != null && shouldShowNgnSecondary(currency.code);

  if (screen === 'submitting') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Text variant="body" style={{ color: colors.textSecondary }}>Creating your order…</Text>
      </SafeAreaView>
    );
  }

  if (screen === 'instructions' && intent) {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Card>
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.xs }}>
              Pay ₦{Number(naira).toLocaleString()} to complete
            </Text>
            {showPaySecondary ? (
              <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.sm }}>
                ≈ {formatPrice(payUsd as number)}
              </Text>
            ) : null}
            <SummaryCard>
              <SummaryRow label="You get" value={`${estMcgp} MCGP`} />
              <Divider spacing="xs" />
              <SummaryRow label="Status" value="Awaiting payment" />
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
                <View style={{ marginTop: spacing.md }}>
                  <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
                    Payment reference (include this in your transfer)
                  </Text>
                  <CopyableField value={payTo.reference || intent.paymentRef || ''} />
                </View>
                <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.md }}>
                  Transfer exactly ₦{Number(naira).toLocaleString()} to the account above and include
                  the reference. Your spendable MCGP appears once the payment is confirmed.
                </Text>
              </>
            ) : (
              <Text variant="bodySm" style={{ color: colors.danger, marginTop: spacing.md }}>
                Payments are temporarily unavailable — no payout account is configured. Please try
                again later. (Reference {intent.paymentRef})
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
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Couldn’t create order</Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>{error}</Text>
        <View style={{ height: spacing.xl }} />
        <Button title="Back" variant="secondary" onPress={() => setScreen('form')} />
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <AmountInput
          label="Amount in Naira"
          value={naira}
          onChangeText={setNaira}
          symbol="₦"
          symbolPosition="prefix"
          error={error || undefined}
        />
        <Button title="Continue" onPress={submit} />
      </ScrollView>
    </SafeAreaView>
  );
}
