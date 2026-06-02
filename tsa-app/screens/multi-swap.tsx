// screens/multi-swap.tsx
//
// Multi-asset swap — pick USDT and/or USDC, set per-asset amounts, and batch-swap
// them all into the fixed MCGP destination. Restyled onto the TSA Signature design
// system (theme tokens + @/components/ui primitives). Every behavior is unchanged:
// the per-asset expand/rotate animation, selection/amount/MAX logic, the
// derived-quote effect, all validation, the confirm + execute animation sequence,
// and ALL amount/USD/fee math + formatting are preserved verbatim — this is a
// visual/structural restyle only.
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  TouchableOpacity,
  ScrollView,
  Animated,
  Platform,
  TextInput,
  Alert,
  KeyboardAvoidingView,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Card,
  Icon,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

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

// Asset Card Component
const AssetCard: React.FC<{
  asset: SwapAsset;
  index: number;
  onToggleSelect: (id: string) => void;
  onAmountChange: (id: string, amount: string) => void;
  onMaxPress: (id: string) => void;
}> = ({ asset, index, onToggleSelect, onAmountChange, onMaxPress }) => {
  const { colors, spacing, radius, fonts } = useTheme();
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
    <Animated.View
      style={{
        height: heightInterpolation,
        backgroundColor: colors.surface,
        borderRadius: radius.md,
        marginBottom: spacing.md,
        borderWidth: 1,
        borderColor: asset.selected ? colors.accent : colors.border,
        overflow: 'hidden',
      }}
    >
      {/* Card Header */}
      <TouchableOpacity
        style={{
          flexDirection: 'row',
          justifyContent: 'space-between',
          alignItems: 'center',
          padding: spacing.lg,
        }}
        onPress={() => setIsExpanded(!isExpanded)}
        activeOpacity={0.7}
      >
        <View style={{ flexDirection: 'row', alignItems: 'center', flex: 1 }}>
          <TouchableOpacity
            style={{ marginRight: spacing.md }}
            onPress={(e) => {
              e.stopPropagation();
              onToggleSelect(asset.id);
            }}
          >
            {asset.selected ? (
              <Icon name="checkCircle" size={24} color={colors.accent} />
            ) : (
              <View
                style={{
                  width: 22,
                  height: 22,
                  borderRadius: radius.sm,
                  borderWidth: 2,
                  borderColor: colors.border,
                }}
              />
            )}
          </TouchableOpacity>

          <View
            style={{
              width: 44,
              height: 44,
              borderRadius: radius.pill,
              justifyContent: 'center',
              alignItems: 'center',
              marginRight: spacing.md,
              backgroundColor: asset.iconColor,
            }}
          >
            <Text variant="title3" weight="800" style={{ color: colors.textInverse }}>
              {asset.symbol.charAt(0)}
            </Text>
          </View>

          <View style={{ flex: 1 }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: 2 }}>
              <Text variant="title3" weight="700">
                {asset.symbol}
              </Text>
              {asset.selected && (
                <Badge label="SELECTED" size="sm" color={colors.accent} background={colors.accentMuted} />
              )}
            </View>
            <Text variant="bodySm" color="textSecondary">
              {asset.name}
            </Text>
            <Text variant="caption" weight="600" color="accent" style={{ marginTop: 2 }}>
              Balance: {asset.balance.toLocaleString()} {asset.symbol}
            </Text>
          </View>
        </View>

        <Animated.View style={{ transform: [{ rotate: rotation }] }}>
          <Icon name="chevronDown" size={24} color={colors.textSecondary} />
        </Animated.View>
      </TouchableOpacity>

      {/* Expanded Content */}
      {isExpanded && (
        <View
          style={{
            paddingHorizontal: spacing.lg,
            paddingBottom: spacing.lg,
            borderTopWidth: 1,
            borderTopColor: colors.hairline,
          }}
        >
          {/* Amount Input Section */}
          <View style={{ marginBottom: spacing.lg }}>
            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'space-between',
                alignItems: 'center',
                marginBottom: spacing.sm,
              }}
            >
              <Text variant="bodySm" weight="600">
                Amount to Swap
              </Text>
              <PressableScale
                onPress={() => onMaxPress(asset.id)}
                style={{
                  backgroundColor: colors.accent,
                  paddingHorizontal: spacing.md,
                  paddingVertical: spacing.xs,
                  borderRadius: radius.sm,
                }}
              >
                <Text variant="overline" weight="800" style={{ color: colors.onAccent }}>
                  MAX
                </Text>
              </PressableScale>
            </View>

            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                backgroundColor: colors.surfaceAlt,
                borderRadius: radius.sm,
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.sm,
                marginBottom: spacing.xs,
              }}
            >
              <TextInput
                style={{
                  flex: 1,
                  fontSize: 24,
                  fontFamily: fonts['800'],
                  color: asset.selected ? colors.textPrimary : colors.textDisabled,
                }}
                value={asset.amount}
                onChangeText={(text) => {
                  const cleaned = text.replace(/[^0-9.]/g, '');
                  onAmountChange(asset.id, cleaned);
                }}
                keyboardType="decimal-pad"
                placeholder="0.0"
                placeholderTextColor={colors.textMuted}
                editable={asset.selected}
              />
              <Text variant="title3" weight="700" style={{ marginLeft: spacing.sm }}>
                {asset.symbol}
              </Text>
            </View>

            <Text variant="caption" color="textSecondary" align="right">
              ≈ ${asset.amountUSD.toLocaleString(undefined, {
                minimumFractionDigits: 2,
                maximumFractionDigits: 2,
              })}
            </Text>
          </View>

          {/* To Receive Section */}
          <View
            style={{
              backgroundColor: colors.accentMuted,
              borderRadius: radius.sm,
              padding: spacing.md,
            }}
          >
            <Text variant="caption" color="textSecondary" style={{ marginBottom: spacing.sm }}>
              You will receive:
            </Text>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <View
                style={{
                  width: 36,
                  height: 36,
                  borderRadius: radius.pill,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginRight: spacing.md,
                  backgroundColor: colors.accent,
                }}
              >
                <Text variant="bodyMed" weight="800" style={{ color: colors.onAccent }}>
                  M
                </Text>
              </View>
              <Text variant="title2" weight="700" style={{ flex: 1 }}>
                {asset.toReceive.toFixed(2)} MCGP
              </Text>
              <Text variant="caption" color="textSecondary">
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
  const { colors, spacing, radius } = useTheme();

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

  const swapDisabled = selectedCount === 0 || isConfirming;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
        >
          {/* Promotion Banner */}
          <AccentCard padding="lg">
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <Icon name="swap" size={24} color={colors.accent} />
              <View style={{ flex: 1 }}>
                <Text variant="bodySm" weight="700">
                  Swap Multiple Assets at Once! Save on Fees with Batch Swaps
                </Text>
              </View>
            </View>
          </AccentCard>

          {/* Summary Card */}
          <Card padding="xl" style={{ marginTop: spacing.lg }}>
            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'space-between',
                alignItems: 'center',
                marginBottom: spacing.lg,
              }}
            >
              <Text variant="title3" weight="700">
                Swap Summary
              </Text>
              <Badge
                label={`${selectedCount} Asset${selectedCount !== 1 ? 's' : ''} Selected`}
                icon="swapVertical"
                color={colors.onAccent}
                background={colors.accent}
              />
            </View>

            <View>
              <View
                style={{
                  flexDirection: 'row',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  marginBottom: spacing.md,
                }}
              >
                <Text variant="bodySm" color="textSecondary">
                  Total to Swap:
                </Text>
                <Text variant="bodyMed" weight="700">
                  ${totalSelectedUSD.toLocaleString(undefined, {
                    minimumFractionDigits: 2,
                    maximumFractionDigits: 2,
                  })}
                </Text>
              </View>

              <View
                style={{
                  flexDirection: 'row',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  marginBottom: spacing.md,
                }}
              >
                <Text variant="bodySm" color="textSecondary">
                  Total to Receive:
                </Text>
                <View style={{ flexDirection: 'row', alignItems: 'center' }}>
                  <View
                    style={{
                      width: 24,
                      height: 24,
                      borderRadius: radius.pill,
                      justifyContent: 'center',
                      alignItems: 'center',
                      marginRight: spacing.sm,
                      backgroundColor: mcgpToken.iconColor,
                    }}
                  >
                    <Text variant="caption" weight="800" style={{ color: '#000000' }}>
                      M
                    </Text>
                  </View>
                  <Text variant="bodyMed" weight="700">
                    {swapQuote.totalToMCGP.toFixed(2)} MCGP
                  </Text>
                </View>
              </View>

              <View
                style={{
                  flexDirection: 'row',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  marginBottom: spacing.md,
                }}
              >
                <Text variant="bodySm" color="textSecondary">
                  Exchange Rate:
                </Text>
                <Text variant="bodyMed" weight="700">
                  1 USD = 2 MCGP
                </Text>
              </View>
            </View>
          </Card>

          {/* Quick Actions */}
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'space-between',
              marginTop: spacing.lg,
              marginBottom: spacing.lg,
              gap: spacing.sm,
            }}
          >
            <PressableScale
              onPress={handleSelectAll}
              style={{ flex: 1 }}
            >
              <Card padding="md" style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.xs }}>
                <Icon name="checkCircle" size={20} color={colors.accent} />
                <Text variant="caption" weight="600" align="center">
                  {assets.every(asset => asset.selected) ? 'Deselect All' : 'Select All'}
                </Text>
              </Card>
            </PressableScale>

            <PressableScale
              onPress={handleMaxAll}
              style={{ flex: 1 }}
            >
              <Card padding="md" style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.xs }}>
                <Icon name="arrowUp" size={20} color={colors.accent} />
                <Text variant="caption" weight="600" align="center">
                  Max All Selected
                </Text>
              </Card>
            </PressableScale>

            <PressableScale
              onPress={handleClearAll}
              style={{ flex: 1 }}
            >
              <Card padding="md" style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.xs }}>
                <Icon name="trash" size={20} color={colors.danger} />
                <Text variant="caption" weight="600" align="center">
                  Clear All
                </Text>
              </Card>
            </PressableScale>
          </View>

          {/* Assets Selection Section */}
          <View style={{ marginTop: spacing.lg }}>
            <Text variant="title2" weight="700" style={{ marginBottom: spacing.xs }}>
              Select Assets to Swap
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
              Choose USDT and/or USDC to swap to MCGP
            </Text>

            <View>
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
          <View style={{ marginTop: spacing['2xl'] }}>
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
              Destination Token
            </Text>
            <AccentCard padding="xl">
              <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
                <View style={{ flexDirection: 'row', alignItems: 'center', flex: 1 }}>
                  <View
                    style={{
                      width: 48,
                      height: 48,
                      borderRadius: radius.pill,
                      justifyContent: 'center',
                      alignItems: 'center',
                      marginRight: spacing.md,
                      backgroundColor: mcgpToken.iconColor,
                    }}
                  >
                    <Text variant="title3" weight="800" style={{ color: '#000000' }}>
                      M
                    </Text>
                  </View>
                  <View style={{ flex: 1 }}>
                    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: 2 }}>
                      <Text variant="title2" weight="800">
                        {mcgpToken.symbol}
                      </Text>
                      <Badge label="FIXED DESTINATION" size="sm" icon="lock" color={colors.textInverse} background={colors.accentHover} />
                    </View>
                    <Text variant="bodySm" color="textSecondary">
                      {mcgpToken.name}
                    </Text>
                    <Text variant="caption" weight="600" color="accent" style={{ marginTop: 2 }}>
                      Price: ${mcgpToken.priceUSD.toLocaleString()}
                    </Text>
                  </View>
                </View>

                <View style={{ marginHorizontal: spacing.lg }}>
                  <Icon name="arrowRight" size={24} color={colors.accent} />
                </View>

                <View style={{ alignItems: 'flex-end' }}>
                  <Text variant="caption" color="textSecondary" style={{ marginBottom: spacing.xs }}>
                    You Will Receive:
                  </Text>
                  <Text variant="title2" weight="800" style={{ marginBottom: 2 }}>
                    {swapQuote.totalToMCGP.toFixed(2)} MCGP
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    ≈ ${(swapQuote.totalToMCGP * mcgpToken.priceUSD).toLocaleString(undefined, {
                      minimumFractionDigits: 2,
                      maximumFractionDigits: 2,
                    })}
                  </Text>
                </View>
              </View>
            </AccentCard>
          </View>

          {/* Swap Details */}
          <Card padding="xl" style={{ marginTop: spacing['2xl'] }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.lg }}>
              <Icon name="list" size={20} color={colors.accentHover} />
              <Text variant="bodyMed" weight="700">
                Swap Details
              </Text>
            </View>

            <View>
              {[
                {
                  label: 'Total Assets Selected',
                  value: `${selectedCount}`,
                },
                {
                  label: 'Total Swap Value',
                  value: `$${swapQuote.totalFromUSD.toLocaleString(undefined, {
                    minimumFractionDigits: 2,
                    maximumFractionDigits: 2,
                  })}`,
                },
                {
                  label: 'Transaction Fees',
                  value: `$${swapQuote.fees.toLocaleString(undefined, {
                    minimumFractionDigits: 4,
                    maximumFractionDigits: 4,
                  })}`,
                },
                {
                  label: 'Slippage Tolerance',
                  value: `${swapQuote.slippage}%`,
                },
                {
                  label: 'Estimated Time',
                  value: swapQuote.estimatedTime,
                },
              ].map((row) => (
                <View
                  key={row.label}
                  style={{
                    flexDirection: 'row',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    marginBottom: spacing.md,
                    paddingBottom: spacing.md,
                    borderBottomWidth: 1,
                    borderBottomColor: colors.hairline,
                  }}
                >
                  <Text variant="bodySm" color="textSecondary">
                    {row.label}
                  </Text>
                  <Text variant="bodySm" weight="600">
                    {row.value}
                  </Text>
                </View>
              ))}
            </View>
          </Card>

          {/* Confirm Button */}
          <Animated.View style={{ transform: [{ scale }], marginTop: spacing['2xl'] }}>
            {isConfirming ? (
              <Card
                padding="lg"
                style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.sm, backgroundColor: colors.accent }}
              >
                <Spinner size="small" color={colors.onAccent} />
                <Text variant="bodyMed" weight="800" style={{ color: colors.onAccent }}>
                  Processing...
                </Text>
              </Card>
            ) : (
              <Button
                title={`Swap ${selectedCount} Asset${selectedCount !== 1 ? 's' : ''} to MCGP`}
                size="lg"
                leftIcon="swap"
                onPress={handleConfirmSwap}
                disabled={swapDisabled}
              />
            )}
          </Animated.View>

          {/* Info Section */}
          <View style={{ marginTop: spacing['2xl'] }}>
            <Card padding="lg" style={{ flexDirection: 'row' }}>
              <Icon name="info" size={20} color={colors.accent} />
              <View style={{ flex: 1, marginLeft: spacing.md }}>
                <Text variant="bodySm" weight="700" style={{ marginBottom: spacing.xs }}>
                  Multi-Asset Swap Features
                </Text>
                <Text variant="caption" color="textSecondary">
                  • Swap both USDT and USDC to MCGP in one transaction{'\n'}
                  • Lower fees when swapping multiple assets{'\n'}
                  • Set individual amounts for each asset{'\n'}
                  • Real-time conversion rates{'\n'}
                  • Fixed destination: All swaps go to MCGP
                </Text>
              </View>
            </Card>
          </View>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default MultiAssetSwapScreen;
