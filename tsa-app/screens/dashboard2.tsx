// screens/dashboard2.tsx
//
// Home dashboard — total portfolio balance, quick actions, the token market
// list, the Real World Marketplace (RWM) widget, and trade shortcuts. Built on
// the TSA Signature design system (@/theme tokens + @/components/ui primitives).
//
// The market list shows PLATFORM-LISTED TOKENS with price and 24h change (PM
// spec) — not the user's holdings, which live on the Wallet tab. The balance
// fetch below survives only to feed BalanceCard's portfolio total.
import React, { useState, useEffect, useCallback, useMemo } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { api } from '@/components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { router } from 'expo-router';
import CartIconBadge from '@/components/common/CartIconBadge';
import CurrencySelector from '@/components/currency/CurrencySelector';
import {
  getWalletBalances,
  normalizeWalletBalances,
} from '../services/walletApi';
import { getPremiumMcgpAsset } from '../services/mcgpSwapApi';

import { BalanceCard } from '@/components/dashboard/BalanceCard';
import { RWMWidget } from '@/components/dashboard/RWMWidget';
import { TradeActions } from '@/components/dashboard/TradeActions';
import { BackupBanner } from '@/components/dashboard/BackupBanner';
import AssetPairSheet from '@/components/spot/AssetPairSheet';
import TokenMarketList from '@/components/spot/TokenMarketList';
import { listingsForSymbol } from '@/utils/spotPairs';
import { buildTokenMarkets, tokensForTab } from '@/utils/spotTokens';
import { type SpotTabKey } from '@/utils/spotTabs';
import { useSpotFavorites } from '@/hooks/useSpotFavorites';
import { useSpotListings } from '@/hooks/useSpotListings';
import { useNetwork } from '../hooks/useNetwork';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Divider,
  Icon,
  PressableScale,
  Skeleton,
  SkeletonGroup,
  Text,
} from '@/components/ui';

// Symbols the portfolio total is summed over. Not a display list — the market
// list below is driven entirely by the platform's spot listings.
const BALANCE_SYMBOLS = ['MCGP', 'USDC', 'USDT'] as const;

/** Tokens shown on Home before "View all" hands off to the Spot Trade tab. */
const HOME_TOKEN_PREVIEW = 5;

const Dashboard: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { network } = useNetwork();
  const { formatDualPrice } = useCurrency();
  // Spot listings drive the token market list AND the pair/chain picker opened
  // by tapping a token. Shared cache — the wallet and Spot tabs read the same
  // dataset.
  const {
    listings: spotListings,
    error: listingsError,
    reload: reloadListings,
  } = useSpotListings();
  const [tab, setTab] = useState<SpotTabKey>('all');
  const { favoriteSymbols, toggleFavorite } = useSpotFavorites();
  const [pickerSymbol, setPickerSymbol] = useState<string | null>(null);
  const [totalUsdValue, setTotalUsdValue] = useState(0);
  const [dailyChange, setDailyChange] = useState(0);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
  // Bumped on pull-to-refresh so the RWM widget re-fetches its tabs too.
  const [rwmRefresh, setRwmRefresh] = useState(0);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [authToken, setAuthToken] = useState<string | null>(null);
  const [tpBalance, setTpBalance] = useState(0);
  const [cashbackBalance, setCashbackBalance] = useState(0);

  // Pairs collapse to one row per token; the active tab decides the order and
  // pins MCGP to #1 where the spec calls for it.
  const tokens = useMemo(() => buildTokenMarkets(spotListings), [spotListings]);
  const visibleTokens = useMemo(
    () => tokensForTab(tokens, tab, favoriteSymbols),
    [tokens, tab, favoriteSymbols],
  );

  // ── Auth token ──
  useEffect(() => {
    (async () => {
      try {
        const token = await api.getStoredToken() || await AsyncStorage.getItem('authToken');
        if (token) { setAuthToken(token); api.setToken(token); }
        else setIsLoading(false);
      } catch { setIsLoading(false); }
    })();
  }, []);

  // ── Safety timeout ──
  useEffect(() => {
    const t = setTimeout(() => { if (isLoading) setIsLoading(false); }, 15000);
    return () => clearTimeout(t);
  }, [isLoading]);

  // ── Fetch data ──
  const fetchData = useCallback(async (refreshing = false) => {
    if (!authToken) { setIsLoading(false); return; }
    try {
      if (refreshing) setIsRefreshing(true); else setIsLoading(true);
      setError(null);

      // Fetch balances from active network
      const balResult = await getWalletBalances();
      // Flatten all chains into symbol -> usdValue
      const usdBySymbol: Record<string, number> = {};
      if (balResult.success && balResult.data) {
        for (const entry of normalizeWalletBalances(balResult.data)) {
          const usd = Number.isFinite(parseFloat(entry.usdValue)) ? parseFloat(entry.usdValue) : 0;
          usdBySymbol[entry.symbol] = (usdBySymbol[entry.symbol] || 0) + usd;
        }
      }

      // MCGP in the wallet is the spendable premium entitlement (Contract B),
      // not an ERC-20, so getWalletBalances reports 0. Overlay the premium
      // value so the total reflects what the user actually holds.
      try {
        const { balance: premium, usdValue } = await getPremiumMcgpAsset();
        if (premium > 0) usdBySymbol.MCGP = usdValue;
      } catch { /* MCGP swap unavailable — leave at 0 */ }

      setTotalUsdValue(BALANCE_SYMBOLS.reduce((s, sym) => s + (usdBySymbol[sym] || 0), 0));

      // Try portfolio API for dailyChange
      try {
        const portRes = await api.getPortfolioAssets();
        if (portRes.success && portRes.data?.totals?.dailyChange) {
          setDailyChange(portRes.data.totals.dailyChange);
        }
      } catch {}

      // Fetch TP and cashback balances
      try {
        const tpRes = await api.getTPBalance();
        if (tpRes.success && tpRes.data) {
          setTpBalance(tpRes.data.tpBalance || 0);
        }
      } catch {}
      try {
        const cbRes = await api.getCashbackBalance();
        if (cbRes.success && cbRes.data) {
          setCashbackBalance(cbRes.data.cashbackBalance || 0);
        }
      } catch {}

    } catch (err: any) {
      console.error('Dashboard fetch error:', err);
      setError(err.message || 'Failed to load dashboard');
    } finally {
      setIsLoading(false);
      setIsRefreshing(false);
    }
  }, [authToken, network]);

  useEffect(() => { if (authToken) fetchData(); }, [authToken, fetchData]);


  const onRefresh = useCallback(() => {
    setRwmRefresh((n) => n + 1);
    return fetchData(true);
  }, [fetchData]);

  // ── Skeleton loading ──
  if (isLoading && !isRefreshing) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg }}>
          <Skeleton height={180} radius="lg" />
          <View style={{ marginTop: spacing.lg }}>
            <Skeleton height={160} radius="lg" />
          </View>
          <View style={{ marginTop: spacing.lg }}>
            <SkeletonGroup shape="adCard" count={2} />
          </View>
        </ScrollView>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl refreshing={isRefreshing} onRefresh={onRefresh} tintColor={colors.accent} colors={[colors.accent]} />
        }
      >
        {/* Header with cart */}
        <View
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            justifyContent: 'space-between',
            marginBottom: spacing.lg,
          }}
        >
          <Text variant="title1" weight="700">
            TSA Connect
          </Text>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <CurrencySelector variant="header" />
            <CartIconBadge color={colors.accent} size={24} />
          </View>
        </View>

        <BackupBanner />

        {/* Error banner */}
        {error && (
          <PressableScale onPress={onRefresh} style={{ marginBottom: spacing.md }}>
            <AccentCard padding="md" railColor={colors.danger}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="warning" size={18} color={colors.danger} />
                <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                  {error}
                </Text>
                <Text variant="caption" color="danger" weight="700">
                  Tap to retry
                </Text>
              </View>
            </AccentCard>
          </PressableScale>
        )}

        <BalanceCard
          totalUsdValue={totalUsdValue}
          dailyChange={dailyChange}
          isValuesHidden={isValuesHidden}
          onToggleVisibility={() => setIsValuesHidden(v => !v)}
        />

        {/* TP & Cashback Summary */}
        {(tpBalance > 0 || cashbackBalance > 0) && (
          <PressableScale onPress={() => router.push('/cashback')} style={{ marginTop: spacing.md }}>
            <AccentCard padding="md">
              <View style={{ flexDirection: 'row', alignItems: 'stretch' }}>
                <View style={{ flex: 1, alignItems: 'center', gap: spacing.xs }}>
                  <Icon name="star" size={16} color={colors.accent} />
                  <Text variant="bodyMed" weight="700" numeric>
                    {tpBalance.toFixed(2)}
                  </Text>
                  <Text variant="caption" color="textMuted">
                    TP
                  </Text>
                </View>
                <Divider vertical spacing="md" />
                <View style={{ flex: 1, alignItems: 'center', gap: spacing.xs }}>
                  <Icon name="wallet" size={16} color={colors.success} />
                  <Text variant="bodyMed" weight="700" numeric>
                    {isValuesHidden ? '••••' : formatDualPrice(cashbackBalance).primary}
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    {isValuesHidden ? '••••' : formatDualPrice(cashbackBalance).secondary}
                  </Text>
                  <Text variant="caption" color="textMuted">
                    Cashback
                  </Text>
                </View>
              </View>
            </AccentCard>
          </PressableScale>
        )}

        <TokenMarketList
          title="Assets"
          tokens={visibleTokens}
          tab={tab}
          onTabChange={setTab}
          favoriteSymbols={favoriteSymbols}
          onToggleFavorite={toggleFavorite}
          onTokenPress={setPickerSymbol}
          limit={HOME_TOKEN_PREVIEW}
          onSeeAll={() => router.push('/easyswap?tab=spot')}
          error={listingsError ?? undefined}
          onRetry={reloadListings}
          style={{ marginTop: spacing.lg }}
        />

        <RWMWidget isValuesHidden={isValuesHidden} refreshSignal={rwmRefresh} />

        <TradeActions />

        <AssetPairSheet
          visible={pickerSymbol != null}
          onClose={() => setPickerSymbol(null)}
          symbol={pickerSymbol ?? ''}
          listings={pickerSymbol ? listingsForSymbol(spotListings, pickerSymbol) : []}
          error={listingsError ?? undefined}
          onRetry={reloadListings}
        />
      </ScrollView>
    </SafeAreaView>
  );
};

export default Dashboard;
