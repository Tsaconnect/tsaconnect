// screens/transfer.tsx
//
// Send / withdraw flow — withdrawal-method picker, asset selector, amount entry
// with custom keypad, recipient details, fee summary, and the send action.
// Migrated onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). All state, effects (network availability + fee/total math),
// callbacks, the asset/network fee rules, amount/decimal handling, validation,
// navigation, and the simulated send are unchanged — this is a visual/structural
// restyle only.

import { router } from 'expo-router';
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  TextInput,
  ScrollView,
  Animated,
  StyleSheet,
  Dimensions,
  Platform,
  KeyboardAvoidingView,
  Alert,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  Text,
  Button,
  Card,
  Surface,
  Icon,
  PressableScale,
} from '@/components/ui';

// Types and interfaces
interface Asset {
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  details?: {
    type: string;
    chain?: string;
    address?: string;
  };
}

interface Network {
  id: string;
  name: string;
  symbol: string;
  fee: number;
  time: string;
  isAvailable: boolean;
}

// Network Selection Component
const NetworkCard: React.FC<{
  network: Network;
  isSelected: boolean;
  onSelect: (network: Network) => void;
}> = ({ network, isSelected, onSelect }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <PressableScale
      onPress={() => network.isAvailable && onSelect(network)}
      disabled={!network.isAvailable}
      style={{
        width: 180,
        backgroundColor: isSelected ? colors.accentMuted : colors.surfaceAlt,
        borderRadius: radius.lg,
        padding: spacing.lg,
        marginRight: spacing.md,
        borderWidth: 2,
        borderColor: isSelected ? colors.accent : 'transparent',
        opacity: !network.isAvailable ? 0.5 : 1,
      }}
    >
      <View style={{ flexDirection: 'row', alignItems: 'center' }}>
        <View style={{ marginRight: spacing.md }}>
          <View
            style={{
              width: 48,
              height: 48,
              borderRadius: 24,
              backgroundColor: isSelected ? colors.accent : colors.surfaceElevated,
              justifyContent: 'center',
              alignItems: 'center',
            }}
          >
            <Text variant="title3" weight="800" style={{ color: isSelected ? colors.onAccent : colors.textPrimary }}>
              {network.symbol.charAt(0)}
            </Text>
          </View>
        </View>

        <View style={{ flex: 1 }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.sm }}>
            <Text
              variant="bodyMed"
              weight="700"
              numberOfLines={1}
              style={{ flex: 1, color: isSelected ? colors.accent : colors.textPrimary }}
            >
              {network.name}
            </Text>
            {isSelected && (
              <View style={{ marginLeft: spacing.xs }}>
                <Icon name="checkCircle" size={14} color={colors.accent} />
              </View>
            )}
          </View>

          <View style={{ gap: spacing.xs }}>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Icon name="card" size={12} color={colors.textMuted} />
              <Text variant="caption" color="textMuted" style={{ marginLeft: spacing.xs }}>
                Fee: {network.fee} USD
              </Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center' }}>
              <Icon name="clock" size={12} color={colors.textMuted} />
              <Text variant="caption" color="textMuted" style={{ marginLeft: spacing.xs }}>
                Time: {network.time}
              </Text>
            </View>
          </View>
        </View>
      </View>

      {!network.isAvailable && (
        <View
          style={{
            position: 'absolute',
            top: 8,
            right: 8,
            backgroundColor: colors.danger,
            paddingHorizontal: spacing.sm,
            paddingVertical: spacing.xs,
            borderRadius: radius.sm,
          }}
        >
          <Text variant="overline" weight="700" style={{ color: '#FFFFFF' }}>
            Unavailable
          </Text>
        </View>
      )}
    </PressableScale>
  );
};


const TransferScreen: React.FC = () => {
  const { colors, spacing, radius, fonts, type } = useTheme();

  // Available assets (from dashboard)
  const [assets, setAssets] = useState<Asset[]>([
    {
      id: '1',
      symbol: 'MCGP',
      name: 'MCGP Chain',
      balance: 1500.75,
      usdValue: 1500.75,
      details: {
        type: 'Gold-Backed Token',
        chain: 'Polygon',
        address: '0x742d35Cc6634C0532925a3b844Bc9e...'
      },
    },
    {
      id: '2',
      symbol: 'USDT',
      name: 'Tether',
      balance: 2500.25,
      usdValue: 2500.25,
      details: {
        type: 'Stablecoin',
        chain: 'Ethereum',
        address: '0xdAC17F958D2ee523a2206206994597C...'
      },
    },
    {
      id: '3',
      symbol: 'USDC',
      name: 'USD Coin',
      balance: 1800.50,
      usdValue: 1800.50,
      details: {
        type: 'Stablecoin',
        chain: 'Ethereum',
        address: '0xA0b86991c6218b36c1d19D4a2e9Eb0c...'
      },
    },
  ]);

  // Available networks (initial state)
  const [allNetworks, setAllNetworks] = useState<Network[]>([
    { id: 'sonic', name: 'Sonic Network', symbol: 'S', fee: 0.00, time: '5-10 mins', isAvailable: false },
    { id: 'binance', name: 'Binance Smart Chain (BEP20)', symbol: 'BSC', fee: 0.80, time: '1-3 mins', isAvailable: false },
  ]);

  const [networks, setNetworks] = useState<Network[]>(allNetworks);

  // Form state
  const [selectedAsset, setSelectedAsset] = useState<Asset | null>(assets[1]); // Default to USDT
  const [selectedNetwork, setSelectedNetwork] = useState<Network | null>(null);
  const [receiveAmount, setReceiveAmount] = useState<string>('0.00');
  const [sendAmount, setSendAmount] = useState<string>('');
  const [recipientAddress, setRecipientAddress] = useState<string>('');
  const [noteToPayee, setNoteToPayee] = useState<string>('');
  const [withdrawalMethod, setWithdrawalMethod] = useState<string>('onchain');
  const [showAssetSelector, setShowAssetSelector] = useState<boolean>(false);
  const [isLoading, setIsLoading] = useState<boolean>(false);
  const [feeDetails, setFeeDetails] = useState<{ fee: number; total: number }>({ fee: 0, total: 0 });

  // Animation refs
  const slideAnimation = useRef(new Animated.Value(0)).current;
  const buttonScale = useRef(new Animated.Value(1)).current;

  // Update available networks based on selected asset
  useEffect(() => {
    if (!selectedAsset) return;

    const updatedNetworks = allNetworks.map(network => {
      let isAvailable = false;
      let fee = network.fee;

      // Apply fee rules based on selected asset
      switch (selectedAsset.symbol) {
        case 'MCGP':
          // MCGP can only use Sonic Network with $0 fee
          if (network.id === 'sonic') {
            isAvailable = true;
            fee = 0.00; // Charge free
          } else {
            isAvailable = false;
          }
          break;

        case 'USDC':
          // USDC can use both networks with different fees
          if (network.id === 'sonic') {
            isAvailable = true;
            fee = 1.00; // $1 fee for Sonic
          } else if (network.id === 'binance') {
            isAvailable = true;
            fee = 0.80; // $0.80 fee for Binance
          }
          break;

        case 'USDT':
          // USDT can only use Binance Network
          if (network.id === 'binance') {
            isAvailable = true;
            fee = 0.80; // $0.80 fee
          } else {
            isAvailable = false;
          }
          break;

        default:
          isAvailable = false;
      }

      return { ...network, isAvailable, fee };
    });

    // Filter to only show available networks
    const availableNetworks = updatedNetworks.filter(network => network.isAvailable);

    setNetworks(availableNetworks);

    // Auto-select network based on rules
    if (availableNetworks.length > 0) {
      // For MCGP, auto-select Sonic (it's the only option)
      if (selectedAsset.symbol === 'MCGP') {
        const sonicNetwork = availableNetworks.find(n => n.id === 'sonic');
        if (sonicNetwork) {
          setSelectedNetwork(sonicNetwork);
        }
      }
      // For USDC, default to Binance (cheaper)
      else if (selectedAsset.symbol === 'USDC') {
        const binanceNetwork = availableNetworks.find(n => n.id === 'binance');
        if (binanceNetwork) {
          setSelectedNetwork(binanceNetwork);
        }
      }
      // For USDT, auto-select Binance (it's the only option)
      else if (selectedAsset.symbol === 'USDT') {
        const binanceNetwork = availableNetworks.find(n => n.id === 'binance');
        if (binanceNetwork) {
          setSelectedNetwork(binanceNetwork);
        }
      }
    } else {
      setSelectedNetwork(null);
    }

    // Reset amount when changing asset
    setSendAmount('');
    setReceiveAmount('0.00');
  }, [selectedAsset]);

  // Calculate fee and total when send amount changes
  useEffect(() => {
    const amount = parseFloat(sendAmount) || 0;
    const fee = selectedNetwork?.fee || 0;
    const total = amount + fee;

    if (selectedAsset && amount > 0) {
      const usdAmount = (amount * selectedAsset.usdValue) / selectedAsset.balance;
      const usdFee = fee;
      const usdTotal = usdAmount + usdFee;

      setFeeDetails({ fee: usdFee, total: usdTotal });
      setReceiveAmount(usdAmount.toFixed(2));
    } else {
      setFeeDetails({ fee: 0, total: 0 });
      setReceiveAmount('0.00');
    }
  }, [sendAmount, selectedNetwork, selectedAsset]);

  // Handle asset selection
  const handleAssetSelect = (asset: Asset) => {
    setSelectedAsset(asset);
    setShowAssetSelector(false);
  };

  // Handle network selection
  const handleNetworkSelect = (network: Network) => {
    setSelectedNetwork(network);
  };

  // Handle withdrawal method selection
  const handleWithdrawalMethod = (method: string) => {
    setWithdrawalMethod(method);
    setRecipientAddress('');
    setNoteToPayee('');

    // Animate slide
    Animated.spring(slideAnimation, {
      toValue: method === 'onchain' ? 0 : 1,
      friction: 8,
      tension: 40,
      useNativeDriver: true,
    }).start();
  };

  // Handle send amount input
  const handleAmountInput = (value: string) => {
    // Allow only numbers and one decimal point
    const cleaned = value.replace(/[^0-9.]/g, '');
    const parts = cleaned.split('.');

    if (parts.length <= 2) {
      if (parts[1] && parts[1].length > 2) {
        // Limit to 2 decimal places
        setSendAmount(parts[0] + '.' + parts[1].substring(0, 2));
      } else {
        setSendAmount(cleaned);
      }
    }
  };

  // Handle percentage quick select
  const handlePercentageSelect = (percentage: number) => {
    if (selectedAsset) {
      const amount = (selectedAsset.balance * percentage) / 100;
      setSendAmount(amount.toFixed(2));
    }
  };

  // Validate form
  const validateForm = () => {
    if (!selectedAsset) {
      Alert.alert('Error', 'Please select an asset');
      return false;
    }

    if (!selectedNetwork) {
      Alert.alert('Error', 'Please select a network');
      return false;
    }

    const amount = parseFloat(sendAmount);
    if (!amount || amount <= 0) {
      Alert.alert('Error', 'Please enter a valid amount');
      return false;
    }

    if (amount > selectedAsset.balance) {
      Alert.alert('Insufficient Balance', `You only have ${selectedAsset.balance} ${selectedAsset.symbol}`);
      return false;
    }

    if (withdrawalMethod === 'onchain' && !recipientAddress) {
      Alert.alert('Error', 'Please enter recipient address');
      return false;
    }

    if (withdrawalMethod === 'internal' && !recipientAddress) {
      Alert.alert('Error', 'Please enter recipient email/phone/ID');
      return false;
    }

    return true;
  };

  // Handle send transaction
  const handleSend = async () => {
    if (!validateForm()) return;

    setIsLoading(true);

    // Animate button press
    Animated.sequence([
      Animated.timing(buttonScale, {
        toValue: 0.95,
        duration: 100,
        useNativeDriver: true,
      }),
      Animated.timing(buttonScale, {
        toValue: 1,
        duration: 100,
        useNativeDriver: true,
      }),
    ]).start();

    // Simulate API call
    setTimeout(() => {
      setIsLoading(false);

      // Show success message
      Alert.alert(
        'Success!',
        `${sendAmount} ${selectedAsset?.symbol} has been sent successfully.\nNetwork Fee: $${selectedNetwork?.fee.toFixed(2)}`,
        [
          {
            text: 'View Transaction',
            onPress: () => console.log('View transaction pressed'),
          },
          {
            text: 'Done',
            onPress: () => router.back(),
          },
        ]
      );

      // Reset form
      setSendAmount('');
      setRecipientAddress('');
      setNoteToPayee('');
      setReceiveAmount('0.00');
    }, 2000);
  };

  // Render network availability info
  const renderNetworkInfo = () => {
    if (!selectedAsset) return null;

    let infoText = '';

    switch (selectedAsset.symbol) {
      case 'MCGP':
        infoText = 'MCGP can only be sent via Sonic Network with $0 fee';
        break;
      case 'USDC':
        infoText = 'USDC can be sent via Sonic Network ($1 fee) or Binance Network ($0.80 fee)';
        break;
      case 'USDT':
        infoText = 'USDT can only be sent via Binance Network with $0.80 fee';
        break;
      default:
        infoText = 'Select an asset to see available networks';
    }

    return (
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          backgroundColor: colors.accentMuted,
          padding: spacing.md,
          borderRadius: radius.md,
          marginBottom: spacing.lg,
        }}
      >
        <Icon name="info" size={14} color={colors.accent} />
        <Text variant="caption" weight="600" color="accent" style={{ marginLeft: spacing.sm, flex: 1 }}>
          {infoText}
        </Text>
      </View>
    );
  };

  // Render numeric keypad
  const renderNumericKeypad = () => (
    <View style={{ marginTop: spacing.sm }}>
      <View style={styles.keypadRow}>
        {['1', '2', '3'].map((num) => (
          <PressableScale
            key={num}
            style={keypadKeyStyle}
            onPress={() => handleAmountInput(sendAmount + num)}
          >
            <Text variant="title2" weight="700">{num}</Text>
          </PressableScale>
        ))}
      </View>
      <View style={styles.keypadRow}>
        {['4', '5', '6'].map((num) => (
          <PressableScale
            key={num}
            style={keypadKeyStyle}
            onPress={() => handleAmountInput(sendAmount + num)}
          >
            <Text variant="title2" weight="700">{num}</Text>
          </PressableScale>
        ))}
      </View>
      <View style={styles.keypadRow}>
        {['7', '8', '9'].map((num) => (
          <PressableScale
            key={num}
            style={keypadKeyStyle}
            onPress={() => handleAmountInput(sendAmount + num)}
          >
            <Text variant="title2" weight="700">{num}</Text>
          </PressableScale>
        ))}
      </View>
      <View style={styles.keypadRow}>
        <PressableScale
          style={keypadKeyStyle}
          onPress={() => handleAmountInput(sendAmount + '.')}
        >
          <Text variant="title2" weight="700">.</Text>
        </PressableScale>
        <PressableScale
          style={keypadKeyStyle}
          onPress={() => handleAmountInput(sendAmount + '0')}
        >
          <Text variant="title2" weight="700">0</Text>
        </PressableScale>
        <PressableScale
          style={keypadKeyStyle}
          onPress={() => setSendAmount(sendAmount.slice(0, -1))}
        >
          <Icon name="close" size={24} color={colors.textPrimary} />
        </PressableScale>
      </View>
    </View>
  );

  // keypad key styling (uses theme tokens + the legacy width math).
  const keypadKeyStyle = {
    width: (Dimensions.get('window').width - 80) / 3,
    height: 60,
    backgroundColor: colors.surfaceAlt,
    borderRadius: radius.lg,
    justifyContent: 'center' as const,
    alignItems: 'center' as const,
  };

  // Render asset icon
  const renderAssetIcon = (symbol: string, size: number = 40) => (
    <View
      style={{
        width: size,
        height: size,
        borderRadius: size / 2,
        backgroundColor: colors.accent,
        justifyContent: 'center',
        alignItems: 'center',
      }}
    >
      <Text weight="800" style={{ fontSize: size / 2, color: colors.onAccent }}>
        {symbol.charAt(0)}
      </Text>
    </View>
  );

  // Asset Selector Modal
  const AssetSelectorModal = () => (
    <View
      style={{
        position: 'absolute',
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        backgroundColor: colors.overlay,
        justifyContent: 'center',
        alignItems: 'center',
        paddingHorizontal: spacing.xl,
      }}
    >
      <Surface
        elevation="lg"
        style={{
          width: '100%',
          maxWidth: 400,
          maxHeight: '70%',
        }}
      >
        <View
          style={{
            flexDirection: 'row',
            justifyContent: 'space-between',
            alignItems: 'center',
            paddingHorizontal: spacing['2xl'],
            paddingVertical: spacing.xl,
            borderBottomWidth: 1,
            borderBottomColor: colors.hairline,
          }}
        >
          <Text variant="title2" weight="800">Select Asset</Text>
          <PressableScale
            onPress={() => setShowAssetSelector(false)}
            hitSlop={8}
          >
            <Icon name="close" size={24} color={colors.textSecondary} />
          </PressableScale>
        </View>

        <ScrollView style={{ padding: spacing.xl }}>
          {assets.map((asset) => {
            const isSelected = selectedAsset?.id === asset.id;
            return (
              <PressableScale
                key={asset.id}
                onPress={() => handleAssetSelect(asset)}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  paddingVertical: spacing.lg,
                  borderBottomWidth: 1,
                  borderBottomColor: colors.hairline,
                  ...(isSelected
                    ? {
                        backgroundColor: colors.surfaceAlt,
                        borderRadius: radius.md,
                        paddingHorizontal: spacing.md,
                      }
                    : null),
                }}
              >
                {renderAssetIcon(asset.symbol, 44)}
                <View style={{ flex: 1, marginLeft: spacing.lg }}>
                  <Text variant="title3" weight="800">{asset.symbol}</Text>
                  <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>{asset.name}</Text>
                  {/* Show network info for each asset */}
                  {asset.symbol === 'MCGP' && (
                    <Text variant="caption" color="accent" style={{ marginTop: spacing.xs, fontStyle: 'italic' }}>Sonic Network only • $0 fee</Text>
                  )}
                  {asset.symbol === 'USDC' && (
                    <Text variant="caption" color="accent" style={{ marginTop: spacing.xs, fontStyle: 'italic' }}>Sonic ($1) or Binance ($0.80)</Text>
                  )}
                  {asset.symbol === 'USDT' && (
                    <Text variant="caption" color="accent" style={{ marginTop: spacing.xs, fontStyle: 'italic' }}>Binance Network only • $0.80 fee</Text>
                  )}
                </View>
                <View style={{ alignItems: 'flex-end', marginRight: spacing.md }}>
                  <Text variant="bodyMed" weight="700">
                    {asset.balance.toFixed(2)} {asset.symbol}
                  </Text>
                  <Text variant="caption" weight="600" color="success" style={{ marginTop: spacing.xs }}>
                    ${asset.usdValue.toLocaleString()}
                  </Text>
                </View>
                {isSelected && (
                  <Icon name="checkCircle" size={24} color={colors.accent} />
                )}
              </PressableScale>
            );
          })}
        </ScrollView>
      </Surface>
    </View>
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        style={{ flex: 1 }}
      >
        <ScrollView style={{ flex: 1 }} showsVerticalScrollIndicator={false}>
          {/* Withdrawal Method Selection */}
          <Card style={{ marginTop: spacing.sm, marginHorizontal: spacing.xl, paddingVertical: spacing['2xl'] }}>
            <Text variant="title2" weight="800" style={{ marginBottom: spacing.lg }}>Select Withdraw Method</Text>

            <View style={{ gap: spacing.md }}>
              {/* Binance Users (Internal Transfer) */}
              <PressableScale
                onPress={() => handleWithdrawalMethod('internal')}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  backgroundColor: withdrawalMethod === 'internal' ? colors.accentMuted : colors.surfaceAlt,
                  borderRadius: radius.lg,
                  padding: spacing.lg,
                  borderWidth: 2,
                  borderColor: withdrawalMethod === 'internal' ? colors.accent : 'transparent',
                }}
              >
                <View style={{ marginRight: spacing.lg }}>
                  <Icon
                    name="user"
                    size={32}
                    color={withdrawalMethod === 'internal' ? colors.accent : colors.textSecondary}
                  />
                </View>
                <View style={{ flex: 1 }}>
                  <Text
                    variant="bodyMed"
                    weight="700"
                    style={{ marginBottom: spacing.xs, color: withdrawalMethod === 'internal' ? colors.accent : colors.textPrimary }}
                  >
                    Send to TSA Connect users
                  </Text>
                  <Text variant="bodySm" color="textSecondary">
                    TSA Connect internal transfer, send via Email/Phone/ID
                  </Text>
                  {withdrawalMethod === 'internal' && (
                    <View
                      style={{
                        flexDirection: 'row',
                        alignItems: 'center',
                        backgroundColor: colors.accent,
                        paddingHorizontal: spacing.sm,
                        paddingVertical: spacing.xs,
                        borderRadius: radius.sm,
                        marginTop: spacing.sm,
                        alignSelf: 'flex-start',
                      }}
                    >
                      <Icon name="bolt" size={12} color={colors.onAccent} />
                      <Text variant="overline" weight="700" style={{ color: colors.onAccent, marginLeft: spacing.xs }}>
                        Win up to 50,000 LINEA when sending at least $0.01
                      </Text>
                    </View>
                  )}
                </View>
                {withdrawalMethod === 'internal' && (
                  <Icon name="checkCircle" size={24} color={colors.accent} />
                )}
              </PressableScale>


            </View>
          </Card>

          {/* Asset Selection */}
          <Card style={{ marginTop: spacing.sm, marginHorizontal: spacing.xl, paddingVertical: spacing['2xl'] }}>
            <Text variant="title2" weight="800" style={{ marginBottom: spacing.lg }}>Select Asset</Text>

            <PressableScale
              onPress={() => setShowAssetSelector(true)}
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                justifyContent: 'space-between',
                backgroundColor: colors.surfaceAlt,
                borderRadius: radius.lg,
                padding: spacing.lg,
                borderWidth: 2,
                borderColor: colors.border,
              }}
            >
              {selectedAsset ? (
                <View style={{ flexDirection: 'row', alignItems: 'center', flex: 1 }}>
                  {renderAssetIcon(selectedAsset.symbol, 44)}
                  <View style={{ marginLeft: spacing.lg, flex: 1 }}>
                    <Text variant="title3" weight="800">{selectedAsset.symbol}</Text>
                    <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>{selectedAsset.name}</Text>
                    <Text variant="caption" weight="600" color="accent" style={{ marginTop: spacing.xs }}>
                      Balance: {selectedAsset.balance.toFixed(2)} {selectedAsset.symbol}
                    </Text>
                    {/* Show network fee info */}
                    {selectedAsset.symbol === 'MCGP' && (
                      <Text variant="caption" color="textSecondary" style={{ marginTop: 2, fontStyle: 'italic' }}>Sonic Network • $0 fee</Text>
                    )}
                    {selectedAsset.symbol === 'USDC' && (
                      <Text variant="caption" color="textSecondary" style={{ marginTop: 2, fontStyle: 'italic' }}>Choose network below</Text>
                    )}
                    {selectedAsset.symbol === 'USDT' && (
                      <Text variant="caption" color="textSecondary" style={{ marginTop: 2, fontStyle: 'italic' }}>Binance Network • $0.80 fee</Text>
                    )}
                  </View>
                </View>
              ) : (
                <View style={{ flexDirection: 'row', alignItems: 'center', flex: 1 }}>
                  <Icon name="wallet" size={28} color={colors.accent} />
                  <Text variant="title3" weight="600" style={{ marginLeft: spacing.md }}>Select Asset</Text>
                </View>
              )}
              <Icon name="chevronDown" size={32} color={colors.accent} />
            </PressableScale>

            {/* Quick Percentage Selector */}
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginTop: spacing.lg }}>
              {[25, 50, 75, 100].map((percentage) => (
                <PressableScale
                  key={percentage}
                  onPress={() => handlePercentageSelect(percentage)}
                  style={{
                    backgroundColor: colors.surfaceAlt,
                    paddingHorizontal: spacing.xl,
                    paddingVertical: spacing.sm,
                    borderRadius: radius.xl,
                    minWidth: 70,
                    alignItems: 'center',
                  }}
                >
                  <Text variant="bodySm" weight="700">{percentage}%</Text>
                </PressableScale>
              ))}
            </View>
          </Card>

          {/* Amount Input */}
          <Card style={{ marginTop: spacing.sm, marginHorizontal: spacing.xl, paddingVertical: spacing['2xl'] }}>
            <Text variant="title2" weight="800" style={{ marginBottom: spacing.lg }}>Amount to Send</Text>

            <View
              style={{
                backgroundColor: colors.surfaceAlt,
                borderRadius: radius.lg,
                padding: spacing.xl,
                marginBottom: spacing.xl,
              }}
            >
              <View
                style={{
                  flexDirection: 'row',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  marginBottom: spacing.md,
                }}
              >
                <Text variant="bodySm" weight="600">Send Amount</Text>
                <Text variant="caption" weight="600" color="accent">
                  Available: {selectedAsset?.balance.toFixed(2) || '0.00'} {selectedAsset?.symbol || ''}
                </Text>
              </View>

              <View style={{ flexDirection: 'row', alignItems: 'center' }}>
                <TextInput
                  style={{
                    flex: 1,
                    fontSize: 36,
                    fontFamily: fonts['800'],
                    color: colors.textPrimary,
                    padding: 0,
                  }}
                  value={sendAmount}
                  onChangeText={handleAmountInput}
                  placeholder="0.00"
                  placeholderTextColor={colors.textMuted}
                  keyboardType="decimal-pad"
                  returnKeyType="done"
                />
                <Text variant="title2" weight="700" color="accent" style={{ marginLeft: spacing.md }}>
                  {selectedAsset?.symbol || 'USD'}
                </Text>
              </View>

              <View style={{ marginTop: spacing.md }}>
                <Text variant="body" weight="600" color="textSecondary">
                  ≈ ${receiveAmount} USD
                </Text>
              </View>
            </View>

            {/* Numeric Keypad */}
            {renderNumericKeypad()}
          </Card>
          {/* Recipient Details */}
          <Animated.View
            style={{
              opacity: slideAnimation.interpolate({
                inputRange: [0, 1],
                outputRange: [1, 0.7],
              }),
            }}
          >
            <Card style={{ marginTop: spacing.sm, marginHorizontal: spacing.xl, paddingVertical: spacing['2xl'] }}>
              <Text variant="title2" weight="800" style={{ marginBottom: spacing.lg }}>
                {withdrawalMethod === 'internal' ? 'Recipient Details' : 'Recipient Address'}
              </Text>

              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  backgroundColor: colors.surfaceAlt,
                  borderRadius: radius.lg,
                  paddingHorizontal: spacing.lg,
                  marginBottom: spacing.xl,
                }}
              >
                {withdrawalMethod === 'internal' ? (
                  <>
                    <TextInput
                      style={{
                        flex: 1,
                        fontSize: type.body.fontSize,
                        fontFamily: fonts['400'],
                        color: colors.textPrimary,
                        paddingVertical: spacing.lg,
                      }}
                      value={recipientAddress}
                      onChangeText={setRecipientAddress}
                      placeholder="Email / Phone / Binance ID"
                      placeholderTextColor={colors.textMuted}
                      keyboardType="default"
                      autoCapitalize="none"
                    />
                    <PressableScale style={{ padding: spacing.sm }}>
                      <Icon name="scan" size={20} color={colors.accent} />
                    </PressableScale>
                  </>
                ) : (
                  <>
                    <TextInput
                      style={{
                        flex: 1,
                        fontSize: type.bodySm.fontSize,
                        color: colors.textPrimary,
                        paddingVertical: spacing.lg,
                        fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
                      }}
                      value={recipientAddress}
                      onChangeText={setRecipientAddress}
                      placeholder="Enter wallet address (0x...)"
                      placeholderTextColor={colors.textMuted}
                      multiline
                      numberOfLines={2}
                      autoCapitalize="none"
                    />
                    <PressableScale style={{ padding: spacing.sm }}>
                      <Icon name="scan" size={20} color={colors.accent} />
                    </PressableScale>
                  </>
                )}
              </View>

              {/* Note to Payee - Only for internal transfers */}
              {withdrawalMethod === 'internal' && (
                <View style={{ marginBottom: spacing.xl }}>
                  <Text variant="bodySm" weight="600" style={{ marginBottom: spacing.sm }}>Note to Payee (Optional)</Text>
                  <TextInput
                    style={{
                      backgroundColor: colors.surfaceAlt,
                      borderRadius: radius.md,
                      padding: spacing.lg,
                      fontSize: type.bodySm.fontSize,
                      fontFamily: fonts['400'],
                      color: colors.textPrimary,
                      minHeight: 80,
                      textAlignVertical: 'top',
                    }}
                    value={noteToPayee}
                    onChangeText={setNoteToPayee}
                    placeholder="Add a note..."
                    placeholderTextColor={colors.textMuted}
                    multiline
                    numberOfLines={2}
                    maxLength={100}
                  />
                  <Text variant="caption" color="textSecondary" align="right" style={{ marginTop: spacing.xs }}>
                    {noteToPayee.length}/100
                  </Text>
                </View>
              )}

              {/* Payee Will Receive - Based on image 3 */}
              <View
                style={{
                  backgroundColor: colors.surfaceAlt,
                  borderRadius: radius.lg,
                  padding: spacing.xl,
                  alignItems: 'center',
                }}
              >
                <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.md }}>Payee will Receive</Text>
                <View style={{ flexDirection: 'row', alignItems: 'baseline', marginBottom: spacing.sm }}>
                  <Text variant="bodyMed" weight="600" color="textSecondary" style={{ marginRight: spacing.sm }}>USD</Text>
                  <Text variant="title1" weight="800">${receiveAmount}</Text>
                </View>
                <Text variant="caption" color="textSecondary" align="center">
                  {withdrawalMethod === 'internal'
                    ? 'Recipient will receive in their preferred currency'
                    : `Amount after ${selectedNetwork?.fee ? `$${selectedNetwork.fee.toFixed(2)} network fee` : 'network fees'}`
                  }
                </Text>
              </View>
            </Card>
          </Animated.View>

          {/* Fee Summary */}
          <Card style={{ marginTop: spacing.sm, marginHorizontal: spacing.xl, paddingVertical: spacing.xl }}>
            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'space-between',
                alignItems: 'center',
                marginBottom: spacing.md,
              }}
            >
              <Text variant="bodySm" color="textSecondary">Network Fee</Text>
              <Text variant="bodySm" weight="600">${feeDetails.fee.toFixed(2)}</Text>
            </View>
            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'space-between',
                alignItems: 'center',
                marginBottom: spacing.md,
              }}
            >
              <Text variant="bodySm" color="textSecondary">Total Amount</Text>
              <Text variant="title3" weight="800" color="accent">${feeDetails.total.toFixed(2)}</Text>
            </View>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                marginTop: spacing.md,
                paddingTop: spacing.md,
                borderTopWidth: 1,
                borderTopColor: colors.hairline,
              }}
            >
              <Icon name="info" size={14} color={colors.accent} />
              <Text variant="caption" color="accent" style={{ marginLeft: spacing.sm, flex: 1 }}>
                {selectedAsset?.symbol === 'MCGP'
                  ? 'MCGP transactions are free on Sonic Network'
                  : selectedAsset?.symbol === 'USDC'
                    ? 'Choose Sonic ($1) or Binance ($0.80) network'
                    : selectedAsset?.symbol === 'USDT'
                      ? 'USDT transactions require $0.80 fee on Binance Network'
                      : 'Network fees are estimated and may vary slightly'
                }
              </Text>
            </View>
          </Card>

          {/* Send Button */}
          <Animated.View
            style={{
              transform: [{ scale: buttonScale }],
              marginHorizontal: spacing.xl,
              marginTop: spacing['2xl'],
              marginBottom: spacing.xl,
            }}
          >
            <Button
              title={`Send Now ${selectedNetwork?.fee ? `($${selectedNetwork.fee.toFixed(2)} fee)` : '(No fee)'}`}
              onPress={handleSend}
              loading={isLoading}
              disabled={!selectedAsset || !selectedNetwork || !sendAmount || !recipientAddress}
              size="lg"
              leftIcon="send"
            />
          </Animated.View>

          {/* Security Note */}
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              paddingBottom: spacing['3xl'],
              marginHorizontal: spacing.xl,
            }}
          >
            <Icon name="shield" size={16} color={colors.accent} />
            <Text variant="caption" color="accent" style={{ marginLeft: spacing.sm }}>
              Your transaction is secured with bank-level encryption
            </Text>
          </View>
        </ScrollView>

        {/* Asset Selector Modal */}
        {showAssetSelector && <AssetSelectorModal />}
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

// Static layout-only styles (no colors). Colors/spacing that depend on the theme
// are inlined via useTheme() at render time.
const styles = StyleSheet.create({
  keypadRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 12,
  },
});

export default TransferScreen;
