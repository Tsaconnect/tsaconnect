// app/wallet/it/sell.tsx
//
// Sell MCGP for Naira. Debits spendable (credited) MCGP on-chain and opens the
// Naira payout to the user's bank (settled by the company once paid out).
import React, { useEffect, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { AmountInput, Button, Card, Icon, SummaryCard, SummaryRow, Text } from '@/components/ui';
import { getITBalance, getITRate, itSell } from '@/services/itApi';
import { useCurrency } from '@/contexts/CurrencyContext';
import { ngnToUsd, shouldShowNgnSecondary } from './currency-display';

type Screen = 'form' | 'submitting' | 'done' | 'error';

export default function ITSell() {
  const { colors, spacing } = useTheme();
  const { formatPrice, allRates, currency } = useCurrency();
  const [screen, setScreen] = useState<Screen>('form');
  const [amount, setAmount] = useState('');
  const [error, setError] = useState('');
  const [credited, setCredited] = useState('0'); // wei
  const [sellRateKobo, setSellRateKobo] = useState('0');
  const [ngnOut, setNgnOut] = useState(''); // formatted for display
  const [ngnOutValue, setNgnOutValue] = useState<number | null>(null); // raw Naira amount

  useEffect(() => {
    (async () => {
      const [b, r] = await Promise.all([getITBalance(), getITRate()]);
      if (b.success && b.data) setCredited(b.data.premiumCredited);
      if (r.success && r.data) setSellRateKobo(r.data.sellRateKobo);
    })();
  }, []);

  const submit = async () => {
    setError('');
    const amt = Number(amount);
    if (!amt || amt <= 0) {
      setError('Enter an amount');
      return;
    }
    let wei: bigint;
    try {
      wei = ethers.parseUnits(amount, 18);
    } catch {
      setError('Invalid amount');
      return;
    }
    if (wei > BigInt(credited || '0')) {
      setError('More than your spendable (credited) balance');
      return;
    }
    setScreen('submitting');
    const r = await itSell(wei.toString());
    if (!r.success || !r.data) {
      setError(r.message || 'Sell failed');
      setScreen('error');
      return;
    }
    try {
      const naira = (Number(ethers.formatUnits(wei, 18)) * (Number(sellRateKobo) / 100));
      setNgnOutValue(naira);
      setNgnOut(naira.toLocaleString(undefined, { maximumFractionDigits: 2 }));
    } catch {
      setNgnOutValue(null);
      setNgnOut('');
    }
    setScreen('done');
  };

  // The payout itself is always real Naira (Nomba settles NGN only) — the
  // "→ ₦X" figure must stay in Naira. This is only a secondary, informational
  // conversion in the user's selected display currency, shown alongside
  // (never instead of) the Naira payout.
  const ngnRate = allRates.NGN?.midRate ?? null;
  const payoutUsd = ngnOutValue != null ? ngnToUsd(ngnOutValue, ngnRate) : null;
  const showPayoutSecondary = payoutUsd != null && shouldShowNgnSecondary(currency.code);

  if (screen === 'submitting') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Text variant="body" style={{ color: colors.textSecondary }}>Submitting sale…</Text>
      </SafeAreaView>
    );
  }

  if (screen === 'done') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="checkCircle" size={64} color={colors.success} />
        <Text variant="title2" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Sale submitted</Text>
        <Text variant="body" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
          {amount} MCGP{ngnOut ? ` → ₦${ngnOut}` : ''}. Your Naira payout is processing.
        </Text>
        {showPayoutSecondary ? (
          <Text variant="bodySm" style={{ marginTop: spacing.xs, color: colors.textMuted, textAlign: 'center' }}>
            ≈ {formatPrice(payoutUsd as number)}
          </Text>
        ) : null}
        <View style={{ height: spacing.xl }} />
        <Button title="Done" onPress={() => router.replace('/wallet/it')} />
      </SafeAreaView>
    );
  }

  if (screen === 'error') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="closeCircle" size={64} color={colors.danger} />
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Sale failed</Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>{error}</Text>
        <View style={{ height: spacing.xl }} />
        <Button title="Back" variant="secondary" onPress={() => setScreen('form')} />
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <Card>
          <SummaryCard>
            <SummaryRow
              label="Spendable (credited)"
              value={`${Number(ethers.formatUnits(credited || '0', 18)).toLocaleString(undefined, { maximumFractionDigits: 4 })} MCGP`}
            />
          </SummaryCard>
        </Card>
        <AmountInput
          label="Amount to sell"
          value={amount}
          onChangeText={setAmount}
          symbol="MCGP"
          symbolPosition="suffix"
          error={error || undefined}
        />
        <Button title="Sell" onPress={submit} />
      </ScrollView>
    </SafeAreaView>
  );
}
