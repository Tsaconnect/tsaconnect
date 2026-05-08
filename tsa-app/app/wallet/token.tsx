// app/wallet/token.tsx
// Token detail screen — shows balance, price, chart, and actions for a single asset
import { router, useLocalSearchParams } from 'expo-router';
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  Text,
  ScrollView,
  StyleSheet,
  Pressable,
  Dimensions,
  ActivityIndicator,
  Image,
} from 'react-native';
import { MaterialIcons as Icon } from '@expo/vector-icons';
import { CHAINS, CHAIN_KEYS, type ChainKey, type ChainConfig } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import ChainSelector from '../../components/wallet/ChainSelector';
import { getWalletBalances, normalizeWalletBalances } from '../../services/walletApi';
import { getTokenPriceHistory } from '../../services/walletApi';

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const CHART_WIDTH = SCREEN_WIDTH - 64;
const CHART_HEIGHT = 180;

// ── Time range options ──
const TIME_RANGES = [
  { label: '24H', days: 1 },
  { label: '7D', days: 7 },
  { label: '1M', days: 30 },
  { label: '3M', days: 90 },
  { label: '1Y', days: 365 },
] as const;

// ── Simple SVG-like line chart using Views ──
const MiniChart: React.FC<{
  data: number[];
  width: number;
  height: number;
  color: string;
  loading: boolean;
}> = ({ data, width, height, color, loading }) => {
  if (loading) {
    return (
      <View style={[{ width, height }, styles.chartPlaceholder]}>
        <ActivityIndicator color={color} />
      </View>
    );
  }

  if (data.length < 2) {
    return (
      <View style={[{ width, height }, styles.chartPlaceholder]}>
        <Text style={styles.chartNoData}>No price data available</Text>
      </View>
    );
  }

  const min = Math.min(...data);
  const max = Math.max(...data);
  const range = max - min || 1;

  // Render as a series of positioned dots connected visually
  const barWidth = width / data.length;

  return (
    <View style={[{ width, height }, styles.chartContainer]}>
      {data.map((val, i) => {
        const normalised = (val - min) / range;
        const barHeight = Math.max(2, normalised * (height - 8));
        return (
          <View
            key={i}
            style={{
              position: 'absolute',
              left: i * barWidth,
              bottom: 0,
              width: Math.max(1, barWidth - 1),
              height: barHeight,
              backgroundColor: color + '40',
              borderTopLeftRadius: 1,
              borderTopRightRadius: 1,
            }}
          />
        );
      })}
      {/* Overlay line */}
      {data.map((val, i) => {
        if (i === 0) return null;
        const normalised = (val - min) / range;
        const y = height - 8 - normalised * (height - 8);
        return (
          <View
            key={`dot-${i}`}
            style={{
              position: 'absolute',
              left: i * barWidth + barWidth / 2 - 2,
              top: y,
              width: 4,
              height: 4,
              borderRadius: 2,
              backgroundColor: color,
            }}
          />
        );
      })}
    </View>
  );
};

// ── Main Screen ──
const TokenDetailScreen: React.FC = () => {
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
  const discoveredDecimals = discoveredDecimalsParam ? parseInt(discoveredDecimalsParam, 10) : undefined;

  const { tokens: allTokens } = useTokens();
  const tokenConfig = allTokens[symbol];
  const availableChains = tokenConfig?.chains ?? (chainKey ? [chainKey as ChainKey] : []);
  const [activeChainKey, setActiveChainKey] = useState<ChainKey>((chainKey as ChainKey) || 'sonic');
  const activeChainConfig = (CHAINS as Record<string, ChainConfig>)[activeChainKey];

  const [balance, setBalance] = useState(0);
  const [usdPrice, setUsdPrice] = useState(0);
  const [usdValue, setUsdValue] = useState(0);
  const [priceHistory, setPriceHistory] = useState<number[]>([]);
  const [selectedRange, setSelectedRange] = useState(1);
  const [loadingBalance, setLoadingBalance] = useState(true);
  const [loadingChart, setLoadingChart] = useState(true);
  const [priceChange, setPriceChange] = useState(0);

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
      const result = await getWalletBalances(CHAINS[activeChainKey]?.chainId);
      if (result.success && result.data) {
        const found = normalizeWalletBalances(result.data, activeChainKey).find(
          (entry) => entry.symbol === symbol && entry.chainKey === activeChainKey
        );
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
      }
    } catch (err) {
      console.error('Token detail fetch error:', err);
    } finally {
      setLoadingBalance(false);
    }
  }, [activeChainKey, symbol, isDiscovered, discoveredBalanceParam, discoveredUsdValueParam]);

  const fetchPriceHistory = useCallback(async (days: number) => {
    setLoadingChart(true);
    try {
      const result = await getTokenPriceHistory(symbol, days);
      if (result.success && result.data) {
        const points = result.data as any[];
        const prices = points.map((p: any) => p.price);
        setPriceHistory(prices);
        // Calculate price change %
        if (prices.length >= 2) {
          const first = prices[0];
          const last = prices[prices.length - 1];
          setPriceChange(first > 0 ? ((last - first) / first) * 100 : 0);
        }
      } else {
        setPriceHistory([]);
        setPriceChange(0);
      }
    } catch {
      setPriceHistory([]);
      setPriceChange(0);
    } finally {
      setLoadingChart(false);
    }
  }, [symbol]);

  useEffect(() => { fetchBalance(); }, [fetchBalance]);
  useEffect(() => { fetchPriceHistory(selectedRange); }, [fetchPriceHistory, selectedRange]);

  const isNative = type === 'Native Token';
  const changeColor = priceChange >= 0 ? '#22C55E' : '#EF4444';
  const displayColor = iconColor || activeChainConfig?.iconColor || '#888';

  return (
    <ScrollView style={styles.container} contentContainerStyle={{ paddingBottom: 100 }}>
      {/* Token Header */}
      <View style={styles.headerCard}>
        <View style={styles.tokenHeader}>
          <View style={[styles.tokenAvatar, { backgroundColor: displayColor + '20' }]}>
            {iconUrl ? (
              <Image source={{ uri: iconUrl }} style={styles.tokenAvatarImage} />
            ) : (
              <Text style={[styles.tokenAvatarText, { color: displayColor }]}>
                {symbol?.charAt(0)}
              </Text>
            )}
          </View>
          <View style={styles.tokenHeaderInfo}>
            <Text style={styles.tokenSymbol}>{symbol}</Text>
            <Text style={styles.tokenName}>{name}</Text>
          </View>
        </View>

        {/* Balance */}
        <View style={styles.balanceSection}>
          {loadingBalance ? (
            <ActivityIndicator color={displayColor} style={{ marginVertical: 12 }} />
          ) : (
            <>
              <Text style={styles.balanceAmount}>
                {balance.toFixed(isNative ? 4 : 2)} {symbol}
              </Text>
              <Text style={styles.balanceUsd}>
                ${usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
              </Text>
            </>
          )}
        </View>

        {/* Price Info */}
        <View style={styles.priceRow}>
          <Text style={styles.priceLabel}>Price</Text>
          <View style={styles.priceValueRow}>
            <Text style={styles.priceValue}>
              ${usdPrice.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 6 })}
            </Text>
            {priceHistory.length >= 2 && (
              <View style={[styles.changeBadge, { backgroundColor: changeColor + '15' }]}>
                <Icon
                  name={priceChange >= 0 ? 'arrow-drop-up' : 'arrow-drop-down'}
                  size={18}
                  color={changeColor}
                />
                <Text style={[styles.changeText, { color: changeColor }]}>
                  {Math.abs(priceChange).toFixed(2)}%
                </Text>
              </View>
            )}
          </View>
        </View>
      </View>

      {/* Chain Selector */}
      {availableChains.length > 1 && (
        <View style={{ paddingHorizontal: 16, marginBottom: 12 }}>
          <ChainSelector
            availableChains={availableChains}
            selectedChain={activeChainKey}
            onSelect={(chain) => setActiveChainKey(chain)}
          />
        </View>
      )}

      {/* Chart */}
      <View style={styles.chartCard}>
        <Text style={styles.sectionTitle}>Price Chart</Text>
        <MiniChart
          data={priceHistory}
          width={CHART_WIDTH}
          height={CHART_HEIGHT}
          color={displayColor}
          loading={loadingChart}
        />
        <View style={styles.timeRangeRow}>
          {TIME_RANGES.map(r => (
            <Pressable
              key={r.days}
              style={[
                styles.timeRangeBtn,
                selectedRange === r.days && { backgroundColor: displayColor + '20' },
              ]}
              onPress={() => setSelectedRange(r.days)}
            >
              <Text style={[
                styles.timeRangeText,
                selectedRange === r.days && { color: displayColor, fontWeight: '700' },
              ]}>
                {r.label}
              </Text>
            </Pressable>
          ))}
        </View>
      </View>

      {/* Token Info */}
      <View style={styles.infoCard}>
        <Text style={styles.sectionTitle}>Details</Text>
        <InfoRow label="Type" value={type || 'Token'} />
        <InfoRow label="Network" value={activeChainConfig?.name || activeChainKey} />
        <InfoRow label="Symbol" value={symbol} />
        {isNative && <InfoRow label="Decimals" value="18" />}
        {activeChainConfig?.explorerUrl && (
          <InfoRow label="Explorer" value={activeChainConfig.shortName || activeChainConfig.name} />
        )}
      </View>

      {/* Actions */}
      <View style={styles.actionsCard}>
        <Pressable
          style={[styles.actionBtn, { backgroundColor: displayColor }]}
          onPress={() => router.push({
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
          } as any)}
        >
          <Icon name="send" size={20} color="#FFF" />
          <Text style={styles.actionBtnText}>Send</Text>
        </Pressable>
        <Pressable
          style={[styles.actionBtn, styles.actionBtnOutline, { borderColor: displayColor }]}
          onPress={() => router.push('/wallet/receive')}
        >
          <Icon name="call-received" size={20} color={displayColor} />
          <Text style={[styles.actionBtnText, { color: displayColor }]}>Receive</Text>
        </Pressable>
      </View>
    </ScrollView>
  );
};

// ── Info Row ──
const InfoRow: React.FC<{ label: string; value: string }> = ({ label, value }) => (
  <View style={styles.infoRow}>
    <Text style={styles.infoLabel}>{label}</Text>
    <Text style={styles.infoValue}>{value}</Text>
  </View>
);

// ── Styles ──
const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },

  // Header Card
  headerCard: {
    marginHorizontal: 16,
    marginTop: 12,
    borderRadius: 16,
    backgroundColor: '#FFF',
    padding: 20,
  },
  tokenHeader: { flexDirection: 'row', alignItems: 'center', marginBottom: 20 },
  tokenAvatar: {
    width: 52,
    height: 52,
    borderRadius: 26,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 14,
  },
  tokenAvatarImage: { width: 32, height: 32, borderRadius: 16 },
  tokenAvatarText: { fontSize: 22, fontWeight: '700' },
  tokenHeaderInfo: { flex: 1 },
  tokenSymbol: { fontSize: 20, fontWeight: '800', color: '#1A1A1A' },
  tokenName: { fontSize: 14, color: '#888', marginTop: 2 },

  // Balance
  balanceSection: { alignItems: 'center', marginBottom: 20 },
  balanceAmount: { fontSize: 28, fontWeight: '800', color: '#1A1A1A' },
  balanceUsd: { fontSize: 16, color: '#888', marginTop: 4 },

  // Price
  priceRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingTop: 16,
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
  },
  priceLabel: { fontSize: 14, color: '#888' },
  priceValueRow: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  priceValue: { fontSize: 16, fontWeight: '600', color: '#1A1A1A' },
  changeBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 8,
  },
  changeText: { fontSize: 13, fontWeight: '600' },

  // Chart
  chartCard: {
    marginHorizontal: 16,
    marginTop: 12,
    borderRadius: 14,
    backgroundColor: '#FFF',
    padding: 16,
  },
  sectionTitle: { fontSize: 16, fontWeight: '700', color: '#1A1A1A', marginBottom: 14 },
  chartContainer: { position: 'relative', overflow: 'hidden' },
  chartPlaceholder: { justifyContent: 'center', alignItems: 'center', backgroundColor: '#FAFAFA', borderRadius: 8 },
  chartNoData: { fontSize: 13, color: '#AAA' },
  timeRangeRow: {
    flexDirection: 'row',
    justifyContent: 'space-around',
    marginTop: 14,
  },
  timeRangeBtn: {
    paddingHorizontal: 14,
    paddingVertical: 6,
    borderRadius: 8,
  },
  timeRangeText: { fontSize: 13, fontWeight: '500', color: '#888' },

  // Info
  infoCard: {
    marginHorizontal: 16,
    marginTop: 12,
    borderRadius: 14,
    backgroundColor: '#FFF',
    padding: 16,
  },
  infoRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingVertical: 10,
    borderBottomWidth: 1,
    borderBottomColor: '#F5F5F5',
  },
  infoLabel: { fontSize: 14, color: '#888' },
  infoValue: { fontSize: 14, fontWeight: '500', color: '#1A1A1A' },

  // Actions
  actionsCard: {
    flexDirection: 'row',
    gap: 12,
    marginHorizontal: 16,
    marginTop: 12,
  },
  actionBtn: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 8,
    paddingVertical: 14,
    borderRadius: 14,
  },
  actionBtnOutline: {
    backgroundColor: 'transparent',
    borderWidth: 1.5,
  },
  actionBtnText: { fontSize: 15, fontWeight: '600', color: '#FFF' },
});

export default TokenDetailScreen;
