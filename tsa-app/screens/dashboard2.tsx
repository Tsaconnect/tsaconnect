import { router } from 'expo-router';
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  Pressable,
  ScrollView,
  Animated,
  StyleSheet,
  Dimensions,
  Platform,
  ActivityIndicator,
  Modal,
  RefreshControl,
  Alert,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { api, Asset, AssetsResponse, PortfolioTotals } from '@/components/services/api';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getWalletBalances } from '../services/walletApi';

// Types and interfaces
interface AssetCardProps {
  asset: Asset;
  isExpanded: boolean;
  isSelected: boolean;
  onCardPress: () => void;
  onSelectPress: () => void;
  onRadioSelect: () => void;
  isHidden: boolean;
}

interface PortfolioData {
  assets: Asset[];
  totals: PortfolioTotals;
  selectedAsset: Asset | null;
  lastUpdated: string;
}

// Default assets constants
const DEFAULT_ASSETS: Asset[] = [
  {
    _id: 'mcgp-001',
    id: 'mcgp-001',
    symbol: 'MCGP',
    name: 'Mason Capital Gold Point',
    balance: 0,
    usdValue: 0,
    isSelected: false,
    isHidden: false,
    details: {
      type: 'Stablecoin',
      chain: 'Polygon',
    },
    currentPrice: 1.00,
    priceChange24h: 0.00,
  },
  {
    _id: 'usdc-001',
    id: 'usdc-001',
    symbol: 'USDC',
    name: 'USD Coin',
    balance: 0,
    usdValue: 0,
    isSelected: false,
    isHidden: false,
    details: {
      type: 'Stablecoin',
      chain: 'Polygon',
    },
    currentPrice: 1.00,
    priceChange24h: 0.01,
  },
  {
    _id: 'usdt-001',
    id: 'usdt-001',
    symbol: 'USDT',
    name: 'Tether USD',
    balance: 0,
    usdValue: 0,
    isSelected: false,
    isHidden: false,
    details: {
      type: 'Stablecoin',
      chain: 'Polygon',
    },
    currentPrice: 1.00,
    priceChange24h: -0.02,
  },
];

// User balance interface
interface UserBalance {
  mcpgBalance: number;
  usdcBalance: number;
  usdtBalance: number;
  totalBalance: number;
}

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
};

// AssetCard Component with Pressable
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
    outputRange: [70, 160],
  });

  // Format price change with color
  const priceChange = asset.priceChange24h || 0;
  const priceChangeColor = priceChange >= 0 ? '#34C759' : '#FF3B30';
  const priceChangeText = priceChange >= 0 ? `+${priceChange.toFixed(2)}%` : `${priceChange.toFixed(2)}%`;

  return (
    <Pressable
      onPress={onCardPress}
      style={({ pressed }) => [
        styles.assetCardContainer,
        isSelected && styles.selectedAssetCardContainer,
        pressed && styles.pressedCard,
      ]}
    >
      <Animated.View style={[styles.assetCard, { height: heightInterpolation }]}>
        {/* Asset Header - Always Visible */}
        <View style={styles.assetHeader}>
          <View style={styles.assetInfo}>
            <View style={[
              styles.assetIcon,
              isSelected && styles.selectedAssetIcon
            ]}>
              <Text style={styles.assetIconText}>
                {asset.symbol.charAt(0)}
              </Text>
            </View>
            <View style={styles.assetTextInfo}>
              <View style={styles.assetTitleRow}>
                <Text style={[
                  styles.assetSymbol,
                  isSelected && styles.selectedAssetSymbol
                ]}>
                  {asset.symbol}
                </Text>
                {isSelected && (
                  <View style={styles.selectedBadge}>
                    <Icon name="check-circle" size={14} color="#000000" />
                    <Text style={styles.selectedBadgeText}>Selected</Text>
                  </View>
                )}
              </View>
              <Text style={styles.assetName}>{asset.name}</Text>
              {asset.priceChange24h !== undefined && (
                <Text style={[styles.priceChange, { color: priceChangeColor }]}>
                  {priceChangeText}
                </Text>
              )}
            </View>
          </View>

          <View style={styles.rightSection}>
            <View style={styles.assetBalanceInfo}>
              <Text style={styles.assetBalance}>
                {isHidden ? '••••••' : asset.balance.toFixed(2)}
              </Text>
              <Text style={styles.assetUSDValue}>
                {isHidden ? '••••' : `$${asset.usdValue.toLocaleString()}`}
              </Text>
            </View>

            {/* Radio Button for Selection */}
            <Pressable
              onPress={(e) => {
                e.stopPropagation();
                onRadioSelect();
              }}
              style={({ pressed }) => [
                styles.radioButtonContainer,
                pressed && styles.pressedRadioButton,
              ]}
            >
              {isSelected ? (
                <View style={styles.radioButtonSelected}>
                  <Icon name="radio-button-checked" size={24} color={GOLD_COLORS.primary} />
                </View>
              ) : (
                <View style={styles.radioButtonUnselected}>
                  <Icon name="radio-button-unchecked" size={24} color="#CCCCCC" />
                </View>
              )}
            </Pressable>
          </View>
        </View>

        {/* Expanded Details */}
        {isExpanded && (
          <View style={styles.assetDetails}>
            <View style={styles.detailSection}>
              <Text style={styles.detailSectionTitle}>Asset Details</Text>
              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Type:</Text>
                <Text style={styles.detailValue}>{asset.details?.type || 'Token'}</Text>
              </View>
              {asset.details?.chain && (
                <View style={styles.detailRow}>
                  <Text style={styles.detailLabel}>Chain:</Text>
                  <Text style={styles.detailValue}>{asset.details.chain}</Text>
                </View>
              )}
              {asset.currentPrice !== undefined && (
                <View style={styles.detailRow}>
                  <Text style={styles.detailLabel}>Current Price:</Text>
                  <Text style={styles.detailValue}>${asset.currentPrice.toFixed(2)}</Text>
                </View>
              )}
              <View style={styles.detailRow}>
                <Text style={styles.detailLabel}>Value:</Text>
                <Text style={styles.detailValue}>
                  ${asset.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
                </Text>
              </View>
            </View>

            {/* Select Button - Only show if not already selected */}
            {!isSelected && (
              <Pressable
                onPress={onSelectPress}
                style={({ pressed }) => [
                  styles.selectButton,
                  pressed && styles.pressedSelectButton,
                ]}
              >
                <Icon name="check-circle" size={20} color="#000000" style={styles.selectIcon} />
                <Text style={styles.selectButtonText}>Select {asset.symbol}</Text>
              </Pressable>
            )}

            {/* Already Selected Indicator */}
            {isSelected && (
              <View style={styles.alreadySelected}>
                <Icon name="verified" size={24} color={GOLD_COLORS.primary} />
                <Text style={styles.alreadySelectedText}>Currently Selected</Text>
              </View>
            )}
          </View>
        )}
      </Animated.View>
    </Pressable>
  );
};

// Main Dashboard Component
const Dashboard: React.FC = () => {
  // State Management
  const [portfolioData, setPortfolioData] = useState<PortfolioData>({
    assets: [],
    totals: { balance: 0, usdValue: 0, dailyChange: 0 },
    selectedAsset: null,
    lastUpdated: new Date().toISOString(),
  });
  const [userBalance, setUserBalance] = useState<UserBalance>({
    mcpgBalance: 0,
    usdcBalance: 0,
    usdtBalance: 0,
    totalBalance: 0,
  });
  const [isAssetListExpanded, setIsAssetListExpanded] = useState(false);
  const [expandedAssetId, setExpandedAssetId] = useState<string | null>(null);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [showFundModal, setShowFundModal] = useState(false);
  const [showSendModal, setShowSendModal] = useState(false);
  const [authToken, setAuthToken] = useState<string | null>(null);
  const [showBackupBanner, setShowBackupBanner] = useState(false);

  // Animation References
  const listAnimation = useRef(new Animated.Value(0)).current;
  const rotationAnimation = useRef(new Animated.Value(0)).current;
  const selectionAnimation = useRef(new Animated.Value(1)).current;

  // Check if seed phrase needs backup
  useEffect(() => {
    const checkBackupStatus = async () => {
      try {
        const backedUp = await AsyncStorage.getItem('seedPhraseBackedUp');
        if (backedUp === 'true') return;

        const dismissed = await AsyncStorage.getItem('backupBannerDismissed');
        if (dismissed === 'true') return;

        const remindAt = await AsyncStorage.getItem('backupBannerRemindAt');
        if (remindAt && new Date().getTime() < parseInt(remindAt, 10)) return;

        setShowBackupBanner(true);
      } catch (_) {}
    };
    checkBackupStatus();
  }, []);

  const handleBackupRemindTomorrow = async () => {
    setShowBackupBanner(false);
    const tomorrow = new Date().getTime() + 24 * 60 * 60 * 1000;
    await AsyncStorage.setItem('backupBannerRemindAt', tomorrow.toString());
  };

  const handleBackupNever = async () => {
    setShowBackupBanner(false);
    await AsyncStorage.setItem('backupBannerDismissed', 'true');
  };

  // Get auth token on mount
  useEffect(() => {
    const getAuthToken = async () => {
      try {
        const token = await api.getStoredToken();
        console.log('Retrieved auth token:', token);
        if (token) {
          setAuthToken(token);
          api.setToken(token);
        } else {
          const token = await AsyncStorage.getItem('authToken');
          if (token) {
            setAuthToken(token);
            api.setToken(token);
          }
        }
      } catch (error) {
        console.error('Error getting auth token:', error);
      }
    };
    getAuthToken();
  }, []);

  // Fetch user balance and update assets
  const fetchUserBalance = async () => {
    try {
      if (!authToken) {
        console.log('No auth token, using default zero balances');
        setUserBalance({ mcpgBalance: 0, usdcBalance: 0, usdtBalance: 0, totalBalance: 0 });
        return;
      }

      // Fetch real on-chain balances from wallet API
      const result = await getWalletBalances();

      if (result.success && result.data) {
        const balancesMap = result.data.balances || result.data;
        const mcpgBalance = parseFloat(balancesMap['MCGP'] || '0');
        const usdcBalance = parseFloat(balancesMap['USDC'] || '0');
        const usdtBalance = parseFloat(balancesMap['USDT'] || '0');
        const totalBalance = mcpgBalance + usdcBalance + usdtBalance;

        setUserBalance({ mcpgBalance, usdcBalance, usdtBalance, totalBalance });
        updateAssetsWithBalances(mcpgBalance, usdcBalance, usdtBalance);
      } else {
        console.warn('Failed to fetch wallet balances:', result.message);
        updateAssetsWithBalances(0, 0, 0);
      }
    } catch (error) {
      console.error('Error fetching user balance:', error);
      updateAssetsWithBalances(0, 0, 0);
    }
  };

  // Update assets with user balances
  const updateAssetsWithBalances = (mcpgBalance: number, usdcBalance: number, usdtBalance: number) => {
    const updatedAssets = DEFAULT_ASSETS.map(asset => {
      let balance = 0;
      let usdValue = 0;
      
      switch (asset.symbol) {
        case 'MCGP':
          balance = mcpgBalance;
          usdValue = mcpgBalance * (asset.currentPrice || 1);
          break;
        case 'USDC':
          balance = usdcBalance;
          usdValue = usdcBalance * (asset.currentPrice || 1);
          break;
        case 'USDT':
          balance = usdtBalance;
          usdValue = usdtBalance * (asset.currentPrice || 1);
          break;
      }
      
      // Check if this asset was previously selected
      const wasSelected = portfolioData.selectedAsset?.symbol === asset.symbol;
      
      return {
        ...asset,
        balance,
        usdValue,
        isSelected: wasSelected || false,
      };
    });

    // Calculate totals
    const totalBalance = mcpgBalance + usdcBalance + usdtBalance;
    const totalUsdValue = updatedAssets.reduce((sum, asset) => sum + asset.usdValue, 0);
    
    // Find selected asset if any
    const selectedAsset = updatedAssets.find(asset => asset.isSelected) || 
                         (updatedAssets.length > 0 ? updatedAssets[0] : null);
    
    // If no asset is selected, select the first one with balance > 0 or first one
    if (!selectedAsset && updatedAssets.length > 0) {
      const assetWithBalance = updatedAssets.find(asset => asset.balance > 0);
      if (assetWithBalance) {
        updatedAssets.forEach(a => a.isSelected = a._id === assetWithBalance._id);
      } else {
        updatedAssets[0].isSelected = true;
      }
    }

    setPortfolioData(prev => ({
      ...prev,
      assets: updatedAssets,
      totals: {
        balance: totalBalance,
        usdValue: totalUsdValue,
        dailyChange: 0,
      },
      selectedAsset: selectedAsset || (updatedAssets.length > 0 ? updatedAssets[0] : null),
    }));
  };

  // Fetch portfolio data on mount and when token changes
  useEffect(() => {
    if (authToken !== null) {
      fetchPortfolioData();
    }
  }, [authToken]);

  // Fetch portfolio data
  const fetchPortfolioData = async () => {
    try {
      setIsLoading(true);
      
      // First fetch user balance
      await fetchUserBalance();
      
      // Then try to get portfolio data from API (if available)
      if (authToken) {
        const response = await api.getPortfolioAssets();
        
        if (response.success && response.data) {
          console.log('Portfolio API data:', response.data);
          // You can merge API data with default assets here if needed
        }
      }
      
    } catch (error: any) {
      console.error('Error fetching portfolio data:', error);
      Alert.alert(
        'Error',
        api.getErrorMessage(error) || 'Failed to load portfolio data. Please try again.',
        [{ text: 'OK' }]
      );
    } finally {
      setIsLoading(false);
      setIsRefreshing(false);
    }
  };

  // Refresh assets
  const refreshAssets = async () => {
    setIsRefreshing(true);
    try {
      await fetchUserBalance();
      if (authToken) {
        await api.refreshPortfolioAssets();
      }
    } catch (error: any) {
      console.error('Error refreshing assets:', error);
      Alert.alert('Error', api.getErrorMessage(error) || 'Failed to refresh assets');
    } finally {
      setIsRefreshing(false);
    }
  };

  // Handle asset selection via radio button or select button
  const handleAssetSelect = async (asset: Asset, shouldCollapseList: boolean = true) => {
    try {
      // Update local state first for immediate UI feedback
      selectionAnimation.setValue(0);
      Animated.spring(selectionAnimation, {
        toValue: 1,
        friction: 3,
        tension: 40,
        useNativeDriver: true,
      }).start();

      // Update local state
      setPortfolioData(prev => ({
        ...prev,
        selectedAsset: asset,
        assets: prev.assets.map(a => ({
          ...a,
          isSelected: a._id === asset._id
        }))
      }));

      // Collapse any expanded asset card
      setExpandedAssetId(null);

      // Collapse the entire asset list if needed
      if (shouldCollapseList && isAssetListExpanded) {
        collapseAssetList();
      }

      // Try to call API only if we have auth token
      if (authToken) {
        const response = await api.selectAsset(asset._id);
        
        if (!response.success) {
          console.warn('API selection failed:', response.message);
          // We don't show alert here since local selection already succeeded
        }
      }
      
    } catch (error: any) {
      console.error('Error selecting asset:', error);
      // Don't show alert for local selection errors
    }
  };

  // Handle radio button selection (different from card tap)
  const handleRadioSelect = (asset: Asset) => {
    handleAssetSelect(asset, false);
  };

  // Handle card press for expansion (not for selection)
  const handleAssetCardPress = (assetId: string) => {
    if (expandedAssetId === assetId) {
      // Collapse if already expanded
      setExpandedAssetId(null);
    } else {
      // Expand new asset and collapse previous
      setExpandedAssetId(assetId);
    }
  };

  // Toggle asset list expansion
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

  // Animation interpolations
  const listHeight = listAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: [0, Math.min(portfolioData.assets.length * 80 + 100, 400)],
  });

  const rotateIcon = rotationAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: ['0deg', '180deg'],
  });

  const selectionScale = selectionAnimation.interpolate({
    inputRange: [0, 0.5, 1],
    outputRange: [0.95, 1.05, 1],
  });

  // Render asset icon
  const renderAssetIcon = (symbol: string, size: number = 40) => (
    <View style={[styles.selectorIcon, { width: size, height: size, borderRadius: size / 2 }]}>
      <Text style={[styles.selectorIconText, { fontSize: size / 2 }]}>
        {symbol.charAt(0)}
      </Text>
    </View>
  );

  // Handle quick action button press
  const handleQuickAction = (action: string) => {
    console.log(`Quick action: ${action}`);

    if (action === 'fund') {
      // Show modal for fund selection
      setShowFundModal(true);
    } else if (action === 'send') {
      // Show modal for send selection
      setShowSendModal(true);
    } else if (action === 'swap') {
      // Navigate to swap screen with selected asset
      if (portfolioData.selectedAsset) {
        router.push({
          pathname: '/swap',
          params: { 
            fromAssetSymbol: portfolioData.selectedAsset.symbol,
            fromAssetId: portfolioData.selectedAsset._id,
            balance: portfolioData.selectedAsset.balance.toString()
          }
        });
      } else {
        Alert.alert('Select Asset', 'Please select an asset first to perform a swap');
      }
    } else {
      // For other actions, navigate directly
      //@ts-expect-error
      router.push(`/${action}`);
    }
  };

  // Handle fund option selection
  const handleFundOption = (option: 'fiat' | 'crypto') => {
    console.log(`Selected fund option: ${option}`);
    setShowFundModal(false);
    if (option === 'crypto') {
      router.push(`/fund`);
    }
    if (option === 'fiat') {
      router.push(`/fundfiat`);
    }
  };

  // Handle send option selection
  const handleSendOption = (option: 'fiat' | 'crypto') => {
    console.log(`Selected send option: ${option}`);
    setShowSendModal(false);
    if (option === 'crypto') {
      if (portfolioData.selectedAsset) {
        router.push({
          pathname: '/send',
          params: { 
            assetSymbol: portfolioData.selectedAsset.symbol,
            assetId: portfolioData.selectedAsset._id,
            balance: portfolioData.selectedAsset.balance.toString()
          }
        });
      } else {
        Alert.alert('Select Asset', 'Please select an asset first to send');
      }
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
            <Pressable
              onPress={() => setShowFundModal(false)}
              style={({ pressed }) => [
                styles.modalCloseButton,
                pressed && styles.pressedButton,
              ]}
            >
              <Icon name="close" size={24} color="#666" />
            </Pressable>
          </View>

          <Text style={styles.modalSubtitle}>Choose how you want to fund your account</Text>

          <View style={styles.optionsContainer}>
            {/* Fiat Deposit Option */}
            <Pressable
              style={({ pressed }) => [
                styles.optionCard,
                pressed && styles.pressedOptionCard,
              ]}
              onPress={() => handleFundOption('fiat')}
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
            </Pressable>

            {/* Crypto Deposit Option */}
            <Pressable
              style={({ pressed }) => [
                styles.optionCard,
                pressed && styles.pressedOptionCard,
              ]}
              onPress={() => handleFundOption('crypto')}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#E8F5E9' }]}>
                <Icon name="currency-bitcoin" size={32} color="#2E7D32" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Crypto Deposit</Text>
                <Text style={styles.optionDescription}>
                  Deposit cryptocurrencies from external wallets or exchanges
                </Text>
                <View style={styles.optionDetails}>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>USDT, USDC, ETH</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>External Wallets</Text>
                  </View>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </Pressable>
          </View>

          <Pressable
            style={({ pressed }) => [
              styles.modalCancelButton,
              pressed && styles.pressedCancelButton,
            ]}
            onPress={() => setShowFundModal(false)}
          >
            <Text style={styles.modalCancelText}>Cancel</Text>
          </Pressable>
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
            <Pressable
              onPress={() => setShowSendModal(false)}
              style={({ pressed }) => [
                styles.modalCloseButton,
                pressed && styles.pressedButton,
              ]}
            >
              <Icon name="close" size={24} color="#666" />
            </Pressable>
          </View>

          <Text style={styles.modalSubtitle}>Choose how you want to withdraw your funds</Text>

          <View style={styles.optionsContainer}>
            {/* Withdraw as Fiat Option */}
            <Pressable
              style={({ pressed }) => [
                styles.optionCard,
                pressed && styles.pressedOptionCard,
              ]}
              onPress={() => handleSendOption('fiat')}
            >
              <View style={[styles.optionIcon, { backgroundColor: '#FFF3E0' }]}>
                <Icon name="attach-money" size={32} color="#F57C00" />
              </View>
              <View style={styles.optionContent}>
                <Text style={styles.optionTitle}>Withdraw as Fiat</Text>
                <Text style={styles.optionDescription}>
                  Convert to USD and withdraw to your bank account or card
                </Text>
                <View style={styles.optionDetails}>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Bank Transfer</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="check-circle" size={16} color="#4CAF50" />
                    <Text style={styles.detailText}>Debit Card</Text>
                  </View>
                  <View style={styles.detailItem}>
                    <Icon name="schedule" size={16} color="#2196F3" />
                    <Text style={styles.detailText}>Direct Transaction</Text>
                  </View>
                </View>
              </View>
              <Icon name="chevron-right" size={24} color="#666" />
            </Pressable>

            {/* Withdraw as Crypto Option */}
            <Pressable
              style={({ pressed }) => [
                styles.optionCard,
                pressed && styles.pressedOptionCard,
              ]}
              onPress={() => handleSendOption('crypto')}
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
            </Pressable>
          </View>

          <Pressable
            style={({ pressed }) => [
              styles.modalCancelButton,
              pressed && styles.pressedCancelButton,
            ]}
            onPress={() => setShowSendModal(false)}
          >
            <Text style={styles.modalCancelText}>Cancel</Text>
          </Pressable>
        </View>
      </View>
    </Modal>
  );

  // Format last updated time
  const formatLastUpdated = (dateString: string) => {
    const date = new Date(dateString);
    const now = new Date();
    const diffInMinutes = Math.floor((now.getTime() - date.getTime()) / (1000 * 60));
    
    if (diffInMinutes < 1) return 'Just now';
    if (diffInMinutes < 60) return `${diffInMinutes}m ago`;
    if (diffInMinutes < 1440) return `${Math.floor(diffInMinutes / 60)}h ago`;
    return date.toLocaleDateString();
  };

  // Loading state
  if (isLoading && !isRefreshing) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color={GOLD_COLORS.primary} />
        <Text style={styles.loadingText}>Loading portfolio...</Text>
      </View>
    );
  }

  return (
    <ScrollView 
      style={styles.container} 
      showsVerticalScrollIndicator={false}
      refreshControl={
        <RefreshControl
          refreshing={isRefreshing}
          onRefresh={refreshAssets}
          tintColor={GOLD_COLORS.primary}
          colors={[GOLD_COLORS.primary]}
        />
      }
    >
      {/* Header */}
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Dashboard</Text>
        <View style={styles.headerRight}>
          <Text style={styles.lastUpdated}>
            Updated {formatLastUpdated(portfolioData.lastUpdated)}
          </Text>
          <Pressable
            onPress={() => setIsValuesHidden(!isValuesHidden)}
            style={({ pressed }) => [
              styles.visibilityButton,
              pressed && styles.pressedButton,
            ]}
          >
            <Icon
              name={isValuesHidden ? 'visibility-off' : 'visibility'}
              size={24}
              color={GOLD_COLORS.dark}
            />
          </Pressable>
        </View>
      </View>

      {/* Seed phrase backup banner */}
      {showBackupBanner && (
        <View style={styles.backupBanner}>
          <Pressable
            style={styles.backupBannerContent}
            onPress={() => router.push('/wallet/seedphrase')}
          >
            <Icon name="shield" size={20} color="#92400E" />
            <Text style={styles.backupBannerText}>
              Back up your seed phrase to protect your funds
            </Text>
          </Pressable>
          <Pressable
            onPress={() => {
              Alert.alert(
                'Dismiss Reminder',
                'When would you like to be reminded?',
                [
                  { text: 'Remind Tomorrow', onPress: handleBackupRemindTomorrow },
                  { text: 'Never', style: 'destructive', onPress: handleBackupNever },
                  { text: 'Cancel', style: 'cancel' },
                ]
              );
            }}
            style={styles.backupBannerClose}
            hitSlop={8}
          >
            <Icon name="close" size={18} color="#92400E" />
          </Pressable>
        </View>
      )}

      {/* Total Balance */}
      <View style={styles.balanceCard}>
        <Text style={styles.balanceLabel}>Total Balance</Text>
        <Text style={styles.balanceAmount}>
          {isValuesHidden ? '••••••' : `$${portfolioData.totals.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
        </Text>
        {portfolioData.totals.dailyChange !== 0 && (
          <View style={styles.dailyChangeContainer}>
            <Icon 
              name={portfolioData.totals.dailyChange >= 0 ? 'trending-up' : 'trending-down'} 
              size={16} 
              color={portfolioData.totals.dailyChange >= 0 ? '#34C759' : '#FF3B30'} 
            />
            <Text style={[
              styles.dailyChangeText,
              { color: portfolioData.totals.dailyChange >= 0 ? '#34C759' : '#FF3B30' }
            ]}>
              {portfolioData.totals.dailyChange >= 0 ? '+' : ''}
              ${Math.abs(portfolioData.totals.dailyChange).toFixed(2)} today
            </Text>
          </View>
        )}
      </View>

      {/* Debit Account Selector */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Debit Account</Text>
        <Text style={styles.sectionSubtitle}>Select asset to debit from</Text>

        <Animated.View style={{ transform: [{ scale: selectionScale }] }}>
          <Pressable
            onPress={toggleAssetList}
            style={({ pressed }) => [
              styles.assetSelector,
              pressed && styles.pressedAssetSelector,
            ]}
            disabled={portfolioData.assets.length === 0}
          >
            <View style={styles.selectorContent}>
              <View style={styles.selectorLeft}>
                {portfolioData.selectedAsset ? (
                  <View style={styles.selectedAssetDisplay}>
                    {renderAssetIcon(portfolioData.selectedAsset.symbol, 44)}
                    <View style={styles.selectedAssetInfo}>
                      <Text style={styles.selectedAssetSymbol}>
                        {portfolioData.selectedAsset.symbol}
                      </Text>
                      <Text style={styles.selectedAssetName}>
                        {portfolioData.selectedAsset.name}
                      </Text>
                      <Text style={styles.selectedAssetBalance}>
                        Available: {isValuesHidden ? '••••••' : portfolioData.selectedAsset.balance.toFixed(2)} {portfolioData.selectedAsset.symbol}
                      </Text>
                    </View>
                  </View>
                ) : portfolioData.assets.length > 0 ? (
                  <View style={styles.placeholderDisplay}>
                    <Icon name="account-balance-wallet" size={28} color={GOLD_COLORS.primary} />
                    <Text style={styles.placeholderText}>Select Asset</Text>
                  </View>
                ) : (
                  <View style={styles.placeholderDisplay}>
                    <Icon name="add-circle" size={28} color={GOLD_COLORS.primary} />
                    <Text style={styles.placeholderText}>No assets found</Text>
                  </View>
                )}
              </View>
              {portfolioData.assets.length > 0 && (
                <Animated.View style={{ transform: [{ rotate: rotateIcon }] }}>
                  <Icon name="keyboard-arrow-down" size={32} color={GOLD_COLORS.dark} />
                </Animated.View>
              )}
            </View>
          </Pressable>
        </Animated.View>

        {/* Expandable Asset List */}
        {isAssetListExpanded && portfolioData.assets.length > 0 && (
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
                <Pressable
                  onPress={refreshAssets}
                  disabled={isRefreshing}
                  style={({ pressed }) => [
                    styles.refreshButton,
                    pressed && styles.pressedRefreshButton,
                  ]}
                >
                  {isRefreshing ? (
                    <ActivityIndicator size="small" color={GOLD_COLORS.primary} />
                  ) : (
                    <Icon name="refresh" size={22} color={GOLD_COLORS.primary} />
                  )}
                </Pressable>
              </View>

              {/* Asset Cards */}
              {portfolioData.assets.map(asset => (
                <AssetCard
                  key={asset._id}
                  asset={asset}
                  isExpanded={expandedAssetId === asset._id}
                  isSelected={portfolioData.selectedAsset?._id === asset._id}
                  onCardPress={() => handleAssetCardPress(asset._id)}
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

      {/* Selected Asset Summary - Only show when an asset is selected */}
      {portfolioData.selectedAsset && (
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
              {renderAssetIcon(portfolioData.selectedAsset.symbol, 48)}
              <View>
                <Text style={styles.summarySymbol}>{portfolioData.selectedAsset.symbol}</Text>
                <Text style={styles.summaryName}>{portfolioData.selectedAsset.name}</Text>
                <Text style={styles.summaryType}>{portfolioData.selectedAsset.details?.type || 'Token'}</Text>
              </View>
            </View>

            <View style={styles.summaryBalance}>
              <View style={styles.balanceRow}>
                <Text style={styles.balanceLabel}>Balance:</Text>
                <Text style={styles.balanceValue}>
                  {isValuesHidden ? '••••••' : portfolioData.selectedAsset.balance.toFixed(2)} {portfolioData.selectedAsset.symbol}
                </Text>
              </View>
              <View style={styles.balanceRow}>
                <Text style={styles.balanceLabel}>USD Value:</Text>
                <Text style={[styles.balanceValue, styles.usdValue]}>
                  {isValuesHidden ? '••••' : `$${portfolioData.selectedAsset.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
                </Text>
              </View>
            </View>
          </View>
        </Animated.View>
      )}

      {/* Quick Actions - Updated to Fund, Swap, Send, More */}
      <View style={styles.quickActionsContainer}>
        <Text style={styles.quickActionsTitle}>Quick Actions</Text>
        <View style={styles.quickActionsGrid}>
          <Pressable
            style={({ pressed }) => [
              styles.quickActionButton,
              pressed && styles.pressedQuickAction,
            ]}
            onPress={() => handleQuickAction('fund')}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="account-balance" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Fund</Text>
          </Pressable>

          <Pressable
            style={({ pressed }) => [
              styles.quickActionButton,
              pressed && styles.pressedQuickAction,
            ]}
            onPress={() => handleQuickAction('swap')}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="swap-horiz" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Swap</Text>
          </Pressable>

          <Pressable
            style={({ pressed }) => [
              styles.quickActionButton,
              pressed && styles.pressedQuickAction,
            ]}
            onPress={() => handleQuickAction('send')}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="send" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Send</Text>
          </Pressable>

          <Pressable
            style={({ pressed }) => [
              styles.quickActionButton,
              pressed && styles.pressedQuickAction,
            ]}
            onPress={() => handleQuickAction('more')}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="more-horiz" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>More</Text>
          </Pressable>
        </View>
      </View>

      <View style={styles.quickActionsContainer}>
        <Text style={styles.quickActionsTitle}>Trade Now</Text>
        <View style={styles.quickActionsGrid}>
          <Pressable
            style={({ pressed }) => [
              styles.quickActionButton,
              pressed && styles.pressedQuickAction,
            ]}
            onPress={() => handleQuickAction('product')}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="shopping-cart" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Buy Product</Text>
          </Pressable>

          <Pressable
            style={({ pressed }) => [
              styles.quickActionButton,
              pressed && styles.pressedQuickAction,
            ]}
            onPress={() => handleQuickAction('services')}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="handyman" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Order Services</Text>
          </Pressable>

          <Pressable
            style={({ pressed }) => [
              styles.quickActionButton,
              pressed && styles.pressedQuickAction,
            ]}
            onPress={() => handleQuickAction('trade')}
          >
            <View style={styles.actionIconContainer}>
              <Icon name="trending-up" size={22} color="#000000" />
            </View>
            <Text style={styles.quickActionText}>Trade and Earn</Text>
          </Pressable>
        </View>
      </View>

      {/* Fund Selection Modal */}
      <FundSelectionModal />

      {/* Send Selection Modal */}
      <SendSelectionModal />
    </ScrollView>
  );
};

// Styles with Pressable support
const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
  },
  backupBanner: {
    backgroundColor: '#FEF3C7',
    marginHorizontal: 16,
    marginTop: 8,
    borderRadius: 10,
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 10,
    paddingHorizontal: 12,
  },
  backupBannerContent: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
  },
  backupBannerText: {
    flex: 1,
    fontSize: 13,
    color: '#92400E',
    fontWeight: '500',
  },
  backupBannerClose: {
    padding: 4,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.background,
  },
  loadingText: {
    marginTop: 16,
    fontSize: 16,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
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
  headerRight: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  lastUpdated: {
    fontSize: 10,
    color: '#666666',
    marginRight: 12,
  },
  visibilityButton: {
    padding: 8,
    borderRadius: 8,
    backgroundColor: GOLD_COLORS.light,
  },
  pressedButton: {
    opacity: 0.7,
    transform: [{ scale: 0.98 }],
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
  balanceLabel: {
    fontSize: 12,
    color: 'rgba(0, 0, 0, 0.7)',
    marginBottom: 8,
    fontWeight: '600',
    marginRight: 8,
  },
  balanceAmount: {
    fontSize: 22,
    fontWeight: '900',
    color: '#000000',
    marginBottom: 8,
  },
  dailyChangeContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  dailyChangeText: {
    fontSize: 12,
    fontWeight: '600',
    marginLeft: 4,
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
  pressedAssetSelector: {
    opacity: 0.8,
    transform: [{ scale: 0.99 }],
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
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
  },
  selectorIconText: {
    color: '#000000',
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
  pressedRefreshButton: {
    opacity: 0.7,
  },
  assetCardContainer: {
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  pressedCard: {
    backgroundColor: '#f9f9f9',
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
    backgroundColor: '#E5E5EA',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  selectedAssetIcon: {
    backgroundColor: GOLD_COLORS.primary,
  },
  assetIconText: {
    color: '#000000',
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
  priceChange: {
    fontSize: 11,
    fontWeight: '600',
    marginTop: 2,
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
  pressedRadioButton: {
    opacity: 0.7,
  },
  radioButtonSelected: {},
  radioButtonUnselected: {},
  assetDetails: {
    marginTop: 16,
    paddingTop: 16,
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
    flex: 2,
    textAlign: 'right',
  },
  detailAddress: {
    fontSize: 12,
    color: '#666666',
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
    flex: 2,
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
  pressedSelectButton: {
    opacity: 0.8,
    transform: [{ scale: 0.98 }],
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
    marginLeft: 16,
    marginTop: 4,
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
  quickActionsTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 16,
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
  pressedQuickAction: {
    opacity: 0.7,
    transform: [{ scale: 0.95 }],
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
  pressedOptionCard: {
    backgroundColor: '#F0F0F0',
    transform: [{ scale: 0.99 }],
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
  pressedCancelButton: {
    opacity: 0.8,
    transform: [{ scale: 0.98 }],
  },
  modalCancelText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
});

export default Dashboard;