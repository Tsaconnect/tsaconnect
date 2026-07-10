// screens/fund.tsx
//
// Crypto deposit / receive — searchable token×chain list with a chain filter,
// per-row copy + QR actions, and a QR bottom sheet for sharing the wallet
// address. Restyled onto the TSA Signature design system (theme tokens +
// @/components/ui primitives). All data (active wallet fetch, token×chain
// flattening, search/chain filtering, popular/rest split), clipboard copy +
// 2s reset, QR target state, share payload, and address truncation are
// unchanged — this is a visual/structural restyle only.
import React, { useState, useEffect, useMemo } from 'react';
import {
  View, TextInput, FlatList,
  Modal, Share, Image, Pressable,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import QRCode from 'react-native-qrcode-svg';
import { getActiveWallet } from '@/services/wallet';
import { NONEVM_CHAINS, isNonEVMChain, type AnyChainKey } from '@/constants/chains';
import { getTronAddress } from '@/services/chains/tronWallet';
import { useTokens } from '@/hooks/useTokens';
import { useTheme } from '@/theme';
import {
  Text,
  Icon,
  Button,
  Avatar,
  Badge,
  EmptyState,
  PressableScale,
} from '@/components/ui';

const POPULAR = ['USDT', 'USDC', 'BNB', 'ETH', 'MCGP'];

interface ReceiveItem {
  id: string;
  symbol: string;
  name: string;
  iconColor: string;
  iconUrl?: string;
  chainKey: AnyChainKey;
  chainName: string;
  chainShortName: string;
  chainIconColor: string;
  chainIconUrl?: string;
}

// Static Tron receive items — TRX (native) + USDT (TRC-20). Their deposit
// address is the derived Tron address, not the EVM one.
const TRON_RECEIVE_ITEMS: ReceiveItem[] = [
  {
    id: 'TRX-tron', symbol: 'TRX', name: 'Tron',
    iconColor: NONEVM_CHAINS.tron.iconColor,
    chainKey: 'tron', chainName: NONEVM_CHAINS.tron.name,
    chainShortName: NONEVM_CHAINS.tron.shortName, chainIconColor: NONEVM_CHAINS.tron.iconColor,
  },
  {
    id: 'USDT-tron', symbol: 'USDT', name: 'Tether USD',
    iconColor: '#26A17B',
    chainKey: 'tron', chainName: NONEVM_CHAINS.tron.name,
    chainShortName: NONEVM_CHAINS.tron.shortName, chainIconColor: NONEVM_CHAINS.tron.iconColor,
  },
];

interface QRTarget {
  item: ReceiveItem;
  address: string;
}

function truncateAddress(addr: string): string {
  if (!addr || addr.length < 12) return addr;
  return `${addr.slice(0, 6)}...${addr.slice(-4)}`;
}

function ChainFilterRow({
  chains, selected, onSelect,
}: {
  chains: { key: AnyChainKey; iconColor: string; iconUrl?: string; shortName: string }[];
  selected: AnyChainKey | 'all';
  onSelect: (k: AnyChainKey | 'all') => void;
}) {
  const { colors, spacing, radius } = useTheme();
  return (
    <View style={{ flexDirection: 'row', gap: spacing.sm }}>
      <PressableScale
        onPress={() => onSelect('all')}
        style={{
          width: 40,
          height: 40,
          borderRadius: radius.pill,
          backgroundColor: selected === 'all' ? colors.accentMuted : colors.surfaceAlt,
          alignItems: 'center',
          justifyContent: 'center',
          borderWidth: 2,
          borderColor: selected === 'all' ? colors.accent : 'transparent',
        }}
      >
        <Text variant="caption" weight="700" style={{ color: selected === 'all' ? colors.accent : colors.textMuted }}>
          All
        </Text>
      </PressableScale>
      {chains.map(c => (
        <PressableScale
          key={c.key}
          onPress={() => onSelect(c.key)}
          style={{
            width: 40,
            height: 40,
            borderRadius: radius.pill,
            backgroundColor: selected === c.key ? colors.accentMuted : colors.surfaceAlt,
            alignItems: 'center',
            justifyContent: 'center',
            borderWidth: 2,
            borderColor: selected === c.key ? colors.accent : 'transparent',
          }}
        >
          {c.iconUrl
            ? <Image source={{ uri: c.iconUrl }} style={{ width: 26, height: 26, borderRadius: 13 }} />
            : <Text variant="caption" weight="700" style={{ color: selected === c.key ? colors.accent : colors.textMuted }}>{c.shortName[0]}</Text>
          }
        </PressableScale>
      ))}
    </View>
  );
}

function ReceiveRow({
  item, address, copiedId, onCopy, onQR,
}: {
  item: ReceiveItem;
  address: string;
  copiedId: string | null;
  onCopy: (item: ReceiveItem) => void;
  onQR: (item: ReceiveItem) => void;
}) {
  const { colors, spacing, radius } = useTheme();
  const isCopied = copiedId === item.id;
  return (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        paddingHorizontal: spacing.lg,
        paddingVertical: spacing.md,
        gap: spacing.sm,
        borderBottomWidth: 1,
        borderBottomColor: colors.hairline,
      }}
    >
      {/* Token icon with chain overlay */}
      <View style={{ position: 'relative', width: 44, height: 44 }}>
        <Avatar name={item.symbol} uri={item.iconUrl} size={40} />
        <View
          style={{
            position: 'absolute',
            bottom: -2,
            right: -2,
            width: 18,
            height: 18,
            borderRadius: 9,
            alignItems: 'center',
            justifyContent: 'center',
            borderWidth: 1.5,
            borderColor: colors.surface,
            backgroundColor: item.chainIconColor,
          }}
        >
          {item.chainIconUrl
            ? <Image source={{ uri: item.chainIconUrl }} style={{ width: 14, height: 14, borderRadius: 7 }} />
            : <Text style={{ fontSize: 7, fontWeight: '700', color: colors.textInverse }}>{item.chainShortName[0]}</Text>
          }
        </View>
      </View>

      {/* Info */}
      <View style={{ flex: 1 }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginBottom: 3 }}>
          <Text variant="bodyMed" weight="700">{item.symbol}</Text>
          <Badge label={item.chainName} size="sm" color={colors.textSecondary} background={colors.surfaceAlt} />
        </View>
        <Text variant="caption" color="textMuted" style={{ fontFamily: 'monospace' }}>{truncateAddress(address)}</Text>
      </View>

      {/* Actions */}
      <PressableScale
        onPress={() => onQR(item)}
        style={{
          width: 36,
          height: 36,
          borderRadius: radius.pill,
          backgroundColor: colors.surfaceAlt,
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Icon name="scan" size={18} color={colors.textSecondary} />
      </PressableScale>
      <PressableScale
        onPress={() => onCopy(item)}
        style={{
          width: 36,
          height: 36,
          borderRadius: radius.pill,
          backgroundColor: isCopied ? colors.accentMuted : colors.surfaceAlt,
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Icon name={isCopied ? 'check' : 'copy'} size={18} color={isCopied ? colors.accent : colors.textSecondary} />
      </PressableScale>
    </View>
  );
}

function QRModal({ target, onClose }: { target: QRTarget | null; onClose: () => void }) {
  const { colors, spacing, radius } = useTheme();
  if (!target) return null;
  const { item, address } = target;

  const handleShare = async () => {
    await Share.share({
      message: `${item.symbol} on ${item.chainName}\n${address}`,
      title: `Receive ${item.symbol}`,
    });
  };

  return (
    <Modal visible={!!target} transparent animationType="slide" onRequestClose={onClose}>
      <Pressable style={{ flex: 1, backgroundColor: colors.overlay, justifyContent: 'flex-end' }} onPress={onClose}>
        <Pressable
          style={{
            backgroundColor: colors.surface,
            borderTopLeftRadius: radius.xl,
            borderTopRightRadius: radius.xl,
            padding: spacing['2xl'],
            paddingTop: spacing.lg,
            alignItems: 'center',
          }}
          onPress={() => {}}
        >
          <View style={{ width: 36, height: 4, borderRadius: 2, backgroundColor: colors.border, marginBottom: spacing.xl }} />

          {/* Token + chain header */}
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, alignSelf: 'flex-start', marginBottom: spacing['2xl'] }}>
            <Avatar name={item.symbol} uri={item.iconUrl} size={44} />
            <View>
              <Text variant="title3" weight="700">{item.symbol}</Text>
              <Text variant="bodySm" color="textMuted" style={{ marginTop: 2 }}>{item.chainName}</Text>
            </View>
          </View>

          {/* QR */}
          <View
            style={{
              padding: spacing.lg,
              borderRadius: radius.lg,
              backgroundColor: '#FFFFFF',
              shadowColor: '#000',
              shadowOffset: { width: 0, height: 2 },
              shadowOpacity: 0.08,
              shadowRadius: 12,
              elevation: 4,
              marginBottom: spacing.xl,
            }}
          >
            <View style={{ padding: spacing.sm, backgroundColor: '#FFFFFF', borderRadius: radius.sm }}>
              <QRCode value={address} size={200} color="#1A1A1A" backgroundColor="#FFF" />
            </View>
          </View>

          {/* Address */}
          <View style={{ width: '100%', backgroundColor: colors.surfaceAlt, borderRadius: radius.md, padding: spacing.md, marginBottom: spacing.md }}>
            <Text variant="overline" color="textMuted" style={{ marginBottom: 6 }}>Wallet Address</Text>
            <Text variant="bodySm" style={{ fontFamily: 'monospace', lineHeight: 20 }} selectable>{address}</Text>
          </View>

          {/* Warning */}
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'flex-start',
              gap: spacing.xs,
              backgroundColor: colors.warningBg,
              borderWidth: 1,
              borderColor: colors.warning,
              borderRadius: radius.md,
              padding: spacing.md,
              width: '100%',
              marginBottom: spacing.xl,
            }}
          >
            <Icon name="warning" size={14} color={colors.warning} />
            <Text variant="caption" color="warning" style={{ flex: 1, lineHeight: 18 }}>
              Only send <Text variant="caption" weight="700" color="warning">{item.symbol}</Text> on <Text variant="caption" weight="700" color="warning">{item.chainName}</Text> to this address
            </Text>
          </View>

          {/* Share */}
          <Button
            title="Share Address"
            leftIcon="send"
            onPress={handleShare}
            style={{ width: '100%', marginBottom: spacing.sm }}
          />

          <Button title="Close" variant="ghost" onPress={onClose} />
        </Pressable>
      </Pressable>
    </Modal>
  );
}

export default function FundScreen() {
  const { colors, spacing, radius, type, fonts } = useTheme();
  const { tokenList, getChainsForToken } = useTokens();
  const [walletAddress, setWalletAddress] = useState('');
  const [tronAddress, setTronAddress] = useState('');
  const [search, setSearch] = useState('');
  const [chainFilter, setChainFilter] = useState<AnyChainKey | 'all'>('all');
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [qrTarget, setQrTarget] = useState<QRTarget | null>(null);

  useEffect(() => {
    getActiveWallet().then(addr => { if (addr) setWalletAddress(addr); });
    // Same seed, standard Tron path — reproduces the TronLink address.
    getTronAddress().then(addr => { if (addr) setTronAddress(addr); }).catch(() => {});
  }, []);

  // The deposit address depends on the item's chain family: EVM chains share
  // the one 0x address; Tron has its own base58 T-address.
  const addressFor = (item: ReceiveItem): string =>
    isNonEVMChain(item.chainKey) ? tronAddress : walletAddress;

  // Flatten tokens × chains into receive items, then append the Tron items.
  const allItems = useMemo<ReceiveItem[]>(() => {
    const items: ReceiveItem[] = [];
    for (const token of tokenList) {
      const chains = getChainsForToken(token.symbol);
      for (const chain of chains) {
        items.push({
          id: `${token.symbol}-${chain.key}`,
          symbol: token.symbol,
          name: token.name,
          iconColor: token.iconColor,
          iconUrl: token.iconUrl,
          chainKey: chain.key,
          chainName: chain.name,
          chainShortName: chain.shortName,
          chainIconColor: chain.iconColor,
          chainIconUrl: chain.iconUrl,
        });
      }
    }
    items.push(...TRON_RECEIVE_ITEMS);
    return items;
  }, [tokenList, getChainsForToken]);

  // Available chains for the filter
  const filterChains = useMemo(() => {
    const seen = new Set<AnyChainKey>();
    const result: { key: AnyChainKey; iconColor: string; iconUrl?: string; shortName: string }[] = [];
    for (const item of allItems) {
      if (!seen.has(item.chainKey)) {
        seen.add(item.chainKey);
        result.push({ key: item.chainKey, iconColor: item.chainIconColor, iconUrl: item.chainIconUrl, shortName: item.chainShortName });
      }
    }
    return result;
  }, [allItems]);

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    return allItems.filter(item => {
      if (chainFilter !== 'all' && item.chainKey !== chainFilter) return false;
      if (q && !item.symbol.toLowerCase().includes(q) && !item.name.toLowerCase().includes(q) && !item.chainName.toLowerCase().includes(q)) return false;
      return true;
    });
  }, [allItems, search, chainFilter]);

  const popular = filtered.filter(i => POPULAR.includes(i.symbol));
  const rest = filtered.filter(i => !POPULAR.includes(i.symbol));

  const handleCopy = async (item: ReceiveItem) => {
    const address = addressFor(item);
    if (!address) return;
    await Clipboard.setStringAsync(address);
    setCopiedId(item.id);
    setTimeout(() => setCopiedId(null), 2000);
  };

  const handleQR = (item: ReceiveItem) => {
    const address = addressFor(item);
    if (!address) return;
    setQrTarget({ item, address });
  };

  const renderItem = (item: ReceiveItem) => (
    <ReceiveRow
      key={item.id}
      item={item}
      address={addressFor(item)}
      copiedId={copiedId}
      onCopy={handleCopy}
      onQR={handleQR}
    />
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {/* Search */}
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.sm,
          marginHorizontal: spacing.lg,
          marginBottom: spacing.md,
          backgroundColor: colors.surfaceAlt,
          borderRadius: radius.md,
          paddingHorizontal: spacing.md,
          paddingVertical: spacing.sm,
        }}
      >
        <Icon name="search" size={16} color={colors.textMuted} />
        <TextInput
          style={{ flex: 1, fontSize: type.body.fontSize, color: colors.textPrimary, fontFamily: fonts['400'], paddingVertical: 0 }}
          placeholder="Search"
          placeholderTextColor={colors.textMuted}
          value={search}
          onChangeText={setSearch}
          autoCapitalize="none"
          autoCorrect={false}
        />
        {search.length > 0 && (
          <PressableScale onPress={() => setSearch('')} hitSlop={8}>
            <Icon name="closeCircle" size={16} color={colors.textMuted} />
          </PressableScale>
        )}
      </View>

      {/* Chain filter */}
      <View style={{ paddingLeft: spacing.lg, marginBottom: spacing.sm }}>
        <ChainFilterRow chains={filterChains} selected={chainFilter} onSelect={setChainFilter} />
      </View>

      {/* No wallet */}
      {!walletAddress && (
        <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
          <EmptyState
            icon="wallet"
            title="No wallet connected"
            subtitle="Set up a wallet first to get your deposit address"
          />
        </View>
      )}

      {/* Token list */}
      {walletAddress && (
        <FlatList
          data={[
            ...(popular.length > 0 ? [{ type: 'header', label: 'Popular' } as any] : []),
            ...popular,
            ...(rest.length > 0 ? [{ type: 'header', label: 'All crypto' } as any] : []),
            ...rest,
          ]}
          keyExtractor={(item, i) => item.id || `header-${i}`}
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ paddingBottom: spacing['4xl'] }}
          renderItem={({ item }) => {
            if (item.type === 'header') {
              return (
                <Text
                  variant="overline"
                  color="textMuted"
                  style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.lg, paddingBottom: 6 }}
                >
                  {item.label}
                </Text>
              );
            }
            return renderItem(item as ReceiveItem);
          }}
        />
      )}

      <QRModal target={qrTarget} onClose={() => setQrTarget(null)} />
    </SafeAreaView>
  );
}
