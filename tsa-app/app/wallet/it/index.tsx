// app/wallet/it/index.tsx
//
// Instant Transfer hub: spendable MCGP balance, Buy/Sell entry points, and
// recent orders (tap for detail).
import React, { useCallback, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  Card,
  Divider,
  PressableScale,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import CurrencySelector from '@/components/currency/CurrencySelector';
import {
  getITAgreement,
  getITBalance,
  getITRate,
  listITIntents,
  type ITBalance,
  type ITIntent,
  type ITRate,
} from '@/services/itApi';
import { useCurrency } from '@/contexts/CurrencyContext';
import { ngnToUsd } from '@/utils/currencyDisplay';
import ITAgreementGate from '@/components/it/ITAgreementGate';

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
const fmtNaira = (kobo?: string) => {
  try {
    return (Number(kobo || '0') / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });
  } catch {
    return '0';
  }
};

function statusLabel(status: string, direction: string): string {
  switch (status) {
    case 'pending_payment':
      return 'Awaiting transfer';
    case 'processing':
      return 'Processing';
    case 'credited':
      return 'Complete';
    case 'debited':
      return 'Payout in progress';
    case 'settled':
      return 'Complete';
    case 'awaiting_rate':
      return 'Awaiting rate';
    case 'failed':
      return 'Failed';
    case 'cancelled':
      return 'Cancelled';
    case 'reversed':
      return 'Reversed';
    default:
      return status || (direction === 'buy' ? 'Buy' : 'Sell');
  }
}

function intentTitle(i: ITIntent): string {
  const amt = i.direction === 'buy' ? `₦${fmtNaira(i.ngnKobo)}` : `${fmtMcgp(i.mcgpAmount)} MCGP`;
  return i.direction === 'buy' ? `Buy · ${amt}` : `Sell · ${amt}`;
}

export default function ITHub() {
  const { colors, spacing } = useTheme();
  const { formatPrice, allRates } = useCurrency();
  const [bal, setBal] = useState<ITBalance | null>(null);
  const [rate, setRate] = useState<ITRate | null>(null);
  const [intents, setIntents] = useState<ITIntent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // null = not checked yet; false = must agree before trading; true = cleared.
  const [agreed, setAgreed] = useState<boolean | null>(null);

  const load = useCallback(async () => {
    setError('');
    const [b, r, list, ag] = await Promise.all([
      getITBalance(), getITRate(), listITIntents(), getITAgreement(),
    ]);
    if (b.success && b.data) setBal(b.data);
    else setError(b.message || 'Failed to load balance');
    if (r.success && r.data) setRate(r.data);
    if (list.success && list.data?.intents) setIntents(list.data.intents);
    // If the agreement can't be loaded, don't hard-block the hub — the backend
    // still gates each trade, so a transient failure won't let an un-agreed
    // user through.
    if (ag.success && ag.data) setAgreed(ag.data.accepted);
    setLoading(false);
  }, []);

  useFocusEffect(useCallback(() => { load(); }, [load]));

  // Block the hub behind the agreement until the user accepts the current
  // version. Cancel takes them out of IT entirely.
  if (agreed === false) {
    return (
      <ITAgreementGate
        onAccepted={() => { setAgreed(true); load(); }}
        onCancel={() => router.back()}
      />
    );
  }

  const spendable = bal
    ? (BigInt(bal.premiumBought || '0') + BigInt(bal.premiumCredited || '0')).toString()
    : '0';

  const ngnRate = allRates.NGN?.midRate ?? null;
  const buyUsd = rate ? ngnToUsd(Number(rate.buyRateKobo) / 100, ngnRate) : null;
  const sellUsd = rate ? ngnToUsd(Number(rate.sellRateKobo) / 100, ngnRate) : null;
  const rateLine = rate
    ? buyUsd != null && sellUsd != null
      ? `Buy ${formatPrice(buyUsd)} · Sell ${formatPrice(sellUsd)} per MCGP`
      : `Buy ₦${fmtNairaPerMcgp(rate.buyRateKobo)} · Sell ₦${fmtNairaPerMcgp(rate.sellRateKobo)} per MCGP`
    : null;

  const recent = intents.filter((i) => !['cancelled'].includes(i.status)).slice(0, 8);

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
            <Button title="Buy" onPress={() => router.push('/wallet/it/buy')} />
          </View>
          <View style={{ flex: 1 }}>
            <Button title="Sell" variant="secondary" onPress={() => router.push('/wallet/it/sell')} />
          </View>
        </View>

        {recent.length > 0 ? (
          <Card>
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary, marginBottom: spacing.sm }}>
              Recent activity
            </Text>
            <SummaryCard>
              {recent.map((i, idx) => (
                <View key={i.id}>
                  {idx > 0 ? <Divider spacing="xs" /> : null}
                  <PressableScale
                    onPress={() => router.push(`/wallet/it/intent/${i.id}`)}
                    accessibilityRole="button"
                    accessibilityLabel={`${intentTitle(i)}, ${statusLabel(i.status, i.direction)}`}
                  >
                    <SummaryRow label={intentTitle(i)} value={statusLabel(i.status, i.direction)} />
                  </PressableScale>
                </View>
              ))}
            </SummaryCard>
          </Card>
        ) : !loading ? (
          <Text variant="bodySm" style={{ color: colors.textMuted }}>
            No Instant Transfer orders yet. Buy opens an order you can track until payment clears.
          </Text>
        ) : null}

        {loading ? <Spinner /> : null}
      </ScrollView>
    </SafeAreaView>
  );
}
