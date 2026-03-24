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
import Icon from 'react-native-vector-icons/MaterialIcons';
import { getWalletBalances } from '../services/walletApi';
import { useTokens } from '../hooks/useTokens';
import { CHAINS, type ChainKey } from '../constants/chains';

// ── Types ──
interface WalletAsset {
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  iconColor: string;
  iconUrl?: string;
  details?: {
    type: string;
    chain: string;
    chainKey: ChainKey;
  };
}

// ── Quick Actions ──
const QUICK_ACTIONS = [
  { key: 'fund', label: 'Fund', icon: 'account-balance' },
  { key: 'swap', label: 'Swap', icon: 'swap-horiz' },
  { key: 'send', label: 'Send', icon: 'send' },
  { key: 'transfer', label: 'Transfer', icon: 'sync-alt' },
] as const;

// ── Balance Card ──
const WalletBalanceCard: React.FC<{
  totalUsdValue: number;
  isValuesHidden: boolean;
  onToggleVisibility: () => void;
  onQuickAction: (action: string) => void;
}> = ({ totalUsdValue, isValuesHidden, onToggleVisibility, onQuickAction }) => (
  <View style={styles.balanceCard}>
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
}> = ({ asset, isValuesHidden }) => {
  const isNative = asset.details?.type === 'Native Token';
  return (
    <View style={styles.assetRow}>
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
        <Text style={styles.assetSymbol}>{asset.symbol}</Text>
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
    </View>
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
          onPress={() => { onClose(); router.push('/send'); }}
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
  const [assets, setAssets] = useState<WalletAsset[]>([]);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [showFundModal, setShowFundModal] = useState(false);
  const [showSendModal, setShowSendModal] = useState(false);

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
    const assetList = buildAssetList();
    try {
      const result = await getWalletBalances();
      if (result.success && result.data) {
        const data = result.data as any;
        // API may return flat WalletBalance[] or nested { balances: { chainKey: { symbol: amount } } }
        const nestedBalances = data.balances || null;

        const updated = assetList.map(asset => {
          const chainKey = asset.details?.chainKey;
          if (!chainKey) return asset;

          let balance = 0;
          if (nestedBalances && nestedBalances[chainKey]) {
            // Nested format: { sonic: { S: "0", MCGP: "0" }, bsc: { tBNB: "0.0001" } }
            const raw = nestedBalances[chainKey][asset.symbol];
            balance = raw ? parseFloat(raw) : 0;
          } else if (Array.isArray(data)) {
            // Flat format: WalletBalance[]
            const entry = data.find((b: any) => b.symbol === asset.symbol);
            balance = entry ? parseFloat(entry.balance || '0') : 0;
          }
          return { ...asset, balance, usdValue: balance * 1.0 };
        });
        setAssets(updated);
      } else {
        setAssets(assetList);
      }
    } catch (err) {
      console.error('Wallet fetch error:', err);
      setAssets(assetList);
    } finally {
      setIsLoading(false);
      setIsRefreshing(false);
    }
  }, [buildAssetList]);

  useEffect(() => { fetchBalances(); }, [fetchBalances]);

  const onRefresh = useCallback(() => fetchBalances(true), [fetchBalances]);

  const totalUsdValue = assets.reduce((s, a) => s + a.usdValue, 0);

  const handleQuickAction = (action: string) => {
    if (action === 'fund') setShowFundModal(true);
    else if (action === 'send') setShowSendModal(true);
    else router.push(`/${action}` as any);
  };

  // Group assets: those with balance first, then zero-balance
  const assetsWithBalance = assets.filter(a => a.balance > 0);
  const assetsWithoutBalance = assets.filter(a => a.balance === 0);

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
      <WalletBalanceCard
        totalUsdValue={totalUsdValue}
        isValuesHidden={isValuesHidden}
        onToggleVisibility={() => setIsValuesHidden(v => !v)}
        onQuickAction={handleQuickAction}
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
              <AssetRow key={asset.id} asset={asset} isValuesHidden={isValuesHidden} />
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
    </ScrollView>
  );
};

// ── Styles ──
const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
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
  assetSymbol: { fontSize: 15, fontWeight: '600', color: '#1A1A1A' },
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
});

export default WalletScreen;
