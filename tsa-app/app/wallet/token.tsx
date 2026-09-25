// app/wallet/token.tsx
//
// Token detail screen — shows balance, price, chart, and actions for a single asset.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives).
//
// FINANCIAL screen: every balance/price/chart fetch, chain/token selection, money
// rendering, and send/receive navigation (including the discovered-ERC-20 param
// passthrough) is preserved verbatim — this is a visual/structural restyle only.
// The legacy ChainSelector + ActivityFeed wallet components are reused unchanged,
// The price chart lives in components/wallet/PriceChart (line/candles, scrubbing).
import { router, useLocalSearchParams } from 'expo-router';
import React, { useState, useEffect, useCallback } from 'react';
import ActivityFeed from '@/components/activity/ActivityFeed';
import { View, ScrollView } from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import { CHAINS, getChainDisplay, isNonEVMChain, type ChainKey, type AnyChainKey } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import ChainSelector from '../../components/wallet/ChainSelector';
import TokenAvatar from '../../components/wallet/TokenAvatar';
import { getWalletBalances, normalizeWalletBalances } from '../../services/walletApi';
import PriceChart from '../../components/wallet/PriceChart';
import { getTronAddress, getTronBalances } from '../../services/chains/tronWallet';
import { getBtcAddress, getBtcBalances } from '../../services/chains/btcWallet';
import { getSolanaAddress, getSolanaBalances } from '../../services/chains/solanaWallet';
import { useTheme } from '@/theme';
import {
  AmountText,
  Button,
  Card,
  Spinner,
  Text,
} from '@/components/ui';

// ── Main Screen ──
const TokenDetailScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const insets = useSafeAreaInsets();

  const params = useLocalSearchParams<{
    symbol: string;
    chainKey: string;
    name: string;
    iconColor: string;
    iconUrl: string;
    type: string;
    // Auto-discovered ERC-20s aren't in supported_tokens; the wallet
    // forwards the contract + financials so this screen (and Send) can
    // address them by contract and render balances without re-fetching.
    contractAddress: string;
    discovered: string;
    balance: string;
    usdValue: string;
    decimals: string;
  }>();

  const {
    symbol, chainKey, name, iconColor, iconUrl, type,
    contractAddress, discovered,
    balance: discoveredBalanceParam,
    usdValue: discoveredUsdValueParam,
    decimals: discoveredDecimalsParam,
  } = params;
  const isDiscovered = discovered === '1';
  // Discovered/imported contracts can reuse a real token's symbol (airdrop spoofs),
  // so only registry tokens carry their identity onto the Receive screen.
  const isCurated = !isDiscovered && type !== 'Imported';
  const discoveredDecimals = discoveredDecimalsParam ? parseInt(discoveredDecimalsParam, 10) : undefined;

  const { tokens: allTokens } = useTokens();
  const tokenConfig = allTokens[symbol];
  // A token opened from a non-EVM chain (Tron) locks to that chain — its
  // balance/send paths are family-specific and don't share the EVM chain switch.
  const availableChains: AnyChainKey[] = isNonEVMChain(chainKey)
    ? [chainKey]
    : tokenConfig?.chains ?? (chainKey ? [chainKey as ChainKey] : []);
  const [activeChainKey, setActiveChainKey] = useState<AnyChainKey>((chainKey as AnyChainKey) || 'sonic');
  const activeChainConfig = getChainDisplay(activeChainKey);

  const [balance, setBalance] = useState(0);
  const [usdPrice, setUsdPrice] = useState(0);
  const [usdValue, setUsdValue] = useState(0);
  const [loadingBalance, setLoadingBalance] = useState(true);

  const fetchBalance = useCallback(async () => {
    setLoadingBalance(true);
    // Discovered tokens aren't in supported_tokens, so getWalletBalances
    // can't return them. The wallet screen already has the numbers from
    // the discovery call — use those directly.
    if (isDiscovered) {
      const parsedBalance = parseFloat(discoveredBalanceParam || '0');
      const parsedUsdValue = parseFloat(discoveredUsdValueParam || '0');
      setBalance(Number.isFinite(parsedBalance) ? parsedBalance : 0);
      setUsdValue(Number.isFinite(parsedUsdValue) ? parsedUsdValue : 0);
      setUsdPrice(parsedBalance > 0 ? parsedUsdValue / parsedBalance : 0);
      setLoadingBalance(false);
      return;
    }
    try {
      // Non-EVM: derive the family-specific address and read balances from the
      // /wallet/nonevm/* path rather than the EVM chainId endpoint.
      const found = isNonEVMChain(activeChainKey)
        ? await (async () => {
            const fam = getChainDisplay(activeChainKey)?.family;
            const addr =
              fam === 'btc' ? await getBtcAddress()
              : fam === 'solana' ? await getSolanaAddress()
              : await getTronAddress();
            if (!addr) return undefined;
            const rows =
              fam === 'btc' ? await getBtcBalances(addr)
              : fam === 'solana' ? await getSolanaBalances(addr)
              : await getTronBalances(addr);
            return rows.find((entry) => entry.symbol === symbol);
          })()
        : await (async () => {
            const result = await getWalletBalances(CHAINS[activeChainKey as ChainKey]?.chainId);
            if (!result.success || !result.data) return undefined;
            return normalizeWalletBalances(result.data, activeChainKey as ChainKey).find(
              (entry) => entry.symbol === symbol && entry.chainKey === activeChainKey
            );
          })();

      if (found) {
        const parsedBalance = parseFloat(found.balance || '0');
        const parsedUsdValue = parseFloat(found.usdValue || '0');
        setBalance(parsedBalance);
        setUsdValue(parsedUsdValue);
        setUsdPrice(found.usdPrice || (parsedBalance > 0 ? parsedUsdValue / parsedBalance : 0));
      } else {
        setBalance(0);
        setUsdValue(0);
        setUsdPrice(0);
      }
    } catch (err) {
      console.error('Token detail fetch error:', err);
    } finally {
      setLoadingBalance(false);
    }
  }, [activeChainKey, symbol, isDiscovered, discoveredBalanceParam, discoveredUsdValueParam]);

  useEffect(() => { fetchBalance(); }, [fetchBalance]);

  const isNative = type === 'Native Token';
  const displayColor = iconColor || activeChainConfig?.iconColor || colors.textMuted;

  const sendParams = {
    pathname: '/wallet/send',
    params: {
      token: symbol,
      chainKey: activeChainKey,
      ...(isDiscovered && contractAddress
        ? {
            contractAddress,
            discovered: '1',
            decimals: discoveredDecimals != null ? String(discoveredDecimals) : '',
            balance: String(balance),
            usdValue: String(usdValue),
            name,
          }
        : {}),
    },
  } as any;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {/* ── Fixed hero: identity, balance, price, chains ── */}
      <View
        style={{
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.sm,
          paddingBottom: spacing.md,
          borderBottomWidth: 1,
          borderBottomColor: colors.hairline,
        }}
      >
        {/* Identity row */}
        <View style={{ flexDirection: 'row', alignItems: 'center' }}>
          <View style={{ marginRight: spacing.md }}>
            <TokenAvatar
              symbol={symbol || ''}
              iconUrl={iconUrl}
              iconColor={displayColor}
              curated={isCurated}
              size={44}
            />
          </View>
          <View style={{ flex: 1 }}>
            <Text variant="title3" weight="700">
              {symbol}
            </Text>
            <Text variant="caption" color="textMuted">
              {name}
            </Text>
          </View>
        </View>

        {/* Balance */}
        <View style={{ marginTop: spacing.md }}>
          {loadingBalance ? (
            <View style={{ height: 44, justifyContent: 'center' }}>
              <Spinner color={displayColor} />
            </View>
          ) : (
            <View style={{ flexDirection: 'row', alignItems: 'flex-end', gap: spacing.sm }}>
              <Text variant="display" weight="700">
                {balance.toFixed(isNative ? 4 : 2)}
              </Text>
              <Text variant="title3" weight="600" color="textSecondary" style={{ marginBottom: 4 }}>
                {symbol}
              </Text>
            </View>
          )}
          {!loadingBalance && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginTop: spacing.xs }}>
              <AmountText
                value={usdValue}
                currency="USD"
                variant="bodyMed"
                weight="500"
                color="textSecondary"
                minimumFractionDigits={2}
                maximumFractionDigits={2}
              />
              <Text variant="bodySm" color="textMuted">·</Text>
              <AmountText
                value={usdPrice}
                currency="USD"
                variant="bodySm"
                weight="400"
                color="textMuted"
                minimumFractionDigits={2}
                maximumFractionDigits={6}
              />
              <Text variant="bodySm" color="textMuted">each</Text>
            </View>
          )}
        </View>

        {/* Chain Selector */}
        {availableChains.length > 1 && (
          <View style={{ marginTop: spacing.md }}>
            <ChainSelector
              availableChains={availableChains}
              selectedChain={activeChainKey}
              onSelect={(chain) => setActiveChainKey(chain)}
              size="compact"
            />
          </View>
        )}
      </View>

      {/* ── Scrolling detail: chart, info, activity ── */}
      <ScrollView
        style={{ flex: 1 }}
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['2xl'] }}
        showsVerticalScrollIndicator={false}
      >
        {/* Chart */}
        <Card padding="lg" style={{ marginBottom: spacing.md }}>
          <PriceChart symbol={symbol} />
        </Card>

        {/* Token Info */}
        <Card padding="lg" style={{ marginBottom: spacing.md }}>
          <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
            Details
          </Text>
          <InfoRow label="Type" value={type || 'Token'} />
          <InfoRow label="Network" value={activeChainConfig?.name || activeChainKey} />
          <InfoRow label="Symbol" value={symbol} />
          {isNative && activeChainConfig?.nativeCurrency && (
            <InfoRow label="Decimals" value={String(activeChainConfig.nativeCurrency.decimals)} />
          )}
          {activeChainConfig?.explorerUrl && (
            <InfoRow label="Explorer" value={activeChainConfig.shortName || activeChainConfig.name} last />
          )}
        </Card>

        {/* Transaction History */}
        <Card padding="lg">
          <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
            Recent Activity
          </Text>
          <ActivityFeed
            types={['wallet', 'swap']}
            embedded
            onSeeAll={() => router.push('/wallet/history' as any)}
          />
        </Card>
      </ScrollView>

      {/* ── Pinned actions ── */}
      <View
        style={{
          flexDirection: 'row',
          gap: spacing.md,
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: Math.max(insets.bottom, spacing.md),
          borderTopWidth: 1,
          borderTopColor: colors.hairline,
          backgroundColor: colors.surface,
        }}
      >
        <Button
          title="Send"
          leftIcon="send"
          style={{ flex: 1 }}
          onPress={() => router.push(sendParams)}
        />
        <Button
          title="Receive"
          variant="secondary"
          leftIcon="arrowDown"
          style={{ flex: 1 }}
          onPress={() =>
            router.push({ pathname: '/wallet/receive', params: { chainKey: activeChainKey, ...(isCurated ? { symbol } : {}) } } as any)
          }
        />
      </View>
    </SafeAreaView>
  );
};

// ── Info Row ──
const InfoRow: React.FC<{ label: string; value: string; last?: boolean }> = ({ label, value, last }) => {
  const { colors, spacing } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row',
        justifyContent: 'space-between',
        paddingVertical: spacing.sm,
        borderBottomWidth: last ? 0 : 1,
        borderBottomColor: colors.hairline,
      }}
    >
      <Text variant="bodySm" color="textMuted">
        {label}
      </Text>
      <Text variant="bodySm" weight="500">
        {value}
      </Text>
    </View>
  );
};

export default TokenDetailScreen;
