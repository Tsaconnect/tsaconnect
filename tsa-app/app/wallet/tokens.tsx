import { router } from 'expo-router';
import React, { useState, useEffect, useCallback, useRef } from 'react';
import { View, Text, FlatList, StyleSheet, Pressable, Image, RefreshControl, ActivityIndicator } from 'react-native';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { MaterialIcons as Icon } from '@expo/vector-icons';
import { ethers } from 'ethers';
import { CHAINS, type ChainKey } from '@/constants/chains';
import { getWalletBalances, normalizeWalletBalances, getDiscoveredTokens } from '@/services/walletApi';
import { getActiveWallet, readErc20BalanceOf, formatTokenBalance } from '@/services/wallet';
import { useTokens } from '@/hooks/useTokens';
import { useNetwork } from '@/hooks/useNetwork';

const GOLD = '#D4AF37';
const CACHE_PREFIX = 'wallet-all-tokens-cache::';

interface Asset {
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  iconColor: string;
  iconUrl?: string;
  chainKey: ChainKey;
  chainName: string;
  isNative: boolean;
  discovered?: boolean;
  imported?: boolean;
  contractAddress?: string;
  decimals?: number;
}

function cacheKey(address: string, network: string): string {
  return `${CACHE_PREFIX}${network}::${address}`;
}

export default function AllTokensScreen() {
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
        const tc = tokenList.find((t) => t.symbol === entry.symbol && t.chains.includes(entry.chainKey));
        const chain = CHAINS[entry.chainKey];
        if (!chain) continue;
        list.push({
          id: String(id++),
          symbol: entry.symbol,
          name: tc?.name || entry.name || entry.symbol,
          balance: bal,
          usdValue: parseFloat(entry.usdValue) || 0,
          iconColor: tc?.iconColor || '#888',
          iconUrl: tc?.iconUrl,
          chainKey: entry.chainKey,
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
    <Pressable style={({ pressed }) => [s.row, pressed && { opacity: 0.7 }]} onPress={() => handlePress(item)}>
      <View style={[s.avatar, { backgroundColor: item.iconColor + '20' }]}>
        {item.iconUrl ? (
          <Image source={{ uri: item.iconUrl }} style={s.avatarImg} />
        ) : (
          <Text style={[s.avatarText, { color: item.iconColor }]}>{item.symbol.charAt(0)}</Text>
        )}
      </View>
      <View style={s.info}>
        <View style={s.symbolRow}>
          <Text style={s.symbol}>{item.symbol}</Text>
          {item.discovered && (
            <View style={s.newBadge}>
              <Text style={s.newBadgeText}>NEW</Text>
            </View>
          )}
          {item.imported && (
            <View style={s.importedBadge}>
              <Text style={s.importedBadgeText}>IMPORTED</Text>
            </View>
          )}
        </View>
        <View style={s.meta}>
          <Text style={s.name}>{item.name}</Text>
          <View style={[s.dot, { backgroundColor: CHAINS[item.chainKey]?.iconColor || '#999' }]} />
          <Text style={s.chain}>{item.chainName}</Text>
        </View>
      </View>
      <View style={s.values}>
        <Text style={s.balance}>
          {item.balance.toFixed(4)} {item.symbol}
        </Text>
        <Text style={s.usd}>
          ${item.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
        </Text>
      </View>
    </Pressable>
  );

  if (loading) {
    return (
      <View style={s.center}>
        <ActivityIndicator color={GOLD} />
      </View>
    );
  }

  return (
    <FlatList
      style={s.container}
      data={assets}
      keyExtractor={(a) => a.id}
      showsVerticalScrollIndicator={false}
      contentContainerStyle={{ paddingBottom: 40 }}
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={() => fetch(true)} tintColor={GOLD} colors={[GOLD]} />
      }
      renderItem={renderItem}
      ListEmptyComponent={
        <View style={s.center}>
          <Icon name="account-balance-wallet" size={36} color="#DDD" />
          <Text style={s.emptyText}>No tokens</Text>
        </View>
      }
    />
  );
}

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  center: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: 12, padding: 40 },
  emptyText: { fontSize: 15, color: '#BBB', fontWeight: '600' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    backgroundColor: '#FFF',
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: '#F5F5F5',
  },
  avatar: { width: 40, height: 40, borderRadius: 20, justifyContent: 'center', alignItems: 'center', marginRight: 12 },
  avatarImg: { width: 24, height: 24, borderRadius: 12 },
  avatarText: { fontSize: 16, fontWeight: '700' },
  info: { flex: 1 },
  symbolRow: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  symbol: { fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
  newBadge: { backgroundColor: 'rgba(212,175,55,0.15)', paddingHorizontal: 6, paddingVertical: 2, borderRadius: 4 },
  newBadgeText: { fontSize: 9, fontWeight: '800', color: '#8B6914' },
  importedBadge: { backgroundColor: 'rgba(47,68,255,0.10)', paddingHorizontal: 6, paddingVertical: 2, borderRadius: 4 },
  importedBadgeText: { fontSize: 9, fontWeight: '800', color: '#2F44FF' },
  meta: { flexDirection: 'row', alignItems: 'center', marginTop: 2 },
  name: { fontSize: 12, color: '#888' },
  dot: { width: 5, height: 5, borderRadius: 2.5, marginHorizontal: 5 },
  chain: { fontSize: 11, color: '#AAA' },
  values: { alignItems: 'flex-end' },
  balance: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  usd: { fontSize: 12, color: '#888', marginTop: 2 },
});
