// app/wallet/it/index.tsx
//
// Instant Transfer hub: buy and sell crypto with your local currency.
//
// One asset list rather than "MCGP, plus some others" — the split was the
// screen's main problem. What differs between the assets is real, though, and
// the design has to carry it honestly:
//
//   MCGP        an entitlement WE hold for the user, at an admin-set rate, so
//               it has a spendable balance and a single published buy/sell
//               price worth showing.
//   USDC/USDT   brokered to the user's OWN wallet. The balance shown is theirs
//               on-chain, not ours. There is no single rate either — pricing
//               moves with order size and with which providers are live, so a
//               figure here could be one the quote screen cannot honour.
//
// Currency support is not uniform and the rows say so rather than failing
// later. Which provider (if any) serves a currency comes from the backend
// currency registry via getRampCurrencies/rampProviderFor — not hardcoded
// here, so adding a currency is a backend change, not an app release.
import React, { useCallback, useMemo, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
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
  getRampCurrencies,
  rampProviderFor,
  type ITBalance,
  type ITIntent,
  type ITRate,
  type RampCurrency,
} from '@/services/itApi';
import {
  listOrders as listPaycrestOrders,
  statusLabel as paycrestStatusLabel,
  type PaycrestOrder,
} from '@/services/paycrestApi';
import { getWalletBalances, normalizeWalletBalances } from '@/services/walletApi';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCurrency } from '@/contexts/CurrencyContext';
import { ngnToUsd } from '@/utils/currencyDisplay';

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
const fmtToken = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: n >= 1 ? 4 : 6 });

function itStatusLabel(status: string, direction: string): string {
  switch (status) {
    case 'pending_payment':
      return 'Awaiting transfer';
    case 'processing':
      return 'Processing';
    case 'credited':
    case 'settled':
      return 'Complete';
    case 'debited':
      return 'Payout in progress';
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
    // Name the asset on a buy too, or "Buy · ₦1,000" is indistinguishable from
    // an MCGP purchase sitting next to it in the same list.
    title: `${o.direction === 'buy' ? 'Buy' : 'Sell'} · ${amt}${
      o.direction === 'buy' ? ` ${o.token}` : ''
    }`,
    status: paycrestStatusLabel(o),
    at: o.createdAt,
    onPress: () => router.push(`/wallet/paycrest/order/${o.id}` as never),
  };
}

/** One row of the asset list. */
type AssetRow = {
  token: string;
  /** Formatted holding, or null while loading. */
  balance: string | null;
  /** Where the balance lives — the distinction matters to the user. */
  balanceNote: string;
  /** Price line, only where a single published rate actually exists. */
  rateLine?: string;
  onBuy?: () => void;
  onSell?: () => void;
  /** Set when the selected currency cannot trade this asset at all. */
  unavailable?: string;
};

export default function ITHub() {
  const { colors, spacing } = useTheme();
  const { formatPrice, allRates } = useCurrency();
  const { address } = useSignableWallet();

  const [bal, setBal] = useState<ITBalance | null>(null);
  const [rate, setRate] = useState<ITRate | null>(null);
  const [intents, setIntents] = useState<ITIntent[]>([]);
  const [orders, setOrders] = useState<PaycrestOrder[]>([]);
  const [stable, setStable] = useState<Record<string, number>>({});
  const [currencies, setCurrencies] = useState<RampCurrency[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setError('');
    // Each optional source degrades on its own. Paycrest is dormant without a
    // server key and the balance call needs a wallet on this device — a hub
    // that fails wholesale because one of those is missing would be worse than
    // one showing a dash.
    const [b, r, list, pc, balances, curList] = await Promise.all([
      getITBalance(),
      getITRate(),
      listITIntents(),
      listPaycrestOrders(20).catch(() => [] as PaycrestOrder[]),
      address ? getWalletBalances(undefined, address).catch(() => null) : Promise.resolve(null),
      // A failed fetch yields an empty table, which resolves to no provider —
      // so the screen says "unavailable" rather than offering a trade the
      // backend would refuse.
      getRampCurrencies().catch(() => [] as RampCurrency[]),
    ]);
    setCurrencies(curList);
    if (b.success && b.data) setBal(b.data);
    else setError(b.message || 'Failed to load balance');
    if (r.success && r.data) setRate(r.data);
    if (list.success && list.data?.intents) setIntents(list.data.intents);
    setOrders(pc);

    if (balances?.success && balances.data) {
      // Stablecoins live on several chains at once, and the user thinks in
      // "how much USDC do I have", not "how much on Base". Sum across chains.
      const totals: Record<string, number> = {};
      for (const row of normalizeWalletBalances(balances.data)) {
        const sym = (row.symbol || '').toUpperCase();
        if (sym !== 'USDC' && sym !== 'USDT') continue;
        totals[sym] = (totals[sym] ?? 0) + (Number(row.balance) || 0);
      }
      setStable(totals);
    }
    setLoading(false);
  }, [address]);

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
  const mcgpRateLine = rate
    ? buyUsd != null && sellUsd != null
      ? `Buy ${formatPrice(buyUsd)} · Sell ${formatPrice(sellUsd)}`
      : `Buy ₦${fmtNairaPerMcgp(rate.buyRateKobo)} · Sell ₦${fmtNairaPerMcgp(rate.sellRateKobo)}`
    : undefined;

  // The ramp's settlement currency is deliberately NOT `currency` from
  // useCurrency() — that context is the price-FORMATTING preference (default
  // USD, persisted in AsyncStorage, auto-derived from device country), not
  // the currency an order actually settles in. Before this screen read
  // `currency` at all, buy/sell here always meant NGN, which is still the
  // only currency the screens (buy.tsx/sell.tsx, Paycrest buy/sell) can
  // transact in. Deriving the ramp from the display preference regressed any
  // user who ever switched it to USD to read prices in dollars: they would
  // see "Not available in USD" where the working NGN ramp used to be. Keep
  // these two independent — CurrencySelector below still governs price
  // formatting, which is its job, but must never gate what currency Buy/Sell
  // actually uses.
  const code = 'NGN';
  const support = rampProviderFor(currencies, code);

  const assets: AssetRow[] = useMemo(() => {
    const stableRow = (token: 'USDC' | 'USDT'): AssetRow => {
      const base: AssetRow = {
        token,
        balance: loading && !address ? null : fmtToken(stable[token] ?? 0),
        balanceNote: address ? 'in your wallet' : 'connect a wallet',
      };
      if (!address) return { ...base, unavailable: 'Connect a wallet to trade' };
      switch (support) {
        case 'paycrest':
          return {
            ...base,
            onBuy: () => router.push(`/wallet/paycrest/buy?asset=${token}` as never),
            onSell: () => router.push(`/wallet/paycrest/sell?asset=${token}` as never),
          };
        case 'flutterwave':
          return {
            ...base,
            onBuy: () => router.push(`/wallet/it/buy-stable?asset=${token}` as never),
            onSell: () => router.push(`/wallet/it/sell-stable?asset=${token}` as never),
          };
        default:
          return { ...base, unavailable: `Not available in ${code}` };
      }
    };
    return [
      {
        token: 'MCGP',
        balance: loading ? null : fmtMcgp(spendable),
        balanceNote: 'spendable',
        rateLine: mcgpRateLine,
        onBuy: () => router.push('/wallet/it/buy'),
        onSell: () => router.push('/wallet/it/sell'),
      },
      stableRow('USDC'),
      stableRow('USDT'),
    ];
  }, [loading, address, stable, support, code, spendable, mcgpRateLine, currencies]);

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
        <View
          style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}
        >
          <Text variant="bodySm" style={{ color: colors.textMuted }}>
            Buy and sell with {code}
          </Text>
          <CurrencySelector variant="header" />
        </View>

        {error ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error}
          </Text>
        ) : null}

        {/* One list, so no asset reads as an afterthought. Balance is the
            anchor on each row; the actions sit under it rather than competing
            with it for the eye. */}
        <Card>
          {assets.map((a, idx) => (
            <View key={a.token}>
              {idx > 0 ? <Divider spacing="md" /> : null}
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'flex-start',
                  justifyContent: 'space-between',
                  gap: spacing.md,
                }}
              >
                <View style={{ flex: 1, gap: 2 }}>
                  <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
                    {a.token}
                  </Text>
                  <Text
                    variant="title3"
                    weight="700"
                    numeric
                    style={{ color: colors.textPrimary }}
                  >
                    {a.balance ?? '—'}
                  </Text>
                  <Text variant="caption" style={{ color: colors.textMuted }}>
                    {a.balanceNote}
                    {a.rateLine ? ` · ${a.rateLine}` : ''}
                  </Text>
                </View>
                {a.unavailable ? (
                  <Text
                    variant="caption"
                    style={{ color: colors.textMuted, maxWidth: 120, textAlign: 'right' }}
                  >
                    {a.unavailable}
                  </Text>
                ) : (
                  <View style={{ flexDirection: 'row', gap: spacing.sm, paddingTop: spacing.xs }}>
                    <Button title="Buy" size="sm" onPress={a.onBuy} />
                    <Button title="Sell" size="sm" variant="secondary" onPress={a.onSell} />
                  </View>
                )}
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
            No orders yet. Buying opens an order you can track until payment clears.
          </Text>
        ) : null}

        {loading ? <Spinner /> : null}
      </ScrollView>
    </SafeAreaView>
  );
}
