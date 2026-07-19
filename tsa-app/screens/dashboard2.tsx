// screens/dashboard2.tsx
//
// Home dashboard — total portfolio balance, quick actions, asset list, the
// Real World Marketplace (RWM) widget, and trade shortcuts. Built on the TSA
// Signature design system (@/theme tokens + @/components/ui primitives). The
// RWM widget replaced the Recent Activity transactions card; balance reads,
// the portfolio/TP/cashback fetches, the auth-token bootstrap, the value-hide
// toggle, navigation routes, and money formatting are preserved verbatim.
import React, { useState, useEffect, useCallback } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { api, Asset } from '@/components/services/api';
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
import { AssetList } from '@/components/dashboard/AssetList';
import { RWMWidget } from '@/components/dashboard/RWMWidget';
import { TradeActions } from '@/components/dashboard/TradeActions';
import { BackupBanner } from '@/components/dashboard/BackupBanner';
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

// The three transactable assets shown on the dashboard
const DEFAULT_ASSETS: Asset[] = [
  {
    _id: 'mcgp-001', id: 'mcgp-001', symbol: 'MCGP', name: 'MCG Protocol',
    balance: 0, usdValue: 0, isSelected: false, isHidden: false,
    details: { type: 'Gold-Backed Token', chain: 'Sonic' }, currentPrice: 0, priceChange24h: 0,
  },
  {
    _id: 'usdc-001', id: 'usdc-001', symbol: 'USDC', name: 'USD Coin',
    balance: 0, usdValue: 0, isSelected: false, isHidden: false,
    details: { type: 'Stablecoin', chain: 'Sonic' }, currentPrice: 1.0, priceChange24h: 0,
  },
  {
    _id: 'usdt-001', id: 'usdt-001', symbol: 'USDT', name: 'Tether USD',
    balance: 0, usdValue: 0, isSelected: false, isHidden: false,
    details: { type: 'Stablecoin', chain: 'Sonic' }, currentPrice: 1.0, priceChange24h: 0,
  },
];

const Dashboard: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { network } = useNetwork();
  const { formatDualPrice } = useCurrency();
  const [assets, setAssets] = useState<Asset[]>(DEFAULT_ASSETS);
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
      let updatedAssets = [...DEFAULT_ASSETS];
      if (balResult.success && balResult.data) {
        // Flatten all chains into symbol -> { balance, usdValue, usdPrice }
        const symbolData: Record<string, { balance: number; usdValue: number; usdPrice: number }> = {};
        for (const entry of normalizeWalletBalances(balResult.data)) {
          const bal = Number.isFinite(parseFloat(entry.balance)) ? parseFloat(entry.balance) : 0;
          const usd = Number.isFinite(parseFloat(entry.usdValue)) ? parseFloat(entry.usdValue) : 0;
          const price = Number.isFinite(entry.usdPrice || NaN) ? Number(entry.usdPrice) : 0;
          const prev = symbolData[entry.symbol];
          symbolData[entry.symbol] = {
            balance: (prev?.balance || 0) + bal,
            usdValue: (prev?.usdValue || 0) + usd,
            usdPrice: price || prev?.usdPrice || 0,
          };
        }
        updatedAssets = DEFAULT_ASSETS.map(a => {
          const d = symbolData[a.symbol];
          if (!d) return a;
          return { ...a, balance: d.balance, usdValue: d.usdValue, currentPrice: d.usdPrice };
        });
      }

      // MCGP in the wallet is the spendable premium entitlement (Contract B),
      // not an ERC-20, so getWalletBalances reports 0. Overlay the premium
      // balance so the dashboard shows what the user actually holds.
      try {
        const { balance: premium, usdValue } = await getPremiumMcgpAsset();
        if (premium > 0) {
          updatedAssets = updatedAssets.map(a =>
            a.symbol === 'MCGP'
              ? { ...a, balance: premium, usdValue, currentPrice: premium > 0 ? usdValue / premium : 0 }
              : a,
          );
        }
      } catch { /* MCGP swap unavailable — leave at 0 */ }

      // Select first asset with balance, or first
      const selected = updatedAssets.find(a => a.balance > 0) || updatedAssets[0];
      if (selected) selected.isSelected = true;
      setAssets(updatedAssets);
      setTotalUsdValue(updatedAssets.reduce((s, a) => s + (Number.isFinite(a.usdValue) ? a.usdValue : 0), 0));

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

        <AssetList assets={assets} isValuesHidden={isValuesHidden} />

        <RWMWidget isValuesHidden={isValuesHidden} refreshSignal={rwmRefresh} />

        <TradeActions />
      </ScrollView>
    </SafeAreaView>
  );
};

export default Dashboard;
