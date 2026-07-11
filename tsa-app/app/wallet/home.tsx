// app/wallet/home.tsx
//
// Wallet home — multi-chain token balances + send / swap / receive entry, recent
// transactions, wallet switcher, and the no-wallet onboarding state. Restyled onto
// the TSA Signature design system (theme tokens + @/components/ui primitives).
//
// FINANCIAL screen: every balance fetch, chain/token selection, address/amount
// rendering, and send/receive/swap navigation is preserved verbatim — this is a
// visual/structural restyle only. The legacy ChainSelector + ImportTokenModal
// wallet components are reused unchanged.
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  TouchableOpacity,
  ScrollView,
  RefreshControl,
  Alert,
  Image,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import {
  getWalletBalances,
  getTransactionHistory,
  normalizeWalletBalances,
  registerWalletAddress,
  type Transaction,
  type WalletBalanceWithChain,
} from '../../services/walletApi';
import {
  getWalletList,
  getActiveWallet,
  setActiveWallet as setActiveWalletService,
  migrateFromSingleWallet,
  WalletMeta,
} from '../../services/wallet';
import { useTokens } from '../../hooks/useTokens';
import { CHAINS, CHAIN_KEYS, getChainDisplay, tokenRankOf, type ChainKey } from '../../constants/chains';
import { loadTronWallet } from '../../services/chains/tronWallet';
import { loadBtcWallet } from '../../services/chains/btcWallet';
import ChainSelector from '../../components/wallet/ChainSelector';
import ImportTokenModal from '../../components/wallet/ImportTokenModal';
import { useNetwork } from '../../hooks/useNetwork';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Avatar,
  Button,
  Card,
  EmptyState,
  Icon,
  PressableScale,
  Sheet,
  Spinner,
  Text,
} from '@/components/ui';

const WalletHome = () => {
  const { colors, spacing, radius } = useTheme();
  const { tokenList, removeToken } = useTokens();

  const TOKEN_COLORS: Record<string, string> = Object.fromEntries(
    tokenList.map(t => [t.symbol, t.iconColor])
  );
  const TOKEN_ICONS: Record<string, string> = Object.fromEntries(
    tokenList.map(t => [t.symbol, t.symbol[0]])
  );
  // Registry tokens carry their own logo; native coins (BTC, TRX, ETH, S, …)
  // fall back to their chain's logo. Anything else keeps the letter circle.
  const TOKEN_URLS: Record<string, string | undefined> = Object.fromEntries(
    tokenList.map(t => [t.symbol, t.iconUrl])
  );
  const tokenLogoUrl = (token: WalletBalanceWithChain): string | undefined => {
    const chain = getChainDisplay(token.chainKey);
    if (TOKEN_URLS[token.symbol]) return TOKEN_URLS[token.symbol];
    return chain?.nativeCurrency?.symbol === token.symbol ? chain?.iconUrl : undefined;
  };

  const [balances, setBalances] = useState<WalletBalanceWithChain[]>([]);
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [refreshing, setRefreshing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [seedBackedUp, setSeedBackedUp] = useState(true);
  const [walletAddress, setWalletAddress] = useState('');
  const [error, setError] = useState('');
  const [walletList, setWalletList] = useState<WalletMeta[]>([]);
  const [activeLabel, setActiveLabel] = useState('');
  const [selectorVisible, setSelectorVisible] = useState(false);
  const [chainFilter, setChainFilter] = useState<ChainKey | 'all'>('all');
  const [importModalVisible, setImportModalVisible] = useState(false);
  const { network } = useNetwork();

  const fetchData = useCallback(async () => {
    setError('');
    try {
      await migrateFromSingleWallet();
      const list = await getWalletList();
      setWalletList(list);
      const activeAddr = await getActiveWallet();
      if (activeAddr) {
        const activeMeta = list.find(w => w.address === activeAddr);
        setActiveLabel(activeMeta?.label || 'Wallet');
      }

      const address = activeAddr || await getActiveWallet();
      if (address) setWalletAddress(address);

      const backedUp = list.find(w => w.address === address)?.backedUp;
      setSeedBackedUp(backedUp ?? false);

      // Fetch balances from all chains in parallel, scoped to the active wallet.
      // Tron and BTC (non-EVM) ride alongside the EVM fan-out; each derives its
      // address from the same mnemonic, self-registers in the background, and
      // stays dormant-safe (yields no rows when the backend chain is unconfigured).
      const [chainResults, tronResult, btcResult, txResult] = await Promise.all([
        Promise.all(
          CHAIN_KEYS.map(async (chainKey) => {
            const result = await getWalletBalances(
              CHAINS[chainKey].chainId,
              address || undefined
            );
            return { chainKey, result };
          })
        ),
        address ? loadTronWallet(address).catch(() => null) : Promise.resolve(null),
        address ? loadBtcWallet(address).catch(() => null) : Promise.resolve(null),
        getTransactionHistory(1, 5),
      ]);

      const allBalances: WalletBalanceWithChain[] = [];
      const seenTokenChains = new Set<string>();

      for (const { chainKey, result } of chainResults) {
        if (!result.success || !result.data) continue;
        for (const entry of normalizeWalletBalances(result.data, chainKey)) {
          allBalances.push(entry);
          seenTokenChains.add(`${entry.symbol}-${entry.chainKey}`);
        }
      }

      for (const entry of [...(tronResult?.balances ?? []), ...(btcResult?.balances ?? [])]) {
        allBalances.push(entry);
        seenTokenChains.add(`${entry.symbol}-${entry.chainKey}`);
      }

      // Merge with tokenList — add 0-balance entries for missing tokens
      for (const t of tokenList) {
        for (const chain of t.chains) {
          const key = `${t.symbol}-${chain}`;
          if (!seenTokenChains.has(key)) {
            allBalances.push({
              symbol: t.symbol,
              name: t.name,
              balance: '0',
              usdValue: '0.00',
              contractAddress: '',
              decimals: t.decimals,
              chainKey: chain as ChainKey,
              chainName: CHAINS[chain as ChainKey]?.shortName || chain,
            });
          }
        }
      }

      setBalances(allBalances);

      if (txResult.success && txResult.data) {
        setTransactions(txResult.data.transactions || []);
      }
    } catch (err: any) {
      console.error('Fetch wallet data error:', err);
      setError('Failed to load wallet data');
      setBalances(
        tokenList.flatMap(t =>
          t.chains.map(chain => ({
            symbol: t.symbol, name: t.name, balance: '0', usdValue: '0.00',
            contractAddress: '', decimals: t.decimals, chainKey: chain as ChainKey,
            chainName: CHAINS[chain as ChainKey]?.shortName || chain,
          }))
        )
      );
    } finally {
      setLoading(false);
    }
  }, [tokenList]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await fetchData();
    setRefreshing(false);
  }, [fetchData]);

  const handleSwitchWallet = async (address: string) => {
    setSelectorVisible(false);
    if (address === walletAddress) return;
    setLoading(true);
    setBalances([]);
    setTransactions([]);
    setWalletAddress(address);
    try {
      await setActiveWalletService(address);
      await registerWalletAddress(address);
      await fetchData();
    } catch (err: any) {
      console.error('Switch wallet error:', err);
    }
  };

  const totalUsdValue = balances.reduce(
    (sum, b) => sum + parseFloat(b.usdValue || '0'),
    0
  );

  const renderTokenCard = (token: WalletBalanceWithChain) => {
    const chain = getChainDisplay(token.chainKey);
    const logoUrl = tokenLogoUrl(token);
    return (
      <PressableScale
        key={`${token.symbol}-${token.chainKey}`}
        onPress={() =>
          router.push(
            `/wallet/token?symbol=${token.symbol}&chainKey=${token.chainKey}&name=${encodeURIComponent(token.name)}&iconColor=${encodeURIComponent(TOKEN_COLORS[token.symbol] || colors.accent)}`
          )
        }
        onLongPress={() => {
          if ((token as any).custom) {
            Alert.alert(
              'Remove Token',
              `Remove ${token.symbol} from your wallet?`,
              [
                { text: 'Cancel', style: 'cancel' },
                {
                  text: 'Remove',
                  style: 'destructive',
                  onPress: async () => { await removeToken(token.symbol, token.chainKey as ChainKey); fetchData(); },
                },
              ]
            );
          }
        }}
      >
        <Card padding="md" style={{ marginBottom: spacing.sm }}>
          <View style={{ flexDirection: 'row', alignItems: 'center' }}>
            {logoUrl ? (
              <Image
                source={{ uri: logoUrl }}
                style={{
                  width: 40,
                  height: 40,
                  borderRadius: radius.pill,
                  marginRight: spacing.md,
                }}
              />
            ) : (
              <View
                style={{
                  width: 40,
                  height: 40,
                  borderRadius: radius.pill,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginRight: spacing.md,
                  backgroundColor: TOKEN_COLORS[token.symbol] || colors.accent,
                }}
              >
                <Text variant="bodyMed" weight="700" style={{ color: colors.onAccent }}>
                  {TOKEN_ICONS[token.symbol] || token.symbol[0]}
                </Text>
              </View>
            )}
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="600">
                {token.symbol}
              </Text>
              <Text variant="caption" color="textMuted">
                {token.name}{chain ? ` · ${chain.shortName}` : ''}
              </Text>
            </View>
            <View style={{ alignItems: 'flex-end' }}>
              <Text variant="bodyMed" weight="600" numeric>
                {parseFloat(token.balance).toLocaleString(undefined, {
                  maximumFractionDigits: 6,
                })}
              </Text>
              <Text variant="caption" color="textMuted" numeric>
                ${parseFloat(token.usdValue).toFixed(2)}
              </Text>
            </View>
          </View>
        </Card>
      </PressableScale>
    );
  };

  const renderTransaction = (tx: Transaction) => {
    const isSend =
      tx.type === 'send' ||
      tx.fromAddress.toLowerCase() === walletAddress.toLowerCase();
    const toneColor = isSend ? colors.danger : colors.success;
    const toneBg = isSend ? colors.dangerBg : colors.successBg;
    return (
      <View
        key={tx.id}
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          paddingVertical: spacing.md,
          borderBottomWidth: 1,
          borderBottomColor: colors.hairline,
        }}
      >
        <View
          style={{
            width: 36,
            height: 36,
            borderRadius: radius.pill,
            justifyContent: 'center',
            alignItems: 'center',
            marginRight: spacing.md,
            backgroundColor: toneBg,
          }}
        >
          <Icon name={isSend ? 'arrowRight' : 'check'} size={18} color={toneColor} />
        </View>
        <View style={{ flex: 1 }}>
          <Text variant="bodySm" weight="600">
            {isSend ? 'Sent' : 'Received'} {tx.tokenSymbol}
          </Text>
          <Text variant="caption" color="textMuted">
            {new Date(tx.createdAt).toLocaleDateString()}
          </Text>
        </View>
        <Text variant="bodySm" weight="700" numeric style={{ color: toneColor }}>
          {isSend ? '-' : '+'}{tx.amount} {tx.tokenSymbol}
        </Text>
      </View>
    );
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading wallet..." />
        </View>
      </SafeAreaView>
    );
  }

  if (!walletAddress) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View
          style={{
            flex: 1,
            justifyContent: 'center',
            alignItems: 'center',
            paddingHorizontal: spacing['3xl'],
          }}
        >
          <View
            style={{
              width: 88,
              height: 88,
              borderRadius: radius.pill,
              backgroundColor: colors.accentMuted,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.xl,
            }}
          >
            <Icon name="wallet" size={48} color={colors.accent} />
          </View>
          <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
            Set Up Your Wallet
          </Text>
          <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing['2xl'] }}>
            A wallet is needed to make purchases, receive cashback, and manage your funds securely.
          </Text>

          <Button
            title="Create New Wallet"
            leftIcon="plus"
            onPress={() => router.push('/wallet/seedphrase')}
            style={{ marginBottom: spacing.md }}
          />
          <Button
            title="Import Existing Wallet"
            variant="secondary"
            leftIcon="upload"
            onPress={() => router.push('/wallet/seedphrase')}
            style={{ marginBottom: spacing['2xl'] }}
          />

          <View style={{ gap: spacing.md, width: '100%' }}>
            {[
              { icon: 'verified' as const, text: 'Secured with seed phrase' },
              { icon: 'swap' as const, text: 'Pay with USDC, USDT or MCGP' },
              { icon: 'star' as const, text: 'Earn cashback on purchases' },
            ].map((item) => (
              <View
                key={item.text}
                style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}
              >
                <Icon name={item.icon} size={16} color={colors.accent} />
                <Text variant="bodySm" color="textSecondary">
                  {item.text}
                </Text>
              </View>
            ))}
          </View>
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={colors.accent} />
        }
      >
        {network === 'testnet' && (
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              gap: spacing.xs,
              backgroundColor: colors.warningBg,
              paddingVertical: spacing.sm,
              borderRadius: radius.md,
              marginBottom: spacing.md,
            }}
          >
            <Icon name="warning" size={14} color={colors.warning} />
            <Text variant="caption" weight="600" color="warning">
              Testnet Mode — Balances are not real
            </Text>
          </View>
        )}

        {/* Seed phrase backup warning */}
        {!seedBackedUp && (
          <PressableScale
            onPress={() => router.push('/wallet/seedphrase')}
            style={{ marginBottom: spacing.md }}
          >
            <AccentCard padding="md" railColor={colors.warning}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="warning" size={18} color={colors.warning} />
                <Text variant="bodySm" weight="600" color="warning" style={{ flex: 1 }}>
                  Back up your seed phrase to protect your funds
                </Text>
                <Icon name="chevronRight" size={16} color={colors.textMuted} />
              </View>
            </AccentCard>
          </PressableScale>
        )}

        {/* Total balance */}
        <AccentCard padding="xl" style={{ marginBottom: spacing.lg }}>
          <TouchableOpacity
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              alignSelf: 'center',
              gap: spacing.xs,
              marginBottom: spacing.sm,
            }}
            onPress={() => setSelectorVisible(true)}
          >
            <Text variant="bodySm" weight="600" color="textSecondary">
              {activeLabel}
            </Text>
            <Icon name="chevronDown" size={16} color={colors.textMuted} />
          </TouchableOpacity>
          <Text variant="caption" color="textMuted" align="center">
            Total Balance
          </Text>
          <AmountText
            value={totalUsdValue}
            currency="USD"
            variant="display"
            align="center"
            minimumFractionDigits={2}
            maximumFractionDigits={2}
            style={{ marginTop: spacing.xs, marginBottom: spacing.sm }}
          />
          <Text
            variant="caption"
            color="textMuted"
            align="center"
            numeric
            numberOfLines={1}
            ellipsizeMode="middle"
            style={{ alignSelf: 'center', maxWidth: '80%' }}
          >
            {walletAddress}
          </Text>
        </AccentCard>

        {/* Action buttons */}
        <View
          style={{
            flexDirection: 'row',
            justifyContent: 'center',
            gap: spacing['2xl'],
            marginBottom: spacing['2xl'],
          }}
        >
          {[
            { label: 'Send', icon: 'send' as const, onPress: () => router.push('/wallet/send') },
            { label: 'MCGP', icon: 'bolt' as const, onPress: () => router.push('/wallet/mcgp') },
            { label: 'Swap', icon: 'swapVertical' as const, onPress: () => router.push('/easyswap?tab=allswap') },
            { label: 'Receive', icon: 'arrowRight' as const, onPress: () => router.push('/wallet/receive') },
          ].map((action) => (
            <TouchableOpacity
              key={action.label}
              style={{ alignItems: 'center', flex: 1 }}
              onPress={action.onPress}
            >
              <View
                style={{
                  width: 48,
                  height: 48,
                  borderRadius: radius.pill,
                  backgroundColor: colors.accent,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginBottom: spacing.sm,
                }}
              >
                <Icon name={action.icon} size={20} color={colors.onAccent} />
              </View>
              <Text variant="bodySm" weight="500">
                {action.label}
              </Text>
            </TouchableOpacity>
          ))}
        </View>

        {/* Error */}
        {error ? (
          <AccentCard padding="md" railColor={colors.danger} style={{ marginBottom: spacing.lg }}>
            <View style={{ alignItems: 'center', gap: spacing.xs }}>
              <Text variant="bodySm" color="danger" align="center">
                {error}
              </Text>
              <PressableScale onPress={onRefresh} hitSlop={8}>
                <Text variant="bodySm" weight="700" color="accent">
                  Tap to retry
                </Text>
              </PressableScale>
            </View>
          </AccentCard>
        ) : null}

        {/* Token balances */}
        <View style={{ marginBottom: spacing.lg }}>
          <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
            Assets
          </Text>
          <ChainSelector
            availableChains={CHAIN_KEYS}
            selectedChain={chainFilter === 'all' ? CHAIN_KEYS[0] : chainFilter}
            onSelect={(chain) => setChainFilter(chain as ChainKey)}
            showAll
            onSelectAll={() => setChainFilter('all')}
            allSelected={chainFilter === 'all'}
            size="compact"
          />
          <View style={{ marginTop: spacing.md }}>
            {(balances as WalletBalanceWithChain[])
              .filter((b) => chainFilter === 'all' || b.chainKey === chainFilter)
              // Non-zero holdings first (by USD value, then raw balance for
              // tokens without a price feed); zero rows by the shared ranking
              // so MCGP leads and majors follow.
              .sort(
                (a, b) =>
                  parseFloat(b.usdValue || '0') - parseFloat(a.usdValue || '0') ||
                  parseFloat(b.balance || '0') - parseFloat(a.balance || '0') ||
                  tokenRankOf(a.symbol) - tokenRankOf(b.symbol) ||
                  a.symbol.localeCompare(b.symbol)
              )
              .map(renderTokenCard)}
          </View>
          <PressableScale
            onPress={() => setImportModalVisible(true)}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              gap: spacing.xs,
              paddingVertical: spacing.md,
              marginTop: spacing.xs,
            }}
          >
            <Icon name="plus" size={18} color={colors.accent} />
            <Text variant="bodySm" weight="600" color="accent">
              Import Token
            </Text>
          </PressableScale>
        </View>

        {/* Recent transactions */}
        <View style={{ marginBottom: spacing.lg }}>
          <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
            Recent Transactions
          </Text>
          {transactions.length === 0 ? (
            <EmptyState
              icon="clock"
              title="No transactions yet"
              subtitle="Your transaction history will appear here"
            />
          ) : (
            transactions.map(renderTransaction)
          )}
        </View>

        <ImportTokenModal
          visible={importModalVisible}
          onClose={() => { setImportModalVisible(false); fetchData(); }}
        />
      </ScrollView>

      {/* Wallet selector sheet */}
      <Sheet visible={selectorVisible} onClose={() => setSelectorVisible(false)} title="Select Wallet">
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm }}>
          {walletList.map((w) => (
            <TouchableOpacity
              key={w.address}
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                paddingVertical: spacing.md,
                borderBottomWidth: 1,
                borderBottomColor: colors.hairline,
              }}
              onPress={() => handleSwitchWallet(w.address)}
            >
              <Avatar name={w.label} size={36} />
              <View style={{ flex: 1, marginLeft: spacing.md }}>
                <Text variant="bodySm" weight="600">
                  {w.label}
                </Text>
                <Text
                  variant="caption"
                  color="textMuted"
                  numeric
                  numberOfLines={1}
                  ellipsizeMode="middle"
                >
                  {w.address}
                </Text>
              </View>
              {w.address === walletAddress && (
                <Icon name="checkCircle" size={22} color={colors.accent} />
              )}
            </TouchableOpacity>
          ))}
          <PressableScale
            onPress={() => {
              setSelectorVisible(false);
              router.push('/wallet/manage');
            }}
            style={{ alignItems: 'center', paddingVertical: spacing.lg }}
          >
            <Text variant="bodySm" weight="700" color="accent">
              Manage Wallets
            </Text>
          </PressableScale>
        </View>
      </Sheet>
    </SafeAreaView>
  );
};

export default WalletHome;
