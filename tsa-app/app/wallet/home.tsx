import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  RefreshControl,
  ActivityIndicator,
} from 'react-native';
import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';
import {
  getWalletBalances,
  getTransactionHistory,
  WalletBalance,
  Transaction,
} from '../../services/walletApi';
import { useTokens } from '../../hooks/useTokens';

const WalletHome = () => {
  const { tokens, tokenList } = useTokens();

  const TOKEN_COLORS: Record<string, string> = Object.fromEntries(
    tokenList.map(t => [t.symbol, t.iconColor])
  );
  const TOKEN_ICONS: Record<string, string> = Object.fromEntries(
    tokenList.map(t => [t.symbol, t.symbol[0]])
  );

  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [refreshing, setRefreshing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [seedBackedUp, setSeedBackedUp] = useState(true);
  const [walletAddress, setWalletAddress] = useState('');
  const [error, setError] = useState('');

  const fetchData = useCallback(async () => {
    setError('');
    try {
      const address = await AsyncStorage.getItem('walletAddress');
      if (address) setWalletAddress(address);

      const backedUp = await AsyncStorage.getItem('seedPhraseBackedUp');
      setSeedBackedUp(backedUp === 'true');

      const [balanceResult, txResult] = await Promise.all([
        getWalletBalances(),
        getTransactionHistory(1, 5),
      ]);

      if (balanceResult.success && Array.isArray(balanceResult.data)) {
        setBalances(balanceResult.data);
      } else {
        // Show default empty balances
        setBalances(
          tokenList.map(t => ({
            symbol: t.symbol, name: t.name, balance: '0', usdValue: '0.00', contractAddress: '', decimals: t.decimals,
          }))
        );
      }

      if (txResult.success && txResult.data) {
        setTransactions(txResult.data.transactions || []);
      }
    } catch (err: any) {
      console.error('Fetch wallet data error:', err);
      setError('Failed to load wallet data');
      setBalances([
        { symbol: 'MCGP', name: 'MCG Protocol', balance: '0', usdValue: '0.00', contractAddress: '', decimals: 18 },
        { symbol: 'USDT', name: 'Tether USD', balance: '0', usdValue: '0.00', contractAddress: '', decimals: 6 },
        { symbol: 'USDC', name: 'USD Coin', balance: '0', usdValue: '0.00', contractAddress: '', decimals: 6 },
      ]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await fetchData();
    setRefreshing(false);
  }, [fetchData]);

  const totalUsdValue = balances.reduce(
    (sum, b) => sum + parseFloat(b.usdValue || '0'),
    0
  );

  const renderTokenCard = (token: WalletBalance) => (
    <View key={token.symbol} style={styles.tokenCard}>
      <View style={styles.tokenRow}>
        <View
          style={[
            styles.tokenIcon,
            { backgroundColor: TOKEN_COLORS[token.symbol] || COLORS.primary },
          ]}
        >
          <Text style={styles.tokenIconText}>
            {TOKEN_ICONS[token.symbol] || token.symbol[0]}
          </Text>
        </View>
        <View style={styles.tokenInfo}>
          <Text style={styles.tokenSymbol}>{token.symbol}</Text>
          <Text style={styles.tokenName}>{token.name}</Text>
        </View>
        <View style={styles.tokenBalanceContainer}>
          <Text style={styles.tokenBalance}>
            {parseFloat(token.balance).toLocaleString(undefined, {
              maximumFractionDigits: 6,
            })}
          </Text>
          <Text style={styles.tokenUsd}>${parseFloat(token.usdValue).toFixed(2)}</Text>
        </View>
      </View>
    </View>
  );

  const renderTransaction = (tx: Transaction) => {
    const isSend =
      tx.type === 'send' ||
      tx.fromAddress.toLowerCase() === walletAddress.toLowerCase();
    return (
      <View key={tx.id} style={styles.txRow}>
        <View
          style={[
            styles.txIcon,
            { backgroundColor: isSend ? '#FEE2E2' : '#D1FAE5' },
          ]}
        >
          <Text
            style={[
              styles.txIconText,
              { color: isSend ? COLORS.danger : COLORS.success },
            ]}
          >
            {isSend ? '-' : '+'}
          </Text>
        </View>
        <View style={styles.txInfo}>
          <Text style={styles.txType}>{isSend ? 'Sent' : 'Received'} {tx.tokenSymbol}</Text>
          <Text style={styles.txDate}>
            {new Date(tx.createdAt).toLocaleDateString()}
          </Text>
        </View>
        <Text
          style={[
            styles.txAmount,
            { color: isSend ? COLORS.danger : COLORS.success },
          ]}
        >
          {isSend ? '-' : '+'}{tx.amount} {tx.tokenSymbol}
        </Text>
      </View>
    );
  };

  if (loading) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color={COLORS.primary} />
        <Text style={styles.loadingText}>Loading wallet...</Text>
      </View>
    );
  }

  if (!walletAddress) {
    return (
      <View style={styles.noWalletContainer}>
        <Text style={styles.noWalletTitle}>No Wallet Found</Text>
        <Text style={styles.noWalletSubtitle}>
          Create a new wallet or import an existing one to get started.
        </Text>
        <TouchableOpacity
          style={styles.noWalletButton}
          onPress={() => router.push('/wallet/seedphrase')}
        >
          <Text style={styles.noWalletButtonText}>Set Up Wallet</Text>
        </TouchableOpacity>
      </View>
    );
  }

  return (
    <ScrollView
      style={styles.container}
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={COLORS.primary} />
      }
    >
      {/* Seed phrase backup warning */}
      {!seedBackedUp && (
        <TouchableOpacity
          style={styles.warningBanner}
          onPress={() => router.push('/wallet/seedphrase')}
        >
          <Text style={styles.warningText}>
            Back up your seed phrase to protect your funds
          </Text>
          <Text style={styles.warningArrow}>{'>'}</Text>
        </TouchableOpacity>
      )}

      {/* Total balance */}
      <View style={styles.balanceCard}>
        <Text style={styles.balanceLabel}>Total Balance</Text>
        <Text style={styles.balanceAmount}>${totalUsdValue.toFixed(2)}</Text>
        <Text style={styles.addressText} numberOfLines={1} ellipsizeMode="middle">
          {walletAddress}
        </Text>
      </View>

      {/* Action buttons */}
      <View style={styles.actionRow}>
        <TouchableOpacity
          style={styles.actionButton}
          onPress={() => router.push('/wallet/send')}
        >
          <View style={styles.actionIconContainer}>
            <Text style={styles.actionIconText}>{'<'}-</Text>
          </View>
          <Text style={styles.actionLabel}>Send</Text>
        </TouchableOpacity>
        <TouchableOpacity
          style={styles.actionButton}
          onPress={() => router.push('/wallet/receive')}
        >
          <View style={styles.actionIconContainer}>
            <Text style={styles.actionIconText}>-{'>'}</Text>
          </View>
          <Text style={styles.actionLabel}>Receive</Text>
        </TouchableOpacity>
      </View>

      {/* Error */}
      {error ? (
        <View style={styles.errorContainer}>
          <Text style={styles.errorText}>{error}</Text>
          <TouchableOpacity onPress={onRefresh}>
            <Text style={styles.retryText}>Tap to retry</Text>
          </TouchableOpacity>
        </View>
      ) : null}

      {/* Token balances */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Assets</Text>
        {balances.map(renderTokenCard)}
      </View>

      {/* Recent transactions */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Recent Transactions</Text>
        {transactions.length === 0 ? (
          <View style={styles.emptyState}>
            <Text style={styles.emptyText}>No transactions yet</Text>
            <Text style={styles.emptySubtext}>
              Your transaction history will appear here
            </Text>
          </View>
        ) : (
          transactions.map(renderTransaction)
        )}
      </View>

      <View style={{ height: 40 }} />
    </ScrollView>
  );
};

export default WalletHome;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: COLORS.white,
  },
  loadingText: {
    ...FONTS.body3,
    color: COLORS.gray,
    marginTop: 12,
  },
  warningBanner: {
    backgroundColor: '#FEF3C7',
    paddingHorizontal: 16,
    paddingVertical: 12,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  warningText: {
    ...FONTS.body4,
    color: '#92400E',
    flex: 1,
    fontWeight: '500',
  },
  warningArrow: {
    ...FONTS.body3,
    color: '#92400E',
    fontWeight: '700',
    marginLeft: 8,
  },
  balanceCard: {
    backgroundColor: COLORS.primary,
    margin: 16,
    borderRadius: 16,
    padding: 24,
    alignItems: 'center',
    ...SHADOWS.large,
  },
  balanceLabel: {
    ...FONTS.body4,
    color: 'rgba(255,255,255,0.8)',
    marginBottom: 4,
  },
  balanceAmount: {
    ...FONTS.h1,
    color: COLORS.white,
    fontWeight: '700',
    marginBottom: 8,
  },
  addressText: {
    ...FONTS.body5,
    color: 'rgba(255,255,255,0.6)',
    maxWidth: '80%',
  },
  actionRow: {
    flexDirection: 'row',
    justifyContent: 'center',
    gap: 24,
    marginBottom: 24,
    paddingHorizontal: 16,
  },
  actionButton: {
    alignItems: 'center',
    flex: 1,
  },
  actionIconContainer: {
    width: 48,
    height: 48,
    borderRadius: 24,
    backgroundColor: COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 8,
    ...SHADOWS.light,
  },
  actionIconText: {
    color: COLORS.white,
    fontWeight: '700',
    fontSize: 16,
  },
  actionLabel: {
    ...FONTS.body4,
    color: COLORS.dark,
    fontWeight: '500',
  },
  section: {
    paddingHorizontal: 16,
    marginBottom: 16,
  },
  sectionTitle: {
    ...FONTS.h4,
    color: COLORS.dark,
    fontWeight: '600',
    marginBottom: 12,
  },
  tokenCard: {
    backgroundColor: COLORS.white,
    borderRadius: 12,
    padding: 16,
    marginBottom: 8,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
  },
  tokenRow: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  tokenIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  tokenIconText: {
    color: COLORS.white,
    fontWeight: '700',
    fontSize: 16,
  },
  tokenInfo: {
    flex: 1,
  },
  tokenSymbol: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
  },
  tokenName: {
    ...FONTS.body5,
    color: COLORS.gray,
  },
  tokenBalanceContainer: {
    alignItems: 'flex-end',
  },
  tokenBalance: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
  },
  tokenUsd: {
    ...FONTS.body5,
    color: COLORS.gray,
  },
  txRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 12,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  txIcon: {
    width: 36,
    height: 36,
    borderRadius: 18,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  txIconText: {
    fontWeight: '700',
    fontSize: 18,
  },
  txInfo: {
    flex: 1,
  },
  txType: {
    ...FONTS.body4,
    fontWeight: '500',
    color: COLORS.dark,
  },
  txDate: {
    ...FONTS.body5,
    color: COLORS.gray,
  },
  txAmount: {
    ...FONTS.body4,
    fontWeight: '600',
  },
  emptyState: {
    alignItems: 'center',
    paddingVertical: 32,
  },
  emptyText: {
    ...FONTS.body3,
    color: COLORS.dark,
    fontWeight: '500',
    marginBottom: 4,
  },
  emptySubtext: {
    ...FONTS.body4,
    color: COLORS.gray,
  },
  errorContainer: {
    alignItems: 'center',
    paddingVertical: 12,
    marginHorizontal: 16,
    backgroundColor: '#FEE2E2',
    borderRadius: 8,
    marginBottom: 16,
  },
  errorText: {
    ...FONTS.body4,
    color: COLORS.danger,
  },
  retryText: {
    ...FONTS.body4,
    color: COLORS.primary,
    fontWeight: '600',
    marginTop: 4,
  },
  noWalletContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: 32,
    backgroundColor: COLORS.white,
  },
  noWalletTitle: {
    ...FONTS.h2,
    color: COLORS.dark,
    fontWeight: '600',
    marginBottom: 8,
  },
  noWalletSubtitle: {
    ...FONTS.body3,
    color: COLORS.gray,
    textAlign: 'center',
    lineHeight: 22,
    marginBottom: 32,
  },
  noWalletButton: {
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    paddingHorizontal: 48,
    borderRadius: 12,
    ...SHADOWS.medium,
  },
  noWalletButtonText: {
    ...FONTS.h4,
    color: COLORS.white,
    fontWeight: '600',
  },
});
