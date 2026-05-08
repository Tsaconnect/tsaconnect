// screens/wallet.tsx
import { router } from 'expo-router';
import React, { useState, useEffect, useCallback } from 'react';
import {
  View,
  Text,
  ScrollView,
  StyleSheet,
  RefreshControl,
  Pressable,
  Modal,
  Image,
} from 'react-native';
import { MaterialIcons as Icon } from '@expo/vector-icons';
import {
  getWalletBalances,
  normalizeWalletBalances,
  registerWalletAddress,
  getDiscoveredTokens,
  type DiscoveredToken,
} from '../services/walletApi';
import {
  getWalletList,
  getActiveWallet,
  setActiveWallet,
  migrateFromSingleWallet,
  WalletMeta,
} from '../services/wallet';
import { useTokens } from '../hooks/useTokens';
import { CHAINS, type ChainKey } from '../constants/chains';
import { useNetwork } from '../hooks/useNetwork';

// ── Types ──
interface WalletAsset {
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  usdPrice: number;
  iconColor: string;
  iconUrl?: string;
  /** Set on rows surfaced by the indexer instead of supported_tokens. */
  discovered?: boolean;
  /** ERC-20 contract address; required for discovered rows so Send works. */
  contractAddress?: string;
  details?: {
    type: string;
    chain: string;
    chainKey: ChainKey;
  };
}

// hashColor produces a stable HSL color from a string — used as a deterministic
// avatar fallback for discovered tokens we don't have an icon for.
function hashColor(str: string): string {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  return `hsl(${Math.abs(hash) % 360}, 55%, 50%)`;
}

// formatTokenBalance converts a raw integer balance string at `decimals`
// precision to a number. Uses string manipulation rather than Number()
// to avoid IEEE-754 precision loss on large 18-decimal balances.
function formatTokenBalance(rawBalance: string, decimals: number): number {
  if (!rawBalance || rawBalance === '0') return 0;
  if (decimals <= 0) return parseFloat(rawBalance);
  const padded = rawBalance.padStart(decimals + 1, '0');
  const cut = padded.length - decimals;
  const whole = padded.slice(0, cut) || '0';
  const frac = padded.slice(cut).replace(/0+$/, '');
  return parseFloat(frac.length > 0 ? `${whole}.${frac}` : whole);
}

// mergeDiscoveredTokens fetches the auto-discovery list and appends any
// rows that aren't already in `existing` (matched by chainKey+symbol or
// chainKey+contract). Returns null when there's nothing to add or the
// indexer is offline, so the caller can skip a useless setState.
async function mergeDiscoveredTokens(
  existing: WalletAsset[],
  address: string | undefined,
): Promise<WalletAsset[] | null> {
  try {
    const result = await getDiscoveredTokens(undefined, address);
    if (!result.success || !result.data || !result.data.providerAvailable) {
      return null;
    }
    const discovered: DiscoveredToken[] = result.data.tokens || [];
    if (discovered.length === 0) return null;

    // Index existing rows by chainKey+symbol AND chainKey+contract so we
    // don't duplicate either USDC-on-base (matched by symbol) or a custom
    // token previously imported (matched by address).
    const seenSymbol = new Set(
      existing
        .filter((a) => a.details?.chainKey)
        .map((a) => `${a.details!.chainKey}::${a.symbol.toUpperCase()}`),
    );
    const seenContract = new Set(
      existing
        .filter((a) => a.contractAddress && a.details?.chainKey)
        .map((a) => `${a.details!.chainKey}::${a.contractAddress!.toLowerCase()}`),
    );

    let nextId = existing.length + 1;
    const additions: WalletAsset[] = [];
    for (const t of discovered) {
      const chainKey = t.chain as ChainKey;
      const chain = CHAINS[chainKey];
      if (!chain) continue;
      const symbolKey = `${chainKey}::${t.symbol.toUpperCase()}`;
      const contractKey = `${chainKey}::${t.contractAddress.toLowerCase()}`;
      if (seenSymbol.has(symbolKey) || seenContract.has(contractKey)) continue;

      const balance = formatTokenBalance(t.balance, t.decimals);
      const usdValue = Number.isFinite(t.usdValue) ? t.usdValue : 0;
      additions.push({
        id: String(nextId++),
        symbol: t.symbol,
        name: t.name || t.symbol,
        balance,
        usdValue,
        usdPrice: balance > 0 && usdValue > 0 ? usdValue / balance : 0,
        iconColor: hashColor(t.symbol),
        discovered: true,
        contractAddress: t.contractAddress,
        details: { type: 'Discovered', chain: chain.name, chainKey },
      });
    }
    if (additions.length === 0) return null;
    return [...existing, ...additions];
  } catch (err) {
    console.warn('[wallet] discovery merge failed:', err);
    return null;
  }
}

// ── Quick Actions ──
const QUICK_ACTIONS = [
  { key: 'fund', label: 'Fund', icon: 'account-balance' },
  { key: 'swap', label: 'Swap', icon: 'swap-horiz' },
  { key: 'send', label: 'Send', icon: 'send' },
  { key: 'instant-pay', label: 'Instant Pay', icon: 'flash-on' },
] as const;

// ── Balance Card ──
const WalletBalanceCard: React.FC<{
  totalUsdValue: number;
  isValuesHidden: boolean;
  activeLabel: string;
  onToggleVisibility: () => void;
  onQuickAction: (action: string) => void;
  onWalletPress: () => void;
}> = ({ totalUsdValue, isValuesHidden, activeLabel, onToggleVisibility, onQuickAction, onWalletPress }) => (
  <View style={styles.balanceCard}>
    <Pressable style={styles.walletSelector} onPress={onWalletPress}>
      <Text style={styles.walletSelectorLabel}>{activeLabel}</Text>
      <Icon name="keyboard-arrow-down" size={18} color="rgba(255,255,255,0.8)" />
    </Pressable>
    <View style={styles.balanceHeader}>
      <Text style={styles.balanceLabel}>Wallet Balance</Text>
      <Pressable onPress={onToggleVisibility} hitSlop={8}>
        <Icon
          name={isValuesHidden ? 'visibility-off' : 'visibility'}
          size={20}
          color="rgba(255,255,255,0.4)"
        />
      </Pressable>
    </View>
    <Text style={styles.balanceAmount}>
      {isValuesHidden
        ? '••••••'
        : `$${totalUsdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
    </Text>
    <View style={styles.actionsRow}>
      {QUICK_ACTIONS.map((action) => (
        <Pressable
          key={action.key}
          style={({ pressed }) => [styles.actionButton, pressed && styles.actionPressed]}
          onPress={() => onQuickAction(action.key)}
        >
          <View style={styles.actionIcon}>
            <Icon name={action.icon} size={20} color="#D4AF37" />
          </View>
          <Text style={styles.actionLabel}>{action.label}</Text>
        </Pressable>
      ))}
    </View>
  </View>
);

// ── Asset Row ──
const AssetRow: React.FC<{
  asset: WalletAsset;
  isValuesHidden: boolean;
  onPress?: () => void;
}> = ({ asset, isValuesHidden, onPress }) => {
  const isNative = asset.details?.type === 'Native Token';
  return (
    <Pressable style={({ pressed }) => [styles.assetRow, pressed && { opacity: 0.7 }]} onPress={onPress}>
      <View style={[styles.assetAvatar, { backgroundColor: asset.iconColor + '20' }]}>
        {asset.iconUrl ? (
          <Image source={{ uri: asset.iconUrl }} style={styles.assetAvatarImage} />
        ) : (
          <Text style={[styles.assetAvatarText, { color: asset.iconColor }]}>
            {asset.symbol.charAt(0)}
          </Text>
        )}
      </View>
      <View style={styles.assetInfo}>
        <View style={styles.assetSymbolRow}>
          <Text style={styles.assetSymbol}>{asset.symbol}</Text>
          {asset.discovered && (
            <View style={styles.discoveredBadge}>
              <Text style={styles.discoveredBadgeText}>NEW</Text>
            </View>
          )}
        </View>
        <View style={styles.assetMeta}>
          <Text style={styles.assetName}>{asset.name}</Text>
          {asset.details?.chain && (
            <>
              <View style={[styles.chainDot, { backgroundColor: asset.details.chainKey ? CHAINS[asset.details.chainKey].iconColor : '#999' }]} />
              <Text style={styles.chainName}>{asset.details.chain}</Text>
            </>
          )}
        </View>
      </View>
      <View style={styles.assetValues}>
        <Text style={styles.assetBalance}>
          {isValuesHidden ? '••••' : asset.balance.toFixed(isNative ? 4 : 2)}
        </Text>
        <Text style={styles.assetUsd}>
          {isValuesHidden ? '••••' : `$${asset.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
        </Text>
      </View>
    </Pressable>
  );
};

// ── Fund Modal ──
const FundModal: React.FC<{ visible: boolean; onClose: () => void }> = ({ visible, onClose }) => (
  <Modal animationType="slide" transparent visible={visible} onRequestClose={onClose}>
    <View style={styles.modalOverlay}>
      <View style={styles.modalContent}>
        <View style={styles.modalHeader}>
          <Text style={styles.modalTitle}>Fund Your Wallet</Text>
          <Pressable onPress={onClose}><Icon name="close" size={24} color="#888" /></Pressable>
        </View>
        <Pressable
          style={styles.modalOption}
          onPress={() => { onClose(); router.push('/fundfiat'); }}
        >
          <View style={[styles.modalOptionIcon, { backgroundColor: '#E3F2FD' }]}>
            <Icon name="account-balance" size={24} color="#1976D2" />
          </View>
          <View style={styles.modalOptionInfo}>
            <Text style={styles.modalOptionTitle}>Fiat Deposit</Text>
            <Text style={styles.modalOptionDesc}>Bank transfer or credit card</Text>
          </View>
          <Icon name="chevron-right" size={20} color="#CCC" />
        </Pressable>
        <Pressable
          style={styles.modalOption}
          onPress={() => { onClose(); router.push('/fund'); }}
        >
          <View style={[styles.modalOptionIcon, { backgroundColor: '#FFF3E0' }]}>
            <Icon name="account-balance-wallet" size={24} color="#E65100" />
          </View>
          <View style={styles.modalOptionInfo}>
            <Text style={styles.modalOptionTitle}>Crypto Deposit</Text>
            <Text style={styles.modalOptionDesc}>From external wallet or exchange</Text>
          </View>
          <Icon name="chevron-right" size={20} color="#CCC" />
        </Pressable>
        <Pressable style={styles.modalCancel} onPress={onClose}>
          <Text style={styles.modalCancelText}>Cancel</Text>
        </Pressable>
      </View>
    </View>
  </Modal>
);

// ── Send Modal ──
const SendModal: React.FC<{ visible: boolean; onClose: () => void }> = ({ visible, onClose }) => (
  <Modal animationType="slide" transparent visible={visible} onRequestClose={onClose}>
    <View style={styles.modalOverlay}>
      <View style={styles.modalContent}>
        <View style={styles.modalHeader}>
          <Text style={styles.modalTitle}>Send Funds</Text>
          <Pressable onPress={onClose}><Icon name="close" size={24} color="#888" /></Pressable>
        </View>
        <Pressable
          style={styles.modalOption}
          onPress={() => { onClose(); router.push('/sendfiat'); }}
        >
          <View style={[styles.modalOptionIcon, { backgroundColor: '#E8F5E9' }]}>
            <Icon name="attach-money" size={24} color="#2E7D32" />
          </View>
          <View style={styles.modalOptionInfo}>
            <Text style={styles.modalOptionTitle}>Withdraw as Fiat</Text>
            <Text style={styles.modalOptionDesc}>Bank transfer or mobile money</Text>
          </View>
          <Icon name="chevron-right" size={20} color="#CCC" />
        </Pressable>
        <Pressable
          style={styles.modalOption}
          onPress={() => { onClose(); router.push('/wallet/send'); }}
        >
          <View style={[styles.modalOptionIcon, { backgroundColor: '#F3E5F5' }]}>
            <Icon name="account-balance-wallet" size={24} color="#7B1FA2" />
          </View>
          <View style={styles.modalOptionInfo}>
            <Text style={styles.modalOptionTitle}>Send Crypto</Text>
            <Text style={styles.modalOptionDesc}>To external wallet or exchange</Text>
          </View>
          <Icon name="chevron-right" size={20} color="#CCC" />
        </Pressable>
        <Pressable style={styles.modalCancel} onPress={onClose}>
          <Text style={styles.modalCancelText}>Cancel</Text>
        </Pressable>
      </View>
    </View>
  </Modal>
);

// ── Main Screen ──
const WalletScreen: React.FC = () => {
  const { tokenList } = useTokens();
  const { network, switchNetwork } = useNetwork();
  const [assets, setAssets] = useState<WalletAsset[]>([]);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [showFundModal, setShowFundModal] = useState(false);
  const [showSendModal, setShowSendModal] = useState(false);
  const [walletList, setWalletList] = useState<WalletMeta[]>([]);
  const [activeLabel, setActiveLabel] = useState('Wallet');
  const [activeAddress, setActiveAddress] = useState('');
  const [showWalletSelector, setShowWalletSelector] = useState(false);

  const buildAssetList = useCallback((): WalletAsset[] => {
    const list: WalletAsset[] = [];
    let id = 1;

    // Native tokens per chain
    for (const [chainKey, chain] of Object.entries(CHAINS)) {
      list.push({
        id: String(id++),
        symbol: chain.nativeCurrency.symbol,
        name: `${chain.name} Native`,
        balance: 0,
        usdValue: 0,
        usdPrice: 0,
        iconColor: chain.iconColor,
        iconUrl: chain.iconUrl,
        details: { type: 'Native Token', chain: chain.name, chainKey: chainKey as ChainKey },
      });
    }

    // ERC-20 tokens per chain
    for (const token of tokenList) {
      for (const chainKey of token.chains) {
        const chain = CHAINS[chainKey];
        list.push({
          id: String(id++),
          symbol: token.symbol,
          name: token.name,
          balance: 0,
          usdValue: 0,
          usdPrice: 0,
          iconColor: token.iconColor,
          iconUrl: token.iconUrl,
          details: {
            type: token.symbol === 'MCGP' ? 'Gold-Backed Token' : 'Stablecoin',
            chain: chain.name,
            chainKey,
          },
        });
      }
    }
    return list;
  }, [tokenList]);

  const fetchBalances = useCallback(async (refreshing = false) => {
    if (refreshing) setIsRefreshing(true); else setIsLoading(true);

    // Load wallet list for switcher
    await migrateFromSingleWallet();
    const list = await getWalletList();
    setWalletList(list);
    const addr = await getActiveWallet();
    if (addr) {
      setActiveAddress(addr);
      const meta = list.find(w => w.address === addr);
      setActiveLabel(meta?.label || 'Wallet');
    }

    const assetList = buildAssetList();
    try {
      // Known-token balances: fast path, single BE call against the
      // supported_tokens table. We render this immediately and then
      // append discovery results below.
      const result = await getWalletBalances();
      let updated = assetList;
      if (result.success && result.data) {
        const normalizedBalances = normalizeWalletBalances(result.data);
        const balanceMap = new Map(
          normalizedBalances.map((entry) => [`${entry.symbol}-${entry.chainKey}`, entry] as const)
        );

        updated = assetList.map(asset => {
          const chainKey = asset.details?.chainKey;
          if (!chainKey) return asset;

          const entry = balanceMap.get(`${asset.symbol}-${chainKey}`);
          const parsedBal = parseFloat(entry?.balance || '0');
          const parsedUsdValue = parseFloat(entry?.usdValue || '0');
          const balance = Number.isFinite(parsedBal) ? parsedBal : 0;
          const usdValue = Number.isFinite(parsedUsdValue) ? parsedUsdValue : 0;
          const usdPrice = entry?.usdPrice || (balance > 0 ? usdValue / balance : 0);
          return { ...asset, balance, usdValue, usdPrice };
        });
      }
      setAssets(updated);

      // Auto-discovery: ask the BE for every ERC-20 the indexer found
      // that isn't already in our supported_tokens list. Slower than the
      // balance fetch (multi-chain fan-out), so we run it after the main
      // list is on screen and merge in additions when it completes.
      void mergeDiscoveredTokens(updated, addr || undefined).then((merged) => {
        if (merged) setAssets(merged);
      });
    } catch (err) {
      console.error('Wallet fetch error:', err);
      setAssets(assetList);
    } finally {
      setIsLoading(false);
      setIsRefreshing(false);
    }
  }, [buildAssetList, network]);

  useEffect(() => { fetchBalances(); }, [fetchBalances]);

  const onRefresh = useCallback(() => fetchBalances(true), [fetchBalances]);

  const handleSwitchWallet = useCallback(async (address: string) => {
    setShowWalletSelector(false);
    if (address === activeAddress) return;
    setIsLoading(true);
    try {
      await setActiveWallet(address);
      await registerWalletAddress(address);
      await fetchBalances();
    } catch (err) {
      console.error('Switch wallet error:', err);
    }
  }, [activeAddress, fetchBalances]);

  const totalUsdValue = assets.reduce((s, a) => s + (Number.isFinite(a.usdValue) ? a.usdValue : 0), 0);

  const handleQuickAction = (action: string) => {
    if (action === 'fund') setShowFundModal(true);
    else if (action === 'send') setShowSendModal(true);
    else if (action === 'instant-pay') router.push('/wallet/instant-pay' as any);
    else router.push(`/${action}` as any);
  };

  const handleAssetPress = (asset: WalletAsset) => {
    router.push({
      pathname: '/wallet/token',
      params: {
        symbol: asset.symbol,
        chainKey: asset.details?.chainKey || '',
        name: asset.name,
        iconColor: asset.iconColor,
        iconUrl: asset.iconUrl || '',
        type: asset.details?.type || '',
      },
    } as any);
  };

  // Group assets: those with balance first, then zero-balance
  const assetsWithBalance = assets.filter(a => a.balance > 0);
  const assetsWithoutBalance = assets.filter(a => !(a.balance > 0));

  // Skeleton loading
  if (isLoading && !isRefreshing) {
    return (
      <ScrollView style={styles.container} contentContainerStyle={styles.skeletonContent}>
        <View style={[styles.skeletonCard, { height: 180 }]} />
        <View style={[styles.skeletonCard, { height: 300, marginTop: 14 }]} />
      </ScrollView>
    );
  }

  return (
    <ScrollView
      style={styles.container}
      contentContainerStyle={{ paddingBottom: 100 }}
      showsVerticalScrollIndicator={false}
      refreshControl={
        <RefreshControl refreshing={isRefreshing} onRefresh={onRefresh} tintColor="#D4AF37" colors={['#D4AF37']} />
      }
    >
      <Pressable
        style={[styles.networkBadge, network === 'testnet' && styles.networkBadgeTestnet]}
        onPress={() => switchNetwork(network === 'mainnet' ? 'testnet' : 'mainnet')}
      >
        <View style={[styles.networkDot, { backgroundColor: network === 'mainnet' ? '#4CAF50' : '#FF9800' }]} />
        <Text style={styles.networkBadgeText}>
          {network === 'mainnet' ? 'Mainnet' : 'Testnet'}
        </Text>
      </Pressable>

      <WalletBalanceCard
        totalUsdValue={totalUsdValue}
        isValuesHidden={isValuesHidden}
        activeLabel={activeLabel}
        onToggleVisibility={() => setIsValuesHidden(v => !v)}
        onQuickAction={handleQuickAction}
        onWalletPress={() => setShowWalletSelector(true)}
      />

      {/* Assets */}
      <View style={styles.assetsCard}>
        <View style={styles.sectionHeader}>
          <Text style={styles.sectionTitle}>Assets</Text>
          <Text style={styles.assetCount}>{assets.length} tokens</Text>
        </View>

        {assetsWithBalance.length > 0 && (
          <>
            {assetsWithBalance.map(asset => (
              <AssetRow key={asset.id} asset={asset} isValuesHidden={isValuesHidden} onPress={() => handleAssetPress(asset)} />
            ))}
            {assetsWithoutBalance.length > 0 && (
              <View style={styles.divider} />
            )}
          </>
        )}

        {assetsWithoutBalance.map(asset => (
          <AssetRow key={asset.id} asset={asset} isValuesHidden={isValuesHidden} />
        ))}
      </View>

      <View style={{ height: 40 }} />

      <FundModal visible={showFundModal} onClose={() => setShowFundModal(false)} />
      <SendModal visible={showSendModal} onClose={() => setShowSendModal(false)} />

      {/* Wallet Selector */}
      <Modal visible={showWalletSelector} transparent animationType="slide" onRequestClose={() => setShowWalletSelector(false)}>
        <Pressable style={styles.modalOverlay} onPress={() => setShowWalletSelector(false)}>
          <View style={styles.modalContent}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>Select Wallet</Text>
              <Pressable onPress={() => setShowWalletSelector(false)}><Icon name="close" size={24} color="#888" /></Pressable>
            </View>
            {walletList.map((w) => (
              <Pressable
                key={w.address}
                style={styles.modalOption}
                onPress={() => handleSwitchWallet(w.address)}
              >
                <View style={[styles.modalOptionIcon, { backgroundColor: w.address === activeAddress ? '#D4AF3720' : '#F5F5F5' }]}>
                  <Icon name="account-balance-wallet" size={24} color={w.address === activeAddress ? '#D4AF37' : '#888'} />
                </View>
                <View style={styles.modalOptionInfo}>
                  <Text style={styles.modalOptionTitle}>{w.label}</Text>
                  <Text style={styles.modalOptionDesc} numberOfLines={1}>{w.address.slice(0, 8)}...{w.address.slice(-6)}</Text>
                </View>
                {w.address === activeAddress && (
                  <Icon name="check-circle" size={22} color="#D4AF37" />
                )}
              </Pressable>
            ))}
            <Pressable
              style={styles.modalCancel}
              onPress={() => { setShowWalletSelector(false); router.push('/wallet/manage'); }}
            >
              <Text style={[styles.modalCancelText, { color: '#D4AF37', fontWeight: '600' }]}>Manage Wallets</Text>
            </Pressable>
          </View>
        </Pressable>
      </Modal>
    </ScrollView>
  );
};

// ── Styles ──
const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  networkBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    alignSelf: 'center',
    backgroundColor: '#E8F5E9',
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 20,
    marginTop: 8,
    marginBottom: 4,
  },
  networkBadgeTestnet: {
    backgroundColor: '#FFF3E0',
  },
  networkDot: {
    width: 8,
    height: 8,
    borderRadius: 4,
    marginRight: 6,
  },
  networkBadgeText: {
    fontSize: 12,
    fontWeight: '600',
    color: '#333',
  },
  skeletonContent: { padding: 16 },
  skeletonCard: {
    backgroundColor: '#E5E5E5',
    borderRadius: 14,
    marginHorizontal: 16,
    opacity: 0.5,
  },

  // Balance Card
  balanceCard: {
    marginHorizontal: 16,
    marginTop: 8,
    borderRadius: 16,
    padding: 20,
    backgroundColor: '#1A1A1A',
  },
  balanceHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  balanceLabel: { fontSize: 13, color: 'rgba(255,255,255,0.6)' },
  balanceAmount: { fontSize: 28, fontWeight: '800', color: '#FFFFFF', marginBottom: 4 },
  actionsRow: {
    flexDirection: 'row',
    justifyContent: 'space-around',
    marginTop: 18,
    paddingTop: 14,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255,255,255,0.1)',
  },
  actionButton: { alignItems: 'center', minWidth: 56 },
  actionPressed: { opacity: 0.7 },
  actionIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: 'rgba(212,175,55,0.15)',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 6,
  },
  actionLabel: { fontSize: 11, color: 'rgba(255,255,255,0.7)', fontWeight: '500' },

  // Assets Card
  assetsCard: {
    marginHorizontal: 16,
    marginTop: 14,
    borderRadius: 14,
    backgroundColor: '#FFFFFF',
    padding: 16,
  },
  sectionHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  sectionTitle: { fontSize: 16, fontWeight: '700', color: '#1A1A1A' },
  assetCount: { fontSize: 12, color: '#888' },
  divider: {
    height: 1,
    backgroundColor: '#F0F0F0',
    marginVertical: 8,
    marginHorizontal: 4,
  },

  // Asset Row
  assetRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 10,
  },
  assetAvatar: {
    width: 40,
    height: 40,
    borderRadius: 20,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  assetAvatarImage: { width: 24, height: 24, borderRadius: 12 },
  assetAvatarText: { fontSize: 16, fontWeight: '700' },
  assetInfo: { flex: 1 },
  assetSymbolRow: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  assetSymbol: { fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
  discoveredBadge: {
    backgroundColor: 'rgba(212,175,55,0.15)',
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 4,
  },
  discoveredBadgeText: { fontSize: 9, fontWeight: '800', color: '#8B6914', letterSpacing: 0.5 },
  assetMeta: { flexDirection: 'row', alignItems: 'center', marginTop: 2 },
  assetName: { fontSize: 12, color: '#888' },
  chainDot: { width: 5, height: 5, borderRadius: 2.5, marginLeft: 6, marginRight: 3 },
  chainName: { fontSize: 11, color: '#AAA' },
  assetValues: { alignItems: 'flex-end' },
  assetBalance: { fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
  assetUsd: { fontSize: 12, color: '#888', marginTop: 2 },

  // Modals
  modalOverlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.5)', justifyContent: 'flex-end' },
  modalContent: { backgroundColor: '#FFF', borderTopLeftRadius: 20, borderTopRightRadius: 20, padding: 20, paddingBottom: 34 },
  modalHeader: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 },
  modalTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },
  modalOption: { flexDirection: 'row', alignItems: 'center', gap: 12, padding: 14, backgroundColor: '#FAFAFA', borderRadius: 14, marginBottom: 10 },
  modalOptionIcon: { width: 48, height: 48, borderRadius: 14, justifyContent: 'center', alignItems: 'center' },
  modalOptionInfo: { flex: 1 },
  modalOptionTitle: { fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
  modalOptionDesc: { fontSize: 12, color: '#888', marginTop: 2 },
  modalCancel: { alignItems: 'center', paddingVertical: 14, marginTop: 4 },
  modalCancelText: { fontSize: 15, color: '#888', fontWeight: '500' },

  // Wallet Selector
  walletSelector: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    marginBottom: 8,
  },
  walletSelectorLabel: {
    fontSize: 13,
    color: 'rgba(255,255,255,0.9)',
    fontWeight: '600',
  },
});

export default WalletScreen;
