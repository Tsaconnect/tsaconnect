// app/wallet/tokens.tsx
//
// All-tokens list — every asset across chains (natives, BE-known ERC-20s,
// user-imported custom tokens, and indexer-discovered tokens), with pull-to-
// refresh and tap-through to the per-token screen. Restyled onto the TSA
// Signature design system (theme tokens + @/components/ui primitives).
//
// FINANCIAL screen: the three-way parallel balance fetch (BE balances +
// discovered tokens + on-chain custom-token reads), the dedup/merge/sort
// pipeline, the AsyncStorage cache snapshot, and the per-token navigation
// params are all preserved verbatim — this is a visual/structural restyle only.
// Per-token/per-chain brand colors (item.iconColor / CHAINS[*].iconColor) come
// from token/chain config data, not the design tokens, so they stay as-is.
import { router } from 'expo-router';
import React, { useState, useEffect, useCallback, useRef } from 'react';
import { View, FlatList, Image, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { ethers } from 'ethers';
import { CHAINS, getChainDisplay, type ChainKey, type AnyChainKey } from '@/constants/chains';
import { loadTronWallet } from '@/services/chains/tronWallet';
import { loadBtcWallet } from '@/services/chains/btcWallet';
import { getPremiumMcgpAsset } from '@/services/mcgpSwapApi';
import { getWalletBalances, normalizeWalletBalances, getDiscoveredTokens } from '@/services/walletApi';
import { getActiveWallet, readErc20BalanceOf, formatTokenBalance } from '@/services/wallet';
import { useTokens } from '@/hooks/useTokens';
import { useNetwork } from '@/hooks/useNetwork';
import { useTheme } from '@/theme';
import {
  AmountText,
  Badge,
  EmptyState,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

const CACHE_PREFIX = 'wallet-all-tokens-cache::';

interface Asset {
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  iconColor: string;
  iconUrl?: string;
  chainKey: AnyChainKey;
  chainName: string;
  isNative: boolean;
  discovered?: boolean;
  imported?: boolean;
  /** Short label chip (e.g. "Spendable" for premium MCGP). */
  tag?: string;
  contractAddress?: string;
  decimals?: number;
}

function cacheKey(address: string, network: string): string {
  return `${CACHE_PREFIX}${network}::${address}`;
}

export default function AllTokensScreen() {
  const { colors, spacing, radius } = useTheme();
  const { tokenList, customTokens } = useTokens();
  const { network } = useNetwork();
  const [assets, setAssets] = useState<Asset[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const fetchTokenRef = useRef(0);

  const fetch = useCallback(async (refresh = false) => {
    const fetchToken = ++fetchTokenRef.current;
    if (refresh) setRefreshing(true);

    const addr = await getActiveWallet();

    // Kick all three lookups off in parallel so the slow one (discovery,
    // 25s server budget) doesn't block the others.
    const [balanceResult, discResult, customBalances] = await Promise.all([
      getWalletBalances().catch(() => null),
      addr
        ? getDiscoveredTokens(undefined, addr).catch(() => null)
        : Promise.resolve(null),
      addr && customTokens.length > 0
        ? Promise.all(
            customTokens.map(async (t) => {
              try {
                const raw = await readErc20BalanceOf(t.contractAddress, addr, t.chainKey);
                return Number(ethers.formatUnits(raw, t.decimals));
              } catch {
                return null;
              }
            }),
          )
        : Promise.resolve([] as (number | null)[]),
    ]);

    if (fetchToken !== fetchTokenRef.current) return;

    const list: Asset[] = [];
    let id = 1;

    // Native tokens (one per chain)
    for (const [ck, chain] of Object.entries(CHAINS)) {
      list.push({
        id: String(id++),
        symbol: chain.nativeCurrency.symbol,
        name: chain.name,
        balance: 0,
        usdValue: 0,
        iconColor: chain.iconColor,
        iconUrl: chain.iconUrl,
        chainKey: ck as ChainKey,
        chainName: chain.name,
        isNative: true,
      });
    }

    // BE balances (natives overlay + ERC-20s the BE already knows about)
    if (balanceResult?.success && balanceResult.data) {
      const normalized = normalizeWalletBalances(balanceResult.data);
      const balanceMap = new Map(normalized.map((e) => [`${e.symbol}-${e.chainKey}`, e]));

      for (const a of list) {
        const entry = balanceMap.get(`${a.symbol}-${a.chainKey}`);
        if (entry) {
          a.balance = parseFloat(entry.balance) || 0;
          a.usdValue = parseFloat(entry.usdValue) || 0;
        }
      }

      for (const entry of normalized) {
        const bal = parseFloat(entry.balance);
        if (!bal || bal <= 0) continue;
        if (list.some((a) => a.symbol === entry.symbol && a.chainKey === entry.chainKey)) continue;
        // EVM-only view: balances come from the EVM fan-out, so chainKey is
        // always an EVM ChainKey here.
        const evmChainKey = entry.chainKey as ChainKey;
        const tc = tokenList.find((t) => t.symbol === entry.symbol && t.chains.includes(evmChainKey));
        const chain = CHAINS[evmChainKey];
        if (!chain) continue;
        list.push({
          id: String(id++),
          symbol: entry.symbol,
          name: tc?.name || entry.name || entry.symbol,
          balance: bal,
          usdValue: parseFloat(entry.usdValue) || 0,
          iconColor: tc?.iconColor || '#888',
          iconUrl: tc?.iconUrl,
          chainKey: evmChainKey,
          chainName: chain.name,
          isNative: false,
          contractAddress: entry.contractAddress || undefined,
          decimals: entry.decimals,
        });
      }
    }

    // User-imported custom tokens — always surface them, with on-chain balance
    // attached when we can read it; 0 otherwise so the row never disappears.
    customTokens.forEach((t, i) => {
      const chain = CHAINS[t.chainKey];
      if (!chain) return;
      if (
        list.some(
          (a) =>
            a.chainKey === t.chainKey &&
            (a.contractAddress?.toLowerCase() === t.contractAddress.toLowerCase() ||
              a.symbol === t.symbol),
        )
      )
        return;
      const balance = customBalances[i] ?? 0;
      list.push({
        id: String(id++),
        symbol: t.symbol,
        name: t.name,
        balance,
        usdValue: 0,
        iconColor: t.iconColor || '#888',
        iconUrl: t.iconUrl,
        chainKey: t.chainKey,
        chainName: chain.name,
        isNative: false,
        imported: true,
        contractAddress: t.contractAddress,
        decimals: t.decimals,
      });
    });

    // Discovered (indexer) tokens — divide by decimals so we don't render raw wei.
    if (discResult?.success && discResult.data?.tokens) {
      const existing = new Set(
        list.map((a) => `${a.chainKey}::${(a.contractAddress || a.symbol).toLowerCase()}`),
      );
      for (const t of discResult.data.tokens) {
        const chainKey = t.chain as ChainKey;
        const chain = CHAINS[chainKey];
        if (!chain) continue;
        const dedupKey = `${chainKey}::${(t.contractAddress || t.symbol).toLowerCase()}`;
        if (existing.has(dedupKey)) continue;
        const balance = formatTokenBalance(t.balance, t.decimals);
        if (!balance || balance <= 0) continue;
        list.push({
          id: String(id++),
          symbol: t.symbol,
          name: t.name || t.symbol,
          balance,
          usdValue: Number.isFinite(t.usdValue) ? t.usdValue : 0,
          iconColor: '#888',
          chainKey,
          chainName: chain.name,
          isNative: false,
          discovered: true,
          contractAddress: t.contractAddress,
          decimals: t.decimals,
        });
      }
    }

    // Non-EVM (Tron): append TRX + TRC-20 balances. Dormant-safe — yields
    // nothing when the backend chain is unconfigured. TRX shows as a native
    // token (visible even at 0) so Tron is always reachable from "See all".
    try {
      const tron = await loadTronWallet(addr || undefined);
      if (tron) {
        const tronColor = getChainDisplay('tron')?.iconColor || '#EF0027';
        for (const row of tron.balances) {
          list.push({
            id: String(id++),
            symbol: row.symbol,
            name: row.symbol === 'TRX' ? 'Tron' : row.name,
            balance: parseFloat(row.balance) || 0,
            usdValue: parseFloat(row.usdValue) || 0,
            iconColor: tronColor,
            chainKey: 'tron',
            chainName: 'Tron',
            isNative: !row.contractAddress,
            contractAddress: row.contractAddress || undefined,
            decimals: row.decimals,
          });
        }
      }
    } catch { /* chain unavailable — skip Tron */ }

    // Non-EVM (BTC): same pattern — BTC shows as a native token (visible even
    // at 0) so Bitcoin is always reachable from "See all".
    try {
      const btc = await loadBtcWallet(addr || undefined);
      if (btc) {
        const btcColor = getChainDisplay('btc')?.iconColor || '#F7931A';
        for (const row of btc.balances) {
          list.push({
            id: String(id++),
            symbol: row.symbol,
            name: row.symbol === 'BTC' ? 'Bitcoin' : row.name,
            balance: parseFloat(row.balance) || 0,
            usdValue: parseFloat(row.usdValue) || 0,
            iconColor: btcColor,
            chainKey: 'btc',
            chainName: 'Bitcoin',
            isNative: true,
            decimals: row.decimals,
          });
        }
      }
    } catch { /* chain unavailable — skip BTC */ }

    // Spendable premium MCGP (Contract B, not ERC-20) — its own tagged row,
    // distinct from any ordinary (withdrawn) ERC-20 MCGP, reachable from "See
    // all", not only the buy/sell screens.
    try {
      const { balance: premium, usdValue } = await getPremiumMcgpAsset();
      if (premium > 0) {
        const mcgpToken = tokenList.find((t) => t.symbol === 'MCGP');
        list.push({
          id: 'mcgp-premium',
          symbol: 'MCGP',
          name: mcgpToken?.name || 'MCG Protocol',
          balance: premium,
          usdValue,
          iconColor: mcgpToken?.iconColor || '#FFD700',
          iconUrl: mcgpToken?.iconUrl,
          chainKey: 'sonic',
          chainName: 'Sonic',
          isNative: false,
          tag: 'Spendable',
        });
      }
    } catch { /* MCGP swap unavailable — skip */ }

    // Visible = anything with balance, plus native zero rows, plus every
    // imported custom token (user explicitly added it).
    const withBalance = list
      .filter((a) => a.balance > 0 || a.imported)
      .sort((a, b) => b.usdValue - a.usdValue);
    const nativeZero = list.filter((a) => a.balance === 0 && a.isNative);
    const final = [...withBalance, ...nativeZero];

    setAssets(final);
    setLoading(false);
    setRefreshing(false);

    if (addr) {
      AsyncStorage.setItem(cacheKey(addr, network), JSON.stringify(final)).catch(() => {});
    }
  }, [tokenList, customTokens, network]);

  // Load cached snapshot synchronously on mount so the screen renders
  // instantly while the live fetch runs in the background.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const addr = await getActiveWallet();
      if (cancelled || !addr) return;
      const raw = await AsyncStorage.getItem(cacheKey(addr, network));
      if (cancelled || !raw) return;
      try {
        const cached = JSON.parse(raw) as Asset[];
        if (Array.isArray(cached) && cached.length > 0) {
          setAssets(cached);
          setLoading(false);
        }
      } catch {}
    })();
    return () => {
      cancelled = true;
    };
  }, [network]);

  useEffect(() => {
    fetch();
  }, [fetch]);

  const handlePress = useCallback(
    (asset: Asset) => {
      // Spendable premium MCGP is a Contract-B entitlement, not a transferable
      // ERC-20 — route to the MCGP hub. Ordinary MCGP falls through to detail.
      if (asset.symbol === 'MCGP' && asset.tag) {
        router.push('/wallet/mcgp' as any);
        return;
      }
      router.push({
        pathname: '/wallet/token',
        params: {
          symbol: asset.symbol,
          chainKey: asset.chainKey,
          name: asset.name,
          iconColor: asset.iconColor,
          iconUrl: asset.iconUrl || '',
          type: asset.isNative ? 'Native Token' : asset.imported ? 'Imported' : 'Token',
          contractAddress: asset.contractAddress || '',
          discovered: asset.discovered ? '1' : '',
          balance: asset.discovered || asset.imported ? String(asset.balance) : '',
          usdValue: asset.discovered ? String(asset.usdValue) : '',
          decimals: asset.decimals != null ? String(asset.decimals) : '',
        },
      } as any);
    },
    [],
  );

  const renderItem = ({ item }: { item: Asset }) => (
    <PressableScale
      onPress={() => handlePress(item)}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.md,
        backgroundColor: colors.surface,
        borderBottomWidth: 1,
        borderBottomColor: colors.hairline,
      }}
    >
      <View
        style={{
          width: 40,
          height: 40,
          borderRadius: radius.pill,
          justifyContent: 'center',
          alignItems: 'center',
          marginRight: spacing.md,
          // Per-token brand tint comes from token/chain config data.
          backgroundColor: item.iconColor + '20',
        }}
      >
        {item.iconUrl ? (
          <Image source={{ uri: item.iconUrl }} style={{ width: 24, height: 24, borderRadius: 12 }} />
        ) : (
          <Text variant="bodyMed" weight="700" style={{ color: item.iconColor }}>
            {item.symbol.charAt(0)}
          </Text>
        )}
      </View>
      <View style={{ flex: 1 }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
          <Text variant="bodyMed" weight="600">
            {item.symbol}
          </Text>
          {item.discovered && (
            <Badge label="NEW" size="sm" color={colors.accent} background={colors.accentMuted} />
          )}
          {item.imported && (
            <Badge label="IMPORTED" size="sm" color={colors.info} background={colors.infoBg} />
          )}
          {item.tag && (
            <Badge label={item.tag} size="sm" color={colors.warning} background={colors.warningBg} />
          )}
        </View>
        <View style={{ flexDirection: 'row', alignItems: 'center', marginTop: 2, gap: spacing.xs }}>
          <Text variant="caption" color="textMuted">
            {item.name}
          </Text>
          <View
            style={{
              width: 5,
              height: 5,
              borderRadius: 2.5,
              // Per-chain brand dot color comes from chain config data.
              backgroundColor: getChainDisplay(item.chainKey)?.iconColor || colors.textMuted,
            }}
          />
          <Text variant="caption" color="textMuted">
            {item.chainName}
          </Text>
        </View>
      </View>
      <View style={{ alignItems: 'flex-end' }}>
        <Text variant="bodySm" weight="600" numeric>
          {item.balance.toFixed(4)} {item.symbol}
        </Text>
        <AmountText
          value={item.usdValue}
          currency="USD"
          variant="caption"
          weight="500"
          color="textMuted"
          minimumFractionDigits={2}
          maximumFractionDigits={2}
          style={{ marginTop: 2 }}
        />
      </View>
    </PressableScale>
  );

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading tokens..." />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <FlatList
        style={{ flex: 1, backgroundColor: colors.background }}
        data={assets}
        keyExtractor={(a) => a.id}
        showsVerticalScrollIndicator={false}
        contentContainerStyle={{ paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl refreshing={refreshing} onRefresh={() => fetch(true)} tintColor={colors.accent} />
        }
        renderItem={renderItem}
        ListEmptyComponent={
          <View style={{ paddingTop: spacing['4xl'] }}>
            <EmptyState icon="wallet" title="No tokens" subtitle="Your assets will appear here." />
          </View>
        }
      />
    </SafeAreaView>
  );
}
