import React, { useState, useEffect, useMemo } from 'react';
import {
  View, Text, TouchableOpacity, TextInput, FlatList, StyleSheet,
  Modal, Share, Image, SafeAreaView, Pressable,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import QRCode from 'react-native-qrcode-svg';
import { getActiveWallet } from '@/services/wallet';
import { CHAINS, type ChainKey } from '@/constants/chains';
import { useTokens } from '@/hooks/useTokens';

const GOLD = '#D4AF37';
const DARK = '#1A1A1A';

const POPULAR = ['USDT', 'USDC', 'BNB', 'ETH', 'MCGP'];

interface ReceiveItem {
  id: string;
  symbol: string;
  name: string;
  iconColor: string;
  iconUrl?: string;
  chainKey: ChainKey;
  chainName: string;
  chainShortName: string;
  chainIconColor: string;
  chainIconUrl?: string;
}

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
  chains: { key: ChainKey; iconColor: string; iconUrl?: string; shortName: string }[];
  selected: ChainKey | 'all';
  onSelect: (k: ChainKey | 'all') => void;
}) {
  return (
    <View style={s.chainRow}>
      <TouchableOpacity
        style={[s.chainCircle, selected === 'all' && s.chainCircleActive]}
        onPress={() => onSelect('all')}
        activeOpacity={0.7}
      >
        <Text style={[s.chainCircleText, selected === 'all' && s.chainCircleTextActive]}>All</Text>
      </TouchableOpacity>
      {chains.map(c => (
        <TouchableOpacity
          key={c.key}
          style={[s.chainCircle, selected === c.key && s.chainCircleActive]}
          onPress={() => onSelect(c.key)}
          activeOpacity={0.7}
        >
          {c.iconUrl
            ? <Image source={{ uri: c.iconUrl }} style={s.chainCircleImg} />
            : <Text style={[s.chainCircleText, selected === c.key && s.chainCircleTextActive]}>{c.shortName[0]}</Text>
          }
        </TouchableOpacity>
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
  const isCopied = copiedId === item.id;
  return (
    <View style={s.row}>
      {/* Token icon with chain overlay */}
      <View style={s.iconWrap}>
        <View style={[s.tokenIcon, { backgroundColor: item.iconColor + '25' }]}>
          {item.iconUrl
            ? <Image source={{ uri: item.iconUrl }} style={s.tokenIconImg} />
            : <Text style={[s.tokenIconLetter, { color: item.iconColor }]}>{item.symbol[0]}</Text>
          }
        </View>
        <View style={[s.chainBadgeIcon, { backgroundColor: item.chainIconColor }]}>
          {item.chainIconUrl
            ? <Image source={{ uri: item.chainIconUrl }} style={s.chainBadgeImg} />
            : <Text style={s.chainBadgeLetter}>{item.chainShortName[0]}</Text>
          }
        </View>
      </View>

      {/* Info */}
      <View style={s.rowInfo}>
        <View style={s.rowTop}>
          <Text style={s.rowSymbol}>{item.symbol}</Text>
          <View style={s.chainPill}>
            <Text style={s.chainPillText}>{item.chainName}</Text>
          </View>
        </View>
        <Text style={s.rowAddress}>{truncateAddress(address)}</Text>
      </View>

      {/* Actions */}
      <TouchableOpacity style={s.actionBtn} onPress={() => onQR(item)} activeOpacity={0.7}>
        <Ionicons name="qr-code-outline" size={18} color="#555" />
      </TouchableOpacity>
      <TouchableOpacity
        style={[s.actionBtn, isCopied && s.actionBtnCopied]}
        onPress={() => onCopy(item)}
        activeOpacity={0.7}
      >
        <Ionicons name={isCopied ? 'checkmark' : 'copy-outline'} size={18} color={isCopied ? GOLD : '#555'} />
      </TouchableOpacity>
    </View>
  );
}

function QRModal({ target, onClose }: { target: QRTarget | null; onClose: () => void }) {
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
      <Pressable style={qr.overlay} onPress={onClose}>
        <Pressable style={qr.sheet} onPress={() => {}}>
          <View style={qr.handle} />

          {/* Token + chain header */}
          <View style={qr.header}>
            <View style={[qr.tokenIcon, { backgroundColor: item.iconColor + '25' }]}>
              {item.iconUrl
                ? <Image source={{ uri: item.iconUrl }} style={{ width: 32, height: 32, borderRadius: 16 }} />
                : <Text style={[qr.tokenIconLetter, { color: item.iconColor }]}>{item.symbol[0]}</Text>
              }
            </View>
            <View>
              <Text style={qr.tokenSymbol}>{item.symbol}</Text>
              <Text style={qr.chainName}>{item.chainName}</Text>
            </View>
          </View>

          {/* QR */}
          <View style={qr.qrWrap}>
            <View style={qr.qrInner}>
              <QRCode value={address} size={200} color={DARK} backgroundColor="#FFF" />
            </View>
          </View>

          {/* Address */}
          <View style={qr.addressWrap}>
            <Text style={qr.addressLabel}>Wallet Address</Text>
            <Text style={qr.addressText} selectable>{address}</Text>
          </View>

          {/* Warning */}
          <View style={qr.warning}>
            <Ionicons name="warning-outline" size={13} color="#92400E" />
            <Text style={qr.warningText}>
              Only send <Text style={{ fontWeight: '700' }}>{item.symbol}</Text> on <Text style={{ fontWeight: '700' }}>{item.chainName}</Text> to this address
            </Text>
          </View>

          {/* Share */}
          <TouchableOpacity style={qr.shareBtn} onPress={handleShare} activeOpacity={0.8}>
            <Ionicons name="share-outline" size={18} color="#FFF" />
            <Text style={qr.shareBtnText}>Share Address</Text>
          </TouchableOpacity>

          <TouchableOpacity style={qr.closeBtn} onPress={onClose}>
            <Text style={qr.closeBtnText}>Close</Text>
          </TouchableOpacity>
        </Pressable>
      </Pressable>
    </Modal>
  );
}

export default function FundScreen() {
  const { tokenList, getChainsForToken } = useTokens();
  const [walletAddress, setWalletAddress] = useState('');
  const [search, setSearch] = useState('');
  const [chainFilter, setChainFilter] = useState<ChainKey | 'all'>('all');
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [qrTarget, setQrTarget] = useState<QRTarget | null>(null);

  useEffect(() => {
    getActiveWallet().then(addr => { if (addr) setWalletAddress(addr); });
  }, []);

  // Flatten tokens × chains into receive items
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
    return items;
  }, [tokenList, getChainsForToken]);

  // Available chains for the filter
  const filterChains = useMemo(() => {
    const seen = new Set<ChainKey>();
    const result: { key: ChainKey; iconColor: string; iconUrl?: string; shortName: string }[] = [];
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
    if (!walletAddress) return;
    await Clipboard.setStringAsync(walletAddress);
    setCopiedId(item.id);
    setTimeout(() => setCopiedId(null), 2000);
  };

  const handleQR = (item: ReceiveItem) => {
    if (!walletAddress) return;
    setQrTarget({ item, address: walletAddress });
  };

  const renderItem = (item: ReceiveItem) => (
    <ReceiveRow
      key={item.id}
      item={item}
      address={walletAddress}
      copiedId={copiedId}
      onCopy={handleCopy}
      onQR={handleQR}
    />
  );

  return (
    <SafeAreaView style={s.container}>
      {/* Header */}
      <View style={s.header}>
        <TouchableOpacity onPress={() => router.back()} hitSlop={12}>
          <Ionicons name="arrow-back" size={22} color={DARK} />
        </TouchableOpacity>
        <Text style={s.headerTitle}>Receive</Text>
        <View style={{ width: 22 }} />
      </View>

      {/* Search */}
      <View style={s.searchRow}>
        <Ionicons name="search" size={16} color="#AAA" />
        <TextInput
          style={s.searchInput}
          placeholder="Search"
          placeholderTextColor="#BBB"
          value={search}
          onChangeText={setSearch}
          autoCapitalize="none"
          autoCorrect={false}
        />
        {search.length > 0 && (
          <TouchableOpacity onPress={() => setSearch('')} hitSlop={8}>
            <Ionicons name="close-circle" size={16} color="#AAA" />
          </TouchableOpacity>
        )}
      </View>

      {/* Chain filter */}
      <View style={s.chainRowWrap}>
        <ChainFilterRow chains={filterChains} selected={chainFilter} onSelect={setChainFilter} />
      </View>

      {/* No wallet */}
      {!walletAddress && (
        <View style={s.noWallet}>
          <Ionicons name="wallet-outline" size={36} color="#DDD" />
          <Text style={s.noWalletText}>No wallet connected</Text>
          <Text style={s.noWalletHint}>Set up a wallet first to get your deposit address</Text>
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
          contentContainerStyle={{ paddingBottom: 40 }}
          renderItem={({ item }) => {
            if (item.type === 'header') {
              return <Text style={s.sectionLabel}>{item.label}</Text>;
            }
            return renderItem(item as ReceiveItem);
          }}
        />
      )}

      <QRModal target={qrTarget} onClose={() => setQrTarget(null)} />
    </SafeAreaView>
  );
}

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FFF' },
  header: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', paddingHorizontal: 20, paddingVertical: 14 },
  headerTitle: { fontSize: 18, fontWeight: '700', color: DARK },

  searchRow: { flexDirection: 'row', alignItems: 'center', gap: 8, marginHorizontal: 16, marginBottom: 12, backgroundColor: '#F5F5F5', borderRadius: 12, paddingHorizontal: 14, paddingVertical: 10 },
  searchInput: { flex: 1, fontSize: 15, color: DARK },

  chainRowWrap: { paddingLeft: 16, marginBottom: 8 },
  chainRow: { flexDirection: 'row', gap: 8 },
  chainCircle: { width: 40, height: 40, borderRadius: 20, backgroundColor: '#F0F0F0', alignItems: 'center', justifyContent: 'center', borderWidth: 2, borderColor: 'transparent' },
  chainCircleActive: { borderColor: GOLD, backgroundColor: '#FFF8E1' },
  chainCircleImg: { width: 26, height: 26, borderRadius: 13 },
  chainCircleText: { fontSize: 11, fontWeight: '700', color: '#888' },
  chainCircleTextActive: { color: GOLD },

  sectionLabel: { fontSize: 13, fontWeight: '700', color: '#888', paddingHorizontal: 16, paddingTop: 16, paddingBottom: 6 },

  row: { flexDirection: 'row', alignItems: 'center', paddingHorizontal: 16, paddingVertical: 12, gap: 10, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: '#F5F5F5' },
  iconWrap: { position: 'relative', width: 44, height: 44 },
  tokenIcon: { width: 40, height: 40, borderRadius: 20, alignItems: 'center', justifyContent: 'center' },
  tokenIconImg: { width: 40, height: 40, borderRadius: 20 },
  tokenIconLetter: { fontSize: 16, fontWeight: '700' },
  chainBadgeIcon: { position: 'absolute', bottom: -2, right: -2, width: 18, height: 18, borderRadius: 9, alignItems: 'center', justifyContent: 'center', borderWidth: 1.5, borderColor: '#FFF' },
  chainBadgeImg: { width: 14, height: 14, borderRadius: 7 },
  chainBadgeLetter: { fontSize: 7, fontWeight: '700', color: '#FFF' },

  rowInfo: { flex: 1 },
  rowTop: { flexDirection: 'row', alignItems: 'center', gap: 6, marginBottom: 3 },
  rowSymbol: { fontSize: 15, fontWeight: '700', color: DARK },
  chainPill: { backgroundColor: '#F0F0F0', borderRadius: 8, paddingHorizontal: 7, paddingVertical: 2 },
  chainPillText: { fontSize: 11, fontWeight: '600', color: '#555' },
  rowAddress: { fontSize: 12, color: '#AAA', fontFamily: 'monospace' },

  actionBtn: { width: 36, height: 36, borderRadius: 18, backgroundColor: '#F5F5F5', alignItems: 'center', justifyContent: 'center' },
  actionBtnCopied: { backgroundColor: '#FFF8E1' },

  noWallet: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: 10 },
  noWalletText: { fontSize: 16, fontWeight: '600', color: '#BBB' },
  noWalletHint: { fontSize: 13, color: '#CCC', textAlign: 'center', paddingHorizontal: 40 },
});

const qr = StyleSheet.create({
  overlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.45)', justifyContent: 'flex-end' },
  sheet: { backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24, padding: 24, paddingTop: 16, alignItems: 'center' },
  handle: { width: 36, height: 4, borderRadius: 2, backgroundColor: '#E0E0E0', marginBottom: 20 },
  header: { flexDirection: 'row', alignItems: 'center', gap: 12, alignSelf: 'flex-start', marginBottom: 24 },
  tokenIcon: { width: 44, height: 44, borderRadius: 22, alignItems: 'center', justifyContent: 'center' },
  tokenIconLetter: { fontSize: 18, fontWeight: '700' },
  tokenSymbol: { fontSize: 17, fontWeight: '700', color: DARK },
  chainName: { fontSize: 13, color: '#888', marginTop: 2 },
  qrWrap: { padding: 16, borderRadius: 16, backgroundColor: '#FFF', shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.08, shadowRadius: 12, elevation: 4, marginBottom: 20 },
  qrInner: { padding: 8, backgroundColor: '#FFF', borderRadius: 8 },
  addressWrap: { width: '100%', backgroundColor: '#F8F8F8', borderRadius: 12, padding: 14, marginBottom: 12 },
  addressLabel: { fontSize: 11, fontWeight: '600', color: '#AAA', letterSpacing: 0.8, marginBottom: 6 },
  addressText: { fontSize: 13, color: DARK, lineHeight: 20, fontFamily: 'monospace' },
  warning: { flexDirection: 'row', alignItems: 'flex-start', gap: 6, backgroundColor: '#FFFBEB', borderWidth: 1, borderColor: '#FDE68A', borderRadius: 10, padding: 10, width: '100%', marginBottom: 20 },
  warningText: { fontSize: 12, color: '#92400E', flex: 1, lineHeight: 18 },
  shareBtn: { flexDirection: 'row', alignItems: 'center', gap: 8, backgroundColor: GOLD, paddingVertical: 14, paddingHorizontal: 32, borderRadius: 14, width: '100%', justifyContent: 'center', marginBottom: 10, shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.3, shadowRadius: 8, elevation: 4 },
  shareBtnText: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  closeBtn: { paddingVertical: 12 },
  closeBtnText: { fontSize: 15, color: '#AAA', fontWeight: '600' },
});
