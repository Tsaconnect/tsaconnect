// components/swap/TokenPickerModal.tsx
//
// Token picker modal (swap / easyswap). Restyled onto the TSA Signature design
// system (@/theme tokens + @/components/ui primitives). The chain-chip row,
// debounced token/address search, cross-chain address fan-out, held-token
// section, BE-paginated "All chains" list, and the onSelect(token, chainId)
// contract are all preserved verbatim — this is a visual/structural restyle
// only. Props interface and the default export are unchanged.
import React, { useState, useMemo, useEffect, useRef } from 'react';
import {
  Modal, View, TextInput, TouchableOpacity,
  StyleSheet, Pressable, SectionList, FlatList, Image,
  ActivityIndicator, Platform, ScrollView,
} from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import type { LiFiToken, LiFiChain } from '../../services/lifi';
import { getLiFiChains, getLiFiToken } from '../../services/lifi';
import type { WalletBalanceWithChain } from '../../services/walletApi';
import { useTheme } from '@/theme';
import type { Theme } from '@/theme';
import {
  Text,
  Icon,
  PressableScale,
} from '@/components/ui';
import TokenAvatar from '@/components/wallet/TokenAvatar';
import { isKnownContract } from '@/utils/tokenLogo';

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
  const theme = useTheme();
  const { colors, spacing, radius } = theme;
  const label = (chain.key || chain.name || '').toUpperCase().slice(0, 4);
  const uri = chainLogoUri(chain);
  return (
    <PressableScale
      onPress={onPress}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        flexShrink: 0,
        backgroundColor: selected ? colors.accentMuted : colors.surfaceAlt,
        borderRadius: radius.pill,
        paddingLeft: spacing.sm,
        paddingRight: spacing.md,
        paddingVertical: spacing.sm,
        borderWidth: 1.5,
        borderColor: selected ? colors.accent : 'transparent',
        minHeight: 34,
        gap: spacing.sm,
      }}
    >
      {uri ? (
        <Image source={{ uri }} style={{ width: 20, height: 20, borderRadius: 10 }} />
      ) : (
        <View
          style={{
            width: 20,
            height: 20,
            borderRadius: 10,
            backgroundColor: colors.surfaceElevated,
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          <Text variant="overline" weight="700">
            {chain.key[0]?.toUpperCase()}
          </Text>
        </View>
      )}
      <Text
        variant="caption"
        weight="700"
        color={selected ? 'accent' : 'textSecondary'}
        numberOfLines={1}
        ellipsizeMode="clip"
      >
        {label}
      </Text>
    </PressableScale>
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
  const theme = useTheme();
  const { colors, spacing } = theme;
  const [search, setSearch] = useState('');

  useEffect(() => { if (visible) setSearch(''); }, [visible]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    if (!q) return chains;
    return chains.filter(c => c.name.toLowerCase().includes(q) || c.key.toLowerCase().includes(q));
  }, [chains, search]);

  return (
    <Modal visible={visible} animationType="slide" onRequestClose={onClose}>
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            justifyContent: 'space-between',
            paddingHorizontal: spacing.xl,
            paddingVertical: spacing.lg,
            borderBottomWidth: 1,
            borderBottomColor: colors.hairline,
          }}
        >
          <TouchableOpacity onPress={onClose} hitSlop={{ top: 8, bottom: 8, left: 8, right: 8 }}>
            <Icon name="chevronLeft" size={22} color={colors.textPrimary} />
          </TouchableOpacity>
          <Text variant="title3" weight="700">Networks</Text>
          <View style={{ width: 22 }} />
        </View>

        <View
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            gap: spacing.sm,
            backgroundColor: colors.surfaceAlt,
            borderRadius: 12,
            paddingHorizontal: spacing.md,
            paddingVertical: spacing.md,
            margin: spacing.lg,
          }}
        >
          <Icon name="search" size={16} color={colors.textMuted} />
          <TextInput
            style={{ flex: 1, fontSize: 15, color: colors.textPrimary }}
            placeholder="Search networks"
            placeholderTextColor={colors.textMuted}
            value={search}
            onChangeText={setSearch}
            autoCapitalize="none"
            autoCorrect={false}
          />
          {search.length > 0 && (
            <TouchableOpacity onPress={() => setSearch('')} hitSlop={{ top: 8, bottom: 8, left: 8, right: 8 }}>
              <Icon name="closeCircle" size={16} color={colors.textMuted} />
            </TouchableOpacity>
          )}
        </View>

        <FlatList
          data={filtered}
          keyExtractor={item => String(item.id)}
          keyboardShouldPersistTaps="handled"
          renderItem={({ item }) => {
            const uri = chainLogoUri(item);
            const active = item.id === selectedId;
            return (
              <TouchableOpacity
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.md,
                  paddingHorizontal: spacing.xl,
                  paddingVertical: spacing.md,
                  borderBottomWidth: StyleSheet.hairlineWidth,
                  borderBottomColor: colors.hairline,
                }}
                onPress={() => { onSelect(item.id); onClose(); }}
                activeOpacity={0.7}
              >
                {uri ? (
                  <Image source={{ uri }} style={{ width: 40, height: 40, borderRadius: 20 }} />
                ) : (
                  <View
                    style={{
                      width: 40,
                      height: 40,
                      borderRadius: 20,
                      backgroundColor: colors.surfaceAlt,
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                  >
                    <Text variant="bodyMed" weight="700" color="textSecondary">
                      {item.key[0]?.toUpperCase()}
                    </Text>
                  </View>
                )}
                <Text variant="bodySm" weight="600" style={{ flex: 1 }}>
                  {item.name}
                </Text>
                <View
                  style={{
                    width: 22,
                    height: 22,
                    borderRadius: 11,
                    borderWidth: 2,
                    borderColor: active ? colors.accent : colors.border,
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  {active && (
                    <View style={{ width: 10, height: 10, borderRadius: 5, backgroundColor: colors.accent }} />
                  )}
                </View>
              </TouchableOpacity>
            );
          }}
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ paddingBottom: spacing['2xl'] }}
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
  const theme = useTheme();
  const { colors, spacing, radius } = theme;
  const displayBalance = balance !== undefined ? formatBalance(balance) : undefined;
  const displayUSD = balance !== undefined ? formatUSD(balance, usdValue ?? '0', token.priceUSD) : null;

  return (
    <PressableScale
      onPress={onPress}
      style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.md, gap: spacing.md }}
    >
      <TokenAvatar
        symbol={token.symbol}
        iconUrl={token.logoURI}
        curated={isKnownContract(token.chainId, token.address, token.symbol)}
        size={36}
      />
      <View style={{ flex: 1 }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
          <Text variant="bodySm" weight="700">{token.symbol}</Text>
          {chainBadge && (
            <View style={{ backgroundColor: colors.surfaceAlt, borderRadius: radius.sm, paddingHorizontal: 5, paddingVertical: 1 }}>
              <Text variant="overline" weight="700" color="textMuted">{chainBadge}</Text>
            </View>
          )}
        </View>
        <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 1 }}>
          {token.name}
        </Text>
      </View>
      {displayBalance !== undefined && (
        <View style={{ alignItems: 'flex-end' }}>
          <Text variant="bodySm" weight="600" numeric>{displayBalance}</Text>
          {displayUSD ? (
            // displayUSD is already a formatted "$X.XX" string (formatUSD) — render
            // verbatim; AmountText would re-parse and mangle it.
            <Text variant="caption" weight="500" color="textMuted">
              {displayUSD}
            </Text>
          ) : null}
        </View>
      )}
    </PressableScale>
  );
}

export default function TokenPickerModal({
  visible, mode, userBalances, onSelect, onClose, allTokens, allChainsTokens, allChainsLoading,
  allChainsHasMore, onAllChainsLoadMore, onAllChainsSearchChange,
  tokensLoading, chainId, onChainChange,
}: Props) {
  const theme = useTheme();
  const { colors, spacing, radius } = theme;
  const insets = useSafeAreaInsets();
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
        <Pressable style={{ flex: 1, backgroundColor: colors.overlay, justifyContent: 'flex-end' }} onPress={onClose}>
          <Pressable
            style={{
              backgroundColor: colors.surface,
              borderTopLeftRadius: radius.xl,
              borderTopRightRadius: radius.xl,
              padding: spacing.xl,
              // Keep the last rows above the Android nav bar / iOS home indicator.
              paddingBottom: Math.max(insets.bottom, spacing.xl),
              maxHeight: '82%',
            }}
            onPress={() => {}}
          >
            <View
              style={{
                width: 40,
                height: 4,
                borderRadius: 2,
                backgroundColor: colors.border,
                alignSelf: 'center',
                marginBottom: spacing.lg,
              }}
            />
            <Text variant="title3" weight="700" align="center" style={{ marginBottom: spacing.lg }}>
              Select Token
            </Text>

            {onChainChange && (
              <ScrollView
                horizontal
                showsHorizontalScrollIndicator={false}
                style={{ marginBottom: spacing.lg }}
                contentContainerStyle={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.sm,
                  paddingRight: spacing.sm,
                }}
              >
                {/* All chip */}
                <PressableScale
                  onPress={() => { setSelectedChainId('all'); setVisibleCount(PAGE_SIZE); }}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    flexShrink: 0,
                    backgroundColor: isAllChains ? colors.accentMuted : colors.surfaceAlt,
                    borderRadius: radius.pill,
                    paddingHorizontal: spacing.lg,
                    paddingVertical: spacing.sm,
                    borderWidth: 1.5,
                    borderColor: isAllChains ? colors.accent : 'transparent',
                    minHeight: 34,
                  }}
                >
                  <Text variant="caption" weight="700" color={isAllChains ? 'accent' : 'textSecondary'} numberOfLines={1}>
                    All
                  </Text>
                </PressableScale>

                {chainsLoading ? (
                  <ActivityIndicator size="small" color={colors.accent} style={{ marginHorizontal: spacing.sm }} />
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
                      <PressableScale
                        onPress={() => setChainPickerVisible(true)}
                        style={{
                          flexDirection: 'row',
                          alignItems: 'center',
                          flexShrink: 0,
                          backgroundColor: colors.surfaceAlt,
                          borderRadius: radius.pill,
                          paddingHorizontal: spacing.md,
                          paddingVertical: spacing.sm,
                          borderWidth: 1.5,
                          borderColor: 'transparent',
                          minHeight: 34,
                          gap: spacing.xs,
                        }}
                      >
                        <Text variant="caption" weight="700" color="textSecondary">{remainingCount}+</Text>
                        <Icon name="chevronDown" size={11} color={colors.textSecondary} />
                      </PressableScale>
                    )}
                  </>
                )}
              </ScrollView>
            )}

            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.sm,
                backgroundColor: colors.surfaceAlt,
                borderRadius: radius.md,
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.sm,
                marginBottom: spacing.sm,
              }}
            >
              <Icon name="search" size={16} color={colors.textMuted} />
              <TextInput
                style={{ flex: 1, fontSize: 14, color: colors.textPrimary }}
                placeholder="Search name, symbol or paste address"
                placeholderTextColor={colors.textMuted}
                value={search}
                onChangeText={setSearch}
                autoCapitalize="none"
                autoCorrect={false}
              />
              {search.length > 0 && (
                <TouchableOpacity onPress={() => setSearch('')} hitSlop={{ top: 8, bottom: 8, left: 8, right: 8 }}>
                  <Icon name="closeCircle" size={16} color={colors.textMuted} />
                </TouchableOpacity>
              )}
            </View>

            {/* Address search result */}
          {isAddressSearch && (
            addrLoading ? (
              <View style={addrRowStyle(theme)}>
                <ActivityIndicator size="small" color={colors.accent} />
                <Text variant="bodySm" color="textSecondary">Looking up contract…</Text>
              </View>
            ) : addrToken ? (
              <PressableScale
                style={addrRowStyle(theme)}
                onPress={() => { onSelect(addrToken, addrChainId ?? chainId); onClose(); }}
              >
                {/* Looked up by a pasted contract address: its symbol is
                    whatever the contract claims, so no registry logo. */}
                <TokenAvatar symbol={addrToken.symbol} iconUrl={addrToken.logoURI} curated={false} size={40} />
                <View style={{ flex: 1 }}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                    <Text variant="bodySm" weight="700">{addrToken.symbol}</Text>
                    {addrChainId != null && chainNameMap.get(addrChainId) && (
                      <View style={{ backgroundColor: colors.surfaceAlt, borderRadius: radius.sm, paddingHorizontal: 5, paddingVertical: 1 }}>
                        <Text variant="overline" weight="700" color="textMuted">{chainNameMap.get(addrChainId)}</Text>
                      </View>
                    )}
                  </View>
                  <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 1 }}>{addrToken.name}</Text>
                  <Text
                    variant="overline"
                    color="textDisabled"
                    numberOfLines={1}
                    style={{ marginTop: 2, fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace' }}
                  >
                    {addrToken.address}
                  </Text>
                </View>
                <View style={{ backgroundColor: colors.accent, borderRadius: radius.sm, paddingHorizontal: spacing.md, paddingVertical: spacing.xs }}>
                  <Text variant="caption" weight="700" style={{ color: colors.onAccent }}>Add</Text>
                </View>
              </PressableScale>
            ) : addrNotFound ? (
              <View style={addrRowStyle(theme)}>
                <Icon name="warning" size={20} color={colors.danger} />
                <Text variant="bodySm" color="danger">
                  {selectedChainId === 'all'
                    ? 'Token not found on any supported chain'
                    : 'Token not found on this chain'}
                </Text>
              </View>
            ) : null
          )}

          {noResults ? (
              <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', paddingVertical: spacing['4xl'], gap: spacing.sm }}>
                <Text variant="bodySm" weight="600" color="textSecondary" align="center">No tokens found for &quot;{search}&quot;</Text>
                <Text variant="caption" color="textMuted" align="center">Try searching by symbol, name, or contract address</Text>
              </View>
            ) : noTokens ? (
              <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', paddingVertical: spacing['4xl'], gap: spacing.sm }}>
                <ActivityIndicator size="small" color={colors.accent} />
                <Text variant="bodySm" weight="600" color="textSecondary" align="center">Loading tokens…</Text>
              </View>
            ) : (
              <SectionList
                sections={sections}
                // Shrink inside the maxHeight panel so the list scrolls instead
                // of overflowing off-screen on short devices.
                style={{ flexShrink: 1 }}
                keyExtractor={(item, index) => `${item.address}-${item.chainId ?? 0}-${index}`}
                renderSectionHeader={({ section }) => (
                  <View
                    style={{
                      flexDirection: 'row',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      backgroundColor: colors.surface,
                      paddingVertical: spacing.sm,
                    }}
                  >
                    <Text variant="overline" color="textMuted">{section.title}</Text>
                    {section.title === 'ALL TOKENS' && filtered.length > visibleCount && (
                      <Text variant="overline" color="textDisabled" numeric>{visibleCount}/{filtered.length}</Text>
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
                    <View style={{ paddingVertical: spacing.lg, alignItems: 'center' }}>
                      <ActivityIndicator size="small" color={colors.accent} />
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

// Shared row layout for the pasted-address lookup result.
function addrRowStyle(theme: Theme) {
  const { colors, spacing } = theme;
  return {
    flexDirection: 'row' as const,
    alignItems: 'center' as const,
    gap: spacing.md,
    paddingVertical: spacing.md,
    paddingHorizontal: spacing.xs,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: colors.hairline,
    marginBottom: spacing.xs,
  };
}
