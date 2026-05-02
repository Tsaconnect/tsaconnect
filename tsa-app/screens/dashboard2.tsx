// screens/dashboard2.tsx
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  Text,
  ScrollView,
  StyleSheet,
  RefreshControl,
  Pressable,
  Platform,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { Ionicons } from '@expo/vector-icons';
import { api, Asset } from '@/components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { router } from 'expo-router';
import CartIconBadge from '@/components/common/CartIconBadge';
import CurrencySelector from '@/components/currency/CurrencySelector';
import {
  getWalletBalances,
  getTransactionHistory,
  normalizeWalletBalances,
  type Transaction,
} from '../services/walletApi';

import { BalanceCard } from '@/components/dashboard/BalanceCard';
import { AssetList } from '@/components/dashboard/AssetList';
import { RecentActivity } from '@/components/dashboard/RecentActivity';
import { TradeActions } from '@/components/dashboard/TradeActions';
import { BackupBanner } from '@/components/dashboard/BackupBanner';
import { useNetwork } from '../hooks/useNetwork';

// The three transactable assets shown on the dashboard
const DEFAULT_ASSETS: Asset[] = [
  {
    _id: 'mcgp-001', id: 'mcgp-001', symbol: 'MCGP', name: 'Mason Capital Gold Point',
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
  const { network } = useNetwork();
  const [assets, setAssets] = useState<Asset[]>(DEFAULT_ASSETS);
  const [totalUsdValue, setTotalUsdValue] = useState(0);
  const [dailyChange, setDailyChange] = useState(0);
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
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

      // Fetch transactions
      try {
        const txRes = await getTransactionHistory(1, 5);
        if (txRes.success && txRes.data?.transactions) {
          setTransactions(txRes.data.transactions);
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

  const onRefresh = useCallback(() => fetchData(true), [fetchData]);

  // ── Skeleton loading ──
  if (isLoading && !isRefreshing) {
    return (
      <ScrollView style={styles.container} contentContainerStyle={styles.skeletonContent}>
        <View style={[styles.skeletonCard, { height: 180 }]} />
        <View style={[styles.skeletonCard, { height: 160, marginTop: 14 }]} />
        <View style={[styles.skeletonCard, { height: 120, marginTop: 14 }]} />
      </ScrollView>
    );
  }

  return (
    <ScrollView
      style={styles.container}
      contentContainerStyle={{ paddingBottom: 100 }}
      showsVerticalScrollIndicator={false}
      refreshControl={
        <RefreshControl refreshing={isRefreshing} onRefresh={onRefresh} tintColor="#D4AF37" colors={['#D4AF37']} />
      }
    >
      {/* Header with cart */}
      <View style={styles.dashHeader}>
        <Text style={styles.dashHeaderTitle}>TSA Connect</Text>
        <View style={styles.dashHeaderActions}>
          <CurrencySelector variant="header" />
          <CartIconBadge color="#D4AF37" size={24} />
        </View>
      </View>

      <BackupBanner />

      {/* Error banner */}
      {error && (
        <Pressable style={styles.errorBanner} onPress={onRefresh}>
          <Icon name="error-outline" size={16} color="#DC2626" />
          <Text style={styles.errorText}>{error}</Text>
          <Text style={styles.errorRetry}>Tap to retry</Text>
        </Pressable>
      )}

      <BalanceCard
        totalUsdValue={totalUsdValue}
        dailyChange={dailyChange}
        isValuesHidden={isValuesHidden}
        onToggleVisibility={() => setIsValuesHidden(v => !v)}
      />

      {/* TP & Cashback Summary */}
      {(tpBalance > 0 || cashbackBalance > 0) && (
        <Pressable
          style={styles.tpCashbackRow}
          onPress={() => router.push('/easyswap')}
        >
          <View style={styles.tpCashbackItem}>
            <Icon name="stars" size={16} color="#D4AF37" />
            <Text style={styles.tpCashbackValue}>{tpBalance.toFixed(2)}</Text>
            <Text style={styles.tpCashbackLabel}>TP</Text>
          </View>
          <View style={styles.tpCashbackDivider} />
          <View style={styles.tpCashbackItem}>
            <Icon name="payments" size={16} color="#4ADE80" />
            <Text style={styles.tpCashbackValue}>
              {isValuesHidden ? '••••' : `$${cashbackBalance.toFixed(2)}`}
            </Text>
            <Text style={styles.tpCashbackLabel}>Cashback</Text>
          </View>
        </Pressable>
      )}

      <AssetList assets={assets} isValuesHidden={isValuesHidden} />

      <RecentActivity transactions={transactions} isValuesHidden={isValuesHidden} />

      <TradeActions />

      <View style={{ height: 40 }} />
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  dashHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 16,
    paddingTop: Platform.OS === 'ios' ? 56 : 16,
    paddingBottom: 10,
    backgroundColor: '#FFF',
  },
  dashHeaderTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: '#1A1A1A',
  },
  dashHeaderActions: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  skeletonContent: { padding: 16 },
  skeletonCard: {
    backgroundColor: '#E5E5E5',
    borderRadius: 14,
    marginHorizontal: 16,
    opacity: 0.5,
  },
  // TP & Cashback Summary
  tpCashbackRow: {
    flexDirection: 'row',
    marginHorizontal: 16,
    marginTop: 12,
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 14,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 3,
    elevation: 1,
  },
  tpCashbackItem: {
    flex: 1,
    alignItems: 'center',
    gap: 4,
  },
  tpCashbackDivider: {
    width: 1,
    backgroundColor: '#F0F0F0',
  },
  tpCashbackValue: {
    fontSize: 16,
    fontWeight: '700',
    color: '#1A1A1A',
  },
  tpCashbackLabel: {
    fontSize: 11,
    color: '#888',
  },

  // Error
  errorBanner: {
    flexDirection: 'row', alignItems: 'center', gap: 8,
    backgroundColor: '#FEF2F2', borderRadius: 10, padding: 12,
    marginHorizontal: 16, marginTop: 8,
  },
  errorText: { flex: 1, fontSize: 13, color: '#DC2626' },
  errorRetry: { fontSize: 12, color: '#DC2626', fontWeight: '600' },
  // Modals
  modalOverlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.5)', justifyContent: 'flex-end' },
  modalContent: { backgroundColor: '#FFF', borderTopLeftRadius: 20, borderTopRightRadius: 20, padding: 20, paddingBottom: 34 },
  modalHeader: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 },
  modalTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },
  modalOption: { flexDirection: 'row', alignItems: 'center', gap: 12, padding: 14, backgroundColor: '#FAFAFA', borderRadius: 14, marginBottom: 10 },
  modalOptionIcon: { width: 48, height: 48, borderRadius: 14, justifyContent: 'center', alignItems: 'center' },
  modalOptionInfo: { flex: 1 },
  modalOptionTitle: { fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
  modalOptionDesc: { fontSize: 12, color: '#888', marginTop: 2 },
  modalCancel: { alignItems: 'center', paddingVertical: 14, marginTop: 4 },
  modalCancelText: { fontSize: 15, color: '#888', fontWeight: '500' },
});

export default Dashboard;
