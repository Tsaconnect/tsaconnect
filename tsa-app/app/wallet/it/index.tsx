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
//
// Direction is a separate question from provider, and the rows carry that too:
// Paycrest has offramp providers for UGX and TZS and no onramp, so those
// currencies show Sell with no Buy. The legs come from the live order book via
// rampLegsFor, so a leg that opens or closes at the provider is reflected
// without shipping anything.
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
  Icon,
  PickerSheet,
  PressableScale,
  Skeleton,
  Text,
} from '@/components/ui';
import ITActivityRow, { ACTIVITY_DIVIDER_INSET } from '@/components/it/ITActivityRow';
import TokenAvatar from '@/components/wallet/TokenAvatar';
import CurrencySelector from '@/components/currency/CurrencySelector';
import { useRampCurrency } from '@/hooks/useRampCurrency';
import {
  getITBalance,
  getITRate,
  listITIntents,
  rampLegsFor,
  rampProviderFor,
  type ITBalance,
  type ITIntent,
  type ITRate,
} from '@/services/itApi';
import { listOrders as listPaycrestOrders, type PaycrestOrder } from '@/services/paycrestApi';
import { getWalletBalances, normalizeWalletBalances } from '@/services/walletApi';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useFiatSymbol } from '@/hooks/useFiatSymbol';
import { ngnToUsd } from '@/utils/currencyDisplay';
import { mcgpRowActions, stableRowActions } from '@/utils/rampRow';
import { buildITActivity } from '@/utils/itActivity';

/** Rows on the hub. The rest are one tap away on the activity screen. */
const RECENT_LIMIT = 5;

/** Asset-row icon edge; the row divider's inset lines up with it. */
const TOKEN_AVATAR_SIZE = 40;

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
const fmtToken = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: n >= 1 ? 4 : 6 });

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
  const symbolFor = useFiatSymbol();
  const { address, ready: walletReady } = useSignableWallet();

  const [bal, setBal] = useState<ITBalance | null>(null);
  const [rate, setRate] = useState<ITRate | null>(null);
  const [intents, setIntents] = useState<ITIntent[]>([]);
  const [orders, setOrders] = useState<PaycrestOrder[]>([]);
  const [stable, setStable] = useState<Record<string, number>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [showCurrencyPicker, setShowCurrencyPicker] = useState(false);

  const load = useCallback(async () => {
    setError('');
    // Each optional source degrades on its own. Paycrest is dormant without a
    // server key and the balance call needs a wallet on this device — a hub
    // that fails wholesale because one of those is missing would be worse than
    // one showing a dash.
    const [b, r, list, pc, balances] = await Promise.all([
      getITBalance(),
      getITRate(),
      listITIntents(),
      listPaycrestOrders(20).catch(() => [] as PaycrestOrder[]),
      address ? getWalletBalances(undefined, address).catch(() => null) : Promise.resolve(null),
    ]);
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
  // `currency` at all, buy/sell here always meant NGN, and deriving the ramp
  // from the display preference regressed any user who ever switched it to
  // USD to read prices in dollars: they would see "Not available in USD"
  // where the working NGN ramp used to be. So it stays a separate, explicit
  // choice — useRampCurrency persists it under its own AsyncStorage key,
  // defaults from the user's own country among currencies that actually have
  // a provider, and CurrencySelector below still governs price formatting
  // only, never what currency Buy/Sell actually uses.
  const { code, setCode, options: currencyOptions, currencies } = useRampCurrency();
  const support = rampProviderFor(currencies, code);
  // Which way this currency actually trades. A provider is not a promise of
  // both directions: Paycrest has offramp providers for UGX and TZS and no
  // onramp, so those currencies sell and cannot be bought. The server derives
  // this from the live order book, which is why it is a per-render fact rather
  // than something baked into the build.
  // Memoized so the row memo below can depend on it: rampLegsFor builds a
  // fresh object each call, and an unstable dependency would rebuild every row
  // on every render.
  const legs = useMemo(() => rampLegsFor(currencies, code), [currencies, code]);

  const assets: AssetRow[] = useMemo(() => {
    // Until the wallet check resolves, no row knows its balance or what it may
    // offer. Treating that as "no wallet" flashed "Connect a wallet to trade"
    // at users who have one.
    if (!walletReady) {
      return [
        { token: 'MCGP', balance: null, balanceNote: '', rateLine: mcgpRateLine },
        { token: 'USDC', balance: null, balanceNote: '' },
        { token: 'USDT', balance: null, balanceNote: '' },
      ];
    }

    const stableRow = (token: 'USDC' | 'USDT'): AssetRow => {
      const base: AssetRow = {
        token,
        balance: address && loading ? null : fmtToken(stable[token] ?? 0),
        balanceNote: address ? 'in your wallet' : 'connect a wallet',
      };
      // What this row may offer, and why not when it may not — see
      // utils/rampRow.ts. Each action is attached only if its direction is
      // live, so a sell-only currency shows Sell alone rather than a Buy that
      // fails once the user has already picked an amount.
      const actions = stableRowActions({
        token,
        currency: code,
        provider: support,
        legs,
        hasWallet: !!address,
      });
      if (actions.unavailable) return { ...base, unavailable: actions.unavailable };

      const route = (dir: 'buy' | 'sell') =>
        support === 'paycrest'
          ? `/wallet/paycrest/${dir}?asset=${token}&currency=${code}`
          : `/wallet/it/${dir}-stable?asset=${token}&currency=${code}`;

      return {
        ...base,
        onBuy: actions.buy ? () => router.push(route('buy') as never) : undefined,
        onSell: actions.sell ? () => router.push(route('sell') as never) : undefined,
      };
    };
    // MCGP needs a wallet this device can sign for, like the stablecoins, and
    // its buy/sell screens are NGN-only — see mcgpRowActions. Without a wallet
    // the balance reads 0 rather than the registered wallet's balance, which
    // this device holds no key to spend.
    const mcgpActions = mcgpRowActions({ currency: code, hasWallet: !!address });
    const mcgpRow: AssetRow = {
      token: 'MCGP',
      balance: address && loading ? null : fmtMcgp(address ? spendable : '0'),
      balanceNote: address ? 'spendable' : 'connect a wallet',
      rateLine: mcgpRateLine,
    };
    return [
      mcgpActions.unavailable
        ? { ...mcgpRow, unavailable: mcgpActions.unavailable }
        : {
            ...mcgpRow,
            onBuy: () => router.push('/wallet/it/buy'),
            onSell: () => router.push('/wallet/it/sell'),
          },
      stableRow('USDC'),
      stableRow('USDT'),
    ];
  }, [walletReady, loading, address, stable, support, legs, code, spendable, mcgpRateLine]);

  const activity = useMemo(
    () => buildITActivity(intents, orders, symbolFor, { hideCancelledIntents: true }),
    [intents, orders, symbolFor],
  );
  const recent = activity.slice(0, RECENT_LIMIT);

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
          {/* Only offer a choice when there is one — a picker listing a
              single currency is noise. */}
          {currencyOptions.length > 1 ? (
            <PressableScale
              onPress={() => setShowCurrencyPicker(true)}
              style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}
            >
              <Text variant="bodySm" style={{ color: colors.textMuted }}>
                Buy and sell with {code}
              </Text>
              <Icon name="chevronDown" size={14} color={colors.textMuted} />
            </PressableScale>
          ) : (
            <Text variant="bodySm" style={{ color: colors.textMuted }}>
              Buy and sell with {code}
            </Text>
          )}
          <CurrencySelector variant="header" />
        </View>

        {error ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error}
          </Text>
        ) : null}

        {/* One list, so no asset reads as an afterthought. The logo names the
            asset, the balance is the anchor, and the actions sit to the right
            where the thumb already is. */}
        <Card>
          {assets.map((a, idx) => (
            <View key={a.token}>
              {idx > 0 ? <Divider spacing="md" inset={TOKEN_AVATAR_SIZE + spacing.md} /> : null}
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <TokenAvatar symbol={a.token} size={TOKEN_AVATAR_SIZE} />
                <View style={{ flex: 1, minWidth: 0 }}>
                  <Text variant="caption" weight="600" style={{ color: colors.textSecondary }}>
                    {a.token}
                  </Text>
                  {a.balance === null ? (
                    <Skeleton width={96} height={22} style={{ marginVertical: 2 }} />
                  ) : (
                    <Text
                      variant="title3"
                      weight="700"
                      numeric
                      numberOfLines={1}
                      adjustsFontSizeToFit
                      style={{ color: colors.textPrimary }}
                    >
                      {a.balance}
                    </Text>
                  )}
                  <Text variant="caption" numberOfLines={1} style={{ color: colors.textMuted }}>
                    {a.balanceNote}
                  </Text>
                  {a.rateLine ? (
                    <Text variant="caption" numberOfLines={1} style={{ color: colors.textMuted }}>
                      {a.rateLine}
                    </Text>
                  ) : null}
                </View>
                {a.unavailable ? (
                  <Text
                    variant="caption"
                    style={{ color: colors.textMuted, maxWidth: 120, textAlign: 'right' }}
                  >
                    {a.unavailable}
                  </Text>
                ) : (
                  // Only the directions that actually trade get a button. A
                  // disabled-looking Buy would still read as "try me"; absence
                  // is the honest rendering of a leg no provider quotes.
                  <View style={{ flexDirection: 'row', gap: spacing.sm }}>
                    {a.onBuy ? <Button title="Buy" size="sm" onPress={a.onBuy} /> : null}
                    {a.onSell ? (
                      <Button title="Sell" size="sm" variant="secondary" onPress={a.onSell} />
                    ) : null}
                  </View>
                )}
              </View>
            </View>
          ))}
        </Card>

        <Card>
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'space-between',
              marginBottom: spacing.xs,
            }}
          >
            <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
              Recent activity
            </Text>
            {/* Offered whenever the full list has anything the hub does not
                show — including cancelled buys, which the hub hides. */}
            {intents.length + orders.length > 0 ? (
              <PressableScale
                onPress={() => router.push('/wallet/it/activity' as never)}
                accessibilityRole="button"
                accessibilityLabel="View all activity"
                hitSlop={8}
                style={{ flexDirection: 'row', alignItems: 'center', gap: 2 }}
              >
                <Text variant="bodySm" weight="600" style={{ color: colors.accent }}>
                  View all
                </Text>
                <Icon name="chevronRight" size={14} color={colors.accent} />
              </PressableScale>
            ) : null}
          </View>

          {loading && recent.length === 0 ? (
            <View style={{ gap: spacing.md, paddingVertical: spacing.sm }}>
              {[0, 1, 2].map((k) => (
                <Skeleton key={k} height={40} radius="md" />
              ))}
            </View>
          ) : recent.length > 0 ? (
            recent.map((row, idx) => (
              <View key={row.key}>
                {idx > 0 ? <Divider inset={ACTIVITY_DIVIDER_INSET} /> : null}
                <ITActivityRow item={row} />
              </View>
            ))
          ) : (
            <Text variant="bodySm" style={{ color: colors.textMuted, paddingVertical: spacing.sm }}>
              {/* Cancelled buys are hidden here but still exist behind View all,
                  so "no orders" would contradict the link above. */}
              {intents.length + orders.length > 0
                ? 'No active orders.'
                : 'No orders yet. Buying opens an order you can track until payment clears.'}
            </Text>
          )}
        </Card>
      </ScrollView>

      <PickerSheet
        visible={showCurrencyPicker}
        onClose={() => setShowCurrencyPicker(false)}
        title="Buy and sell with"
        items={currencyOptions}
        selectedId={code}
        onSelect={(item) => {
          setCode(item.id);
          setShowCurrencyPicker(false);
        }}
        emptyText="No ramp currencies available."
      />
    </SafeAreaView>
  );
}
