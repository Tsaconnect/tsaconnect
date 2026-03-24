# Dashboard Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bloated dashboard with a clean layout: hero balance card with embedded quick actions, compact asset list, and recent activity feed.

**Architecture:** Extract the monolithic `screens/dashboard2.tsx` (~2000 lines) into 5 focused child components under `components/dashboard/`. The parent screen becomes a thin orchestrator that fetches data and passes props. No new API endpoints needed — reuses existing `api.getPortfolioAssets()` for assets/totals and `getTransactionHistory()` for recent activity.

**Tech Stack:** React Native, Expo Router, MaterialIcons, AsyncStorage, api.ts + walletApi.ts services

**Spec:** `docs/superpowers/specs/2026-03-24-dashboard-redesign-design.md`

---

### File Structure

```
components/dashboard/          ← NEW directory
  BalanceCard.tsx               ← hero card + quick actions (NEW)
  AssetList.tsx                 ← compact token rows (NEW)
  RecentActivity.tsx            ← transaction list + empty state (NEW)
  TradeActions.tsx              ← buy/order/trade cards (NEW, extracted)
  BackupBanner.tsx              ← seed phrase reminder (NEW, extracted)
screens/dashboard2.tsx          ← REWRITE: thin orchestrator
```

---

### Task 1: Create BalanceCard component

**Files:**
- Create: `components/dashboard/BalanceCard.tsx`

- [ ] **Step 1: Create BalanceCard component**

```tsx
// components/dashboard/BalanceCard.tsx
import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { router } from 'expo-router';

interface BalanceCardProps {
  totalUsdValue: number;
  dailyChange: number;
  isValuesHidden: boolean;
  onToggleVisibility: () => void;
  onDepositPress: () => void;
  onSendPress: () => void;
}

const QUICK_ACTIONS = [
  { key: 'deposit', label: 'Deposit', icon: 'arrow-downward' },
  { key: 'send', label: 'Send', icon: 'arrow-upward' },
  { key: 'swap', label: 'Swap', icon: 'swap-horiz', route: '/swap' },
  { key: 'buy', label: 'Buy', icon: 'shopping-cart', route: '/(dashboard)/(tabs)/marketplace' },
] as const;

export const BalanceCard: React.FC<BalanceCardProps> = ({
  totalUsdValue,
  dailyChange,
  isValuesHidden,
  onToggleVisibility,
  onDepositPress,
  onSendPress,
}) => {
  const handleAction = (key: string) => {
    if (key === 'deposit') return onDepositPress();
    if (key === 'send') return onSendPress();
    const action = QUICK_ACTIONS.find(a => a.key === key);
    if (action && 'route' in action) router.push(action.route as any);
  };

  const changeColor = dailyChange >= 0 ? '#4ADE80' : '#F87171';
  const changePrefix = dailyChange >= 0 ? '+' : '';

  return (
    <View style={styles.card}>
      <View style={styles.header}>
        <Text style={styles.label}>Total Portfolio</Text>
        <Pressable onPress={onToggleVisibility} hitSlop={8}>
          <Icon
            name={isValuesHidden ? 'visibility-off' : 'visibility'}
            size={20}
            color="rgba(255,255,255,0.4)"
          />
        </Pressable>
      </View>

      <Text style={styles.balance}>
        {isValuesHidden
          ? '••••••'
          : `$${totalUsdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
      </Text>

      {dailyChange !== 0 && (
        <View style={styles.changeRow}>
          <Icon
            name={dailyChange >= 0 ? 'trending-up' : 'trending-down'}
            size={14}
            color={changeColor}
          />
          <Text style={[styles.changeText, { color: changeColor }]}>
            {changePrefix}${Math.abs(dailyChange).toFixed(2)} today
          </Text>
        </View>
      )}

      <View style={styles.actionsRow}>
        {QUICK_ACTIONS.map((action) => (
          <Pressable
            key={action.key}
            style={({ pressed }) => [styles.actionButton, pressed && styles.actionPressed]}
            onPress={() => handleAction(action.key)}
          >
            <View style={styles.actionIcon}>
              <Icon name={action.icon} size={20} color="#D4AF37" />
            </View>
            <Text style={styles.actionLabel}>{action.label}</Text>
          </Pressable>
        ))}
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  card: {
    marginHorizontal: 16,
    marginTop: 8,
    borderRadius: 16,
    padding: 20,
    backgroundColor: '#1A1A1A',
    // Using a dark solid color — LinearGradient would need expo-linear-gradient
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  label: { fontSize: 13, color: 'rgba(255,255,255,0.6)' },
  balance: { fontSize: 28, fontWeight: '800', color: '#FFFFFF', marginBottom: 4 },
  changeRow: { flexDirection: 'row', alignItems: 'center', gap: 4, marginBottom: 4 },
  changeText: { fontSize: 12, fontWeight: '500' },
  actionsRow: {
    flexDirection: 'row',
    justifyContent: 'space-around',
    marginTop: 18,
    paddingTop: 14,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255,255,255,0.1)',
  },
  actionButton: { alignItems: 'center', minWidth: 56 },
  actionPressed: { opacity: 0.7 },
  actionIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: 'rgba(212,175,55,0.15)',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 6,
  },
  actionLabel: { fontSize: 11, color: 'rgba(255,255,255,0.7)', fontWeight: '500' },
});

export default BalanceCard;
```

- [ ] **Step 2: Verify no TypeScript errors**

Run: `cd /Users/ojay/Projects/tsaconnect/tsa-dev/tsa-app && npx tsc --noEmit --pretty 2>&1 | grep "BalanceCard" | head -5`
Expected: No errors for BalanceCard.tsx

- [ ] **Step 3: Commit**

```bash
git add components/dashboard/BalanceCard.tsx
git commit -m "feat(dashboard): add BalanceCard component with embedded quick actions"
```

---

### Task 2: Create AssetList component

**Files:**
- Create: `components/dashboard/AssetList.tsx`

- [ ] **Step 1: Create AssetList component**

```tsx
// components/dashboard/AssetList.tsx
import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import { Asset } from '@/components/services/api';

const ASSET_COLORS: Record<string, string[]> = {
  MCGP: ['#D4AF37', '#B8941F'],
  USDC: ['#2775CA', '#1A5BA8'],
  USDT: ['#26A17B', '#1A8A66'],
};

interface AssetListProps {
  assets: Asset[];
  isValuesHidden: boolean;
}

export const AssetList: React.FC<AssetListProps> = ({ assets, isValuesHidden }) => {
  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Assets</Text>
      </View>

      {assets.map((asset, index) => {
        const colors = ASSET_COLORS[asset.symbol] || ['#666', '#444'];
        const isLast = index === assets.length - 1;
        return (
          <View
            key={asset._id || asset.id || asset.symbol}
            style={[styles.row, !isLast && styles.rowBorder]}
          >
            <View style={[styles.avatar, { backgroundColor: colors[0] }]}>
              <Text style={styles.avatarText}>{asset.symbol.charAt(0)}</Text>
            </View>
            <View style={styles.info}>
              <Text style={styles.symbol}>{asset.symbol}</Text>
              <Text style={styles.name}>{asset.name}</Text>
            </View>
            <View style={styles.balanceCol}>
              <Text style={styles.balanceText}>
                {isValuesHidden ? '••••' : asset.balance.toFixed(2)}
              </Text>
              <Text style={styles.usdText}>
                {isValuesHidden ? '••••' : `$${asset.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
              </Text>
            </View>
          </View>
        );
      })}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    marginHorizontal: 16,
    marginTop: 14,
    backgroundColor: '#FFF',
    borderRadius: 14,
    overflow: 'hidden',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  headerTitle: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    gap: 12,
  },
  rowBorder: { borderBottomWidth: 1, borderBottomColor: '#F5F5F5' },
  avatar: {
    width: 36,
    height: 36,
    borderRadius: 18,
    justifyContent: 'center',
    alignItems: 'center',
  },
  avatarText: { color: '#FFF', fontSize: 14, fontWeight: '700' },
  info: { flex: 1 },
  symbol: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  name: { fontSize: 11, color: '#888', marginTop: 1 },
  balanceCol: { alignItems: 'flex-end' },
  balanceText: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  usdText: { fontSize: 11, color: '#888', marginTop: 1 },
});

export default AssetList;
```

- [ ] **Step 2: Commit**

```bash
git add components/dashboard/AssetList.tsx
git commit -m "feat(dashboard): add AssetList component with compact token rows"
```

---

### Task 3: Create RecentActivity component

**Files:**
- Create: `components/dashboard/RecentActivity.tsx`

- [ ] **Step 1: Create RecentActivity component**

```tsx
// components/dashboard/RecentActivity.tsx
import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { Transaction } from '@/services/walletApi';

interface RecentActivityProps {
  transactions: Transaction[];
  isValuesHidden: boolean;
}

const TX_ICONS: Record<string, { icon: string; color: string }> = {
  send: { icon: 'arrow-upward', color: '#F87171' },
  receive: { icon: 'arrow-downward', color: '#4ADE80' },
  swap: { icon: 'swap-horiz', color: '#60A5FA' },
};

function truncateAddress(addr: string): string {
  if (!addr || addr.length < 10) return addr || '';
  return `${addr.slice(0, 6)}...${addr.slice(-4)}`;
}

function deriveDescription(tx: Transaction): string {
  const type = tx.type?.toLowerCase() || 'transfer';
  if (type.includes('send') || type.includes('transfer')) {
    return `Sent ${tx.amount} ${tx.tokenSymbol} to ${truncateAddress(tx.toAddress)}`;
  }
  if (type.includes('receive')) {
    return `Received ${tx.amount} ${tx.tokenSymbol} from ${truncateAddress(tx.fromAddress)}`;
  }
  if (type.includes('swap')) {
    return `Swapped ${tx.amount} ${tx.tokenSymbol}`;
  }
  return `${tx.type} ${tx.amount} ${tx.tokenSymbol}`;
}

function formatTime(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMin = Math.floor(diffMs / 60000);
  if (diffMin < 1) return 'Just now';
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  const diffDay = Math.floor(diffHr / 24);
  if (diffDay < 7) return `${diffDay}d ago`;
  return date.toLocaleDateString();
}

export const RecentActivity: React.FC<RecentActivityProps> = ({ transactions, isValuesHidden }) => {
  const txType = (tx: Transaction) => {
    const t = tx.type?.toLowerCase() || '';
    if (t.includes('send') || t.includes('transfer')) return 'send';
    if (t.includes('receive')) return 'receive';
    if (t.includes('swap')) return 'swap';
    return 'send';
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Recent Activity</Text>
      </View>

      {transactions.length === 0 ? (
        <View style={styles.empty}>
          <Icon name="receipt-long" size={32} color="#DDD" />
          <Text style={styles.emptyText}>No recent transactions</Text>
        </View>
      ) : (
        transactions.map((tx, index) => {
          const typeInfo = TX_ICONS[txType(tx)] || TX_ICONS.send;
          const isLast = index === transactions.length - 1;
          return (
            <View key={tx.id || tx.txHash || index} style={[styles.row, !isLast && styles.rowBorder]}>
              <View style={[styles.txIcon, { backgroundColor: `${typeInfo.color}15` }]}>
                <Icon name={typeInfo.icon} size={18} color={typeInfo.color} />
              </View>
              <View style={styles.txInfo}>
                <Text style={styles.txDesc} numberOfLines={1}>
                  {isValuesHidden ? `${tx.type || 'Transaction'} ••••` : deriveDescription(tx)}
                </Text>
                <Text style={styles.txTime}>{formatTime(tx.createdAt)}</Text>
              </View>
              <View style={styles.txStatus}>
                <Text style={[
                  styles.statusText,
                  tx.status === 'confirmed' && styles.statusConfirmed,
                  tx.status === 'pending' && styles.statusPending,
                  tx.status === 'failed' && styles.statusFailed,
                ]}>
                  {tx.status}
                </Text>
              </View>
            </View>
          );
        })
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    marginHorizontal: 16,
    marginTop: 14,
    backgroundColor: '#FFF',
    borderRadius: 14,
    overflow: 'hidden',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  headerTitle: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  empty: { alignItems: 'center', paddingVertical: 28 },
  emptyText: { fontSize: 13, color: '#BBB', marginTop: 8 },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    gap: 12,
  },
  rowBorder: { borderBottomWidth: 1, borderBottomColor: '#F5F5F5' },
  txIcon: {
    width: 36,
    height: 36,
    borderRadius: 18,
    justifyContent: 'center',
    alignItems: 'center',
  },
  txInfo: { flex: 1 },
  txDesc: { fontSize: 13, fontWeight: '500', color: '#1A1A1A' },
  txTime: { fontSize: 11, color: '#AAA', marginTop: 2 },
  txStatus: {},
  statusText: { fontSize: 11, fontWeight: '500', color: '#888', textTransform: 'capitalize' },
  statusConfirmed: { color: '#16A34A' },
  statusPending: { color: '#D97706' },
  statusFailed: { color: '#DC2626' },
});

export default RecentActivity;
```

- [ ] **Step 2: Commit**

```bash
git add components/dashboard/RecentActivity.tsx
git commit -m "feat(dashboard): add RecentActivity component with transaction history"
```

---

### Task 4: Extract TradeActions component

**Files:**
- Create: `components/dashboard/TradeActions.tsx`
- Reference: `screens/dashboard2.tsx` lines ~1100-1200 (Trade Now section)

- [ ] **Step 1: Create TradeActions component**

Extract the existing "Trade Now" section (Buy Product, Order Services, Trade and Earn) from dashboard2.tsx into its own component.

```tsx
// components/dashboard/TradeActions.tsx
import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { router } from 'expo-router';

const TRADE_ITEMS = [
  {
    key: 'product',
    title: 'Buy Product',
    subtitle: 'Browse marketplace',
    icon: 'shopping-bag',
    color: '#D4AF37',
    bg: 'rgba(212,175,55,0.1)',
    route: '/products',
  },
  {
    key: 'services',
    title: 'Order Services',
    subtitle: 'Find service providers',
    icon: 'miscellaneous-services',
    color: '#3B82F6',
    bg: 'rgba(59,130,246,0.1)',
    route: '/serviceshome',
  },
  {
    key: 'trade',
    title: 'Trade & Earn',
    subtitle: 'Swap and earn rewards',
    icon: 'trending-up',
    color: '#16A34A',
    bg: 'rgba(22,163,74,0.1)',
    route: '/trade',
  },
] as const;

export const TradeActions: React.FC = () => {
  return (
    <View style={styles.container}>
      <Text style={styles.sectionTitle}>Trade Now</Text>
      {TRADE_ITEMS.map((item) => (
        <Pressable
          key={item.key}
          style={({ pressed }) => [styles.card, pressed && styles.cardPressed]}
          onPress={() => router.push(item.route as any)}
        >
          <View style={[styles.iconWrap, { backgroundColor: item.bg }]}>
            <Icon name={item.icon} size={24} color={item.color} />
          </View>
          <View style={styles.cardInfo}>
            <Text style={styles.cardTitle}>{item.title}</Text>
            <Text style={styles.cardSubtitle}>{item.subtitle}</Text>
          </View>
          <Icon name="chevron-right" size={22} color="#CCC" />
        </Pressable>
      ))}
    </View>
  );
};

const styles = StyleSheet.create({
  container: { marginHorizontal: 16, marginTop: 20 },
  sectionTitle: { fontSize: 16, fontWeight: '700', color: '#1A1A1A', marginBottom: 10 },
  card: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 14,
    marginBottom: 8,
    gap: 12,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  cardPressed: { opacity: 0.7 },
  iconWrap: { width: 44, height: 44, borderRadius: 12, justifyContent: 'center', alignItems: 'center' },
  cardInfo: { flex: 1 },
  cardTitle: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  cardSubtitle: { fontSize: 12, color: '#888', marginTop: 2 },
});

export default TradeActions;
```

- [ ] **Step 2: Commit**

```bash
git add components/dashboard/TradeActions.tsx
git commit -m "feat(dashboard): extract TradeActions component"
```

---

### Task 5: Extract BackupBanner component

**Files:**
- Create: `components/dashboard/BackupBanner.tsx`
- Reference: `screens/dashboard2.tsx` lines 305-333, 987-1017

- [ ] **Step 1: Create BackupBanner component**

```tsx
// components/dashboard/BackupBanner.tsx
import React, { useState, useEffect } from 'react';
import { View, Text, Pressable, Alert, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';

export const BackupBanner: React.FC = () => {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const backedUp = await AsyncStorage.getItem('seedPhraseBackedUp');
        if (backedUp === 'true') return;
        const dismissed = await AsyncStorage.getItem('backupBannerDismissed');
        if (dismissed === 'true') return;
        const remindAt = await AsyncStorage.getItem('backupBannerRemindAt');
        if (remindAt && Date.now() < parseInt(remindAt, 10)) return;
        setVisible(true);
      } catch {}
    })();
  }, []);

  if (!visible) return null;

  const handleRemindTomorrow = async () => {
    setVisible(false);
    await AsyncStorage.setItem('backupBannerRemindAt', (Date.now() + 86400000).toString());
  };

  const handleNever = async () => {
    setVisible(false);
    await AsyncStorage.setItem('backupBannerDismissed', 'true');
  };

  return (
    <View style={styles.banner}>
      <Pressable style={styles.content} onPress={() => router.push('/wallet/seedphrase')}>
        <Icon name="shield" size={20} color="#92400E" />
        <Text style={styles.text}>Back up your seed phrase to protect your funds</Text>
      </Pressable>
      <Pressable
        onPress={() =>
          Alert.alert('Dismiss Reminder', 'When would you like to be reminded?', [
            { text: 'Remind Tomorrow', onPress: handleRemindTomorrow },
            { text: 'Never', style: 'destructive', onPress: handleNever },
            { text: 'Cancel', style: 'cancel' },
          ])
        }
        style={styles.close}
        hitSlop={8}
      >
        <Icon name="close" size={18} color="#92400E" />
      </Pressable>
    </View>
  );
};

const styles = StyleSheet.create({
  banner: {
    flexDirection: 'row',
    alignItems: 'center',
    marginHorizontal: 16,
    marginTop: 8,
    padding: 12,
    backgroundColor: '#FFFBEB',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: '#FDE68A',
  },
  content: { flex: 1, flexDirection: 'row', alignItems: 'center', gap: 8 },
  text: { flex: 1, fontSize: 13, color: '#92400E', fontWeight: '500' },
  close: { marginLeft: 8 },
});

export default BackupBanner;
```

- [ ] **Step 2: Commit**

```bash
git add components/dashboard/BackupBanner.tsx
git commit -m "feat(dashboard): extract BackupBanner component with snooze/dismiss logic"
```

---

### Task 6: Rewrite dashboard2.tsx as thin orchestrator

**Files:**
- Modify: `screens/dashboard2.tsx` (complete rewrite — replace all ~2000 lines)

This is the main task. The new version will be ~300 lines — it fetches data and composes child components.

- [ ] **Step 1: Rewrite dashboard2.tsx with the following code**

```tsx
// screens/dashboard2.tsx
import { router } from 'expo-router';
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  Text,
  ScrollView,
  StyleSheet,
  RefreshControl,
  Modal,
  Pressable,
  Alert,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { api, Asset } from '@/components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getWalletBalances, getTransactionHistory, Transaction } from '../services/walletApi';

import { BalanceCard } from '@/components/dashboard/BalanceCard';
import { AssetList } from '@/components/dashboard/AssetList';
import { RecentActivity } from '@/components/dashboard/RecentActivity';
import { TradeActions } from '@/components/dashboard/TradeActions';
import { BackupBanner } from '@/components/dashboard/BackupBanner';

// Default assets — updated with real balances at runtime
const DEFAULT_ASSETS: Asset[] = [
  {
    _id: 'mcgp-001', id: 'mcgp-001', symbol: 'MCGP', name: 'Mason Capital Gold Point',
    balance: 0, usdValue: 0, isSelected: false, isHidden: false,
    details: { type: 'Stablecoin', chain: 'Polygon' }, currentPrice: 1.0, priceChange24h: 0,
  },
  {
    _id: 'usdc-001', id: 'usdc-001', symbol: 'USDC', name: 'USD Coin',
    balance: 0, usdValue: 0, isSelected: false, isHidden: false,
    details: { type: 'Stablecoin', chain: 'Polygon' }, currentPrice: 1.0, priceChange24h: 0.01,
  },
  {
    _id: 'usdt-001', id: 'usdt-001', symbol: 'USDT', name: 'Tether USD',
    balance: 0, usdValue: 0, isSelected: false, isHidden: false,
    details: { type: 'Stablecoin', chain: 'Polygon' }, currentPrice: 1.0, priceChange24h: -0.02,
  },
];

const Dashboard: React.FC = () => {
  const [assets, setAssets] = useState<Asset[]>(DEFAULT_ASSETS);
  const [totalUsdValue, setTotalUsdValue] = useState(0);
  const [dailyChange, setDailyChange] = useState(0);
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [authToken, setAuthToken] = useState<string | null>(null);
  const [showFundModal, setShowFundModal] = useState(false);
  const [showSendModal, setShowSendModal] = useState(false);
  const [selectedAsset, setSelectedAsset] = useState<Asset | null>(null);

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

      // Fetch balances
      const balResult = await getWalletBalances();
      let updatedAssets = [...DEFAULT_ASSETS];
      if (balResult.success && balResult.data) {
        const bals = balResult.data.balances || balResult.data;
        updatedAssets = DEFAULT_ASSETS.map(a => {
          const bal = parseFloat(bals[a.symbol] || '0');
          return { ...a, balance: bal, usdValue: bal * (a.currentPrice || 1) };
        });
      }
      // Select first asset with balance, or first
      const selected = updatedAssets.find(a => a.balance > 0) || updatedAssets[0];
      if (selected) selected.isSelected = true;
      setAssets(updatedAssets);
      setSelectedAsset(selected || null);
      setTotalUsdValue(updatedAssets.reduce((s, a) => s + a.usdValue, 0));

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

    } catch (err: any) {
      console.error('Dashboard fetch error:', err);
      setError(err.message || 'Failed to load dashboard');
    } finally {
      setIsLoading(false);
      setIsRefreshing(false);
    }
  }, [authToken]);

  useEffect(() => { if (authToken) fetchData(); }, [authToken, fetchData]);

  const onRefresh = useCallback(() => fetchData(true), [fetchData]);

  // ── Fund / Send modal handlers (preserved from current) ──
  const handleFundOption = (option: 'fiat' | 'crypto') => {
    setShowFundModal(false);
    router.push(option === 'fiat' ? '/fundfiat' : '/fund');
  };

  const handleSendOption = (option: 'fiat' | 'crypto') => {
    setShowSendModal(false);
    if (option === 'fiat') { router.push('/sendfiat'); return; }
    if (selectedAsset) {
      router.push({
        pathname: '/send',
        params: { assetSymbol: selectedAsset.symbol, assetId: selectedAsset._id, balance: selectedAsset.balance.toString() },
      });
    } else {
      Alert.alert('Select Asset', 'Please select an asset first to send');
    }
  };

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
        onDepositPress={() => setShowFundModal(true)}
        onSendPress={() => setShowSendModal(true)}
      />

      <AssetList assets={assets} isValuesHidden={isValuesHidden} />

      <RecentActivity transactions={transactions} isValuesHidden={isValuesHidden} />

      <TradeActions />

      <View style={{ height: 40 }} />

      {/* Fund Modal */}
      <Modal animationType="slide" transparent visible={showFundModal} onRequestClose={() => setShowFundModal(false)}>
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>Select Deposit Method</Text>
              <Pressable onPress={() => setShowFundModal(false)}><Icon name="close" size={24} color="#666" /></Pressable>
            </View>
            <Pressable style={({ pressed }) => [styles.modalOption, pressed && { opacity: 0.7 }]} onPress={() => handleFundOption('fiat')}>
              <View style={[styles.modalOptionIcon, { backgroundColor: '#E3F2FD' }]}><Icon name="account-balance" size={28} color="#1976D2" /></View>
              <View style={styles.modalOptionInfo}><Text style={styles.modalOptionTitle}>Fiat Deposit</Text><Text style={styles.modalOptionDesc}>Bank transfer or card</Text></View>
              <Icon name="chevron-right" size={22} color="#CCC" />
            </Pressable>
            <Pressable style={({ pressed }) => [styles.modalOption, pressed && { opacity: 0.7 }]} onPress={() => handleFundOption('crypto')}>
              <View style={[styles.modalOptionIcon, { backgroundColor: '#E8F5E9' }]}><Icon name="currency-bitcoin" size={28} color="#2E7D32" /></View>
              <View style={styles.modalOptionInfo}><Text style={styles.modalOptionTitle}>Crypto Deposit</Text><Text style={styles.modalOptionDesc}>From external wallet</Text></View>
              <Icon name="chevron-right" size={22} color="#CCC" />
            </Pressable>
            <Pressable style={styles.modalCancel} onPress={() => setShowFundModal(false)}><Text style={styles.modalCancelText}>Cancel</Text></Pressable>
          </View>
        </View>
      </Modal>

      {/* Send Modal */}
      <Modal animationType="slide" transparent visible={showSendModal} onRequestClose={() => setShowSendModal(false)}>
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>Select Withdrawal Method</Text>
              <Pressable onPress={() => setShowSendModal(false)}><Icon name="close" size={24} color="#666" /></Pressable>
            </View>
            <Pressable style={({ pressed }) => [styles.modalOption, pressed && { opacity: 0.7 }]} onPress={() => handleSendOption('fiat')}>
              <View style={[styles.modalOptionIcon, { backgroundColor: '#FFF3E0' }]}><Icon name="attach-money" size={28} color="#F57C00" /></View>
              <View style={styles.modalOptionInfo}><Text style={styles.modalOptionTitle}>Withdraw as Fiat</Text><Text style={styles.modalOptionDesc}>To bank account or card</Text></View>
              <Icon name="chevron-right" size={22} color="#CCC" />
            </Pressable>
            <Pressable style={({ pressed }) => [styles.modalOption, pressed && { opacity: 0.7 }]} onPress={() => handleSendOption('crypto')}>
              <View style={[styles.modalOptionIcon, { backgroundColor: '#F3E5F5' }]}><Icon name="account-balance-wallet" size={28} color="#7B1FA2" /></View>
              <View style={styles.modalOptionInfo}><Text style={styles.modalOptionTitle}>Withdraw as Crypto</Text><Text style={styles.modalOptionDesc}>To external wallet</Text></View>
              <Icon name="chevron-right" size={22} color="#CCC" />
            </Pressable>
            <Pressable style={styles.modalCancel} onPress={() => setShowSendModal(false)}><Text style={styles.modalCancelText}>Cancel</Text></Pressable>
          </View>
        </View>
      </Modal>
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  skeletonContent: { padding: 16 },
  skeletonCard: {
    backgroundColor: '#E5E5E5',
    borderRadius: 14,
    marginHorizontal: 16,
    opacity: 0.5,
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
```

- [ ] **Step 2: Verify the app loads without crashes**

Run: `cd /Users/ojay/Projects/tsaconnect/tsa-dev/tsa-app && npx expo start` and check the dashboard tab loads.

- [ ] **Step 3: Commit**

```bash
git add screens/dashboard2.tsx
git commit -m "feat(dashboard): rewrite as thin orchestrator composing child components"
```

---

### Task 7: Clean up and verify

- [ ] **Step 1: Check for TypeScript errors**

Run: `cd /Users/ojay/Projects/tsaconnect/tsa-dev/tsa-app && npx tsc --noEmit 2>&1 | head -30`

Fix any errors found.

- [ ] **Step 2: Test all dashboard interactions**

Verify manually:
- Balance card shows total portfolio value
- Eye icon toggles value visibility across all sections
- Quick actions (Deposit, Send, Swap, Buy) navigate correctly
- Fund modal opens and both options work
- Send modal opens and both options work
- Asset list shows all 3 tokens with correct colors
- Recent activity shows empty state or transactions
- Trade Now cards navigate correctly
- Pull-to-refresh reloads data
- Backup banner appears/dismisses correctly

- [ ] **Step 3: Final commit**

```bash
git add -A
git commit -m "feat(dashboard): complete redesign with hero balance, compact assets, recent activity"
```
