import React, { useState, useMemo, useEffect, useRef } from 'react';
import {
  Modal, View, Text, TextInput, TouchableOpacity,
  StyleSheet, Pressable, SectionList, FlatList, Image,
  ActivityIndicator, SafeAreaView, Platform, ScrollView,
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
const PAGE_SIZE = 50;

interface Props {
  visible: boolean;
  mode: 'from' | 'to';
  userBalances: WalletBalanceWithChain[];
  onSelect: (token: LiFiToken, chainId: number) => void;
  onClose: () => void;
  allTokens: LiFiToken[];
  allChainsTokens?: LiFiToken[];   // combined list across all chains
  allChainsLoading?: boolean;
  allChainsHasMore?: boolean;
  onAllChainsLoadMore?: () => void;
  onAllChainsSearchChange?: (q: string) => void;
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
  const label = (chain.key || chain.name || '').toUpperCase().slice(0, 4);
  return (
    <TouchableOpacity style={[cs.chip, selected && cs.chipActive]} onPress={onPress} activeOpacity={0.75}>
      {chainLogoUri(chain) ? (
        <Image source={{ uri: chainLogoUri(chain) }} style={cs.chipImg} />
      ) : (
        <View style={cs.chipImgFallback}>
          <Text style={cs.chipImgLetter}>{chain.key[0]?.toUpperCase()}</Text>
        </View>
      )}
      <Text
        style={[cs.chipLabel, selected && cs.chipLabelActive]}
        numberOfLines={1}
        ellipsizeMode="clip"
      >
        {label}
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
  token, chainId, balance, usdValue, chainBadge, onPress,
}: {
  token: LiFiToken; chainId: number; balance?: string; usdValue?: string;
  chainBadge?: string; onPress: () => void;
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
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
          <Text style={s.symbol}>{token.symbol}</Text>
          {chainBadge && (
            <View style={s.chainBadge}>
              <Text style={s.chainBadgeText}>{chainBadge}</Text>
            </View>
          )}
        </View>
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
  visible, mode, userBalances, onSelect, onClose, allTokens, allChainsTokens, allChainsLoading,
  allChainsHasMore, onAllChainsLoadMore, onAllChainsSearchChange,
  tokensLoading, chainId, onChainChange,
}: Props) {
  const [search, setSearch] = useState('');
  const [chains, setChains] = useState<LiFiChain[]>([]);
  const [chainsLoading, setChainsLoading] = useState(true);
  const [chainPickerVisible, setChainPickerVisible] = useState(false);
  const [selectedChainId, setSelectedChainId] = useState<number | 'all'>('all');
  const [visibleCount, setVisibleCount] = useState(PAGE_SIZE);
  const [addrToken, setAddrToken] = useState<LiFiToken | null>(null);
  const [addrChainId, setAddrChainId] = useState<number | null>(null);
  const [addrLoading, setAddrLoading] = useState(false);
  const [addrNotFound, setAddrNotFound] = useState(false);
  const addrTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Build chainId → name map for badges
  const chainNameMap = useMemo(() => {
    const m = new Map<number, string>();
    chains.forEach(c => m.set(c.id, c.key.toUpperCase()));
    return m;
  }, [chains]);

  useEffect(() => {
    getLiFiChains()
      .then(setChains)
      .catch(() => {})
      .finally(() => setChainsLoading(false));
  }, []);

  useEffect(() => {
    if (visible) {
      setSearch('');
      setAddrToken(null);
      setAddrChainId(null);
      setAddrNotFound(false);
      setSelectedChainId('all');
      setVisibleCount(PAGE_SIZE);
    }
  }, [visible]);

  // Address lookup — debounced 600ms. When the user has the "All" chip
  // selected we fan out across every LiFi-supported chain in parallel and
  // pick the first hit, because the pasted contract is almost always on a
  // chain other than whichever `chainId` happens to be passed in as a prop.
  const isAddressSearch = search.trim().startsWith('0x') && search.trim().length >= 40;
  useEffect(() => {
    if (!isAddressSearch) {
      setAddrToken(null);
      setAddrChainId(null);
      setAddrNotFound(false);
      return;
    }
    setAddrLoading(true);
    setAddrToken(null);
    setAddrChainId(null);
    setAddrNotFound(false);
    if (addrTimer.current) clearTimeout(addrTimer.current);
    addrTimer.current = setTimeout(async () => {
      const trimmed = search.trim();
      const shouldFanOut = selectedChainId === 'all' && chains.length > 0;
      if (shouldFanOut) {
        const candidateIds = chains.map((c) => c.id);
        const results = await Promise.allSettled(
          candidateIds.map((id) => getLiFiToken(id, trimmed)),
        );
        let hitToken: LiFiToken | null = null;
        let hitChainId: number | null = null;
        for (let i = 0; i < results.length; i++) {
          const r = results[i];
          if (r.status === 'fulfilled' && r.value) {
            hitToken = r.value;
            hitChainId = candidateIds[i];
            break;
          }
        }
        setAddrToken(hitToken);
        setAddrChainId(hitChainId);
        setAddrNotFound(!hitToken);
      } else {
        const lookupChainId = typeof selectedChainId === 'number' ? selectedChainId : chainId;
        const token = await getLiFiToken(lookupChainId, trimmed).catch(() => null);
        setAddrToken(token);
        setAddrChainId(token ? lookupChainId : null);
        setAddrNotFound(!token);
      }
      setAddrLoading(false);
    }, 600);
    return () => {
      if (addrTimer.current) clearTimeout(addrTimer.current);
    };
  }, [search, chainId, selectedChainId, chains, isAddressSearch]);

  // Forward typing into the search box to the parent's all-chains fetcher
  // (debounced 350ms) so the BE-paginated list resets to a fresh query
  // page. We skip this for address searches — they have their own path
  // above — and for non-"All" chips since per-chain results are already
  // pre-fetched client-side.
  const searchTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastForwardedSearchRef = useRef<string>('');
  const isAllChainsSelected = selectedChainId === 'all';
  useEffect(() => {
    if (!onAllChainsSearchChange) return;
    if (!isAllChainsSelected) return;
    if (isAddressSearch) return;
    const next = search.trim();
    if (next === lastForwardedSearchRef.current) return;
    if (searchTimer.current) clearTimeout(searchTimer.current);
    searchTimer.current = setTimeout(() => {
      lastForwardedSearchRef.current = next;
      onAllChainsSearchChange(next);
    }, 350);
    return () => {
      if (searchTimer.current) clearTimeout(searchTimer.current);
    };
  }, [search, isAllChainsSelected, isAddressSearch, onAllChainsSearchChange]);

  // Inline chips: always include selected chain, fill rest from list
  const inlineChains = useMemo(() => {
    if (chains.length === 0) return [];
    const selected = chains.find(c => c.id === chainId);
    const others = chains.filter(c => c.id !== chainId).slice(0, INLINE_CHAIN_COUNT);
    const inline = selected ? [selected, ...others].slice(0, INLINE_CHAIN_COUNT) : others;
    return inline;
  }, [chains, chainId]);

  const remainingCount = chains.length - inlineChains.length;

  // Source list: "All" uses combined cross-chain tokens, specific chain uses that chain's list
  const isAllChains = selectedChainId === 'all';
  const sourceTokens = isAllChains
    ? (allChainsTokens ?? allTokens)
    : allTokens.filter(t => !t.chainId || t.chainId === selectedChainId);

  const heldTokens = useMemo(() => {
    if (mode === 'to') return [];
    return userBalances
      .filter(b => Number(b.balance) > 0)
      .map(b => {
        const match = (allChainsTokens ?? allTokens).find(
          t => t.symbol.toLowerCase() === b.symbol.toLowerCase()
        );
        return match ? { token: match, balance: b.balance, usdValue: b.usdValue } : null;
      })
      .filter(Boolean) as { token: LiFiToken; balance: string; usdValue: string }[];
  }, [userBalances, allTokens, allChainsTokens, mode]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    const heldAddresses = new Set(heldTokens.map(h => h.token.address));
    return sourceTokens.filter(
      t =>
        !heldAddresses.has(t.address) &&
        (q === '' ||
          t.symbol.toLowerCase().includes(q) ||
          t.name.toLowerCase().includes(q) ||
          t.address.toLowerCase().startsWith(q))
    );
  }, [sourceTokens, search, heldTokens]);

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

  // Paginate the "ALL TOKENS" list
  const paginatedFiltered = useMemo(() => filtered.slice(0, visibleCount), [filtered, visibleCount]);

  const sections = useMemo(() => {
    const result: { title: string; data: (LiFiToken & { _held?: { balance: string; usdValue: string } })[] }[] = [];
    if (filteredHeld.length > 0) {
      result.push({ title: 'YOUR TOKENS', data: filteredHeld.map(h => ({ ...h.token, _held: { balance: h.balance, usdValue: h.usdValue } })) });
    }
    if (paginatedFiltered.length > 0) {
      result.push({ title: 'ALL TOKENS', data: paginatedFiltered });
    }
    return result;
  }, [filteredHeld, paginatedFiltered]);

  const isLoading = isAllChains ? !!allChainsLoading : !!tokensLoading;
  const noResults = sections.length === 0 && search.trim().length > 0 && !isAddressSearch && !isLoading;
  const noTokens = sections.length === 0 && search.trim().length === 0 && isLoading;

  return (
    <>
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
                {/* All chip */}
                <TouchableOpacity
                  style={[cs.chip, cs.chipAll, isAllChains && cs.chipActive]}
                  onPress={() => { setSelectedChainId('all'); setVisibleCount(PAGE_SIZE); }}
                  activeOpacity={0.75}
                >
                  <Text
                    style={[cs.chipLabel, cs.chipLabelStandalone, isAllChains && cs.chipLabelActive]}
                    numberOfLines={1}
                  >
                    All
                  </Text>
                </TouchableOpacity>

                {chainsLoading ? (
                  <ActivityIndicator size="small" color={GOLD} style={{ marginHorizontal: 8 }} />
                ) : (
                  <>
                    {inlineChains.map(chain => (
                      <ChainChip
                        key={chain.id}
                        chain={chain}
                        selected={selectedChainId === chain.id}
                        onPress={() => {
                          setSelectedChainId(chain.id);
                          setVisibleCount(PAGE_SIZE);
                          onChainChange(chain.id);
                        }}
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
                onPress={() => { onSelect(addrToken, addrChainId ?? chainId); onClose(); }}
                activeOpacity={0.7}
              >
                {addrToken.logoURI
                  ? <Image source={{ uri: addrToken.logoURI }} style={s.addrIcon} />
                  : <View style={[s.addrIcon, s.addrIconFallback]}><Text style={s.addrIconLetter}>{addrToken.symbol[0]}</Text></View>
                }
                <View style={{ flex: 1 }}>
                  <View style={s.addrSymbolRow}>
                    <Text style={s.addrSymbol}>{addrToken.symbol}</Text>
                    {addrChainId != null && chainNameMap.get(addrChainId) && (
                      <View style={s.chainBadge}>
                        <Text style={s.chainBadgeText}>{chainNameMap.get(addrChainId)}</Text>
                      </View>
                    )}
                  </View>
                  <Text style={s.addrName} numberOfLines={1}>{addrToken.name}</Text>
                  <Text style={s.addrAddress} numberOfLines={1}>{addrToken.address}</Text>
                </View>
                <View style={s.addrBadge}><Text style={s.addrBadgeText}>Add</Text></View>
              </TouchableOpacity>
            ) : addrNotFound ? (
              <View style={s.addrRow}>
                <Ionicons name="alert-circle-outline" size={20} color="#DC2626" />
                <Text style={[s.addrLabel, { color: '#DC2626' }]}>
                  {selectedChainId === 'all'
                    ? 'Token not found on any supported chain'
                    : 'Token not found on this chain'}
                </Text>
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
                <Text style={s.emptyText}>Loading tokens…</Text>
              </View>
            ) : (
              <SectionList
                sections={sections}
                keyExtractor={(item, index) => `${item.address}-${item.chainId ?? 0}-${index}`}
                renderSectionHeader={({ section }) => (
                  <View style={s.sectionHeaderRow}>
                    <Text style={s.sectionLabel}>{section.title}</Text>
                    {section.title === 'ALL TOKENS' && filtered.length > visibleCount && (
                      <Text style={s.sectionCount}>{visibleCount}/{filtered.length}</Text>
                    )}
                  </View>
                )}
                renderItem={({ item }) => {
                  const held = (item as any)._held;
                  const tokenChainId = item.chainId ?? chainId;
                  const chainBadge = isAllChains && item.chainId ? chainNameMap.get(item.chainId) : undefined;
                  return (
                    <TokenRow
                      token={item}
                      chainId={tokenChainId}
                      balance={held?.balance}
                      usdValue={held?.usdValue}
                      chainBadge={chainBadge}
                      onPress={() => { onSelect(item, tokenChainId); onClose(); }}
                    />
                  );
                }}
                onEndReached={() => {
                  // Client-side window grows for the per-chain path (cheap
                  // slice over the already-loaded `allTokens`). For the
                  // all-chains path the server holds the canonical list,
                  // so we ask the parent to fetch the next page.
                  setVisibleCount(c => c + PAGE_SIZE);
                  if (isAllChains && allChainsHasMore && !allChainsLoading) {
                    onAllChainsLoadMore?.();
                  }
                }}
                onEndReachedThreshold={0.3}
                ListFooterComponent={
                  isAllChains && allChainsLoading && sections.length > 0 ? (
                    <View style={s.footerLoader}>
                      <ActivityIndicator size="small" color={GOLD} />
                    </View>
                  ) : null
                }
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
    flexDirection: 'row',
    alignItems: 'center',
    flexShrink: 0,
    backgroundColor: '#1A1A1A',
    borderRadius: 20,
    paddingLeft: 8,
    paddingRight: 14,
    paddingVertical: 8,
    borderWidth: 1.5,
    borderColor: 'transparent',
    minHeight: 34,
  },
  chipAll: {
    paddingHorizontal: 16,
  },
  chipActive: { borderColor: GOLD, backgroundColor: '#2A2516' },
  chipImg: { width: 20, height: 20, borderRadius: 10 },
  chipImgFallback: {
    width: 20, height: 20, borderRadius: 10,
    backgroundColor: '#444', alignItems: 'center', justifyContent: 'center',
  },
  chipImgLetter: { fontSize: 10, fontWeight: '700', color: '#FFF' },
  chipLabel: {
    fontSize: 13,
    fontWeight: '700',
    color: '#D4D4D4',
    marginLeft: 8,
    letterSpacing: 0.3,
    includeFontPadding: false,
    textAlignVertical: 'center',
  },
  chipLabelStandalone: { marginLeft: 0 },
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
  chainScroll: { marginBottom: 16 },
  chainRow: {
    flexDirection: 'row', alignItems: 'center', gap: 8,
    paddingRight: 8,
  },
  moreBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    flexShrink: 0,
    backgroundColor: '#1A1A1A',
    borderRadius: 20,
    paddingHorizontal: 14,
    paddingVertical: 8,
    borderWidth: 1.5,
    borderColor: 'transparent',
    minHeight: 34,
  },
  moreBtnText: {
    fontSize: 13,
    fontWeight: '700',
    color: '#D4D4D4',
    marginRight: 4,
    letterSpacing: 0.3,
    includeFontPadding: false,
    textAlignVertical: 'center',
  },
  searchRow: {
    flexDirection: 'row', alignItems: 'center', gap: 8,
    backgroundColor: '#F5F5F5', borderRadius: 10, paddingHorizontal: 12, paddingVertical: 8, marginBottom: 8,
  },
  searchInput: { flex: 1, fontSize: 14, color: '#1A1A1A' },
  sectionHeaderRow: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', backgroundColor: '#FFF', paddingVertical: 8 },
  sectionLabel: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1 },
  sectionCount: { fontSize: 10, color: '#CCC' },
  chainBadge: { backgroundColor: '#1A1A1A', borderRadius: 6, paddingHorizontal: 5, paddingVertical: 1 },
  chainBadgeText: { fontSize: 9, fontWeight: '700', color: '#AAA' },
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
  addrSymbolRow: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  addrSymbol: { fontSize: 15, fontWeight: '700', color: '#1A1A1A' },
  addrName: { fontSize: 12, color: '#888', marginTop: 1 },
  addrAddress: { fontSize: 10, color: '#BBB', marginTop: 2, fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace' },
  addrBadge: { backgroundColor: '#1A1A1A', borderRadius: 10, paddingHorizontal: 10, paddingVertical: 4 },
  addrBadgeText: { fontSize: 11, fontWeight: '700', color: '#FFF' },
  emptyWrap: { flex: 1, alignItems: 'center', justifyContent: 'center', paddingVertical: 48, gap: 8 },
  emptyText: { fontSize: 14, fontWeight: '600', color: '#555', textAlign: 'center' },
  emptyHint: { fontSize: 12, color: '#AAA', textAlign: 'center' },
  footerLoader: { paddingVertical: 18, alignItems: 'center' },
});
