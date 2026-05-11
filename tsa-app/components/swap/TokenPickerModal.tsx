import React, { useState, useMemo } from 'react';
import {
  Modal, View, Text, TextInput, TouchableOpacity,
  StyleSheet, Pressable, SectionList, Image,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import type { LiFiToken } from '../../services/lifi';
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
}

function formatBalance(balance: string): string {
  const n = Number(balance);
  if (isNaN(n) || n === 0) return '0';
  if (n < 0.000001) return '<0.000001';
  // up to 6 significant decimals, trailing zeros removed
  return n.toLocaleString('en-US', { maximumFractionDigits: 6, minimumFractionDigits: 0 });
}

function formatUSD(balance: string, usdValue: string, priceUSD?: string): string | null {
  const usd = Number(usdValue);
  if (usd > 0) return usd.toLocaleString('en-US', { maximumFractionDigits: 2, minimumFractionDigits: 2 });
  // fall back to LiFi price × balance
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
  visible, mode, userBalances, onSelect, onClose, allTokens, chainId,
}: Props) {
  const [search, setSearch] = useState('');

  const heldTokens = useMemo(() => {
    if (mode === 'to') return [];
    return userBalances
      .filter(b => Number(b.balance) > 0)
      .map(b => {
        const match = allTokens.find(
          t => t.symbol.toLowerCase() === b.symbol.toLowerCase()
        );
        return match ? { token: match, balance: b.balance, usdValue: b.usdValue } : null;
      })
      .filter(Boolean) as { token: LiFiToken; balance: string; usdValue: string }[];
  }, [userBalances, allTokens, mode]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase();
    return allTokens.filter(
      t =>
        t.symbol.toLowerCase().includes(q) ||
        t.name.toLowerCase().includes(q) ||
        t.address.toLowerCase().includes(q)
    );
  }, [allTokens, search]);

  const sections = useMemo(() => {
    const result: { title: string; data: (LiFiToken & { _held?: { balance: string; usdValue: string } })[] }[] = [];
    if (heldTokens.length > 0) {
      result.push({ title: 'YOUR TOKENS', data: heldTokens.map(h => ({ ...h.token, _held: { balance: h.balance, usdValue: h.usdValue } })) });
    }
    result.push({ title: 'ALL TOKENS', data: filtered });
    return result;
  }, [heldTokens, filtered]);

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose}>
      <Pressable style={s.overlay} onPress={onClose}>
        <Pressable style={s.sheet} onPress={() => {}}>
          <View style={s.handle} />
          <Text style={s.title}>Select Token</Text>
          <View style={s.searchRow}>
            <Ionicons name="search" size={16} color="#AAA" />
            <TextInput
              style={s.searchInput}
              placeholder="Search name or paste address"
              placeholderTextColor="#CCC"
              value={search}
              onChangeText={setSearch}
              autoCapitalize="none"
              autoCorrect={false}
            />
          </View>
          <SectionList
            sections={sections}
            keyExtractor={item => item.address}
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
            stickySectionHeadersEnabled={false}
            showsVerticalScrollIndicator={false}
          />
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
});
