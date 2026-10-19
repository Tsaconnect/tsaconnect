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
import { api } from '@/components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { router, useNavigation } from 'expo-router';
import { DrawerActions } from '@react-navigation/native';
import { readPortfolioUsd } from '@/services/portfolioTotal';

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

/** Tokens shown on Home before "View all" hands off to the Spot Trade tab. */
const HOME_TOKEN_PREVIEW = 5;

const Dashboard: React.FC = () => {
  const { colors, spacing } = useTheme();
  const navigation = useNavigation();
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

      // Unknown keeps the last total on screen and says so, rather than
      // showing a holder $0.00.
      const total = await readPortfolioUsd();
      if (total === null) setError("We couldn't load your balance.");
      else setTotalUsdValue(total);

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
      <View style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg }}>
          <Skeleton height={180} radius="lg" />
          <View style={{ marginTop: spacing.lg }}>
            <Skeleton height={160} radius="lg" />
          </View>
          <View style={{ marginTop: spacing.lg }}>
            <SkeletonGroup shape="adCard" count={2} />
          </View>
        </ScrollView>
      </View>
    );
  }

  // No top safe-area inset: the drawer header above already clears the status bar.
  return (
    <View style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl refreshing={isRefreshing} onRefresh={onRefresh} tintColor={colors.accent} colors={[colors.accent]} />
        }
      >
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
          onMore={() => navigation.dispatch(DrawerActions.openDrawer())}
        />

        {/* TP & Cashback Summary */}
        {/* Each half taps through to where that balance is explained: TP to
            Trade & Earn (Trade Points), cashback to the cashback breakdown. */}
        {(tpBalance > 0 || cashbackBalance > 0) && (
          <AccentCard padding="md" style={{ marginTop: spacing.md }}>
            <View style={{ flexDirection: 'row', alignItems: 'stretch' }}>
              <PressableScale
                onPress={() => router.push('/trade')}
                accessibilityLabel="View Trade Points"
                style={{ flex: 1, alignItems: 'center', gap: spacing.xs }}
              >
                <Icon name="star" size={16} color={colors.accent} />
                <Text variant="bodyMed" weight="700" numeric>
                  {tpBalance.toFixed(2)}
                </Text>
                <Text variant="caption" color="textMuted">
                  TP
                </Text>
              </PressableScale>
              <Divider vertical spacing="md" />
              <PressableScale
                onPress={() => router.push('/cashback')}
                accessibilityLabel="View cashback"
                style={{ flex: 1, alignItems: 'center', gap: spacing.xs }}
              >
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
              </PressableScale>
            </View>
          </AccentCard>
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
    </View>
  );
};

export default Dashboard;
