
import { router } from 'expo-router';
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  TextInput,
  Animated,
  StyleSheet,
  Dimensions,
  Platform,
  ActivityIndicator,
  KeyboardAvoidingView,
  Alert
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from "react-native-safe-area-context"
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

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#28A745',
};

// Network Selection Component
const NetworkCard: React.FC<{
  network: Network;
  isSelected: boolean;
  onSelect: (network: Network) => void;
}> = ({ network, isSelected, onSelect }) => {
  return (
    <TouchableOpacity
      style={[
        styles.networkCard,
        isSelected && styles.selectedNetworkCard,
        !network.isAvailable && styles.disabledNetworkCard,
      ]}
      onPress={() => network.isAvailable && onSelect(network)}
      disabled={!network.isAvailable}
      activeOpacity={0.7}
    >
      <View style={styles.networkCardContent}>
        <View style={styles.networkIconContainer}>
          <View style={[
            styles.networkIcon,
            isSelected && styles.selectedNetworkIcon
          ]}>
            <Text style={styles.networkIconText}>
              {network.symbol.charAt(0)}
            </Text>
          </View>
        </View>

        <View style={styles.networkInfo}>
          <View style={styles.networkHeader}>
            <Text style={[
              styles.networkName,
              isSelected && styles.selectedNetworkName
            ]}>
              {network.name}
            </Text>
            {isSelected && (
              <View style={styles.selectedNetworkBadge}>
                <Icon name="check-circle" size={14} color="#000000" />
              </View>
            )}
          </View>

          <View style={styles.networkDetails}>
            <View style={styles.networkDetailItem}>
              <Icon name="money" size={12} color="#666666" />
              <Text style={styles.networkDetailText}>
                Fee: {network.fee} USD
              </Text>
            </View>
            <View style={styles.networkDetailItem}>
              <Icon name="schedule" size={12} color="#666666" />
              <Text style={styles.networkDetailText}>
                Time: {network.time}
              </Text>
            </View>
          </View>
        </View>
      </View>

      {!network.isAvailable && (
        <View style={styles.unavailableOverlay}>
          <Text style={styles.unavailableText}>Unavailable</Text>
        </View>
      )}
    </TouchableOpacity>
  );
};


const TransferScreen: React.FC = () => {
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
      <View style={styles.networkInfoContainer}>
        <Icon name="info" size={14} color={GOLD_COLORS.dark} />
        <Text style={styles.networkInfoText}>{infoText}</Text>
      </View>
    );
  };

  // Render numeric keypad
  const renderNumericKeypad = () => (
    <View style={styles.keypadContainer}>
      <View style={styles.keypadRow}>
        {['1', '2', '3'].map((num) => (
          <TouchableOpacity
            key={num}
            style={styles.keypadKey}
            onPress={() => handleAmountInput(sendAmount + num)}
          >
            <Text style={styles.keypadKeyText}>{num}</Text>
          </TouchableOpacity>
        ))}
      </View>
      <View style={styles.keypadRow}>
        {['4', '5', '6'].map((num) => (
          <TouchableOpacity
            key={num}
            style={styles.keypadKey}
            onPress={() => handleAmountInput(sendAmount + num)}
          >
            <Text style={styles.keypadKeyText}>{num}</Text>
          </TouchableOpacity>
        ))}
      </View>
      <View style={styles.keypadRow}>
        {['7', '8', '9'].map((num) => (
          <TouchableOpacity
            key={num}
            style={styles.keypadKey}
            onPress={() => handleAmountInput(sendAmount + num)}
          >
            <Text style={styles.keypadKeyText}>{num}</Text>
          </TouchableOpacity>
        ))}
      </View>
      <View style={styles.keypadRow}>
        <TouchableOpacity
          style={styles.keypadKey}
          onPress={() => handleAmountInput(sendAmount + '.')}
        >
          <Text style={styles.keypadKeyText}>.</Text>
        </TouchableOpacity>
        <TouchableOpacity
          style={styles.keypadKey}
          onPress={() => handleAmountInput(sendAmount + '0')}
        >
          <Text style={styles.keypadKeyText}>0</Text>
        </TouchableOpacity>
        <TouchableOpacity
          style={styles.keypadKey}
          onPress={() => setSendAmount(sendAmount.slice(0, -1))}
        >
          <Icon name="backspace" size={24} color="#000000" />
        </TouchableOpacity>
      </View>
    </View>
  );

  // Render asset icon
  const renderAssetIcon = (symbol: string, size: number = 40) => (
    <View style={[styles.assetIcon, { width: size, height: size, borderRadius: size / 2 }]}>
      <Text style={[styles.assetIconText, { fontSize: size / 2 }]}>
        {symbol.charAt(0)}
      </Text>
    </View>
  );

  // Asset Selector Modal
  const AssetSelectorModal = () => (
    <View style={styles.modalOverlay}>
      <View style={styles.modalContent}>
        <View style={styles.modalHeader}>
          <Text style={styles.modalTitle}>Select Asset</Text>
          <TouchableOpacity
            onPress={() => setShowAssetSelector(false)}
            style={styles.modalCloseButton}
          >
            <Icon name="close" size={24} color="#666" />
          </TouchableOpacity>
        </View>

        <ScrollView style={styles.assetList}>
          {assets.map((asset) => (
            <TouchableOpacity
              key={asset.id}
              style={[
                styles.assetOption,
                selectedAsset?.id === asset.id && styles.selectedAssetOption
              ]}
              onPress={() => handleAssetSelect(asset)}
              activeOpacity={0.7}
            >
              {renderAssetIcon(asset.symbol, 44)}
              <View style={styles.assetOptionInfo}>
                <Text style={styles.assetOptionSymbol}>{asset.symbol}</Text>
                <Text style={styles.assetOptionName}>{asset.name}</Text>
                {/* Show network info for each asset */}
                {asset.symbol === 'MCGP' && (
                  <Text style={styles.assetNetworkInfo}>Sonic Network only • $0 fee</Text>
                )}
                {asset.symbol === 'USDC' && (
                  <Text style={styles.assetNetworkInfo}>Sonic ($1) or Binance ($0.80)</Text>
                )}
                {asset.symbol === 'USDT' && (
                  <Text style={styles.assetNetworkInfo}>Binance Network only • $0.80 fee</Text>
                )}
              </View>
              <View style={styles.assetOptionBalance}>
                <Text style={styles.assetOptionBalanceText}>
                  {asset.balance.toFixed(2)} {asset.symbol}
                </Text>
                <Text style={styles.assetOptionUSDValue}>
                  ${asset.usdValue.toLocaleString()}
                </Text>
              </View>
              {selectedAsset?.id === asset.id && (
                <Icon name="check-circle" size={24} color={GOLD_COLORS.primary} />
              )}
            </TouchableOpacity>
          ))}
        </ScrollView>
      </View>
    </View>
  );

  return (
    <SafeAreaView style={styles.container}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        style={styles.container}
      >
        <ScrollView style={styles.scrollContainer} showsVerticalScrollIndicator={false}>
          {/* Withdrawal Method Selection */}
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Select Withdraw Method</Text>

            <View style={styles.methodContainer}>
              {/* Binance Users (Internal Transfer) */}
              <TouchableOpacity
                style={[
                  styles.methodCard,
                  withdrawalMethod === 'internal' && styles.selectedMethodCard
                ]}
                onPress={() => handleWithdrawalMethod('internal')}
                activeOpacity={0.8}
              >
                <View style={styles.methodIconContainer}>
                  <Icon
                    name="people"
                    size={32}
                    color={withdrawalMethod === 'internal' ? GOLD_COLORS.primary : '#666'}
                  />
                </View>
                <View style={styles.methodInfo}>
                  <Text style={[
                    styles.methodTitle,
                    withdrawalMethod === 'internal' && styles.selectedMethodTitle
                  ]}>
                    Send to TSA Connect users
                  </Text>
                  <Text style={styles.methodDescription}>
                    TSA Connect internal transfer, send via Email/Phone/ID
                  </Text>
                  {withdrawalMethod === 'internal' && (
                    <View style={styles.promoBadge}>
                      <Icon name="bolt" size={12} color="#000000" />
                      <Text style={styles.promoText}>
                        Win up to 50,000 LINEA when sending at least $0.01
                      </Text>
                    </View>
                  )}
                </View>
                {withdrawalMethod === 'internal' && (
                  <Icon name="check-circle" size={24} color={GOLD_COLORS.primary} />
                )}
              </TouchableOpacity>


            </View>
          </View>

          {/* Asset Selection */}
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Select Asset</Text>

            <TouchableOpacity
              style={styles.assetSelector}
              onPress={() => setShowAssetSelector(true)}
              activeOpacity={0.8}
            >
              {selectedAsset ? (
                <View style={styles.selectedAssetDisplay}>
                  {renderAssetIcon(selectedAsset.symbol, 44)}
                  <View style={styles.selectedAssetInfo}>
                    <Text style={styles.selectedAssetSymbol}>{selectedAsset.symbol}</Text>
                    <Text style={styles.selectedAssetName}>{selectedAsset.name}</Text>
                    <Text style={styles.selectedAssetBalance}>
                      Balance: {selectedAsset.balance.toFixed(2)} {selectedAsset.symbol}
                    </Text>
                    {/* Show network fee info */}
                    {selectedAsset.symbol === 'MCGP' && (
                      <Text style={styles.selectedAssetFeeInfo}>Sonic Network • $0 fee</Text>
                    )}
                    {selectedAsset.symbol === 'USDC' && (
                      <Text style={styles.selectedAssetFeeInfo}>Choose network below</Text>
                    )}
                    {selectedAsset.symbol === 'USDT' && (
                      <Text style={styles.selectedAssetFeeInfo}>Binance Network • $0.80 fee</Text>
                    )}
                  </View>
                </View>
              ) : (
                <View style={styles.placeholderDisplay}>
                  <Icon name="account-balance-wallet" size={28} color={GOLD_COLORS.primary} />
                  <Text style={styles.placeholderText}>Select Asset</Text>
                </View>
              )}
              <Icon name="keyboard-arrow-down" size={32} color={GOLD_COLORS.dark} />
            </TouchableOpacity>

            {/* Quick Percentage Selector */}
            <View style={styles.percentageContainer}>
              {[25, 50, 75, 100].map((percentage) => (
                <TouchableOpacity
                  key={percentage}
                  style={styles.percentageButton}
                  onPress={() => handlePercentageSelect(percentage)}
                  activeOpacity={0.7}
                >
                  <Text style={styles.percentageText}>{percentage}%</Text>
                </TouchableOpacity>
              ))}
            </View>
          </View>

          {/* Amount Input */}
          <View style={styles.section}>
            <Text style={styles.sectionTitle}>Amount to Send</Text>

            <View style={styles.amountInputContainer}>
              <View style={styles.amountInputHeader}>
                <Text style={styles.amountLabel}>Send Amount</Text>
                <Text style={styles.balanceText}>
                  Available: {selectedAsset?.balance.toFixed(2) || '0.00'} {selectedAsset?.symbol || ''}
                </Text>
              </View>

              <View style={styles.amountInputRow}>
                <TextInput
                  style={styles.amountInput}
                  value={sendAmount}
                  onChangeText={handleAmountInput}
                  placeholder="0.00"
                  placeholderTextColor="#999"
                  keyboardType="decimal-pad"
                  returnKeyType="done"
                />
                <Text style={styles.amountCurrency}>
                  {selectedAsset?.symbol || 'USD'}
                </Text>
              </View>

              <View style={styles.usdValueContainer}>
                <Text style={styles.usdValueText}>
                  ≈ ${receiveAmount} USD
                </Text>
              </View>
            </View>

            {/* Numeric Keypad */}
            {renderNumericKeypad()}
          </View>
          {/* Recipient Details */}
          <Animated.View
            style={[
              styles.section,
              {
                opacity: slideAnimation.interpolate({
                  inputRange: [0, 1],
                  outputRange: [1, 0.7],
                }),
              }
            ]}
          >
            <Text style={styles.sectionTitle}>
              {withdrawalMethod === 'internal' ? 'Recipient Details' : 'Recipient Address'}
            </Text>

            <View style={styles.recipientContainer}>
              {withdrawalMethod === 'internal' ? (
                <>
                  <TextInput
                    style={styles.recipientInput}
                    value={recipientAddress}
                    onChangeText={setRecipientAddress}
                    placeholder="Email / Phone / Binance ID"
                    placeholderTextColor="#999"
                    keyboardType="default"
                    autoCapitalize="none"
                  />
                  <TouchableOpacity style={styles.scanButton}>
                    <Icon name="qr-code-scanner" size={20} color={GOLD_COLORS.primary} />
                  </TouchableOpacity>
                </>
              ) : (
                <>
                  <TextInput
                    style={[styles.recipientInput, styles.addressInput]}
                    value={recipientAddress}
                    onChangeText={setRecipientAddress}
                    placeholder="Enter wallet address (0x...)"
                    placeholderTextColor="#999"
                    multiline
                    numberOfLines={2}
                    autoCapitalize="none"
                  />
                  <TouchableOpacity style={styles.scanButton}>
                    <Icon name="qr-code-scanner" size={20} color={GOLD_COLORS.primary} />
                  </TouchableOpacity>
                </>
              )}
            </View>

            {/* Note to Payee - Only for internal transfers */}
            {withdrawalMethod === 'internal' && (
              <View style={styles.noteContainer}>
                <Text style={styles.noteLabel}>Note to Payee (Optional)</Text>
                <TextInput
                  style={styles.noteInput}
                  value={noteToPayee}
                  onChangeText={setNoteToPayee}
                  placeholder="Add a note..."
                  placeholderTextColor="#999"
                  multiline
                  numberOfLines={2}
                  maxLength={100}
                />
                <Text style={styles.noteCounter}>
                  {noteToPayee.length}/100
                </Text>
              </View>
            )}

            {/* Payee Will Receive - Based on image 3 */}
            <View style={styles.receiveContainer}>
              <Text style={styles.receiveLabel}>Payee will Receive</Text>
              <View style={styles.receiveAmountContainer}>
                <Text style={styles.receiveCurrency}>USD</Text>
                <Text style={styles.receiveAmount}>${receiveAmount}</Text>
              </View>
              <Text style={styles.receiveNote}>
                {withdrawalMethod === 'internal'
                  ? 'Recipient will receive in their preferred currency'
                  : `Amount after ${selectedNetwork?.fee ? `$${selectedNetwork.fee.toFixed(2)} network fee` : 'network fees'}`
                }
              </Text>
            </View>
          </Animated.View>

          {/* Fee Summary */}
          <View style={styles.feeSection}>
            <View style={styles.feeRow}>
              <Text style={styles.feeLabel}>Network Fee</Text>
              <Text style={styles.feeValue}>${feeDetails.fee.toFixed(2)}</Text>
            </View>
            <View style={styles.feeRow}>
              <Text style={styles.feeLabel}>Total Amount</Text>
              <Text style={styles.totalValue}>${feeDetails.total.toFixed(2)}</Text>
            </View>
            <View style={styles.feeNote}>
              <Icon name="info" size={14} color={GOLD_COLORS.dark} />
              <Text style={styles.feeNoteText}>
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
          </View>

          {/* Send Button */}
          <Animated.View style={{ transform: [{ scale: buttonScale }] }}>
            <TouchableOpacity
              style={[
                styles.sendButton,
                (!selectedAsset || !selectedNetwork || !sendAmount || !recipientAddress) &&
                styles.sendButtonDisabled
              ]}
              onPress={handleSend}
              disabled={isLoading || !selectedAsset || !selectedNetwork || !sendAmount || !recipientAddress}
              activeOpacity={0.8}
            >
              {isLoading ? (
                <ActivityIndicator size="small" color="#000000" />
              ) : (
                <>
                  <Icon name="send" size={20} color="#000000" style={styles.sendIcon} />
                  <Text style={styles.sendButtonText}>
                    Send Now {selectedNetwork?.fee ? `($${selectedNetwork.fee.toFixed(2)} fee)` : '(No fee)'}
                  </Text>
                </>
              )}
            </TouchableOpacity>
          </Animated.View>

          {/* Security Note */}
          <View style={styles.securityNote}>
            <Icon name="security" size={16} color={GOLD_COLORS.dark} />
            <Text style={styles.securityText}>
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
    paddingTop: Platform.OS === 'ios' ? 10 : 20,
    paddingBottom: 20,
    backgroundColor: '#FFFFFF',
  },
  backButton: {
    padding: 8,
  },
  headerTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
  },
  helpButton: {
    padding: 8,
  },
  section: {
    backgroundColor: '#FFFFFF',
    marginTop: 8,
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
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 16,
  },
  sectionSubtitle: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 20,
  },
  networkInfoContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    padding: 12,
    borderRadius: 12,
    marginBottom: 16,
  },
  networkInfoText: {
    fontSize: 13,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
    fontWeight: '600',
  },
  methodContainer: {
    gap: 12,
  },
  methodCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    borderWidth: 2,
    borderColor: 'transparent',
  },
  selectedMethodCard: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  methodIconContainer: {
    marginRight: 16,
  },
  methodInfo: {
    flex: 1,
  },
  methodTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 4,
  },
  selectedMethodTitle: {
    color: GOLD_COLORS.dark,
  },
  methodDescription: {
    fontSize: 13,
    color: '#666666',
  },
  promoBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 8,
    marginTop: 8,
    alignSelf: 'flex-start',
  },
  promoText: {
    fontSize: 11,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 4,
  },
  assetSelector: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    borderWidth: 2,
    borderColor: GOLD_COLORS.muted,
  },
  selectedAssetDisplay: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  assetIcon: {
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
  },
  assetIconText: {
    color: '#000000',
    fontWeight: '900',
  },
  selectedAssetInfo: {
    marginLeft: 16,
    flex: 1,
  },
  selectedAssetSymbol: {
    fontSize: 18,
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
  selectedAssetFeeInfo: {
    fontSize: 12,
    color: '#666666',
    marginTop: 2,
    fontStyle: 'italic',
  },
  placeholderDisplay: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  placeholderText: {
    fontSize: 18,
    fontWeight: '600',
    color: '#000000',
    marginLeft: 12,
  },
  percentageContainer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginTop: 16,
  },
  percentageButton: {
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 20,
    paddingVertical: 10,
    borderRadius: 20,
    minWidth: 70,
    alignItems: 'center',
  },
  percentageText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
  },
  amountInputContainer: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
  },
  amountInputHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  amountLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  balanceText: {
    fontSize: 13,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
  },
  amountInputRow: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  amountInput: {
    flex: 1,
    fontSize: 36,
    fontWeight: '800',
    color: '#000000',
    padding: 0,
  },
  amountCurrency: {
    fontSize: 24,
    fontWeight: '700',
    color: GOLD_COLORS.dark,
    marginLeft: 12,
  },
  usdValueContainer: {
    marginTop: 12,
  },
  usdValueText: {
    fontSize: 16,
    color: '#666666',
    fontWeight: '600',
  },
  keypadContainer: {
    marginTop: 10,
  },
  keypadRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 12,
  },
  keypadKey: {
    width: (Dimensions.get('window').width - 80) / 3,
    height: 60,
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    justifyContent: 'center',
    alignItems: 'center',
  },
  keypadKeyText: {
    fontSize: 24,
    fontWeight: '700',
    color: '#000000',
  },
  networkScroll: {
    marginHorizontal: -20,
    paddingHorizontal: 20,
  },
  networkCard: {
    width: 180,
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 16,
    marginRight: 12,
    borderWidth: 2,
    borderColor: 'transparent',
  },
  selectedNetworkCard: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD_COLORS.primary,
  },
  disabledNetworkCard: {
    opacity: 0.5,
  },
  networkCardContent: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  networkIconContainer: {
    marginRight: 12,
  },
  networkIcon: {
    width: 48,
    height: 48,
    borderRadius: 24,
    backgroundColor: '#E5E5EA',
    justifyContent: 'center',
    alignItems: 'center',
  },
  selectedNetworkIcon: {
    backgroundColor: GOLD_COLORS.primary,
  },
  networkIconText: {
    fontSize: 18,
    fontWeight: '900',
    color: '#000000',
  },
  networkInfo: {
    flex: 1,
  },
  networkHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 8,
  },
  networkName: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    flex: 1,
  },
  selectedNetworkName: {
    color: GOLD_COLORS.dark,
  },
  selectedNetworkBadge: {
    marginLeft: 4,
  },
  networkDetails: {
    gap: 6,
  },
  networkDetailItem: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  networkDetailText: {
    fontSize: 12,
    color: '#666666',
    marginLeft: 4,
  },
  unavailableOverlay: {
    position: 'absolute',
    top: 8,
    right: 8,
    backgroundColor: GOLD_COLORS.error,
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 8,
  },
  unavailableText: {
    fontSize: 10,
    fontWeight: '700',
    color: '#FFFFFF',
  },
  noNetworksContainer: {
    alignItems: 'center',
    justifyContent: 'center',
    padding: 30,
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
  },
  noNetworksText: {
    fontSize: 14,
    color: GOLD_COLORS.error,
    fontWeight: '600',
    marginTop: 12,
    textAlign: 'center',
  },
  recipientContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    paddingHorizontal: 16,
    marginBottom: 20,
  },
  recipientInput: {
    flex: 1,
    fontSize: 16,
    color: '#000000',
    paddingVertical: 16,
  },
  addressInput: {
    fontSize: 14,
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
  },
  scanButton: {
    padding: 8,
  },
  noteContainer: {
    marginBottom: 20,
  },
  noteLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 8,
  },
  noteInput: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    fontSize: 14,
    color: '#000000',
    minHeight: 80,
    textAlignVertical: 'top',
  },
  noteCounter: {
    fontSize: 12,
    color: '#666666',
    textAlign: 'right',
    marginTop: 4,
  },
  receiveContainer: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 20,
    alignItems: 'center',
  },
  receiveLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#666666',
    marginBottom: 12,
  },
  receiveAmountContainer: {
    flexDirection: 'row',
    alignItems: 'baseline',
    marginBottom: 8,
  },
  receiveCurrency: {
    fontSize: 16,
    fontWeight: '600',
    color: '#666666',
    marginRight: 8,
  },
  receiveAmount: {
    fontSize: 32,
    fontWeight: '900',
    color: '#000000',
  },
  receiveNote: {
    fontSize: 13,
    color: '#666666',
    textAlign: 'center',
  },
  feeSection: {
    backgroundColor: '#FFFFFF',
    marginTop: 8,
    paddingHorizontal: 20,
    paddingVertical: 20,
    borderRadius: 16,
    marginHorizontal: 20,
  },
  feeRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  feeLabel: {
    fontSize: 15,
    color: '#666666',
  },
  feeValue: {
    fontSize: 15,
    fontWeight: '600',
    color: '#000000',
  },
  totalValue: {
    fontSize: 18,
    fontWeight: '800',
    color: GOLD_COLORS.dark,
  },
  feeNote: {
    flexDirection: 'row',
    alignItems: 'center',
    marginTop: 12,
    paddingTop: 12,
    borderTopWidth: 1,
    borderTopColor: '#F2F2F7',
  },
  feeNoteText: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
    flex: 1,
  },
  sendButton: {
    backgroundColor: GOLD_COLORS.primary,
    marginHorizontal: 20,
    marginTop: 24,
    marginBottom: 20,
    borderRadius: 16,
    paddingVertical: 20,
    alignItems: 'center',
    flexDirection: 'row',
    justifyContent: 'center',
    shadowColor: GOLD_COLORS.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
  sendButtonDisabled: {
    opacity: 0.5,
  },
  sendIcon: {
    marginRight: 12,
  },
  sendButtonText: {
    fontSize: 18,
    fontWeight: '900',
    color: '#000000',
  },
  securityNote: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingBottom: 30,
    marginHorizontal: 20,
  },
  securityText: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    marginLeft: 8,
  },
  // Modal Styles
  modalOverlay: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
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
    maxHeight: '70%',
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
    paddingHorizontal: 24,
    paddingVertical: 20,
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  modalTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
  },
  modalCloseButton: {
    padding: 4,
  },
  assetList: {
    padding: 20,
  },
  assetOption: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 16,
    borderBottomWidth: 1,
    borderBottomColor: '#F2F2F7',
  },
  selectedAssetOption: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    paddingHorizontal: 12,
  },
  assetOptionInfo: {
    flex: 1,
    marginLeft: 16,
  },
  assetOptionSymbol: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  assetOptionName: {
    fontSize: 14,
    color: '#666666',
    marginTop: 2,
  },
  assetNetworkInfo: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    marginTop: 4,
    fontStyle: 'italic',
  },
  assetOptionBalance: {
    alignItems: 'flex-end',
    marginRight: 12,
  },
  assetOptionBalanceText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
  },
  assetOptionUSDValue: {
    fontSize: 13,
    color: '#34C759',
    marginTop: 4,
    fontWeight: '600',
  },
});

export default TransferScreen;