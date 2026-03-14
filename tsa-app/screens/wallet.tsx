import { router } from 'expo-router';
import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  Animated,
  StyleSheet,
  Dimensions,
  Platform,
  ActivityIndicator,
  Modal,
  RefreshControl,
  Image,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { getWalletBalances } from '../services/walletApi';
import { useTokens } from '../hooks/useTokens';
import { CHAINS, type ChainKey } from '../constants/chains';

// Types and interfaces
interface Asset {
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

interface AssetCardProps {
  asset: Asset;
  isExpanded: boolean;
  isSelected: boolean;
  onCardPress: () => void;
  onSelectPress: () => void;
  onRadioSelect: () => void;
  isHidden: boolean;
}

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
};

// AssetCard Component with Radio Button
const AssetCard: React.FC<AssetCardProps> = ({
  asset,
  isExpanded,
  isSelected,
  onCardPress,
  onSelectPress,
  onRadioSelect,
  isHidden,
}) => {
  const animation = useRef(new Animated.Value(0)).current;

  useEffect(() => {
    Animated.timing(animation, {
      toValue: isExpanded ? 1 : 0,
      duration: 300,
      useNativeDriver: false,
    }).start();
  }, [isExpanded]);

  const heightInterpolation = animation.interpolate({
    inputRange: [0, 1],
    outputRange: [0, 160],
  });

  return (
    <View style={[styles.assetCardContainer, isSelected && styles.selectedAssetCardContainer]}>
      <TouchableOpacity
        onPress={onCardPress}
        activeOpacity={0.7}
        style={styles.assetCard}
      >
        {/* Asset Header Row */}
        <View style={styles.assetHeader}>
          <View style={styles.assetInfo}>
            <View style={[styles.assetIcon, { backgroundColor: asset.iconColor + '20' }, isSelected && { backgroundColor: asset.iconColor + '40' }]}>
              {asset.iconUrl ? (
                <Image source={{ uri: asset.iconUrl }} style={styles.assetIconImage} />
              ) : (
                <Text style={[styles.assetIconText, { color: asset.iconColor }]}>
                  {asset.symbol.charAt(0)}
                </Text>
              )}
            </View>
            <View style={styles.assetTextInfo}>
              <View style={styles.assetTitleRow}>
                <Text style={styles.assetSymbol}>{asset.symbol}</Text>
                {isSelected && (
                  <View style={styles.selectedBadge}>
                    <Icon name="check-circle" size={12} color="#000" />
                    <Text style={styles.selectedBadgeText}>Selected</Text>
                  </View>
                )}
              </View>
              <Text style={styles.assetName}>{asset.name}</Text>
              {asset.details?.chain && (
                <View style={styles.chainTag}>
                  <View style={[styles.chainDot, { backgroundColor: asset.details.chainKey ? CHAINS[asset.details.chainKey].iconColor : '#999' }]} />
                  <Text style={styles.chainTagText}>{asset.details.chain}</Text>
                </View>
              )}
            </View>
          </View>
          <View style={styles.rightSection}>
            <View style={styles.assetBalanceInfo}>
              <Text style={styles.assetBalance}>
                {isHidden ? '••••' : asset.balance.toFixed(asset.details?.type === 'Native Token' ? 4 : 2)}
              </Text>
              <Text style={styles.assetUSDValue}>
                {isHidden ? '••••' : `$${asset.usdValue.toLocaleString()}`}
              </Text>
            </View>
            <TouchableOpacity
              onPress={onRadioSelect}
              style={[styles.radioButtonContainer, isSelected ? styles.radioButtonSelected : styles.radioButtonUnselected]}
              hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}
            >
              <Icon
                name={isSelected ? 'radio-button-checked' : 'radio-button-unchecked'}
                size={24}
                color={isSelected ? GOLD_COLORS.dark : '#C7C7CC'}
              />
            </TouchableOpacity>
          </View>
        </View>

        {/* Expandable Details */}
        <Animated.View style={{ height: heightInterpolation, opacity: animation, overflow: 'hidden' }}>
          <View style={styles.assetDetails}>
            <View style={styles.detailSection}>
              <Text style={styles.detailSectionTitle}>Asset Details</Text>
              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Type:</Text>
                <Text style={styles.detailValue}>{asset.details?.type}</Text>
              </View>
              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Chain:</Text>
                <View style={{ flexDirection: 'row', alignItems: 'center', flex: 2, justifyContent: 'flex-end' }}>
                  <View style={{
                    width: 8, height: 8, borderRadius: 4,
                    backgroundColor: asset.details?.chainKey ? CHAINS[asset.details.chainKey].iconColor : '#999',
                    marginRight: 6,
                  }} />
                  <Text style={styles.detailValue}>{asset.details?.chain}</Text>
                </View>
              </View>
            </View>
            {!isSelected ? (
              <TouchableOpacity
                onPress={onSelectPress}
                style={styles.selectButton}
                activeOpacity={0.8}
              >
                <Icon name="check-circle" size={20} color="#000" style={styles.selectIcon} />
                <Text style={styles.selectButtonText}>Select as Debit Account</Text>
              </TouchableOpacity>
            ) : (
              <View style={styles.alreadySelected}>
                <Icon name="verified" size={20} color={GOLD_COLORS.dark} />
                <Text style={styles.alreadySelectedText}>Currently Selected</Text>
              </View>
            )}
          </View>
        </Animated.View>
      </TouchableOpacity>
    </View>
  );
};

const WalletScreen: React.FC = () => {
  const { tokens, tokenList } = useTokens();

  // Build asset list from token config + chain config (including native tokens)
  const buildAssetList = useCallback((): Asset[] => {
    const assetList: Asset[] = [];
    let idCounter = 1;

    // Add native tokens for each chain
    for (const [chainKey, chain] of Object.entries(CHAINS)) {
      assetList.push({
        id: String(idCounter++),
        symbol: chain.nativeCurrency.symbol,
        name: `${chain.name} Native`,
        balance: 0,
        usdValue: 0,
        iconColor: chain.iconColor,
        iconUrl: chain.iconUrl,
        details: {
          type: 'Native Token',
          chain: chain.name,
          chainKey: chainKey as ChainKey,
        },
      });
    }

    // Add ERC-20 tokens per chain they support
    for (const token of tokenList) {
      for (const chainKey of token.chains) {
        const chain = CHAINS[chainKey];
        assetList.push({
          id: String(idCounter++),
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

    return assetList;
  }, [tokenList]);

  // State Management
  const [assets, setAssets] = useState<Asset[]>([]);
  const [selectedAsset, setSelectedAsset] = useState<Asset | null>(null);
  const [isAssetListExpanded, setIsAssetListExpanded] = useState(false);
  const [expandedAssetId, setExpandedAssetId] = useState<string | null>(null);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [showFundModal, setShowFundModal] = useState(false);
  const [showSendModal, setShowSendModal] = useState(false);

  // Animation References
  const listAnimation = useRef(new Animated.Value(0)).current;
  const rotationAnimation = useRef(new Animated.Value(0)).current;
  const selectionAnimation = useRef(new Animated.Value(1)).current;

  // Fetch wallet balances from API
  const fetchBalances = useCallback(async () => {
    const assetList = buildAssetList();
    try {
      const result = await getWalletBalances();
      if (result.success && result.data) {
        // API returns: { balances: { sonic: { S: "0", MCGP: "0", ... }, bsc: { tBNB: "0.000145", ... } } }
        const balancesData = result.data.balances || result.data;

        const updatedAssets = assetList.map(asset => {
          const chainKey = asset.details?.chainKey;
          if (!chainKey) return asset;

          const chainBalances = balancesData[chainKey];
          if (!chainBalances) return asset;

          const rawBalance = chainBalances[asset.symbol];
          const balance = rawBalance ? parseFloat(rawBalance) : 0;
          // For stablecoins, usdValue ≈ balance. For others, use balance as placeholder.
          const usdValue = balance * 1.0;
          return { ...asset, balance, usdValue };
        });

        setAssets(updatedAssets);

        // Select first asset with a balance, or USDT on Sonic, or the first asset
        const withBalance = updatedAssets.find(a => a.balance > 0);
        const usdt = updatedAssets.find(a => a.symbol === 'USDT' && a.details?.chainKey === 'sonic');
        handleAssetSelect(withBalance || usdt || updatedAssets[0], false);
      } else {
        setAssets(assetList);
        const usdt = assetList.find(a => a.symbol === 'USDT' && a.details?.chainKey === 'sonic');
        if (usdt) handleAssetSelect(usdt, false);
      }
    } catch (err) {
      console.error('Failed to fetch wallet balances:', err);
      setAssets(assetList);
      const usdt = assetList.find(a => a.symbol === 'USDT' && a.details?.chainKey === 'sonic');
      if (usdt) handleAssetSelect(usdt, false);
    } finally {
      setIsLoading(false);
    }
  }, [buildAssetList]);

  useEffect(() => {
    fetchBalances();
  }, [fetchBalances]);

  // Handle asset selection via radio button or select button
  const handleAssetSelect = (asset: Asset, shouldCollapseList: boolean = true) => {
    selectionAnimation.setValue(0);
    Animated.spring(selectionAnimation, {
      toValue: 1,
      friction: 3,
      tension: 40,
      useNativeDriver: true,
    }).start();

    setSelectedAsset(asset);
    setExpandedAssetId(null);

    if (shouldCollapseList && isAssetListExpanded) {
      collapseAssetList();
    }
  };

  const handleRadioSelect = (asset: Asset) => {
    handleAssetSelect(asset, false);
  };

  const handleAssetCardPress = (assetId: string) => {
    if (expandedAssetId === assetId) {
      setExpandedAssetId(null);
    } else {
      setExpandedAssetId(assetId);
    }
  };

  const toggleAssetList = () => {
    if (isAssetListExpanded) {
      collapseAssetList();
    } else {
      expandAssetList();
    }
  };

  const expandAssetList = () => {
    setIsAssetListExpanded(true);
    Animated.parallel([
      Animated.timing(listAnimation, {
        toValue: 1,
        duration: 300,
        useNativeDriver: false,
      }),
      Animated.timing(rotationAnimation, {
        toValue: 1,
        duration: 300,
        useNativeDriver: true,
      }),
    ]).start();
  };

  const collapseAssetList = () => {
    Animated.parallel([
      Animated.timing(listAnimation, {
        toValue: 0,
        duration: 200,
        useNativeDriver: false,
      }),
      Animated.timing(rotationAnimation, {
        toValue: 0,
        duration: 200,
        useNativeDriver: true,
      }),
    ]).start(() => {
      setIsAssetListExpanded(false);
      setExpandedAssetId(null);
    });
  };

  const refreshAssets = async () => {
    setIsRefreshing(true);
    await fetchBalances();
    setIsRefreshing(false);
  };

  // Animation interpolations
  const maxListHeight = Math.min(assets.length * 80 + 100, 500);
  const listHeight = listAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: [0, maxListHeight],
  });

  const rotateIcon = rotationAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: ['0deg', '180deg'],
  });

  const selectionScale = selectionAnimation.interpolate({
    inputRange: [0, 0.5, 1],
    outputRange: [0.95, 1.05, 1],
  });

  // Render asset icon with token logo or fallback letter
  const renderAssetIcon = (symbol: string, color: string, size: number = 40, iconUrl?: string) => (
    <View style={[styles.selectorIcon, { width: size, height: size, borderRadius: size / 2, backgroundColor: color + '30' }]}>
      {iconUrl ? (
        <Image source={{ uri: iconUrl }} style={{ width: size * 0.6, height: size * 0.6, borderRadius: size * 0.3 }} />
      ) : (
        <Text style={[styles.selectorIconText, { fontSize: size / 2, color }]}>
          {symbol.charAt(0)}
        </Text>
      )}
    </View>
  );

  const handleTransfer = () => {
    router.push(`/transfer`);
  };

  const handleQuickAction = (action: string) => {
    if (action === 'fund') {
      setShowFundModal(true);
    } else if (action === 'send') {
      setShowSendModal(true);
    } else {
      //@ts-expect-error
      router.push(`/${action}`);
    }
  };

  const handleFundOption = (option: 'fiat' | 'crypto') => {
    setShowFundModal(false);
    if (option === 'crypto') {
      router.push(`/fund`);
    }
    if (option === 'fiat') {
      router.push(`/fundfiat`);
    }
  };

  const handleSendOption = (option: 'fiat' | 'crypto') => {
    setShowSendModal(false);
    if (option === 'crypto') {
      router.push(`/send`);
    }
    if (option === 'fiat') {
      router.push(`/sendfiat`);
    }
  };

  // Fund Selection Modal Component
  const FundSelectionModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showFundModal}
      onRequestClose={() => setShowFundModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Select Deposit Method</Text>
            <TouchableOpacity
              onPress={() => setShowFundModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <Text style={styles.modalSubtitle}>Choose how you want to fund your account</Text>

          <View style={styles.optionsContainer}>
            {/* Fiat Deposit Option */}
            <TouchableOpacity
              style={styles.optionCard}
              onPress={() => handleFundOption('fiat')}
              activeOpacity={0.8}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#E3F2FD' }]}>
                <Icon name="account-balance" size={32} color="#1976D2" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Fiat Deposit</Text>
                <Text style={styles.optionDescription}>
                  Deposit USD via bank transfer, credit card, or other fiat methods
                </Text>
                <View style={styles.optionDetails}>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Bank Transfer</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Credit/Debit Card</Text>
                  </View>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </TouchableOpacity>

            {/* Crypto Deposit Option */}
            <TouchableOpacity
              style={styles.optionCard}
              onPress={() => handleFundOption('crypto')}
              activeOpacity={0.8}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#FFF3E0' }]}>
                <Icon name="account-balance-wallet" size={32} color="#E65100" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Crypto Deposit</Text>
                <Text style={styles.optionDescription}>
                  Deposit cryptocurrency from external wallets or exchanges
                </Text>
                <View style={styles.optionDetails}>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>External Wallets</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Crypto Exchanges</Text>
                  </View>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <TouchableOpacity
            style={styles.modalCancelButton}
            onPress={() => setShowFundModal(false)}
            activeOpacity={0.8}
          >
            <Text style={styles.modalCancelText}>Cancel</Text>
          </TouchableOpacity>
        </View>
      </View>
    </Modal>
  );

  // Send Selection Modal Component
  const SendSelectionModal = () => (
    <Modal
      animationType="slide"
      transparent={true}
      visible={showSendModal}
      onRequestClose={() => setShowSendModal(false)}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContent}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Select Withdrawal Method</Text>
            <TouchableOpacity
              onPress={() => setShowSendModal(false)}
              style={styles.modalCloseButton}
            >
              <Icon name="close" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <Text style={styles.modalSubtitle}>Choose how you want to send funds</Text>

          <View style={styles.optionsContainer}>
            {/* Withdraw as Fiat Option */}
            <TouchableOpacity
              style={styles.optionCard}
              onPress={() => handleSendOption('fiat')}
              activeOpacity={0.8}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#E8F5E9' }]}>
                <Icon name="attach-money" size={32} color="#2E7D32" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Withdraw as Fiat</Text>
                <Text style={styles.optionDescription}>
                  Withdraw funds to your bank account or mobile money
                </Text>
                <View style={styles.optionDetails}>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Bank Transfer</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Mobile Money</Text>
                  </View>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </TouchableOpacity>

            {/* Withdraw as Crypto Option */}
            <TouchableOpacity
              style={styles.optionCard}
              onPress={() => handleSendOption('crypto')}
              activeOpacity={0.8}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#F3E5F5' }]}>
                <Icon name="account-balance-wallet" size={32} color="#7B1FA2" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Withdraw as Crypto</Text>
                <Text style={styles.optionDescription}>
                  Send cryptocurrencies directly to external wallets or exchanges
                </Text>
                <View style={styles.optionDetails}>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>External Wallets</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Crypto Exchanges</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="bolt" size={16} color="#FF9800" />
                    <Text style={styles.detailText}>Fast Transactions</Text>
                  </View>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </TouchableOpacity>
          </View>

          <TouchableOpacity
            style={styles.modalCancelButton}
            onPress={() => setShowSendModal(false)}
            activeOpacity={0.8}
          >
            <Text style={styles.modalCancelText}>Cancel</Text>
          </TouchableOpacity>
        </View>
      </View>
    </Modal>
  );

  return (
    <ScrollView
      style={styles.container}
      showsVerticalScrollIndicator={false}
      refreshControl={
        <RefreshControl refreshing={isRefreshing} onRefresh={refreshAssets} tintColor={GOLD_COLORS.primary} />
      }
    >
      {/* Header */}
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Wallet</Text>
        <TouchableOpacity
          onPress={() => setIsValuesHidden(!isValuesHidden)}
          style={styles.visibilityButton}
        >
          <Icon
            name={isValuesHidden ? 'visibility-off' : 'visibility'}
            size={24}
            color={GOLD_COLORS.dark}
          />
        </TouchableOpacity>
      </View>

      {/* Total Balance */}
      <View style={styles.balanceCard}>
        <Text style={styles.balanceLabelHeader}>Total Balance</Text>
        <Text style={styles.balanceAmount}>
          {isValuesHidden ? '••••••' : `$${assets.reduce((sum, asset) => sum + asset.usdValue, 0).toLocaleString()}`}
        </Text>
      </View>

      {/* Quick Actions */}
      <View style={styles.quickActionsContainer}>
        <View style={styles.quickActionsGrid}>
          <TouchableOpacity
            style={styles.quickActionButton}
            onPress={() => handleQuickAction('fund')}
            activeOpacity={0.8}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="account-balance" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Fund</Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={styles.quickActionButton}
            onPress={() => handleQuickAction('swap')}
            activeOpacity={0.8}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="swap-horiz" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Swap</Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={styles.quickActionButton}
            onPress={() => handleQuickAction('send')}
            activeOpacity={0.8}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="send" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Send</Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={styles.quickActionButton}
            onPress={() => handleTransfer()}
            activeOpacity={0.8}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="sync-alt" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Transfer</Text>
          </TouchableOpacity>
        </View>
      </View>

      {/* Debit Account Selector */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Debit Account</Text>
        <Text style={styles.sectionSubtitle}>Select asset to debit from</Text>

        <Animated.View style={{ transform: [{ scale: selectionScale }] }}>
          <TouchableOpacity
            onPress={toggleAssetList}
            style={styles.assetSelector}
            activeOpacity={0.8}
          >
            <View style={styles.selectorContent}>
              <View style={styles.selectorLeft}>
                {selectedAsset ? (
                  <View style={styles.selectedAssetDisplay}>
                    {renderAssetIcon(selectedAsset.symbol, selectedAsset.iconColor, 44, selectedAsset.iconUrl)}
                    <View style={styles.selectedAssetInfo}>
                      <Text style={styles.selectedAssetSymbol}>
                        {selectedAsset.symbol}
                      </Text>
                      <Text style={styles.selectedAssetName}>
                        {selectedAsset.name}
                      </Text>
                      <Text style={styles.selectedAssetBalance}>
                        Available: {isValuesHidden ? '••••••' : selectedAsset.balance.toFixed(selectedAsset.details?.type === 'Native Token' ? 4 : 2)} {selectedAsset.symbol}
                      </Text>
                    </View>
                  </View>
                ) : (
                  <View style={styles.placeholderDisplay}>
                    <Icon name="account-balance-wallet" size={28} color={GOLD_COLORS.primary} />
                    <Text style={styles.placeholderText}>Select Asset</Text>
                  </View>
                )}
              </View>
              <Animated.View style={{ transform: [{ rotate: rotateIcon }] }}>
                <Icon name="keyboard-arrow-down" size={32} color={GOLD_COLORS.dark} />
              </Animated.View>
            </View>
          </TouchableOpacity>
        </Animated.View>

        {/* Expandable Asset List */}
        {isAssetListExpanded && (
          <Animated.View style={[styles.assetListContainer, { height: listHeight }]}>
            <ScrollView
              style={styles.assetListScroll}
              showsVerticalScrollIndicator={true}
              nestedScrollEnabled={true}
            >
              <View style={styles.assetListHeader}>
                <Text style={styles.assetListTitle}>
                  Select Asset <Text style={styles.instructionText}>(Click radio button to select)</Text>
                </Text>
                <TouchableOpacity
                  onPress={refreshAssets}
                  disabled={isLoading}
                  style={styles.refreshButton}
                >
                  {isLoading ? (
                    <ActivityIndicator size="small" color={GOLD_COLORS.primary} />
                  ) : (
                    <Icon name="refresh" size={22} color={GOLD_COLORS.primary} />
                  )}
                </TouchableOpacity>
              </View>

              {/* Asset Cards */}
              {assets.map(asset => (
                <AssetCard
                  key={asset.id}
                  asset={asset}
                  isExpanded={expandedAssetId === asset.id}
                  isSelected={selectedAsset?.id === asset.id}
                  onCardPress={() => handleAssetCardPress(asset.id)}
                  onSelectPress={() => handleAssetSelect(asset)}
                  onRadioSelect={() => handleRadioSelect(asset)}
                  isHidden={isValuesHidden}
                />
              ))}

              {/* Selection Help Text */}
              <View style={styles.helpTextContainer}>
                <Icon name="info" size={16} color={GOLD_COLORS.dark} />
                <Text style={styles.helpText}>
                  Click the radio button to select an asset, or tap the card to expand details
                </Text>
              </View>
            </ScrollView>
          </Animated.View>
        )}
      </View>

      {/* Selected Asset Summary */}
      {selectedAsset && (
        <Animated.View
          style={[
            styles.selectedAssetSummary,
            { transform: [{ scale: selectionScale }] }
          ]}
        >
          <View style={styles.summaryHeader}>
            <Text style={styles.summaryTitle}>Selected Asset Summary</Text>
            <View style={styles.summaryBadge}>
              <Text style={styles.summaryBadgeText}>ACTIVE</Text>
            </View>
          </View>

          <View style={styles.summaryContent}>
            <View style={styles.summaryAssetInfo}>
              {renderAssetIcon(selectedAsset.symbol, selectedAsset.iconColor, 48, selectedAsset.iconUrl)}
              <View>
                <Text style={styles.summarySymbol}>{selectedAsset.symbol}</Text>
                <Text style={styles.summaryName}>{selectedAsset.name}</Text>
                <View style={{ flexDirection: 'row', alignItems: 'center', marginLeft: 16, marginTop: 4 }}>
                  <View style={{
                    width: 8, height: 8, borderRadius: 4,
                    backgroundColor: selectedAsset.details?.chainKey ? CHAINS[selectedAsset.details.chainKey].iconColor : '#999',
                    marginRight: 4,
                  }} />
                  <Text style={styles.summaryType}>{selectedAsset.details?.chain}</Text>
                </View>
              </View>
            </View>

            <View style={styles.summaryBalance}>
              <View style={styles.balanceRow}>
                <Text style={styles.balanceLabel}>Balance:</Text>
                <Text style={styles.balanceValue}>
                  {isValuesHidden ? '••••••' : selectedAsset.balance.toFixed(selectedAsset.details?.type === 'Native Token' ? 4 : 2)} {selectedAsset.symbol}
                </Text>
              </View>
              <View style={styles.balanceRow}>
                <Text style={styles.balanceLabel}>USD Value:</Text>
                <Text style={[styles.balanceValue, styles.usdValue]}>
                  {isValuesHidden ? '••••' : `$${selectedAsset.usdValue.toLocaleString()}`}
                </Text>
              </View>
            </View>
          </View>
        </Animated.View>
      )}

      {/* Fund Selection Modal */}
      <FundSelectionModal />

      {/* Send Selection Modal */}
      <SendSelectionModal />
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 60 : 40,
    paddingBottom: 20,
    backgroundColor: '#FFFFFF',
  },
  headerTitle: {
    fontSize: 24,
    fontWeight: '800',
    color: '#000000',
  },
  visibilityButton: {
    padding: 8,
    borderRadius: 8,
    backgroundColor: GOLD_COLORS.light,
  },
  balanceCard: {
    backgroundColor: GOLD_COLORS.primary,
    marginHorizontal: 20,
    marginTop: 20,
    borderRadius: 20,
    padding: 24,
    shadowColor: GOLD_COLORS.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
  balanceLabelHeader: {
    fontSize: 12,
    color: 'rgba(0, 0, 0, 0.7)',
    marginBottom: 8,
    fontWeight: '600',
  },
  balanceAmount: {
    fontSize: 22,
    fontWeight: '900',
    color: '#000000',
  },
  section: {
    backgroundColor: '#FFFFFF',
    marginTop: 20,
    paddingHorizontal: 20,
    paddingVertical: 24,
    borderRadius: 16,
    marginHorizontal: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  sectionTitle: {
    fontSize: 24,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  sectionSubtitle: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 20,
  },
  assetSelector: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    paddingHorizontal: 20,
    paddingVertical: 18,
    borderWidth: 2,
    borderColor: GOLD_COLORS.muted,
  },
  selectorContent: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  selectorLeft: {
    flex: 1,
  },
  placeholderDisplay: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  placeholderText: {
    fontSize: 18,
    fontWeight: '600',
    color: '#000000',
    marginLeft: 12,
  },
  selectedAssetDisplay: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  selectorIcon: {
    justifyContent: 'center',
    alignItems: 'center',
  },
  selectorIconText: {
    fontWeight: '900',
  },
  selectedAssetInfo: {
    marginLeft: 16,
    flex: 1,
  },
  selectedAssetSymbol: {
    fontSize: 20,
    fontWeight: '900',
    color: '#000000',
  },
  selectedAssetName: {
    fontSize: 14,
    color: '#666666',
    marginTop: 2,
  },
  selectedAssetBalance: {
    fontSize: 13,
    color: GOLD_COLORS.dark,
    marginTop: 6,
    fontWeight: '600',
  },
  assetListContainer: {
    marginTop: 20,
    overflow: 'hidden',
  },
  assetListScroll: {
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: '#E5E5EA',
  },
  assetListHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 16,
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  assetListTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    flex: 1,
  },
  instructionText: {
    fontSize: 12,
    fontWeight: 'normal',
    color: '#666666',
  },
  refreshButton: {
    padding: 6,
    marginLeft: 10,
  },
  assetCardContainer: {
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  selectedAssetCardContainer: {
    backgroundColor: GOLD_COLORS.light,
    borderLeftWidth: 4,
    borderLeftColor: GOLD_COLORS.primary,
  },
  assetCard: {
    paddingHorizontal: 16,
    paddingVertical: 12,
    overflow: 'hidden',
  },
  assetHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  assetInfo: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  assetTextInfo: {
    flex: 1,
  },
  assetIcon: {
    width: 44,
    height: 44,
    borderRadius: 22,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  assetIconImage: {
    width: 28,
    height: 28,
    borderRadius: 14,
  },
  assetIconText: {
    fontWeight: '800',
    fontSize: 18,
  },
  assetTitleRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 2,
  },
  assetSymbol: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  selectedBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 10,
    marginLeft: 8,
  },
  selectedBadgeText: {
    fontSize: 10,
    fontWeight: '900',
    color: '#000000',
    marginLeft: 4,
  },
  assetName: {
    fontSize: 13,
    color: '#666666',
  },
  chainTag: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 3,
  },
  chainDot: {
    width: 6,
    height: 6,
    borderRadius: 3,
    marginRight: 4,
  },
  chainTagText: {
    fontSize: 11,
    color: '#999999',
    fontWeight: '500',
  },
  rightSection: {
    flexDirection: 'row',
    alignItems: 'center',
    marginLeft: 10,
  },
  assetBalanceInfo: {
    alignItems: 'flex-end',
    marginRight: 12,
  },
  assetBalance: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  assetUSDValue: {
    fontSize: 13,
    color: '#34C759',
    marginTop: 4,
    fontWeight: '600',
  },
  radioButtonContainer: {
    padding: 4,
    marginLeft: 4,
  },
  radioButtonSelected: {},
  radioButtonUnselected: {},
  assetDetails: {
    marginTop: 16,
    paddingTop: 16,
    paddingBottom: 12,
    borderTopWidth: 1,
    borderTopColor: '#E5E5EA',
  },
  detailSection: {
    marginBottom: 16,
  },
  detailSectionTitle: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 12,
  },
  detailRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 8,
  },
  detailLabel: {
    fontSize: 13,
    color: '#666666',
    flex: 1,
  },
  detailValue: {
    fontSize: 13,
    color: '#000000',
    fontWeight: '600',
    textAlign: 'right',
  },
  selectButton: {
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 14,
    paddingHorizontal: 20,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    shadowColor: GOLD_COLORS.dark,
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.2,
    shadowRadius: 4,
    elevation: 3,
  },
  selectIcon: {
    marginRight: 10,
  },
  selectButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
  },
  alreadySelected: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 12,
  },
  alreadySelectedText: {
    fontSize: 14,
    fontWeight: '600',
    color: GOLD_COLORS.dark,
    marginLeft: 8,
  },
  helpTextContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    backgroundColor: GOLD_COLORS.light,
    margin: 12,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  helpText: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
    fontWeight: '500',
  },
  selectedAssetSummary: {
    backgroundColor: '#FFFFFF',
    marginHorizontal: 15,
    marginTop: 20,
    marginBottom: 40,
    borderRadius: 16,
    padding: 10,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 3 },
    shadowOpacity: 0.08,
    shadowRadius: 6,
    elevation: 4,
  },
  summaryHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 20,
  },
  summaryTitle: {
    fontSize: 14,
    fontWeight: '800',
    color: '#000000',
  },
  summaryBadge: {
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 10,
    paddingVertical: 6,
    borderRadius: 12,
  },
  summaryBadgeText: {
    fontSize: 11,
    fontWeight: '900',
    color: '#000000',
  },
  summaryContent: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
  },
  summaryAssetInfo: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  summarySymbol: {
    fontSize: 14,
    fontWeight: '900',
    color: '#000000',
    marginLeft: 16,
  },
  summaryName: {
    fontSize: 14,
    color: '#666666',
    marginLeft: 16,
    marginTop: 2,
  },
  summaryType: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
  },
  summaryBalance: {
    alignItems: 'flex-end',
  },
  balanceRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 8,
  },
  balanceLabel: {
    fontSize: 12,
    color: '#666666',
    marginRight: 8,
  },
  balanceValue: {
    fontSize: 14,
    fontWeight: '800',
    color: '#000000',
  },
  usdValue: {
    color: '#34C759',
  },
  // Quick Actions Styles
  quickActionsContainer: {
    backgroundColor: '#FFFFFF',
    marginHorizontal: 20,
    marginTop: 20,
    marginBottom: 40,
    borderRadius: 16,
    padding: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  quickActionsGrid: {
    flexDirection: 'row',
    justifyContent: 'space-between',
  },
  quickActionButton: {
    alignItems: 'center',
    justifyContent: 'center',
    width: (Dimensions.get('window').width - 80) / 4,
    paddingVertical: 12,
  },
  actionIconContainer: {
    width: 48,
    height: 48,
    borderRadius: 24,
    backgroundColor: GOLD_COLORS.light,
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: 8,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  quickActionText: {
    fontSize: 12,
    fontWeight: '600',
    color: '#000000',
    textAlign: 'center',
  },
  // Modal Styles
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 20,
  },
  modalContent: {
    backgroundColor: '#FFFFFF',
    borderRadius: 20,
    width: '100%',
    maxWidth: 400,
    padding: 24,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 10 },
    shadowOpacity: 0.25,
    shadowRadius: 20,
    elevation: 10,
  },
  modalHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 16,
  },
  modalTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
  },
  modalCloseButton: {
    padding: 4,
  },
  modalSubtitle: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 24,
  },
  optionsContainer: {
    marginBottom: 24,
  },
  optionCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FAFAFA',
    borderRadius: 16,
    padding: 16,
    marginBottom: 16,
    borderWidth: 1,
    borderColor: '#E0E0E0',
  },
  optionIcon: {
    width: 56,
    height: 56,
    borderRadius: 28,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 16,
  },
  optionContent: {
    flex: 1,
  },
  optionTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  optionDescription: {
    fontSize: 13,
    color: '#666666',
    marginBottom: 12,
  },
  optionDetails: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
  },
  detailItem: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F5F5F5',
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 8,
  },
  detailText: {
    fontSize: 12,
    color: '#333',
    marginLeft: 4,
    fontWeight: '500',
  },
  modalCancelButton: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    paddingVertical: 16,
    alignItems: 'center',
  },
  modalCancelText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
});

export default WalletScreen;
