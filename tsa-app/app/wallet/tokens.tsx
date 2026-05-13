import { router } from 'expo-router';
import React, { useState, useEffect, useCallback } from 'react';
import { View, Text, FlatList, StyleSheet, Pressable, Image, RefreshControl, ActivityIndicator } from 'react-native';
import { MaterialIcons as Icon } from '@expo/vector-icons';
import { CHAINS, type ChainKey } from '@/constants/chains';
import { getWalletBalances, normalizeWalletBalances, getDiscoveredTokens } from '@/services/walletApi';
import { getActiveWallet } from '@/services/wallet';
import { useTokens } from '@/hooks/useTokens';

const GOLD = '#D4AF37';

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
  contractAddress?: string;
  decimals?: number;
}

export default function AllTokensScreen() {
  const { tokenList } = useTokens();
  const [assets, setAssets] = useState<Asset[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const fetch = useCallback(async (refresh = false) => {
    if (refresh) setRefreshing(true); else setLoading(true);
    const addr = await getActiveWallet();
    const result = await getWalletBalances().catch(() => null);

    const list: Asset[] = [];
    let id = 1;

    // Native tokens
    for (const [ck, chain] of Object.entries(CHAINS)) {
      list.push({ id: String(id++), symbol: chain.nativeCurrency.symbol, name: chain.name, balance: 0, usdValue: 0, iconColor: chain.iconColor, iconUrl: chain.iconUrl, chainKey: ck as ChainKey, chainName: chain.name, isNative: true });
    }

    if (result?.success && result.data) {
      const normalized = normalizeWalletBalances(result.data);
      const balanceMap = new Map(normalized.map(e => [`${e.symbol}-${e.chainKey}`, e]));

      // Overlay native balances
      for (const a of list) {
        const entry = balanceMap.get(`${a.symbol}-${a.chainKey}`);
        if (entry) { a.balance = parseFloat(entry.balance) || 0; a.usdValue = parseFloat(entry.usdValue) || 0; }
      }

      // Add ERC-20s with balance > 0
      for (const entry of normalized) {
        const bal = parseFloat(entry.balance);
        if (!bal || bal <= 0) continue;
        if (list.some(a => a.symbol === entry.symbol && a.chainKey === entry.chainKey)) continue;
        const tc = tokenList.find(t => t.symbol === entry.symbol && t.chains.includes(entry.chainKey));
        const chain = CHAINS[entry.chainKey];
        if (!chain) continue;
        list.push({ id: String(id++), symbol: entry.symbol, name: tc?.name || entry.name || entry.symbol, balance: bal, usdValue: parseFloat(entry.usdValue) || 0, iconColor: tc?.iconColor || '#888', iconUrl: tc?.iconUrl, chainKey: entry.chainKey, chainName: chain.name, isNative: false, contractAddress: entry.contractAddress || undefined, decimals: entry.decimals });
      }
    }

    // Discovery
    if (addr) {
      const disc = await getDiscoveredTokens(undefined, addr).catch(() => null);
      if (disc?.success && disc.data?.tokens) {
        const existing = new Set(list.map(a => `${a.chainKey}::${a.symbol}`));
        for (const t of disc.data.tokens) {
          if (existing.has(`${t.chain}::${t.symbol}`)) continue;
          const chain = CHAINS[t.chain as ChainKey];
          if (!chain) continue;
          const bal = parseFloat(t.balance);
          if (!bal || bal <= 0) continue;
          list.push({ id: String(id++), symbol: t.symbol, name: t.name || t.symbol, balance: bal, usdValue: t.usdValue, iconColor: '#888', chainKey: t.chain as ChainKey, chainName: chain.name, isNative: false, discovered: true, contractAddress: t.contractAddress, decimals: t.decimals });
        }
      }
    }

    const sorted = list.filter(a => a.balance > 0).sort((a, b) => b.usdValue - a.usdValue);
    const nativeZero = list.filter(a => a.balance === 0 && a.isNative);
    setAssets([...sorted, ...nativeZero]);
    setLoading(false);
    setRefreshing(false);
  }, [tokenList]);

  useEffect(() => { fetch(); }, [fetch]);

  const handlePress = (asset: Asset) => {
    router.push({
      pathname: '/wallet/token',
      params: { symbol: asset.symbol, chainKey: asset.chainKey, name: asset.name, iconColor: asset.iconColor, iconUrl: asset.iconUrl || '', type: asset.isNative ? 'Native Token' : 'Token', contractAddress: asset.contractAddress || '', discovered: asset.discovered ? '1' : '', balance: asset.discovered ? String(asset.balance) : '', usdValue: asset.discovered ? String(asset.usdValue) : '', decimals: asset.decimals != null ? String(asset.decimals) : '' },
    } as any);
  };

  if (loading) return <View style={s.center}><ActivityIndicator color={GOLD} /></View>;

  return (
    <FlatList
      style={s.container}
      data={assets}
      keyExtractor={a => a.id}
      showsVerticalScrollIndicator={false}
      contentContainerStyle={{ paddingBottom: 40 }}
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => fetch(true)} tintColor={GOLD} colors={[GOLD]} />}
      renderItem={({ item }) => (
        <Pressable style={({ pressed }) => [s.row, pressed && { opacity: 0.7 }]} onPress={() => handlePress(item)}>
          <View style={[s.avatar, { backgroundColor: item.iconColor + '20' }]}>
            {item.iconUrl
              ? <Image source={{ uri: item.iconUrl }} style={s.avatarImg} />
              : <Text style={[s.avatarText, { color: item.iconColor }]}>{item.symbol.charAt(0)}</Text>
            }
          </View>
          <View style={s.info}>
            <View style={s.symbolRow}>
              <Text style={s.symbol}>{item.symbol}</Text>
              {item.discovered && <View style={s.newBadge}><Text style={s.newBadgeText}>NEW</Text></View>}
            </View>
            <View style={s.meta}>
              <Text style={s.name}>{item.name}</Text>
              <View style={[s.dot, { backgroundColor: CHAINS[item.chainKey]?.iconColor || '#999' }]} />
              <Text style={s.chain}>{item.chainName}</Text>
            </View>
          </View>
          <View style={s.values}>
            <Text style={s.balance}>{item.balance.toFixed(item.isNative ? 4 : 2)} {item.symbol}</Text>
            <Text style={s.usd}>${item.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</Text>
          </View>
        </Pressable>
      )}
      ListEmptyComponent={<View style={s.center}><Icon name="account-balance-wallet" size={36} color="#DDD" /><Text style={s.emptyText}>No tokens</Text></View>}
    />
  );
}

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  center: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: 12, padding: 40 },
  emptyText: { fontSize: 15, color: '#BBB', fontWeight: '600' },
  row: { flexDirection: 'row', alignItems: 'center', paddingHorizontal: 16, paddingVertical: 12, backgroundColor: '#FFF', borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: '#F5F5F5' },
  avatar: { width: 40, height: 40, borderRadius: 20, justifyContent: 'center', alignItems: 'center', marginRight: 12 },
  avatarImg: { width: 24, height: 24, borderRadius: 12 },
  avatarText: { fontSize: 16, fontWeight: '700' },
  info: { flex: 1 },
  symbolRow: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  symbol: { fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
  newBadge: { backgroundColor: 'rgba(212,175,55,0.15)', paddingHorizontal: 6, paddingVertical: 2, borderRadius: 4 },
  newBadgeText: { fontSize: 9, fontWeight: '800', color: '#8B6914' },
  meta: { flexDirection: 'row', alignItems: 'center', marginTop: 2 },
  name: { fontSize: 12, color: '#888' },
  dot: { width: 5, height: 5, borderRadius: 2.5, marginHorizontal: 5 },
  chain: { fontSize: 11, color: '#AAA' },
  values: { alignItems: 'flex-end' },
  balance: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  usd: { fontSize: 12, color: '#888', marginTop: 2 },
});
