import React, { useState, useMemo, useEffect, useRef } from 'react';
import {
  Modal, View, Text, TextInput, TouchableOpacity,
  StyleSheet, Pressable, SectionList, FlatList, Image,
  ActivityIndicator, SafeAreaView, Platform,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import type { LiFiToken, LiFiChain } from '../../services/lifi';
import { getLiFiChains, getLiFiToken } from '../../services/lifi';
import type { WalletBalanceWithChain } from '../../services/walletApi';

// Reliable chain logos by chain ID — LiFi's nativeToken.logoURI for L2s
// all resolve to the ETH logo (their native token IS ETH), so we maintain
// correct per-chain logos here for the chains we care about.
const CHAIN_LOGOS: Record<number, string> = {
  1:       'https://coin-images.coingecko.com/coins/images/279/small/ethereum.png',
  56:      'https://coin-images.coingecko.com/coins/images/825/small/bnb-icon2_2x.png',
  137:     'https://coin-images.coingecko.com/coins/images/4713/small/polygon.png',
  10:      'https://coin-images.coingecko.com/coins/images/25244/small/Optimism.png',
  42161:   'https://coin-images.coingecko.com/coins/images/16547/small/arb.jpg',
  8453:    'https://coin-images.coingecko.com/asset_platforms/images/131/small/base-network.png',
  43114:   'https://coin-images.coingecko.com/coins/images/12559/small/Avalanche_Circle_RedWhite_Trans.png',
  59144:   'https://coin-images.coingecko.com/asset_platforms/images/135/small/linea.jpeg',
  146:     'https://coin-images.coingecko.com/coins/images/38108/small/200x200_Sonic_Logo.png',
  100:     'https://coin-images.coingecko.com/coins/images/11062/small/Identity-Primary-DarkBG.png',
  324:     'https://coin-images.coingecko.com/coins/images/24091/small/zksync.png',
  534352:  'https://coin-images.coingecko.com/coins/images/30393/small/scrollEcosystemToken.png',
  5000:    'https://coin-images.coingecko.com/coins/images/30980/small/token.png',
  81457:   'https://coin-images.coingecko.com/coins/images/35585/small/blast.jpeg',
  8217:    'https://coin-images.coingecko.com/coins/images/22597/small/klay.png',
  42220:   'https://coin-images.coingecko.com/coins/images/11090/small/InjXBvM.png',
  250:     'https://coin-images.coingecko.com/coins/images/4001/small/Fantom_round.png',
  1284:    'https://coin-images.coingecko.com/coins/images/22459/small/glmr.png',
  1088:    'https://coin-images.coingecko.com/coins/images/15595/small/metis.jpeg',
  25:      'https://coin-images.coingecko.com/coins/images/7310/small/cro_token_logo.png',
  130:     'https://coin-images.coingecko.com/asset_platforms/images/237/small/unichain.png',
  480:     'https://coin-images.coingecko.com/asset_platforms/images/162/small/world-chain.png',
};

function chainLogoUri(chain: LiFiChain): string | undefined {
  return CHAIN_LOGOS[chain.id] || chain.nativeToken?.logoURI;
}

const GOLD = '#D4AF37';
const INLINE_CHAIN_COUNT = 4;

interface Props {
  visible: boolean;
  mode: 'from' | 'to';
  userBalances: WalletBalanceWithChain[];
  onSelect: (token: LiFiToken, chainId: number) => void;
  onClose: () => void;
  allTokens: LiFiToken[];
  tokensLoading?: boolean;
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

function ChainChip({ chain, selected, onPress }: { chain: LiFiChain; selected: boolean; onPress: () => void }) {
  return (
    <TouchableOpacity style={[cs.chip, selected && cs.chipActive]} onPress={onPress} activeOpacity={0.75}>
      {chainLogoUri(chain) ? (
        <Image source={{ uri: chainLogoUri(chain) }} style={cs.chipImg} />
      ) : (
        <View style={cs.chipImgFallback}>
          <Text style={cs.chipImgLetter}>{chain.key[0]?.toUpperCase()}</Text>
        </View>
      )}
      <Text style={[cs.chipLabel, selected && cs.chipLabelActive]} numberOfLines={1}>
        {chain.key.toUpperCase()}
      </Text>
    </TouchableOpacity>
  );
}

function ChainPickerModal({
  visible, chains, selectedId, onSelect, onClose,
}: {
  visible: boolean;
  chains: LiFiChain[];
  selectedId: number;
  onSelect: (id: number) => void;
  onClose: () => void;
}) {
  const [search, setSearch] = useState('');

  useEffect(() => { if (visible) setSearch(''); }, [visible]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    if (!q) return chains;
    return chains.filter(c => c.name.toLowerCase().includes(q) || c.key.toLowerCase().includes(q));
  }, [chains, search]);

  return (
    <Modal visible={visible} animationType="slide" onRequestClose={onClose}>
      <SafeAreaView style={cp.container}>
        <View style={cp.header}>
          <TouchableOpacity onPress={onClose} hitSlop={{ top: 8, bottom: 8, left: 8, right: 8 }}>
            <Ionicons name="arrow-back" size={22} color="#1A1A1A" />
          </TouchableOpacity>
          <Text style={cp.title}>Networks</Text>
          <View style={{ width: 22 }} />
        </View>

        <View style={cp.searchRow}>
          <Ionicons name="search" size={16} color="#AAA" />
          <TextInput
            style={cp.searchInput}
            placeholder="Search networks"
            placeholderTextColor="#AAA"
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

        <FlatList
          data={filtered}
          keyExtractor={item => String(item.id)}
          keyboardShouldPersistTaps="handled"
          renderItem={({ item }) => (
            <TouchableOpacity
              style={cp.row}
              onPress={() => { onSelect(item.id); onClose(); }}
              activeOpacity={0.7}
            >
              <View style={cp.chainIcon}>
                {chainLogoUri(item) ? (
                  <Image source={{ uri: chainLogoUri(item) }} style={cp.chainImg} />
                ) : (
                  <View style={[cp.chainImg, cp.chainFallback]}>
                    <Text style={cp.chainLetter}>{item.key[0]?.toUpperCase()}</Text>
                  </View>
                )}
              </View>
              <Text style={cp.chainName}>{item.name}</Text>
              <View style={[cp.radio, item.id === selectedId && cp.radioActive]}>
                {item.id === selectedId && <View style={cp.radioDot} />}
              </View>
            </TouchableOpacity>
          )}
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ paddingBottom: 24 }}
        />
      </SafeAreaView>
    </Modal>
  );
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
  visible, mode, userBalances, onSelect, onClose, allTokens, tokensLoading, chainId, onChainChange,
}: Props) {
  const [search, setSearch] = useState('');
  const [chains, setChains] = useState<LiFiChain[]>([]);
  const [chainsLoading, setChainsLoading] = useState(true);
  const [chainPickerVisible, setChainPickerVisible] = useState(false);
  const [addrToken, setAddrToken] = useState<LiFiToken | null>(null);
  const [addrLoading, setAddrLoading] = useState(false);
  const [addrNotFound, setAddrNotFound] = useState(false);
  const addrTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    getLiFiChains()
      .then(setChains)
      .catch(() => {})
      .finally(() => setChainsLoading(false));
  }, []);

  useEffect(() => {
    if (visible) { setSearch(''); setAddrToken(null); setAddrNotFound(false); }
  }, [visible]);

  // Address lookup — debounced 600ms
  const isAddressSearch = search.trim().startsWith('0x') && search.trim().length >= 40;
  useEffect(() => {
    if (!isAddressSearch) { setAddrToken(null); setAddrNotFound(false); return; }
    setAddrLoading(true); setAddrToken(null); setAddrNotFound(false);
    if (addrTimer.current) clearTimeout(addrTimer.current);
    addrTimer.current = setTimeout(async () => {
      const token = await getLiFiToken(chainId, search.trim()).catch(() => null);
      setAddrToken(token);
      setAddrNotFound(!token);
      setAddrLoading(false);
    }, 600);
    return () => { if (addrTimer.current) clearTimeout(addrTimer.current); };
  }, [search, chainId]);

  // Inline chips: always include selected chain, fill rest from list
  const inlineChains = useMemo(() => {
    if (chains.length === 0) return [];
    const selected = chains.find(c => c.id === chainId);
    const others = chains.filter(c => c.id !== chainId).slice(0, INLINE_CHAIN_COUNT);
    const inline = selected ? [selected, ...others].slice(0, INLINE_CHAIN_COUNT) : others;
    return inline;
  }, [chains, chainId]);

  const remainingCount = chains.length - inlineChains.length;

  const heldTokens = useMemo(() => {
    if (mode === 'to') return [];
    return userBalances
      .filter(b => Number(b.balance) > 0)
      .map(b => {
        const match = allTokens.find(t => t.symbol.toLowerCase() === b.symbol.toLowerCase());
        return match ? { token: match, balance: b.balance, usdValue: b.usdValue } : null;
      })
      .filter(Boolean) as { token: LiFiToken; balance: string; usdValue: string }[];
  }, [userBalances, allTokens, mode]);

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

  const noResults = sections.length === 0 && search.trim().length > 0 && !tokensLoading;
  const noTokens = sections.length === 0 && search.trim().length === 0 && (allTokens.length === 0 || !!tokensLoading);

  return (
    <>
      <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose}>
        <Pressable style={s.overlay} onPress={onClose}>
          <Pressable style={s.sheet} onPress={() => {}}>
            <View style={s.handle} />
            <Text style={s.title}>Select Token</Text>

            {onChainChange && (
              <View style={s.chainRow}>
                {chainsLoading ? (
                  <ActivityIndicator size="small" color={GOLD} />
                ) : (
                  <>
                    {inlineChains.map(chain => (
                      <ChainChip
                        key={chain.id}
                        chain={chain}
                        selected={chain.id === chainId}
                        onPress={() => onChainChange(chain.id)}
                      />
                    ))}
                    {remainingCount > 0 && (
                      <TouchableOpacity style={s.moreBtn} onPress={() => setChainPickerVisible(true)} activeOpacity={0.75}>
                        <Text style={s.moreBtnText}>{remainingCount}+</Text>
                        <Ionicons name="chevron-down" size={11} color="#FFF" />
                      </TouchableOpacity>
                    )}
                  </>
                )}
              </View>
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

            {/* Address search result */}
          {isAddressSearch && (
            addrLoading ? (
              <View style={s.addrRow}>
                <ActivityIndicator size="small" color={GOLD} />
                <Text style={s.addrLabel}>Looking up contract…</Text>
              </View>
            ) : addrToken ? (
              <TouchableOpacity
                style={s.addrRow}
                onPress={() => { onSelect(addrToken, chainId); onClose(); }}
                activeOpacity={0.7}
              >
                {addrToken.logoURI
                  ? <Image source={{ uri: addrToken.logoURI }} style={s.addrIcon} />
                  : <View style={[s.addrIcon, s.addrIconFallback]}><Text style={s.addrIconLetter}>{addrToken.symbol[0]}</Text></View>
                }
                <View style={{ flex: 1 }}>
                  <Text style={s.addrSymbol}>{addrToken.symbol}</Text>
                  <Text style={s.addrName} numberOfLines={1}>{addrToken.name}</Text>
                  <Text style={s.addrAddress} numberOfLines={1}>{addrToken.address}</Text>
                </View>
                <View style={s.addrBadge}><Text style={s.addrBadgeText}>Add</Text></View>
              </TouchableOpacity>
            ) : addrNotFound ? (
              <View style={s.addrRow}>
                <Ionicons name="alert-circle-outline" size={20} color="#DC2626" />
                <Text style={[s.addrLabel, { color: '#DC2626' }]}>Token not found on this chain</Text>
              </View>
            ) : null
          )}

          {noResults ? (
              <View style={s.emptyWrap}>
                <Text style={s.emptyText}>No tokens found for "{search}"</Text>
                <Text style={s.emptyHint}>Try searching by symbol, name, or contract address</Text>
              </View>
            ) : noTokens ? (
              <View style={s.emptyWrap}>
                <ActivityIndicator size="small" color={GOLD} />
                <Text style={s.emptyText}>{tokensLoading ? 'Loading tokens…' : 'No tokens available'}</Text>
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

      <ChainPickerModal
        visible={chainPickerVisible}
        chains={chains}
        selectedId={chainId}
        onSelect={(id) => { onChainChange?.(id); }}
        onClose={() => setChainPickerVisible(false)}
      />
    </>
  );
}

// Chain chip styles
const cs = StyleSheet.create({
  chip: {
    flexDirection: 'row', alignItems: 'center', gap: 6,
    backgroundColor: '#1A1A1A', borderRadius: 20,
    paddingHorizontal: 10, paddingVertical: 7,
    borderWidth: 1.5, borderColor: 'transparent',
  },
  chipActive: { borderColor: GOLD },
  chipImg: { width: 18, height: 18, borderRadius: 9 },
  chipImgFallback: {
    width: 18, height: 18, borderRadius: 9,
    backgroundColor: '#444', alignItems: 'center', justifyContent: 'center',
  },
  chipImgLetter: { fontSize: 9, fontWeight: '700', color: '#FFF' },
  chipLabel: { fontSize: 11, fontWeight: '700', color: '#AAA' },
  chipLabelActive: { color: GOLD },
});

// Chain picker modal styles
const cp = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FFF' },
  header: {
    flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between',
    paddingHorizontal: 20, paddingVertical: 16, borderBottomWidth: 1, borderBottomColor: '#F0F0F0',
  },
  title: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },
  searchRow: {
    flexDirection: 'row', alignItems: 'center', gap: 8,
    backgroundColor: '#F5F5F5', borderRadius: 12, paddingHorizontal: 14, paddingVertical: 10,
    margin: 16,
  },
  searchInput: { flex: 1, fontSize: 15, color: '#1A1A1A' },
  row: {
    flexDirection: 'row', alignItems: 'center', gap: 14,
    paddingHorizontal: 20, paddingVertical: 14,
    borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: '#F0F0F0',
  },
  chainIcon: { width: 40, height: 40 },
  chainImg: { width: 40, height: 40, borderRadius: 20 },
  chainFallback: { backgroundColor: '#E0E0E0', alignItems: 'center', justifyContent: 'center' },
  chainLetter: { fontSize: 16, fontWeight: '700', color: '#555' },
  chainName: { flex: 1, fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
  radio: {
    width: 22, height: 22, borderRadius: 11,
    borderWidth: 2, borderColor: '#CCC',
    alignItems: 'center', justifyContent: 'center',
  },
  radioActive: { borderColor: GOLD },
  radioDot: { width: 10, height: 10, borderRadius: 5, backgroundColor: GOLD },
});

// Token picker bottom sheet styles
const s = StyleSheet.create({
  overlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.45)', justifyContent: 'flex-end' },
  sheet: {
    backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24,
    padding: 20, maxHeight: '82%',
  },
  handle: {
    width: 40, height: 4, borderRadius: 2, backgroundColor: '#E0E0E0',
    alignSelf: 'center', marginBottom: 16,
  },
  title: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginBottom: 16, textAlign: 'center' },
  chainRow: {
    flexDirection: 'row', alignItems: 'center', gap: 10,
    marginBottom: 16,
  },
  moreBtn: {
    flexDirection: 'row', alignItems: 'center', gap: 3,
    backgroundColor: '#1A1A1A', borderRadius: 20,
    paddingHorizontal: 10, paddingVertical: 7,
    borderWidth: 1.5, borderColor: 'transparent',
  },
  moreBtnText: { fontSize: 11, fontWeight: '700', color: '#AAA' },
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
  addrRow: { flexDirection: 'row', alignItems: 'center', gap: 12, paddingVertical: 12, paddingHorizontal: 4, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: '#F0F0F0', marginBottom: 4 },
  addrIcon: { width: 40, height: 40, borderRadius: 20 },
  addrIconFallback: { backgroundColor: GOLD, alignItems: 'center', justifyContent: 'center' },
  addrIconLetter: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  addrLabel: { fontSize: 13, color: '#888' },
  addrSymbol: { fontSize: 15, fontWeight: '700', color: '#1A1A1A' },
  addrName: { fontSize: 12, color: '#888', marginTop: 1 },
  addrAddress: { fontSize: 10, color: '#BBB', marginTop: 2, fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace' },
  addrBadge: { backgroundColor: '#1A1A1A', borderRadius: 10, paddingHorizontal: 10, paddingVertical: 4 },
  addrBadgeText: { fontSize: 11, fontWeight: '700', color: '#FFF' },
  emptyWrap: { flex: 1, alignItems: 'center', justifyContent: 'center', paddingVertical: 48, gap: 8 },
  emptyText: { fontSize: 14, fontWeight: '600', color: '#555', textAlign: 'center' },
  emptyHint: { fontSize: 12, color: '#AAA', textAlign: 'center' },
});
