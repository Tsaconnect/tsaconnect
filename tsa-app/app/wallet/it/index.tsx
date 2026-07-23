// app/wallet/it/index.tsx
//
// Instant Transfer hub: spendable (premium) MCGP balance + buy/sell with Naira.
import React, { useCallback, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Divider, Spinner, SummaryCard, SummaryRow, Text } from '@/components/ui';
import CurrencySelector from '@/components/currency/CurrencySelector';
import { getITBalance, getITRate, type ITBalance, type ITRate } from '@/services/itApi';
import { useCurrency } from '@/contexts/CurrencyContext';
import { ngnToUsd } from './currency-display';

const fmtMcgp = (wei?: string) => {
  try {
    return Number(ethers.formatUnits(wei || '0', 18)).toLocaleString(undefined, { maximumFractionDigits: 4 });
  } catch {
    return '0';
  }
};
const fmtNairaPerMcgp = (kobo?: string) => {
  try {
    return (Number(kobo || '0') / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });
  } catch {
    return '0';
  }
};

export default function ITHub() {
  const { colors, spacing } = useTheme();
  const { formatPrice, allRates } = useCurrency();
  const [bal, setBal] = useState<ITBalance | null>(null);
  const [rate, setRate] = useState<ITRate | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setError('');
    const [b, r] = await Promise.all([getITBalance(), getITRate()]);
    if (b.success && b.data) setBal(b.data);
    else setError(b.message || 'Failed to load balance');
    if (r.success && r.data) setRate(r.data);
    setLoading(false);
  }, []);

  useFocusEffect(useCallback(() => { load(); }, [load]));

  const spendable = bal
    ? (BigInt(bal.premiumBought || '0') + BigInt(bal.premiumCredited || '0')).toString()
    : '0';

  // The buy/sell rate is USD-anchored on the backend, but /api/it/rate only
  // exposes the derived NGN figure (buyRateKobo/sellRateKobo). Convert back
  // to USD via the live NGN rate so it can render in the selected display
  // currency; fall back to raw Naira if the NGN rate hasn't loaded yet.
  const ngnRate = allRates.NGN?.midRate ?? null;
  const buyUsd = rate ? ngnToUsd(Number(rate.buyRateKobo) / 100, ngnRate) : null;
  const sellUsd = rate ? ngnToUsd(Number(rate.sellRateKobo) / 100, ngnRate) : null;
  const rateLine = rate
    ? buyUsd != null && sellUsd != null
      ? `Buy ${formatPrice(buyUsd)} · Sell ${formatPrice(sellUsd)} per MCGP`
      : `Buy ₦${fmtNairaPerMcgp(rate.buyRateKobo)} · Sell ₦${fmtNairaPerMcgp(rate.sellRateKobo)} per MCGP`
    : null;

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={loading} onRefresh={load} tintColor={colors.accent} />}
      >
        <View style={{ flexDirection: 'row', justifyContent: 'flex-end' }}>
          <CurrencySelector variant="header" />
        </View>

        <AccentCard>
          <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
            Spendable MCGP
          </Text>
          <Text variant="title1" weight="700" numeric style={{ color: colors.textPrimary, marginTop: spacing.xs }}>
            {loading ? '—' : fmtMcgp(spendable)} MCGP
          </Text>
          {rateLine ? (
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
              {rateLine}
            </Text>
          ) : null}
        </AccentCard>

        {error ? <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text> : null}

        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          <View style={{ flex: 1 }}>
            <Button title="Buy with ₦" onPress={() => router.push('/wallet/it/buy')} />
          </View>
          <View style={{ flex: 1 }}>
            <Button title="Sell for ₦" variant="secondary" onPress={() => router.push('/wallet/it/sell')} />
          </View>
        </View>

        <Card>
          <SummaryCard>
            <SummaryRow label="Bought (USDC)" value={`${fmtMcgp(bal?.premiumBought)} MCGP`} />
            <Divider spacing="xs" />
            <SummaryRow label="Credited (Naira)" value={`${fmtMcgp(bal?.premiumCredited)} MCGP`} />
            <SummaryRow label="Selling (pending ₦)" value={`${fmtMcgp(bal?.pendingSettle)} MCGP`} />
          </SummaryCard>
          <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
            MCGP bought with Naira is spendable in the marketplace and cashed out by selling back for
            Naira.
          </Text>
        </Card>

        {loading ? <Spinner /> : null}
      </ScrollView>
    </SafeAreaView>
  );
}
