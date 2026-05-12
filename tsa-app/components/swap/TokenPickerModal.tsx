import React, { useState, useMemo, useEffect } from 'react';
import {
  Modal, View, Text, TextInput, TouchableOpacity,
  StyleSheet, Pressable, SectionList, Image, ScrollView,
  ActivityIndicator,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import type { LiFiToken, LiFiChain } from '../../services/lifi';
import { getLiFiChains } from '../../services/lifi';
import type { WalletBalanceWithChain } from '../../services/walletApi';

const GOLD = '#D4AF37';

interface Props {
  visible: boolean;
  mode: 'from' | 'to';
  userBalances: WalletBalanceWithChain[];
  onSelect: (token: LiFiToken, chainId: number) => void;
  onClose: () => void;
  allTokens: LiFiToken[];
  chainId: number;
  onChainChange?: (chainId: number) => void;
}

function formatBalance(balance: string): string {
  const n = Number(balance);
  if (isNaN(n) || n === 0) return '0';
  if (n < 0.000001) return '<0.000001';
  return n.toLocaleString('en-US', { maximumFractionDigits: 6, minimumFractionDigits: 0 });
}

function formatUSD(balance: string, usdValue: string, priceUSD?: string): string | null {
  const usd = Number(usdValue);
  if (usd > 0) return usd.toLocaleString('en-US', { maximumFractionDigits: 2, minimumFractionDigits: 2 });
  if (priceUSD) {
    const computed = Number(balance) * Number(priceUSD);
    if (computed > 0) return computed.toLocaleString('en-US', { maximumFractionDigits: 2, minimumFractionDigits: 2 });
  }
  return null;
}

function TokenRow({
  token, chainId, balance, usdValue, onPress,
}: {
  token: LiFiToken; chainId: number; balance?: string; usdValue?: string; onPress: () => void;
}) {
  const initial = token.symbol[0] ?? '?';
  const displayBalance = balance !== undefined ? formatBalance(balance) : undefined;
  const displayUSD = balance !== undefined ? formatUSD(balance, usdValue ?? '0', token.priceUSD) : null;

  return (
    <TouchableOpacity style={s.row} onPress={onPress} activeOpacity={0.7}>
      <View style={s.iconWrap}>
        {token.logoURI ? (
          <Image source={{ uri: token.logoURI }} style={s.iconImg} />
        ) : (
          <View style={[s.iconFallback, { backgroundColor: GOLD }]}>
            <Text style={s.iconText}>{initial}</Text>
          </View>
        )}
      </View>
      <View style={s.rowMid}>
        <Text style={s.symbol}>{token.symbol}</Text>
        <Text style={s.name} numberOfLines={1}>{token.name}</Text>
      </View>
      {displayBalance !== undefined && (
        <View style={s.rowRight}>
          <Text style={s.balance}>{displayBalance}</Text>
          {displayUSD ? <Text style={s.usd}>${displayUSD}</Text> : null}
        </View>
      )}
    </TouchableOpacity>
  );
}

export default function TokenPickerModal({
  visible, mode, userBalances, onSelect, onClose, allTokens, chainId, onChainChange,
}: Props) {
  const [search, setSearch] = useState('');
  const [chains, setChains] = useState<LiFiChain[]>([]);
  const [chainsLoading, setChainsLoading] = useState(true);

  useEffect(() => {
    getLiFiChains()
      .then(setChains)
      .catch(() => {})
      .finally(() => setChainsLoading(false));
  }, []);

  // Reset search each time the picker opens
  useEffect(() => {
    if (visible) setSearch('');
  }, [visible]);

  const heldTokens = useMemo(() => {
    if (mode === 'to') return [];
    const activeChain = chains.find(c => c.id === chainId);
    return userBalances
      .filter(b => Number(b.balance) > 0 && (!activeChain || b.chainKey === activeChain.key.toLowerCase()))
      .map(b => {
        const match = allTokens.find(
          t => t.symbol.toLowerCase() === b.symbol.toLowerCase()
        );
        return match ? { token: match, balance: b.balance, usdValue: b.usdValue } : null;
      })
      .filter(Boolean) as { token: LiFiToken; balance: string; usdValue: string }[];
  }, [userBalances, allTokens, mode, chainId, chains]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    const heldAddresses = new Set(heldTokens.map(h => h.token.address));
    return allTokens.filter(
      t =>
        !heldAddresses.has(t.address) &&
        (q === '' ||
          t.symbol.toLowerCase().includes(q) ||
          t.name.toLowerCase().includes(q) ||
          t.address.toLowerCase().startsWith(q))
    );
  }, [allTokens, search, heldTokens]);

  const filteredHeld = useMemo(() => {
    if (!search.trim()) return heldTokens;
    const q = search.toLowerCase().trim();
    return heldTokens.filter(
      h =>
        h.token.symbol.toLowerCase().includes(q) ||
        h.token.name.toLowerCase().includes(q) ||
        h.token.address.toLowerCase().startsWith(q)
    );
  }, [heldTokens, search]);

  const sections = useMemo(() => {
    const result: { title: string; data: (LiFiToken & { _held?: { balance: string; usdValue: string } })[] }[] = [];
    if (filteredHeld.length > 0) {
      result.push({ title: 'YOUR TOKENS', data: filteredHeld.map(h => ({ ...h.token, _held: { balance: h.balance, usdValue: h.usdValue } })) });
    }
    if (filtered.length > 0) {
      result.push({ title: 'ALL TOKENS', data: filtered });
    }
    return result;
  }, [filteredHeld, filtered]);

  const noResults = sections.length === 0 && search.trim().length > 0;
  const noTokens = sections.length === 0 && search.trim().length === 0 && allTokens.length === 0;

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose}>
      <Pressable style={s.overlay} onPress={onClose}>
        <Pressable style={s.sheet} onPress={() => {}}>
          <View style={s.handle} />
          <Text style={s.title}>Select Token</Text>

          {onChainChange && (
            <ScrollView
              horizontal
              showsHorizontalScrollIndicator={false}
              style={s.chainScroll}
              contentContainerStyle={s.chainRow}
            >
              {chainsLoading ? (
                <ActivityIndicator size="small" color={GOLD} style={{ marginHorizontal: 8 }} />
              ) : (
                chains.map(chain => (
                  <TouchableOpacity
                    key={chain.id}
                    style={[s.chainChip, chain.id === chainId && s.chainChipActive]}
                    onPress={() => onChainChange(chain.id)}
                    activeOpacity={0.7}
                  >
                    {chain.logoURI ? (
                      <Image source={{ uri: chain.logoURI }} style={s.chainIcon} />
                    ) : null}
                    <Text
                      style={[s.chainChipText, chain.id === chainId && s.chainChipTextActive]}
                      numberOfLines={1}
                      adjustsFontSizeToFit
                      minimumFontScale={0.85}
                    >
                      {chain.key}
                    </Text>
                  </TouchableOpacity>
                ))
              )}
            </ScrollView>
          )}

          <View style={s.searchRow}>
            <Ionicons name="search" size={16} color="#AAA" />
            <TextInput
              style={s.searchInput}
              placeholder="Search name, symbol or paste address"
              placeholderTextColor="#CCC"
              value={search}
              onChangeText={setSearch}
              autoCapitalize="none"
              autoCorrect={false}
            />
            {search.length > 0 && (
              <TouchableOpacity onPress={() => setSearch('')} hitSlop={{ top: 8, bottom: 8, left: 8, right: 8 }}>
                <Ionicons name="close-circle" size={16} color="#AAA" />
              </TouchableOpacity>
            )}
          </View>

          {noResults ? (
            <View style={s.emptyWrap}>
              <Text style={s.emptyText}>No tokens found for "{search}"</Text>
              <Text style={s.emptyHint}>Try searching by symbol, name, or contract address</Text>
            </View>
          ) : noTokens ? (
            <View style={s.emptyWrap}>
              <ActivityIndicator size="small" color={GOLD} />
              <Text style={s.emptyText}>Loading tokens…</Text>
            </View>
          ) : (
            <SectionList
              sections={sections}
              keyExtractor={(item, index) => `${item.address}-${index}`}
              renderSectionHeader={({ section }) => (
                <Text style={s.sectionLabel}>{section.title}</Text>
              )}
              renderItem={({ item }) => {
                const held = (item as any)._held;
                return (
                  <TokenRow
                    token={item}
                    chainId={chainId}
                    balance={held?.balance}
                    usdValue={held?.usdValue}
                    onPress={() => { onSelect(item, chainId); onClose(); }}
                  />
                );
              }}
              keyboardShouldPersistTaps="handled"
              stickySectionHeadersEnabled={false}
              showsVerticalScrollIndicator={false}
            />
          )}
        </Pressable>
      </Pressable>
    </Modal>
  );
}

const s = StyleSheet.create({
  overlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.45)', justifyContent: 'flex-end' },
  sheet: {
    backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24,
    padding: 20, maxHeight: '80%',
  },
  handle: {
    width: 40, height: 4, borderRadius: 2, backgroundColor: '#E0E0E0',
    alignSelf: 'center', marginBottom: 16,
  },
  title: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginBottom: 12, textAlign: 'center' },
  chainScroll: { height: 42, marginBottom: 16 },
  chainRow: { flexDirection: 'row', alignItems: 'center', gap: 8, paddingVertical: 4, paddingRight: 4 },
  chainChip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    minWidth: 52,
    height: 34,
    paddingHorizontal: 10,
    borderRadius: 16,
    backgroundColor: '#F0F0F0',
    justifyContent: 'center',
  },
  chainChipActive: { backgroundColor: GOLD },
  chainIcon: { width: 14, height: 14, borderRadius: 7 },
  chainChipText: { fontSize: 11, lineHeight: 16, fontWeight: '700', color: '#555', textAlign: 'center' },
  chainChipTextActive: { color: '#FFF' },
  searchRow: {
    flexDirection: 'row', alignItems: 'center', gap: 8,
    backgroundColor: '#F5F5F5', borderRadius: 10, paddingHorizontal: 12, paddingVertical: 8, marginBottom: 8,
  },
  searchInput: { flex: 1, fontSize: 14, color: '#1A1A1A' },
  sectionLabel: {
    fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1,
    paddingVertical: 8, backgroundColor: '#FFF',
  },
  row: { flexDirection: 'row', alignItems: 'center', paddingVertical: 12, gap: 12 },
  iconWrap: { width: 36, height: 36 },
  iconImg: { width: 36, height: 36, borderRadius: 18 },
  iconFallback: { width: 36, height: 36, borderRadius: 18, justifyContent: 'center', alignItems: 'center' },
  iconText: { fontSize: 14, fontWeight: '700', color: '#FFF' },
  rowMid: { flex: 1 },
  symbol: { fontSize: 15, fontWeight: '700', color: '#1A1A1A' },
  name: { fontSize: 12, color: '#888', marginTop: 1 },
  rowRight: { alignItems: 'flex-end' },
  balance: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  usd: { fontSize: 11, color: '#AAA', marginTop: 1 },
  emptyWrap: { flex: 1, alignItems: 'center', justifyContent: 'center', paddingVertical: 48, gap: 8 },
  emptyText: { fontSize: 14, fontWeight: '600', color: '#555', textAlign: 'center' },
  emptyHint: { fontSize: 12, color: '#AAA', textAlign: 'center' },
});
