import { router } from 'expo-router';
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
  price: number;
  priceUSD: number;
  iconColor: string;
  balance: number;
  balanceUSD: number;
}

interface SwapToken extends Token {
  amount: string;
  amountUSD: number;
}

interface SwapQuote {
  fromToken: SwapToken;
  toToken: SwapToken;
  exchangeRate: number;
  slippage: number;
  fees: number;
  estimatedTime: string;
  minimumReceived: number;
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
  darkBackground: '#1A1A1A',
};

// Token Card Component
const TokenCard: React.FC<{
  token: Token;
  isSelected: boolean;
  onSelect: (token: Token) => void;
}> = ({ token, isSelected, onSelect }) => {
  return (
    <TouchableOpacity
      style={[
        styles.tokenCard,
        isSelected && styles.selectedTokenCard,
      ]}
      onPress={() => onSelect(token)}
      activeOpacity={0.7}
    >
      <View style={styles.tokenCardContent}>
        <View style={styles.tokenInfo}>
          <View
            style={[
              styles.tokenIcon,
              { backgroundColor: token.iconColor },
              isSelected && styles.selectedTokenIcon,
            ]}
          >
            <Text style={styles.tokenIconText}>{token.symbol.charAt(0)}</Text>
          </View>
          <View style={styles.tokenDetails}>
            <View style={styles.tokenHeader}>
              <Text style={styles.tokenSymbol}>{token.symbol}</Text>
            </View>
            <Text style={styles.tokenName}>{token.name}</Text>
            <Text style={styles.tokenPrice}>
              ${token.priceUSD.toLocaleString(undefined, {
                minimumFractionDigits: 2,
                maximumFractionDigits: 6,
              })}
            </Text>
          </View>
        </View>

        <View style={styles.tokenBalance}>
          <Text style={styles.tokenBalanceAmount}>
            {token.balance.toLocaleString(undefined, {
              minimumFractionDigits: 2,
              maximumFractionDigits: 8,
            })}
          </Text>
          <Text style={styles.tokenBalanceUSD}>
            ≈ ${token.balanceUSD.toLocaleString(undefined, {
              minimumFractionDigits: 2,
              maximumFractionDigits: 2,
            })}
          </Text>
        </View>
      </View>

      {isSelected && (
        <View style={styles.selectedIndicator}>
          <Icon name="check-circle" size={20} color={GOLD_COLORS.primary} />
        </View>
      )}
    </TouchableOpacity>
  );
};

// Main SwapScreen Component
const SwapScreen: React.FC = () => {
  // Only USDT and USDC as source tokens, MCGP as destination
  const sourceTokens: Token[] = [
    {
      id: 'usdt',
      symbol: 'USDT',
      name: 'Tether',
      price: 1,
      priceUSD: 1,
      iconColor: '#26A17B',
      balance: 1000,
      balanceUSD: 1000.0,
    },
    {
      id: 'usdc',
      symbol: 'USDC',
      name: 'USD Coin',
      price: 1,
      priceUSD: 1,
      iconColor: '#2775CA',
      balance: 1500,
      balanceUSD: 1500.0,
    },
  ];

  // MCGP as the only destination token
  const mcgpToken: Token = {
    id: 'mcgp',
    symbol: 'MCGP',
    name: 'MCGP Chain',
    price: 0.5, // Example price
    priceUSD: 0.5,
    iconColor: '#FFD700',
    balance: 0,
    balanceUSD: 0,
  };

  // State Management
  const [fromToken, setFromToken] = useState<SwapToken>({
    ...sourceTokens[0], // Default to USDT
    amount: '',
    amountUSD: 0,
  });
  
  const [toToken, setToToken] = useState<SwapToken>({
    ...mcgpToken,
    amount: '',
    amountUSD: 0,
  });

  const [swapType, setSwapType] = useState<'Instant' | 'Recurring' | 'Limit'>('Instant');
  const [swapQuote, setSwapQuote] = useState<SwapQuote>({
    fromToken,
    toToken,
    exchangeRate: 2, // 1 USDT = 2 MCGP (example)
    slippage: 0.5,
    fees: 0.1,
    estimatedTime: '10 seconds',
    minimumReceived: 0,
  });

  const [showFromSelector, setShowFromSelector] = useState(false);
  const [showToSelector, setShowToSelector] = useState(false);
  
  // Animation References
  const swapAnimation = useRef(new Animated.Value(0)).current;

  // Calculate swap when fromToken amount changes
  useEffect(() => {
    const fromAmount = parseFloat(fromToken.amount) || 0;
    const exchangeRate = 2; // 1 USDT/USDC = 2 MCGP
    
    const toAmount = fromAmount * exchangeRate;
    const toAmountUSD = toAmount * toToken.priceUSD;
    
    setToToken(prev => ({
      ...prev,
      amount: toAmount.toFixed(2),
      amountUSD: toAmountUSD,
    }));

    // Update swap quote
    const newSwapQuote = {
      fromToken,
      toToken: { ...toToken, amount: toAmount.toFixed(2), amountUSD: toAmountUSD },
      exchangeRate,
      slippage: 0.5,
      fees: fromAmount * 0.001, // 0.1% fee
      estimatedTime: '10 seconds',
      minimumReceived: toAmount * 0.995, // 0.5% slippage
    };
    
    setSwapQuote(newSwapQuote);
  }, [fromToken.amount]);

  // Handle token selection (only for source token)
  const handleTokenSelect = (token: Token) => {
    setFromToken({
      ...token,
      amount: fromToken.amount,
      amountUSD: parseFloat(fromToken.amount || '0') * token.priceUSD,
    });
    setShowFromSelector(false);
    
    // Animate swap
    Animated.sequence([
      Animated.timing(swapAnimation, {
        toValue: 1,
        duration: 300,
        useNativeDriver: true,
      }),
      Animated.timing(swapAnimation, {
        toValue: 0,
        duration: 300,
        useNativeDriver: true,
      }),
    ]).start();
  };

  // Handle swap tokens (swap source and destination)
  const handleSwapTokens = () => {
    // Only allow swapping if there's an amount entered
    if (!fromToken.amount) return;
    
    // Calculate new exchange rate (inverse)
    const newExchangeRate = 1 / swapQuote.exchangeRate;
    
    // Calculate new amounts
    const newFromAmount = parseFloat(toToken.amount) * newExchangeRate;
    
    setFromToken(prev => ({
      ...prev,
      amount: newFromAmount.toFixed(2),
      amountUSD: newFromAmount * prev.priceUSD,
    }));
    
    setToToken(prev => ({
      ...prev,
      amount: fromToken.amount,
      amountUSD: parseFloat(fromToken.amount || '0') * prev.priceUSD,
    }));
    
    // Update exchange rate
    setSwapQuote(prev => ({
      ...prev,
      exchangeRate: newExchangeRate,
    }));
    
    // Animate swap
    Animated.sequence([
      Animated.timing(swapAnimation, {
        toValue: 1,
        duration: 300,
        useNativeDriver: true,
      }),
      Animated.timing(swapAnimation, {
        toValue: 0,
        duration: 300,
        useNativeDriver: true,
      }),
    ]).start();
  };

  // Handle max amount
  const handleMaxAmount = () => {
    setFromToken(prev => ({
      ...prev,
      amount: prev.balance.toString(),
      amountUSD: prev.balance * prev.priceUSD,
    }));
  };

  // Handle swap confirmation
  const handleConfirmSwap = () => {
    if (!fromToken.amount || parseFloat(fromToken.amount) <= 0) {
      Alert.alert('Invalid Amount', 'Please enter an amount to swap');
      return;
    }

    if (parseFloat(fromToken.amount) > fromToken.balance) {
      Alert.alert('Insufficient Balance', `You only have ${fromToken.balance} ${fromToken.symbol}`);
      return;
    }

    Alert.alert(
      'Confirm Swap',
      `Swap ${fromToken.amount} ${fromToken.symbol} for ${toToken.amount} ${toToken.symbol}?`,
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Confirm',
          style: 'destructive',
          onPress: () => {
            Alert.alert(
              'Swap Successful!',
              `Successfully swapped ${fromToken.amount} ${fromToken.symbol} for ${toToken.amount} ${toToken.symbol}`,
              [{ text: 'OK' }]
            );
          },
        },
      ]
    );
  };

  // Animation interpolation for swap
  const swapRotation = swapAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: ['0deg', '180deg'],
  });

  // Render token selector modal for source tokens
// Render token selector modal for source tokens
const renderTokenSelector = () => {
  return (
    <View style={styles.selectorModal}>
      <View style={styles.selectorHeader}>
        {/* Swap Type Selection Buttons */}
        <View style={styles.swapTypeContainer}>
          <TouchableOpacity
            style={[styles.swapTypeButton, styles.swapTypeButtonActive]}
            onPress={() => console.log('Single swap selected')}
          >
            <Text style={[styles.swapTypeButtonText, styles.swapTypeButtonTextActive]}>
              Single Swap
            </Text>
          </TouchableOpacity>
          
          <TouchableOpacity
            style={styles.swapTypeButton}
            onPress={() =>router.push('/multiswap')}
          >
            <Text style={styles.swapTypeButtonText}>
              Multiple Swap
            </Text>
          </TouchableOpacity>
        </View>

        <View style={styles.titleRow}>
          <Text style={styles.selectorTitle}>Select Token to Swap</Text>
          <TouchableOpacity
            onPress={() => setShowFromSelector(false)}
            style={styles.closeButton}
          >
            <Icon name="close" size={24} color="#000000" />
          </TouchableOpacity>
        </View>
      </View>

      {/* Token List - Only USDT and USDC */}
      <ScrollView style={styles.tokenList} showsVerticalScrollIndicator={false}>
        {sourceTokens.map(token => (
          <TokenCard
            key={token.id}
            token={token}
            isSelected={token.id === fromToken.id}
            onSelect={handleTokenSelect}
          />
        ))}
        
        {/* MCGP Info (non-selectable) */}
        <View style={styles.mcgpInfoCard}>
          <View style={styles.tokenInfo}>
            <View style={[styles.tokenIcon, { backgroundColor: mcgpToken.iconColor }]}>
              <Text style={styles.tokenIconText}>M</Text>
            </View>
            <View style={styles.tokenDetails}>
              <View style={styles.tokenHeader}>
                <Text style={styles.tokenSymbol}>{mcgpToken.symbol}</Text>
                <View style={styles.destinationBadge}>
                  <Icon name="arrow-forward" size={10} color="#FFFFFF" />
                  <Text style={styles.destinationBadgeText}>DESTINATION ONLY</Text>
                </View>
              </View>
              <Text style={styles.tokenName}>{mcgpToken.name}</Text>
              <Text style={styles.tokenPrice}>
                ${mcgpToken.priceUSD.toLocaleString()}
              </Text>
            </View>
          </View>
          <View style={styles.destinationNote}>
            <Icon name="info" size={16} color={GOLD_COLORS.dark} />
            <Text style={styles.destinationNoteText}>
              MCGP is the only destination token for swaps
            </Text>
          </View>
        </View>
      </ScrollView>
    </View>
  );
};
  return (
    <KeyboardAvoidingView
      style={styles.container}
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
    >
      <ScrollView style={styles.scrollContainer} showsVerticalScrollIndicator={false}>
        {/* Header */}
        <View style={styles.header}>
          <Text style={styles.headerTitle}>Swap to MCGP</Text>
          <TouchableOpacity style={styles.headerButton}>
            <Icon name="history" size={24} color="#000000" />
          </TouchableOpacity>
        </View>

        {/* Promotion Banner */}
        <View style={styles.promotionBanner}>
          <Icon name="swap-horiz" size={24} color={GOLD_COLORS.primary} />
          <View style={styles.promotionContent}>
            <Text style={styles.promotionTitle}>
              Swap USDT or USDC to MCGP - Enjoy Low Fees & Fast Transactions!
            </Text>
          </View>
        </View>

        {/* Swap Type Tabs */}
        <View style={styles.swapTypeContainer}>
          {['Instant', 'Recurring', 'Limit'].map((type) => (
            <TouchableOpacity
              key={type}
              style={[
                styles.swapTypeButton,
                swapType === type && styles.swapTypeButtonActive,
              ]}
              onPress={() => setSwapType(type as any)}
            >
              <Text
                style={[
                  styles.swapTypeText,
                  swapType === type && styles.swapTypeTextActive,
                ]}
              >
                {type}
              </Text>
              {swapType === type && (
                <View style={styles.activeIndicator} />
              )}
            </TouchableOpacity>
          ))}
        </View>

        {/* Swap Interface */}
        <View style={styles.swapInterface}>
          {/* From Token (USDT/USDC) */}
          <View style={styles.tokenSection}>
            <View style={styles.sectionHeader}>
              <Text style={styles.sectionLabel}>From</Text>
              <Text style={styles.balanceText}>
                Available: {fromToken.balance.toLocaleString()} {fromToken.symbol}
              </Text>
            </View>
            
            <TouchableOpacity
              style={styles.tokenInputContainer}
              onPress={() => setShowFromSelector(true)}
            >
              <View style={styles.tokenSelector}>
                <View style={[styles.tokenIcon, { backgroundColor: fromToken.iconColor }]}>
                  <Text style={styles.tokenIconText}>{fromToken.symbol.charAt(0)}</Text>
                </View>
                <View style={styles.tokenSelectorInfo}>
                  <Text style={styles.tokenSelectorSymbol}>{fromToken.symbol}</Text>
                  <Text style={styles.tokenSelectorName}>{fromToken.name}</Text>
                </View>
                <Icon name="arrow-drop-down" size={24} color="#000000" />
              </View>
              
              <View style={styles.amountContainer}>
                <TextInput
                  style={styles.amountInput}
                  value={fromToken.amount}
                  onChangeText={(text) => {
                    const cleaned = text.replace(/[^0-9.]/g, '');
                    setFromToken(prev => ({
                      ...prev,
                      amount: cleaned,
                      amountUSD: parseFloat(cleaned || '0') * prev.priceUSD,
                    }));
                  }}
                  keyboardType="decimal-pad"
                  placeholder="0.0"
                  placeholderTextColor="#999"
                />
                <Text style={styles.amountUSD}>
                  ≈ ${fromToken.amountUSD.toLocaleString(undefined, {
                    minimumFractionDigits: 2,
                    maximumFractionDigits: 2,
                  })}
                </Text>
              </View>
            </TouchableOpacity>
            
            <TouchableOpacity style={styles.maxButton} onPress={handleMaxAmount}>
              <Text style={styles.maxButtonText}>MAX</Text>
            </TouchableOpacity>
          </View>

          {/* Swap Button */}
          <TouchableOpacity style={styles.swapButton} onPress={handleSwapTokens}>
            <Animated.View style={{ transform: [{ rotate: swapRotation }] }}>
              <Icon name="swap-vert" size={28} color="#000000" />
            </Animated.View>
          </TouchableOpacity>

          {/* To Token (MCGP - Fixed) */}
          <View style={styles.tokenSection}>
            <View style={styles.sectionHeader}>
              <Text style={styles.sectionLabel}>To</Text>
              <Text style={styles.balanceText}>
                Balance: {toToken.balance.toLocaleString()} {toToken.symbol}
              </Text>
            </View>
            
            <View style={[styles.tokenInputContainer, styles.destinationTokenContainer]}>
              <View style={styles.tokenSelector}>
                <View style={[styles.tokenIcon, { backgroundColor: toToken.iconColor }]}>
                  <Text style={styles.tokenIconText}>{toToken.symbol.charAt(0)}</Text>
                </View>
                <View style={styles.tokenSelectorInfo}>
                  <View style={styles.tokenHeader}>
                    <Text style={styles.tokenSelectorSymbol}>{toToken.symbol}</Text>
                    <View style={styles.fixedBadge}>
                      <Icon name="lock" size={10} color="#FFFFFF" />
                      <Text style={styles.fixedBadgeText}>FIXED</Text>
                    </View>
                  </View>
                  <Text style={styles.tokenSelectorName}>{toToken.name}</Text>
                </View>
                <Icon name="lock" size={20} color="#666666" />
              </View>
              
              <View style={styles.amountContainer}>
                <Text style={styles.receivedAmount}>{toToken.amount || '0.00'}</Text>
                <Text style={styles.amountUSD}>
                  ≈ ${toToken.amountUSD.toLocaleString(undefined, {
                    minimumFractionDigits: 2,
                    maximumFractionDigits: 2,
                  })}
                </Text>
              </View>
            </View>
          </View>

          {/* Exchange Rate */}
          <View style={styles.exchangeRateContainer}>
            <Text style={styles.exchangeRateText}>
              1 {fromToken.symbol} = {swapQuote.exchangeRate.toFixed(2)} {toToken.symbol}
            </Text>
            <Icon name="info" size={16} color="#666666" />
          </View>

          {/* Swap Details */}
          <View style={styles.swapDetails}>
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Slippage Tolerance</Text>
              <Text style={styles.detailValue}>{swapQuote.slippage}%</Text>
            </View>
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Network Fees</Text>
              <Text style={styles.detailValue}>${swapQuote.fees.toFixed(4)}</Text>
            </View>
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Estimated Time</Text>
              <Text style={styles.detailValue}>{swapQuote.estimatedTime}</Text>
            </View>
            <View style={styles.detailRow}>
              <Text style={styles.detailLabel}>Minimum Received</Text>
              <Text style={styles.detailValue}>
                {swapQuote.minimumReceived.toFixed(2)} {toToken.symbol}
              </Text>
            </View>
          </View>

          {/* Confirm Button */}
          <TouchableOpacity 
            style={[
              styles.confirmButton,
              (!fromToken.amount || parseFloat(fromToken.amount) <= 0) && styles.confirmButtonDisabled
            ]} 
            onPress={handleConfirmSwap}
            disabled={!fromToken.amount || parseFloat(fromToken.amount) <= 0}
          >
            <Text style={styles.confirmButtonText}>
              {swapType === 'Instant' ? 'Swap Now (8s)' : `Swap ${swapType}`}
            </Text>
          </TouchableOpacity>
        </View>

        {/* Available Tokens Section */}
        <View style={styles.availableTokensSection}>
          <Text style={styles.sectionTitle}>Available to Swap</Text>
          <View style={styles.availableTokensList}>
            {sourceTokens.map(token => (
              <TouchableOpacity
                key={token.id}
                style={[
                  styles.availableToken,
                  fromToken.id === token.id && styles.selectedAvailableToken,
                ]}
                onPress={() => handleTokenSelect(token)}
              >
                <View style={[styles.availableTokenIcon, { backgroundColor: token.iconColor }]}>
                  <Text style={styles.availableTokenIconText}>{token.symbol.charAt(0)}</Text>
                </View>
                <Text style={styles.availableTokenSymbol}>{token.symbol}</Text>
                <Text style={styles.availableTokenBalance}>
                  {token.balance.toLocaleString()}
                </Text>
              </TouchableOpacity>
            ))}
          </View>
          <Text style={styles.availableTokensNote}>
            * Only USDT and USDC can be swapped to MCGP
          </Text>
        </View>

        {/* Info Section */}
        <View style={styles.infoSection}>
          <View style={styles.infoCard}>
            <Icon name="info" size={24} color={GOLD_COLORS.primary} style={styles.infoIcon} />
            <View style={styles.infoContent}>
              <Text style={styles.infoTitle}>About MCGP Swaps</Text>
              <Text style={styles.infoText}>
                • Swap USDT or USDC to MCGP tokens{'\n'}
                • Low 0.1% transaction fee{'\n'}
                • Fast confirmation time (10 seconds){'\n'}
                • Fixed destination: Only MCGP is supported{'\n'}
                • Minimum swap: 10 {fromToken.symbol}
              </Text>
            </View>
          </View>
        </View>
      </ScrollView>

      {/* Token Selector Modal */}
      {showFromSelector && renderTokenSelector()}
    </KeyboardAvoidingView>
  );
};

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
  swapTypeContainer: {
    flexDirection: 'row',
    marginHorizontal: 20,
    marginTop: 20,
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    padding: 4,
  },
  swapTypeButton: {
    flex: 1,
    paddingVertical: 12,
    alignItems: 'center',
    position: 'relative',
  },
  swapTypeButtonActive: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 8,
  },
  swapTypeText: {
    fontSize: 14,
    fontWeight: '600',
    color: '#666666',
  },
  swapTypeTextActive: {
    color: '#000000',
    fontWeight: '800',
  },
  activeIndicator: {
    position: 'absolute',
    bottom: -4,
    width: 20,
    height: 3,
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 2,
  },
  swapInterface: {
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
  tokenSection: {
    marginBottom: 20,
    position: 'relative',
  },
  sectionHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  sectionLabel: {
    fontSize: 16,
    fontWeight: '600',
    color: '#000000',
  },
  balanceText: {
    fontSize: 12,
    color: '#666666',
  },
  tokenInputContainer: {
    backgroundColor: GOLD_COLORS.background,
    borderRadius: 16,
    padding: 16,
    borderWidth: 2,
    borderColor: '#E5E5EA',
  },
  destinationTokenContainer: {
    backgroundColor: '#F8F8F8',
    borderColor: GOLD_COLORS.muted,
  },
  tokenSelector: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  tokenIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  tokenIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 16,
  },
  tokenSelectorInfo: {
    flex: 1,
  },
  tokenSelectorSymbol: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  tokenSelectorName: {
    fontSize: 12,
    color: '#666666',
  },
  amountContainer: {
    alignItems: 'flex-end',
  },
  amountInput: {
    fontSize: 32,
    fontWeight: '800',
    color: '#000000',
    textAlign: 'right',
    marginBottom: 4,
    width: '100%',
  },
  receivedAmount: {
    fontSize: 32,
    fontWeight: '800',
    color: '#000000',
    textAlign: 'right',
    marginBottom: 4,
  },
  amountUSD: {
    fontSize: 14,
    color: '#666666',
    fontWeight: '500',
  },
  maxButton: {
    position: 'absolute',
    right: 16,
    bottom: 16,
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 8,
  },
  maxButtonText: {
    fontSize: 12,
    fontWeight: '800',
    color: '#000000',
  },
  swapButton: {
    alignSelf: 'center',
    backgroundColor: GOLD_COLORS.primary,
    width: 56,
    height: 56,
    borderRadius: 28,
    justifyContent: 'center',
    alignItems: 'center',
    marginVertical: -10,
    zIndex: 1,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 8,
    elevation: 6,
  },
  exchangeRateContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    marginTop: 20,
    marginBottom: 20,
    paddingVertical: 12,
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
  },
  exchangeRateText: {
    fontSize: 14,
    color: '#666666',
    marginRight: 8,
  },
  swapDetails: {
    backgroundColor: GOLD_COLORS.background,
    borderRadius: 12,
    padding: 16,
    marginBottom: 20,
  },
  detailRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
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
    paddingVertical: 18,
    borderRadius: 16,
    alignItems: 'center',
  },
  confirmButtonDisabled: {
    backgroundColor: '#E5E5EA',
    opacity: 0.6,
  },
  confirmButtonText: {
    fontSize: 18,
    fontWeight: '900',
    color: '#000000',
  },
  selectorModal: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: '#FFFFFF',
    zIndex: 1000,
    paddingTop: Platform.OS === 'ios' ? 60 : 40,
  },
  selectorHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingVertical: 16,
    borderBottomWidth: 1,
    borderBottomColor: '#E5E5EA',
  },
  selectorTitle: {
    fontSize: 20,
    fontWeight: '800',
    color: '#000000',
  },
  closeButton: {
    padding: 4,
  },
  tokenList: {
    flex: 1,
    paddingHorizontal: 20,
  },
  tokenCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    padding: 16,
    marginBottom: 8,
    borderWidth: 1,
    borderColor: '#E5E5EA',
  },
  selectedTokenCard: {
    borderColor: GOLD_COLORS.primary,
    borderWidth: 2,
    backgroundColor: GOLD_COLORS.light,
  },
  tokenCardContent: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  tokenInfo: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  tokenDetails: {
    flex: 1,
  },
  tokenHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 2,
  },
  tokenSymbol: {
    fontSize: 16,
    fontWeight: '800',
    color: '#000000',
    marginRight: 8,
  },
  tokenName: {
    fontSize: 12,
    color: '#666666',
    marginBottom: 4,
  },
  tokenPrice: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  selectedTokenIcon: {
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
  },
  tokenBalance: {
    alignItems: 'flex-end',
  },
  tokenBalanceAmount: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 2,
  },
  tokenBalanceUSD: {
    fontSize: 12,
    color: '#666666',
  },
  selectedIndicator: {
    position: 'absolute',
    top: 8,
    right: 8,
  },
  mcgpInfoCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginTop: 8,
    borderWidth: 2,
    borderColor: GOLD_COLORS.muted,
  },
  destinationBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.dark,
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 8,
    marginLeft: 8,
  },
  destinationBadgeText: {
    fontSize: 8,
    fontWeight: '900',
    color: '#FFFFFF',
    marginLeft: 2,
  },
  destinationNote: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 12,
    padding: 8,
    backgroundColor: 'rgba(184, 134, 11, 0.1)',
    borderRadius: 8,
  },
  destinationNoteText: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
  },
  fixedBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#666666',
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 8,
    marginLeft: 8,
  },
  fixedBadgeText: {
    fontSize: 8,
    fontWeight: '900',
    color: '#FFFFFF',
    marginLeft: 2,
  },
  availableTokensSection: {
    marginHorizontal: 20,
    marginTop: 24,
  },
  sectionTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 16,
  },
  availableTokensList: {
    flexDirection: 'row',
    justifyContent: 'space-around',
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    marginBottom: 8,
  },
  availableToken: {
    alignItems: 'center',
    padding: 12,
    borderRadius: 12,
    width: 100,
  },
  selectedAvailableToken: {
    backgroundColor: GOLD_COLORS.light,
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
  },
  availableTokenIcon: {
    width: 48,
    height: 48,
    borderRadius: 24,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 8,
  },
  availableTokenIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 18,
  },
  availableTokenSymbol: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  availableTokenBalance: {
    fontSize: 12,
    color: '#666666',
  },
  availableTokensNote: {
    fontSize: 11,
    color: '#999999',
    textAlign: 'center',
    fontStyle: 'italic',
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
    padding: 20,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  infoIcon: {
    marginRight: 12,
    marginTop: 2,
  },
  infoContent: {
    flex: 1,
  },
  infoTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 8,
  },
  infoText: {
    fontSize: 13,
    color: '#666666',
    lineHeight: 18,
  },
 
  swapTypeButtonText: {
    fontSize: 14,
    fontWeight: '500',
    color: '#666666',
  },
  
  swapTypeButtonTextActive: {
    color: '#000000',
    fontWeight: '600',
  },
  
  titleRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    width: '100%',
  },
  
  selectorTitle: {
    fontSize: 20,
    fontWeight: 'bold',
    color: '#000000',
    flex: 1,
    textAlign: 'center',
    marginLeft: 40, // To balance with close button on the right
  },
  
  closeButton: {
    width: 40,
    height: 40,
    justifyContent: 'center',
    alignItems: 'center',
  },
  
  selectorHeader: {
    paddingHorizontal: 20,
    paddingTop: 20,
    paddingBottom: 16,
    borderBottomWidth: 1,
    borderBottomColor: '#E0E0E0',
    alignItems: 'center',
  },
});

export default SwapScreen;