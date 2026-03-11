import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  Animated,
  StyleSheet,
  Dimensions,
  Platform,
  TextInput,
  Alert,
  KeyboardAvoidingView,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';

// Types and interfaces
interface Token {
  id: string;
  symbol: string;
  name: string;
  priceUSD: number;
  iconColor: string;
  balance: number;
  balanceUSD: number;
}

interface SwapAsset extends Token {
  selected: boolean;
  amount: string;
  amountUSD: number;
  toReceive: number; // MCGP amount to receive
}

interface SwapQuote {
  totalFromUSD: number;
  totalToMCGP: number;
  exchangeRates: { [key: string]: number };
  fees: number;
  estimatedTime: string;
  slippage: number;
}

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  success: '#34C759',
  warning: '#FF9500',
  error: '#FF3B30',
};

// Asset Card Component
const AssetCard: React.FC<{
  asset: SwapAsset;
  index: number;
  onToggleSelect: (id: string) => void;
  onAmountChange: (id: string, amount: string) => void;
  onMaxPress: (id: string) => void;
}> = ({ asset, index, onToggleSelect, onAmountChange, onMaxPress }) => {
  const [isExpanded, setIsExpanded] = useState(false);
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
    outputRange: [80, 160],
  });

  const rotation = animation.interpolate({
    inputRange: [0, 1],
    outputRange: ['0deg', '180deg'],
  });

  return (
    <Animated.View style={[styles.assetCardContainer, { height: heightInterpolation }]}>
      {/* Card Header */}
      <TouchableOpacity
        style={styles.assetCardHeader}
        onPress={() => setIsExpanded(!isExpanded)}
        activeOpacity={0.7}
      >
        <View style={styles.assetSelection}>
          <TouchableOpacity
            style={styles.checkbox}
            onPress={(e) => {
              e.stopPropagation();
              onToggleSelect(asset.id);
            }}
          >
            {asset.selected ? (
              <Icon name="check-box" size={24} color={GOLD_COLORS.primary} />
            ) : (
              <Icon name="check-box-outline-blank" size={24} color="#CCCCCC" />
            )}
          </TouchableOpacity>
          
          <View style={[styles.assetIcon, { backgroundColor: asset.iconColor }]}>
            <Text style={styles.assetIconText}>{asset.symbol.charAt(0)}</Text>
          </View>
          
          <View style={styles.assetInfo}>
            <View style={styles.assetHeaderRow}>
              <Text style={styles.assetSymbol}>{asset.symbol}</Text>
              {asset.selected && (
                <View style={styles.selectedBadge}>
                  <Text style={styles.selectedBadgeText}>SELECTED</Text>
                </View>
              )}
            </View>
            <Text style={styles.assetName}>{asset.name}</Text>
            <Text style={styles.assetBalance}>
              Balance: {asset.balance.toLocaleString()} {asset.symbol}
            </Text>
          </View>
        </View>
        
        <Animated.View style={{ transform: [{ rotate: rotation }] }}>
          <Icon name="keyboard-arrow-down" size={24} color="#000000" />
        </Animated.View>
      </TouchableOpacity>

      {/* Expanded Content */}
      {isExpanded && (
        <View style={styles.assetExpandedContent}>
          {/* Amount Input Section */}
          <View style={styles.amountSection}>
            <View style={styles.amountHeader}>
              <Text style={styles.amountLabel}>Amount to Swap</Text>
              <TouchableOpacity
                style={styles.maxButton}
                onPress={() => onMaxPress(asset.id)}
              >
                <Text style={styles.maxButtonText}>MAX</Text>
              </TouchableOpacity>
            </View>
            
            <View style={styles.amountInputContainer}>
              <TextInput
                style={[
                  styles.amountInput,
                  !asset.selected && styles.amountInputDisabled,
                ]}
                value={asset.amount}
                onChangeText={(text) => {
                  const cleaned = text.replace(/[^0-9.]/g, '');
                  onAmountChange(asset.id, cleaned);
                }}
                keyboardType="decimal-pad"
                placeholder="0.0"
                placeholderTextColor="#999"
                editable={asset.selected}
              />
              <Text style={styles.amountSymbol}>{asset.symbol}</Text>
            </View>
            
            <Text style={styles.amountUSD}>
              ≈ ${asset.amountUSD.toLocaleString(undefined, {
                minimumFractionDigits: 2,
                maximumFractionDigits: 2,
              })}
            </Text>
          </View>

          {/* To Receive Section */}
          <View style={styles.toReceiveSection}>
            <Text style={styles.toReceiveLabel}>You will receive:</Text>
            <View style={styles.toReceiveAmountContainer}>
              <View style={[styles.mcgpIcon, { backgroundColor: '#FFD700' }]}>
                <Text style={styles.mcgpIconText}>M</Text>
              </View>
              <Text style={styles.toReceiveAmount}>
                {asset.toReceive.toFixed(2)} MCGP
              </Text>
              <Text style={styles.toReceiveUSD}>
                ≈ ${(asset.toReceive * 0.5).toLocaleString(undefined, {
                  minimumFractionDigits: 2,
                  maximumFractionDigits: 2,
                })}
              </Text>
            </View>
          </View>
        </View>
      )}
    </Animated.View>
  );
};

// Main MultiAssetSwapScreen Component
const MultiAssetSwapScreen: React.FC = () => {
  // Initial assets (USDT and USDC)
  const [assets, setAssets] = useState<SwapAsset[]>([
    {
      id: 'usdt',
      symbol: 'USDT',
      name: 'Tether',
      priceUSD: 1,
      iconColor: '#26A17B',
      balance: 1000,
      balanceUSD: 1000,
      selected: false,
      amount: '',
      amountUSD: 0,
      toReceive: 0,
    },
    {
      id: 'usdc',
      symbol: 'USDC',
      name: 'USD Coin',
      priceUSD: 1,
      iconColor: '#2775CA',
      balance: 1500,
      balanceUSD: 1500,
      selected: false,
      amount: '',
      amountUSD: 0,
      toReceive: 0,
    },
  ]);

  // MCGP token info
  const mcgpToken = {
    symbol: 'MCGP',
    name: 'MCGP Chain',
    priceUSD: 0.5,
    iconColor: '#FFD700',
  };

  const [swapQuote, setSwapQuote] = useState<SwapQuote>({
    totalFromUSD: 0,
    totalToMCGP: 0,
    exchangeRates: {
      usdt: 2, // 1 USDT = 2 MCGP
      usdc: 2, // 1 USDC = 2 MCGP
    },
    fees: 0,
    estimatedTime: '10 seconds',
    slippage: 0.5,
  });

  const [swapMode, setSwapMode] = useState<'individual' | 'batch'>('individual');
  const [isConfirming, setIsConfirming] = useState(false);
  const animation = useRef(new Animated.Value(0)).current;

  // Calculate swap amounts
  useEffect(() => {
    let totalFromUSD = 0;
    let totalToMCGP = 0;
    
    const updatedAssets = assets.map(asset => {
      const amount = parseFloat(asset.amount) || 0;
      const amountUSD = amount * asset.priceUSD;
      const toReceive = amount * swapQuote.exchangeRates[asset.id];
      
      if (asset.selected) {
        totalFromUSD += amountUSD;
        totalToMCGP += toReceive;
      }
      
      return {
        ...asset,
        amountUSD,
        toReceive,
      };
    });

    // Calculate fees (0.1% of total)
    const fees = totalFromUSD * 0.001;

    setAssets(updatedAssets);
    setSwapQuote(prev => ({
      ...prev,
      totalFromUSD,
      totalToMCGP,
      fees,
    }));
  }, [assets.map(asset => asset.amount + asset.selected).join(',')]);

  // Handle asset selection toggle
  const handleToggleSelect = (assetId: string) => {
    setAssets(prev => prev.map(asset => {
      if (asset.id === assetId) {
        return {
          ...asset,
          selected: !asset.selected,
          amount: !asset.selected ? '' : asset.amount,
        };
      }
      return asset;
    }));
  };

  // Handle amount change
  const handleAmountChange = (assetId: string, amount: string) => {
    setAssets(prev => prev.map(asset => {
      if (asset.id === assetId) {
        const amountNum = parseFloat(amount) || 0;
        if (amountNum > asset.balance) {
          Alert.alert(
            'Insufficient Balance',
            `You only have ${asset.balance} ${asset.symbol}`,
            [{ text: 'OK' }]
          );
          return asset;
        }
        return { ...asset, amount };
      }
      return asset;
    }));
  };

  // Handle max button press
  const handleMaxPress = (assetId: string) => {
    setAssets(prev => prev.map(asset => {
      if (asset.id === assetId) {
        return {
          ...asset,
          amount: asset.balance.toString(),
          selected: true,
        };
      }
      return asset;
    }));
  };

  // Select all assets
  const handleSelectAll = () => {
    const allSelected = assets.every(asset => asset.selected);
    setAssets(prev => prev.map(asset => ({
      ...asset,
      selected: !allSelected,
      amount: !allSelected ? asset.balance.toString() : '',
    })));
  };

  // Set max for all selected assets
  const handleMaxAll = () => {
    setAssets(prev => prev.map(asset => ({
      ...asset,
      amount: asset.selected ? asset.balance.toString() : asset.amount,
    })));
  };

  // Clear all selections
  const handleClearAll = () => {
    setAssets(prev => prev.map(asset => ({
      ...asset,
      selected: false,
      amount: '',
    })));
  };

  // Validate swap
  const validateSwap = () => {
    const selectedAssets = assets.filter(asset => asset.selected);
    
    if (selectedAssets.length === 0) {
      Alert.alert('No Assets Selected', 'Please select at least one asset to swap');
      return false;
    }

    for (const asset of selectedAssets) {
      const amount = parseFloat(asset.amount) || 0;
      if (amount <= 0) {
        Alert.alert('Invalid Amount', `Please enter an amount for ${asset.symbol}`);
        return false;
      }
      if (amount > asset.balance) {
        Alert.alert('Insufficient Balance', `You only have ${asset.balance} ${asset.symbol}`);
        return false;
      }
    }

    return true;
  };

  // Handle swap confirmation
  const handleConfirmSwap = () => {
    if (!validateSwap()) return;

    const selectedAssets = assets.filter(asset => asset.selected);
    const assetNames = selectedAssets.map(asset => `${asset.amount} ${asset.symbol}`).join(' + ');
    
    Alert.alert(
      'Confirm Multi-Asset Swap',
      `Swap ${assetNames} for ${swapQuote.totalToMCGP.toFixed(2)} MCGP?`,
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Confirm Swap',
          style: 'destructive',
          onPress: () => executeSwap(),
        },
      ]
    );
  };

  // Execute the swap
  const executeSwap = () => {
    setIsConfirming(true);
    
    // Animate confirmation
    Animated.sequence([
      Animated.timing(animation, {
        toValue: 1,
        duration: 500,
        useNativeDriver: true,
      }),
      Animated.timing(animation, {
        toValue: 0,
        duration: 500,
        useNativeDriver: true,
      }),
    ]).start(() => {
      const selectedAssets = assets.filter(asset => asset.selected);
      const assetNames = selectedAssets.map(asset => asset.symbol).join(', ');
      
      Alert.alert(
        'Swap Successful!',
        `Successfully swapped ${assetNames} for ${swapQuote.totalToMCGP.toFixed(2)} MCGP`,
        [
          {
            text: 'OK',
            onPress: () => {
              setIsConfirming(false);
              // Reset selected assets
              setAssets(prev => prev.map(asset => ({
                ...asset,
                selected: false,
                amount: '',
              })));
            },
          },
        ]
      );
    });
  };

  // Calculate selected assets count
  const selectedCount = assets.filter(asset => asset.selected).length;
  const totalSelectedUSD = assets
    .filter(asset => asset.selected)
    .reduce((sum, asset) => sum + (parseFloat(asset.amount) || 0) * asset.priceUSD, 0);

  // Animation interpolation
  const scale = animation.interpolate({
    inputRange: [0, 0.5, 1],
    outputRange: [1, 1.1, 1],
  });

  return (
    <KeyboardAvoidingView
      style={styles.container}
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
    >
      <ScrollView style={styles.scrollContainer} showsVerticalScrollIndicator={false}>
        {/* Header */}
        <View style={styles.header}>
          <Text style={styles.headerTitle}>Multi-Asset Swap to MCGP</Text>
          <TouchableOpacity style={styles.headerButton}>
            <Icon name="history" size={24} color="#000000" />
          </TouchableOpacity>
        </View>

        {/* Promotion Banner */}
        <View style={styles.promotionBanner}>
          <Icon name="swap-horiz" size={24} color={GOLD_COLORS.primary} />
          <View style={styles.promotionContent}>
            <Text style={styles.promotionTitle}>
              Swap Multiple Assets at Once! Save on Fees with Batch Swaps
            </Text>
          </View>
        </View>

        {/* Summary Card */}
        <View style={styles.summaryCard}>
          <View style={styles.summaryHeader}>
            <Text style={styles.summaryTitle}>Swap Summary</Text>
            <View style={styles.summaryBadge}>
              <Icon name="swap-vert" size={16} color="#000000" />
              <Text style={styles.summaryBadgeText}>
                {selectedCount} Asset{selectedCount !== 1 ? 's' : ''} Selected
              </Text>
            </View>
          </View>
          
          <View style={styles.summaryContent}>
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Total to Swap:</Text>
              <Text style={styles.summaryValue}>
                ${totalSelectedUSD.toLocaleString(undefined, {
                  minimumFractionDigits: 2,
                  maximumFractionDigits: 2,
                })}
              </Text>
            </View>
            
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Total to Receive:</Text>
              <View style={styles.receiveSummary}>
                <View style={[styles.mcgpIconSmall, { backgroundColor: mcgpToken.iconColor }]}>
                  <Text style={styles.mcgpIconTextSmall}>M</Text>
                </View>
                <Text style={styles.summaryValue}>
                  {swapQuote.totalToMCGP.toFixed(2)} MCGP
                </Text>
              </View>
            </View>
            
            <View style={styles.summaryRow}>
              <Text style={styles.summaryLabel}>Exchange Rate:</Text>
              <Text style={styles.summaryValue}>1 USD = 2 MCGP</Text>
            </View>
          </View>
        </View>

        {/* Quick Actions */}
        <View style={styles.quickActions}>
          <TouchableOpacity style={styles.quickActionButton} onPress={handleSelectAll}>
            <Icon name="check-box" size={20} color={GOLD_COLORS.primary} />
            <Text style={styles.quickActionText}>
              {assets.every(asset => asset.selected) ? 'Deselect All' : 'Select All'}
            </Text>
          </TouchableOpacity>
          
          <TouchableOpacity style={styles.quickActionButton} onPress={handleMaxAll}>
            <Icon name="all-inclusive" size={20} color={GOLD_COLORS.primary} />
            <Text style={styles.quickActionText}>Max All Selected</Text>
          </TouchableOpacity>
          
          <TouchableOpacity style={styles.quickActionButton} onPress={handleClearAll}>
            <Icon name="clear-all" size={20} color={GOLD_COLORS.error} />
            <Text style={styles.quickActionText}>Clear All</Text>
          </TouchableOpacity>
        </View>

        {/* Assets Selection Section */}
        <View style={styles.assetsSection}>
          <Text style={styles.sectionTitle}>Select Assets to Swap</Text>
          <Text style={styles.sectionSubtitle}>
            Choose USDT and/or USDC to swap to MCGP
          </Text>
          
          <View style={styles.assetsList}>
            {assets.map((asset, index) => (
              <AssetCard
                key={asset.id}
                asset={asset}
                index={index}
                onToggleSelect={handleToggleSelect}
                onAmountChange={handleAmountChange}
                onMaxPress={handleMaxPress}
              />
            ))}
          </View>
        </View>

        {/* Destination Token Display */}
        <View style={styles.destinationSection}>
          <Text style={styles.destinationTitle}>Destination Token</Text>
          <View style={styles.destinationCard}>
            <View style={styles.destinationTokenInfo}>
              <View style={[styles.destinationIcon, { backgroundColor: mcgpToken.iconColor }]}>
                <Text style={styles.destinationIconText}>M</Text>
              </View>
              <View style={styles.destinationTokenDetails}>
                <View style={styles.destinationHeader}>
                  <Text style={styles.destinationSymbol}>{mcgpToken.symbol}</Text>
                  <View style={styles.fixedBadge}>
                    <Icon name="lock" size={12} color="#FFFFFF" />
                    <Text style={styles.fixedBadgeText}>FIXED DESTINATION</Text>
                  </View>
                </View>
                <Text style={styles.destinationName}>{mcgpToken.name}</Text>
                <Text style={styles.destinationPrice}>
                  Price: ${mcgpToken.priceUSD.toLocaleString()}
                </Text>
              </View>
            </View>
            
            <View style={styles.destinationArrow}>
              <Icon name="arrow-forward" size={24} color={GOLD_COLORS.primary} />
            </View>
            
            <View style={styles.destinationAmount}>
              <Text style={styles.destinationAmountLabel}>You Will Receive:</Text>
              <Text style={styles.destinationAmountValue}>
                {swapQuote.totalToMCGP.toFixed(2)} MCGP
              </Text>
              <Text style={styles.destinationAmountUSD}>
                ≈ ${(swapQuote.totalToMCGP * mcgpToken.priceUSD).toLocaleString(undefined, {
                  minimumFractionDigits: 2,
                  maximumFractionDigits: 2,
                })}
              </Text>
            </View>
          </View>
        </View>

        {/* Swap Details */}
        <View style={styles.swapDetails}>
          <View style={styles.detailsHeader}>
            <Icon name="receipt" size={20} color={GOLD_COLORS.dark} />
            <Text style={styles.detailsTitle}>Swap Details</Text>
          </View>
          
          <View style={styles.detailsContent}>
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Total Assets Selected</Text>
              <Text style={styles.detailValue}>{selectedCount}</Text>
            </View>
            
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Total Swap Value</Text>
              <Text style={styles.detailValue}>
                ${swapQuote.totalFromUSD.toLocaleString(undefined, {
                  minimumFractionDigits: 2,
                  maximumFractionDigits: 2,
                })}
              </Text>
            </View>
            
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Transaction Fees</Text>
              <Text style={styles.detailValue}>
                ${swapQuote.fees.toLocaleString(undefined, {
                  minimumFractionDigits: 4,
                  maximumFractionDigits: 4,
                })}
              </Text>
            </View>
            
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Slippage Tolerance</Text>
              <Text style={styles.detailValue}>{swapQuote.slippage}%</Text>
            </View>
            
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Estimated Time</Text>
              <Text style={styles.detailValue}>{swapQuote.estimatedTime}</Text>
            </View>
          </View>
        </View>

        {/* Confirm Button */}
        <Animated.View style={{ transform: [{ scale }] }}>
          <TouchableOpacity
            style={[
              styles.confirmButton,
              (selectedCount === 0 || isConfirming) && styles.confirmButtonDisabled,
            ]}
            onPress={handleConfirmSwap}
            disabled={selectedCount === 0 || isConfirming}
          >
            {isConfirming ? (
              <>
                <ActivityIndicator color="#000000" size="small" />
                <Text style={styles.confirmButtonText}>Processing...</Text>
              </>
            ) : (
              <>
                <Icon name="swap-horiz" size={24} color="#000000" />
                <Text style={styles.confirmButtonText}>
                  Swap {selectedCount} Asset{selectedCount !== 1 ? 's' : ''} to MCGP
                </Text>
              </>
            )}
          </TouchableOpacity>
        </Animated.View>

        {/* Info Section */}
        <View style={styles.infoSection}>
          <View style={styles.infoCard}>
            <Icon name="info" size={20} color={GOLD_COLORS.primary} />
            <View style={styles.infoContent}>
              <Text style={styles.infoTitle}>Multi-Asset Swap Features</Text>
              <Text style={styles.infoText}>
                • Swap both USDT and USDC to MCGP in one transaction{'\n'}
                • Lower fees when swapping multiple assets{'\n'}
                • Set individual amounts for each asset{'\n'}
                • Real-time conversion rates{'\n'}
                • Fixed destination: All swaps go to MCGP
              </Text>
            </View>
          </View>
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

// ActivityIndicator component (add this if not already imported)
const ActivityIndicator = ({ color, size }: { color: string; size: 'small' | 'large' }) => (
  <View style={{ width: size === 'small' ? 20 : 36, height: size === 'small' ? 20 : 36 }}>
    {/* Simplified spinner */}
    <View style={{
      width: size === 'small' ? 20 : 36,
      height: size === 'small' ? 20 : 36,
      borderRadius: size === 'small' ? 10 : 18,
      borderWidth: 2,
      borderColor: color,
      borderTopColor: 'transparent',
      animation: 'spin 1s linear infinite',
    }} />
  </View>
);

// Styles
const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
  },
  scrollContainer: {
    flex: 1,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 60 : 40,
    paddingBottom: 16,
    backgroundColor: '#FFFFFF',
    borderBottomWidth: 1,
    borderBottomColor: '#E5E5EA',
  },
  headerTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
  },
  headerButton: {
    padding: 8,
  },
  promotionBanner: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    marginHorizontal: 20,
    marginTop: 20,
    padding: 16,
    borderRadius: 16,
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
  },
  promotionContent: {
    flex: 1,
    marginLeft: 12,
  },
  promotionTitle: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    lineHeight: 18,
  },
  summaryCard: {
    backgroundColor: '#FFFFFF',
    marginHorizontal: 20,
    marginTop: 20,
    padding: 20,
    borderRadius: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.1,
    shadowRadius: 12,
    elevation: 8,
  },
  summaryHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 16,
  },
  summaryTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  summaryBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 10,
    paddingVertical: 6,
    borderRadius: 12,
  },
  summaryBadgeText: {
    fontSize: 11,
    fontWeight: '900',
    color: '#000000',
    marginLeft: 4,
  },
  summaryContent: {
    // Content styles
  },
  summaryRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  summaryLabel: {
    fontSize: 14,
    color: '#666666',
  },
  summaryValue: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  receiveSummary: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  mcgpIconSmall: {
    width: 24,
    height: 24,
    borderRadius: 12,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 8,
  },
  mcgpIconTextSmall: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 12,
  },
  quickActions: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginHorizontal: 20,
    marginTop: 20,
    marginBottom: 20,
  },
  quickActionButton: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    paddingVertical: 12,
    paddingHorizontal: 8,
    marginHorizontal: 4,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  quickActionText: {
    fontSize: 12,
    fontWeight: '600',
    color: '#000000',
    marginLeft: 6,
    textAlign: 'center',
  },
  assetsSection: {
    marginHorizontal: 20,
    marginTop: 20,
  },
  sectionTitle: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  sectionSubtitle: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 16,
  },
  assetsList: {
    // Asset cards styled individually
  },
  assetCardContainer: {
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#E5E5EA',
    overflow: 'hidden',
  },
  assetCardHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: 16,
  },
  assetSelection: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  checkbox: {
    marginRight: 12,
  },
  assetIcon: {
    width: 44,
    height: 44,
    borderRadius: 22,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  assetIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 18,
  },
  assetInfo: {
    flex: 1,
  },
  assetHeaderRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 2,
  },
  assetSymbol: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginRight: 8,
  },
  selectedBadge: {
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 8,
  },
  selectedBadgeText: {
    fontSize: 8,
    fontWeight: '900',
    color: '#000000',
  },
  assetName: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 2,
  },
  assetBalance: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
  },
  assetExpandedContent: {
    paddingHorizontal: 16,
    paddingBottom: 16,
    borderTopWidth: 1,
    borderTopColor: '#E5E5EA',
  },
  amountSection: {
    marginBottom: 16,
  },
  amountHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  amountLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  maxButton: {
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: 6,
  },
  maxButtonText: {
    fontSize: 10,
    fontWeight: '900',
    color: '#000000',
  },
  amountInputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.background,
    borderRadius: 8,
    paddingHorizontal: 12,
    paddingVertical: 8,
    marginBottom: 4,
  },
  amountInput: {
    flex: 1,
    fontSize: 24,
    fontWeight: '800',
    color: '#000000',
  },
  amountInputDisabled: {
    color: '#CCCCCC',
  },
  amountSymbol: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },
  amountUSD: {
    fontSize: 12,
    color: '#666666',
    textAlign: 'right',
  },
  toReceiveSection: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 8,
    padding: 12,
  },
  toReceiveLabel: {
    fontSize: 12,
    color: '#666666',
    marginBottom: 8,
  },
  toReceiveAmountContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  mcgpIcon: {
    width: 36,
    height: 36,
    borderRadius: 18,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  mcgpIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 16,
  },
  toReceiveAmount: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
    flex: 1,
  },
  toReceiveUSD: {
    fontSize: 12,
    color: '#666666',
  },
  destinationSection: {
    marginHorizontal: 20,
    marginTop: 24,
  },
  destinationTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 12,
  },
  destinationCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  destinationTokenInfo: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  destinationIcon: {
    width: 48,
    height: 48,
    borderRadius: 24,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  destinationIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 20,
  },
  destinationTokenDetails: {
    flex: 1,
  },
  destinationHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 2,
  },
  destinationSymbol: {
    fontSize: 20,
    fontWeight: '900',
    color: '#000000',
    marginRight: 8,
  },
  fixedBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.dark,
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 8,
  },
  fixedBadgeText: {
    fontSize: 8,
    fontWeight: '900',
    color: '#FFFFFF',
    marginLeft: 2,
  },
  destinationName: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 2,
  },
  destinationPrice: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
  },
  destinationArrow: {
    marginHorizontal: 16,
  },
  destinationAmount: {
    alignItems: 'flex-end',
  },
  destinationAmountLabel: {
    fontSize: 12,
    color: '#666666',
    marginBottom: 4,
  },
  destinationAmountValue: {
    fontSize: 20,
    fontWeight: '900',
    color: '#000000',
    marginBottom: 2,
  },
  destinationAmountUSD: {
    fontSize: 12,
    color: '#666666',
  },
  swapDetails: {
    backgroundColor: '#FFFFFF',
    marginHorizontal: 20,
    marginTop: 24,
    padding: 20,
    borderRadius: 16,
  },
  detailsHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  detailsTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },
  detailsContent: {
    // Content styles
  },
  detailRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
    paddingBottom: 12,
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  detailLabel: {
    fontSize: 14,
    color: '#666666',
  },
  detailValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  confirmButton: {
    backgroundColor: GOLD_COLORS.primary,
    marginHorizontal: 20,
    marginTop: 24,
    paddingVertical: 18,
    borderRadius: 16,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
  },
  confirmButtonDisabled: {
    backgroundColor: '#E5E5EA',
    opacity: 0.6,
  },
  confirmButtonText: {
    fontSize: 18,
    fontWeight: '900',
    color: '#000000',
    marginLeft: 8,
  },
  infoSection: {
    marginHorizontal: 20,
    marginTop: 24,
    marginBottom: 40,
  },
  infoCard: {
    flexDirection: 'row',
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 16,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  infoContent: {
    flex: 1,
    marginLeft: 12,
  },
  infoTitle: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 6,
  },
  infoText: {
    fontSize: 12,
    color: '#666666',
    lineHeight: 16,
  },
});

export default MultiAssetSwapScreen;