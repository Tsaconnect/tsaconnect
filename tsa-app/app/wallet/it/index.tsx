// app/wallet/it/index.tsx
//
// Instant Transfer hub: buy and sell crypto with Naira.
//
// IT covers two very different assets and the hub has to be honest about that:
//
//   MCGP        an entitlement we hold for the user, at an admin-set rate. It
//               has a spendable balance and a single published buy/sell price,
//               so both are worth showing.
//   USDC/USDT   brokered through Paycrest to the user's OWN wallet. There is no
//               custodial balance to show, and no single rate either — pricing
//               depends on order size and on which providers are live. A number
//               on this screen would be a promise the quote screen might not
//               keep, so we show none and let the buy screen quote.
//
// Recent activity merges both sides. They are separate tables behind separate
// APIs, and a user who buys USDC does not care about that — omitting Paycrest
// orders here would mean their purchase simply vanished from the hub.
import React, { useCallback, useMemo, useState } from 'react';
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
  getITBalance,
  getITRate,
  listITIntents,
  type ITBalance,
  type ITIntent,
  type ITRate,
} from '@/services/itApi';
import {
  listOrders as listPaycrestOrders,
  statusLabel as paycrestStatusLabel,
  type PaycrestOrder,
} from '@/services/paycrestApi';
import { useCurrency } from '@/contexts/CurrencyContext';
import { ngnToUsd } from '@/utils/currencyDisplay';

/** Stablecoins routed through Paycrest. MCGP is deliberately not in this list —
 *  it is a different product with a different screen. */
const STABLES = ['USDC', 'USDT'] as const;

const fmtMcgp = (wei?: string) => {
  try {
    return Number(ethers.formatUnits(wei || '0', 18)).toLocaleString(undefined, {
      maximumFractionDigits: 4,
    });
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

function itStatusLabel(status: string, direction: string): string {
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

/** One row of activity, whichever system produced it. */
type Activity = {
  key: string;
  title: string;
  status: string;
  at: string;
  onPress: () => void;
};

function itActivity(i: ITIntent): Activity {
  // A stablecoin intent carries asset/assetAmount and leaves mcgpAmount empty.
  // Formatting every sell as MCGP rendered those as "Sell · 0 MCGP".
  const asset = i.asset && i.asset !== 'MCGP' ? i.asset : null;
  const amt =
    i.direction === 'buy'
      ? `₦${fmtNaira(i.ngnKobo)}`
      : asset
        ? `${i.assetAmount ?? '—'} ${asset}`
        : `${fmtMcgp(i.mcgpAmount)} MCGP`;
  return {
    key: `it-${i.id}`,
    title: `${i.direction === 'buy' ? 'Buy' : 'Sell'} · ${amt}`,
    status: itStatusLabel(i.status, i.direction),
    at: i.createdAt,
    onPress: () => router.push(`/wallet/it/intent/${i.id}`),
  };
}

function paycrestActivity(o: PaycrestOrder): Activity {
  const amt = o.direction === 'buy' ? `₦${o.amount}` : `${o.tokenAmount ?? o.amount} ${o.token}`;
  return {
    key: `pc-${o.id}`,
    // Name the asset on a buy too — otherwise "Buy · ₦1,000" is
    // indistinguishable from an MCGP purchase in the same list.
    title: `${o.direction === 'buy' ? 'Buy' : 'Sell'} · ${amt}${o.direction === 'buy' ? ` ${o.token}` : ''}`,
    status: paycrestStatusLabel(o),
    at: o.createdAt,
    onPress: () => router.push(`/wallet/paycrest/order/${o.id}` as never),
  };
}

export default function ITHub() {
  const { colors, spacing } = useTheme();
  const { formatPrice, allRates } = useCurrency();
  const [bal, setBal] = useState<ITBalance | null>(null);
  const [rate, setRate] = useState<ITRate | null>(null);
  const [intents, setIntents] = useState<ITIntent[]>([]);
  const [orders, setOrders] = useState<PaycrestOrder[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setError('');
    // Paycrest is settled separately: it is dormant without a server key, and a
    // hub that fails to load because one optional rail is off would be worse
    // than one that quietly shows no stablecoin history.
    const [b, r, list, pc] = await Promise.all([
      getITBalance(),
      getITRate(),
      listITIntents(),
      listPaycrestOrders(20).catch(() => [] as PaycrestOrder[]),
    ]);
    if (b.success && b.data) setBal(b.data);
    else setError(b.message || 'Failed to load balance');
    if (r.success && r.data) setRate(r.data);
    if (list.success && list.data?.intents) setIntents(list.data.intents);
    setOrders(pc);
    setLoading(false);
  }, []);

  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

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

  const recent = useMemo(() => {
    const rows = [
      ...intents.filter((i) => i.status !== 'cancelled').map(itActivity),
      ...orders.map(paycrestActivity),
    ];
    // Newest first across both systems; an undated row sorts last rather than
    // jumping to the top on a bad parse.
    rows.sort((a, b) => (Date.parse(b.at) || 0) - (Date.parse(a.at) || 0));
    return rows.slice(0, 8);
  }, [intents, orders]);

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={
          <RefreshControl refreshing={loading} onRefresh={load} tintColor={colors.accent} />
        }
      >
        <View style={{ flexDirection: 'row', justifyContent: 'flex-end' }}>
          <CurrencySelector variant="header" />
        </View>

        {/* MCGP keeps the prominent card: it is the only asset here with a
            balance we hold and a single published price. */}
        <AccentCard>
          <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
            Spendable MCGP
          </Text>
          <Text
            variant="title1"
            weight="700"
            numeric
            style={{ color: colors.textPrimary, marginTop: spacing.xs }}
          >
            {loading ? '—' : fmtMcgp(spendable)} MCGP
          </Text>
          {rateLine ? (
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
              {rateLine}
            </Text>
          ) : null}
          <View style={{ flexDirection: 'row', gap: spacing.md, marginTop: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Button title="Buy" size="sm" onPress={() => router.push('/wallet/it/buy')} />
            </View>
            <View style={{ flex: 1 }}>
              <Button
                title="Sell"
                size="sm"
                variant="secondary"
                onPress={() => router.push('/wallet/it/sell')}
              />
            </View>
          </View>
        </AccentCard>

        {error ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error}
          </Text>
        ) : null}

        {/* Stablecoins get their own section rather than hiding behind an asset
            picker inside Buy. No rate shown: it depends on order size and on
            which providers are live, so any figure here could be one the quote
            screen cannot honour. */}
        <Card>
          <Text
            variant="title3"
            weight="700"
            style={{ color: colors.textPrimary, marginBottom: spacing.xs }}
          >
            Stablecoins
          </Text>
          <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.md }}>
            Bought and sold with Naira, straight to and from your own wallet.
          </Text>
          {STABLES.map((token, idx) => (
            <View key={token}>
              {idx > 0 ? <Divider spacing="sm" /> : null}
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: spacing.md,
                }}
              >
                <Text variant="bodyMed" weight="600" style={{ color: colors.textPrimary }}>
                  {token}
                </Text>
                <View style={{ flexDirection: 'row', gap: spacing.sm }}>
                  <Button
                    title="Buy"
                    size="sm"
                    onPress={() => router.push(`/wallet/paycrest/buy?asset=${token}` as never)}
                  />
                  <Button
                    title="Sell"
                    size="sm"
                    variant="secondary"
                    onPress={() => router.push(`/wallet/paycrest/sell?asset=${token}` as never)}
                  />
                </View>
              </View>
            </View>
          ))}
        </Card>

        {recent.length > 0 ? (
          <Card>
            <Text
              variant="title3"
              weight="700"
              style={{ color: colors.textPrimary, marginBottom: spacing.sm }}
            >
              Recent activity
            </Text>
            <SummaryCard>
              {recent.map((row, idx) => (
                <View key={row.key}>
                  {idx > 0 ? <Divider spacing="xs" /> : null}
                  <PressableScale
                    onPress={row.onPress}
                    accessibilityRole="button"
                    accessibilityLabel={`${row.title}, ${row.status}`}
                  >
                    <SummaryRow label={row.title} value={row.status} />
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
