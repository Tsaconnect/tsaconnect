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
  ActivityIndicator,
  Clipboard,
  Alert,
  Modal,
  TextInput,
  KeyboardAvoidingView,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from "react-native-safe-area-context"
// Types and interfaces
interface Asset {
  id: string;
  symbol: string;
  name: string;
  network: string;
  networkType: string;
  walletAddress: string;
  minFundingAmount: number;
  iconColor: string;
  depositAvailable: boolean;
}

interface AssetCardProps {
  asset: Asset;
  isExpanded: boolean;
  isSelected: boolean;
  onCardPress: (assetId: string) => void;
  onSelectPress: (asset: Asset) => void;
  onCopyAddress: (asset: Asset) => void;
}

interface VerificationModalProps {
  visible: boolean;
  selectedAsset: Asset | null;
  onClose: () => void;
  onVerify: (assetId: string, amount: string, sourceWallet: string) => void;
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
  info: '#5AC8FA',
};

// Verification Modal Component
const VerificationModal: React.FC<VerificationModalProps> = ({
  visible,
  selectedAsset,
  onClose,
  onVerify,
}) => {
  const [amount, setAmount] = useState('');
  const [sourceWallet, setSourceWallet] = useState('');
  const [selectedAssetForVerification, setSelectedAssetForVerification] = useState<string | null>(
    selectedAsset?.id || null
  );
  const [isLoading, setIsLoading] = useState(false);
  const modalAnimation = useRef(new Animated.Value(0)).current;

  // Asset options for verification (only available assets)
  const verificationAssets = [
    {
      id: '1',
      symbol: 'USDT',
      name: 'Tether',
      iconColor: '#26A17B',
    },
    {
      id: '2',
      symbol: 'USDC',
      name: 'USD Coin',
      iconColor: '#2775CA',
    },
  ];

  useEffect(() => {
    if (visible) {
      // Reset form when modal opens
      setAmount('');
      setSourceWallet('');
      setIsLoading(false);

      // Set default asset if selectedAsset is available
      if (selectedAsset && (selectedAsset.symbol === 'USDT' || selectedAsset.symbol === 'USDC')) {
        setSelectedAssetForVerification(selectedAsset.id);
      } else {
        // Default to USDT if no valid asset selected
        setSelectedAssetForVerification('1');
      }

      // Animate modal entrance
      Animated.spring(modalAnimation, {
        toValue: 1,
        tension: 50,
        friction: 7,
        useNativeDriver: true,
      }).start();
    } else {
      modalAnimation.setValue(0);
    }
  }, [visible, selectedAsset]);

  const handleAssetSelect = (assetId: string) => {
    setSelectedAssetForVerification(assetId);
  };

  const handleVerify = async () => {
    if (!selectedAssetForVerification || !amount || !sourceWallet) {
      Alert.alert('Missing Information', 'Please fill in all fields');
      return;
    }

    const numericAmount = parseFloat(amount);
    if (isNaN(numericAmount) || numericAmount <= 0) {
      Alert.alert('Invalid Amount', 'Please enter a valid amount');
      return;
    }

    // Get selected asset details
    const selectedVerificationAsset = verificationAssets.find(
      asset => asset.id === selectedAssetForVerification
    );

    if (!selectedVerificationAsset) {
      Alert.alert('Invalid Asset', 'Please select a valid asset');
      return;
    }

    // For demo purposes, set minimum amount
    const minAmount = selectedVerificationAsset.symbol === 'USDT' ? 10 : 10;
    if (numericAmount < minAmount) {
      Alert.alert(
        'Minimum Amount Required',
        `Minimum deposit amount is ${minAmount} ${selectedVerificationAsset.symbol}`
      );
      return;
    }

    setIsLoading(true);

    try {
      // In a real app, this would call your smart contract
      // For demo, simulate verification delay
      await new Promise(resolve => setTimeout(resolve, 1500));

      // Simulate smart contract verification
      const verificationResult = await simulateSmartContractVerification(
        selectedAssetForVerification,
        amount,
        sourceWallet
      );

      if (verificationResult.success) {
        onVerify(selectedAssetForVerification, amount, sourceWallet);
      } else {
        Alert.alert('Verification Failed', verificationResult.message);
      }
    } catch (error) {
      Alert.alert('Verification Error', 'Failed to verify transaction. Please try again.');
    } finally {
      setIsLoading(false);
    }
  };

  const simulateSmartContractVerification = async (
    assetId: string,
    amount: string,
    sourceWallet: string
  ) => {
    // Simulate smart contract verification logic
    // In production, this would interact with your actual smart contract

    // Basic validation
    if (!sourceWallet.startsWith('0x') || sourceWallet.length !== 42) {
      return {
        success: false,
        message: 'Invalid source wallet address format',
      };
    }

    if (parseFloat(amount) <= 0) {
      return {
        success: false,
        message: 'Amount must be greater than 0',
      };
    }

    // Simulate network delay and successful verification
    return {
      success: true,
      message: 'Transaction verified successfully by smart contract',
      transactionHash: '0x' + Math.random().toString(16).substring(2, 42),
      gasUsed: '21000',
      status: 'success',
    };
  };

  const translateY = modalAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: [300, 0],
  });

  const opacity = modalAnimation.interpolate({
    inputRange: [0, 1],
    outputRange: [0, 1],
  });

  const getSelectedAssetDetails = () => {
    return verificationAssets.find(asset => asset.id === selectedAssetForVerification);
  };

  const selectedAssetDetails = getSelectedAssetDetails();

  return (
    <Modal
      visible={visible}
      transparent
      animationType="none"
      onRequestClose={onClose}
    >
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        style={styles.modalOverlay}
      >
        <Animated.View style={[styles.modalOverlay, { opacity }]}>
          <TouchableOpacity
            style={styles.modalBackdrop}
            activeOpacity={1}
            onPress={onClose}
          />

          <Animated.View
            style={[
              styles.modalContainer,
              {
                transform: [{ translateY }],
                opacity: modalAnimation,
              },
            ]}
          >
            <SafeAreaView style={styles.modalContent}>
              {/* Modal Header */}
              <View style={styles.modalHeader}>
                <View style={styles.modalHeaderIcon}>
                  <Icon name="verified" size={28} color={GOLD_COLORS.primary} />
                </View>
                <Text style={styles.modalTitle}>Smart Contract Verification</Text>
                <Text style={styles.modalSubtitle}>
                  Enter details for smart contract verification
                </Text>
                <TouchableOpacity
                  style={styles.closeButton}
                  onPress={onClose}
                  hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}
                >
                  <Icon name="close" size={24} color="#666666" />
                </TouchableOpacity>
              </View>

              {/* Verification Form */}
              <ScrollView
                style={styles.verificationScrollView}
                showsVerticalScrollIndicator={false}
              >
                {/* Step 1: Select Asset */}
                <View style={styles.formSection}>
                  <View style={styles.sectionHeader}>
                    <View style={styles.stepIndicator}>
                      <Text style={styles.stepNumber}>1</Text>
                    </View>
                    <Text style={styles.sectionTitle}>Select Asset</Text>
                  </View>
                  <Text style={styles.sectionDescription}>
                    Choose the asset you want to deposit
                  </Text>

                  <View style={styles.assetSelectionGrid}>
                    {verificationAssets.map(asset => (
                      <TouchableOpacity
                        key={asset.id}
                        style={[
                          styles.assetOption,
                          selectedAssetForVerification === asset.id && styles.assetOptionSelected,
                        ]}
                        onPress={() => handleAssetSelect(asset.id)}
                        activeOpacity={0.7}
                      >
                        <View style={[
                          styles.assetOptionIcon,
                          { backgroundColor: asset.iconColor + '20' }
                        ]}>
                          <View style={[
                            styles.assetOptionIconInner,
                            { backgroundColor: asset.iconColor }
                          ]}>
                            <Text style={styles.assetOptionIconText}>
                              {asset.symbol.charAt(0)}
                            </Text>
                          </View>
                        </View>
                        <Text style={[
                          styles.assetOptionSymbol,
                          selectedAssetForVerification === asset.id && styles.assetOptionSymbolSelected,
                        ]}>
                          {asset.symbol}
                        </Text>
                        <Text style={styles.assetOptionName}>
                          {asset.name}
                        </Text>

                        {selectedAssetForVerification === asset.id && (
                          <View style={styles.selectedIndicator}>
                            <Icon name="check-circle" size={20} color={GOLD_COLORS.primary} />
                          </View>
                        )}
                      </TouchableOpacity>
                    ))}
                  </View>
                </View>

                {/* Step 2: Enter Amount */}
                <View style={styles.formSection}>
                  <View style={styles.sectionHeader}>
                    <View style={styles.stepIndicator}>
                      <Text style={styles.stepNumber}>2</Text>
                    </View>
                    <Text style={styles.sectionTitle}>Enter Amount</Text>
                  </View>
                  <Text style={styles.sectionDescription}>
                    Amount to deposit (Minimum: {selectedAssetDetails?.symbol === 'USDT' ? '10' : '10'} {selectedAssetDetails?.symbol})
                  </Text>

                  <View style={styles.amountInputContainer}>
                    <TextInput
                      style={styles.amountInput}
                      value={amount}
                      onChangeText={setAmount}
                      placeholder={`Enter amount in ${selectedAssetDetails?.symbol || 'USDT'}`}
                      placeholderTextColor="#999999"
                      keyboardType="decimal-pad"
                      autoCapitalize="none"
                      autoCorrect={false}
                    />
                    {selectedAssetDetails && (
                      <View style={styles.amountCurrency}>
                        <View style={[
                          styles.currencyIcon,
                          { backgroundColor: selectedAssetDetails.iconColor }
                        ]}>
                          <Text style={styles.currencyIconText}>
                            {selectedAssetDetails.symbol.charAt(0)}
                          </Text>
                        </View>
                        <Text style={styles.currencyText}>
                          {selectedAssetDetails.symbol}
                        </Text>
                      </View>
                    )}
                  </View>
                </View>

                {/* Step 3: Enter Source Wallet */}
                <View style={styles.formSection}>
                  <View style={styles.sectionHeader}>
                    <View style={styles.stepIndicator}>
                      <Text style={styles.stepNumber}>3</Text>
                    </View>
                    <Text style={styles.sectionTitle}>Source Wallet</Text>
                  </View>
                  <Text style={styles.sectionDescription}>
                    Your external wallet address where funds will be sent from
                  </Text>

                  <View style={styles.walletInputContainer}>
                    <TextInput
                      style={styles.walletInput}
                      value={sourceWallet}
                      onChangeText={setSourceWallet}
                      placeholder="0x..."
                      placeholderTextColor="#999999"
                      autoCapitalize="none"
                      autoCorrect={false}
                      multiline
                      numberOfLines={2}
                      textAlignVertical="top"
                    />
                    <TouchableOpacity
                      style={styles.pasteButton}
                      onPress={async () => {
                        // Read from clipboard
                        const clipboardContent = await Clipboard.getString();
                        if (clipboardContent) {
                          setSourceWallet(clipboardContent);
                        }
                      }}
                    >
                      <Icon name="content-paste" size={16} color={GOLD_COLORS.primary} />
                      <Text style={styles.pasteButtonText}>Paste</Text>
                    </TouchableOpacity>
                  </View>

                  {/* Wallet Preview */}
                  {sourceWallet && (
                    <View style={styles.walletPreview}>
                      <Text style={styles.walletPreviewLabel}>Wallet Preview:</Text>
                      <Text style={styles.walletPreviewAddress} numberOfLines={1}>
                        {sourceWallet}
                      </Text>
                    </View>
                  )}
                </View>

                {/* Smart Contract Info */}
                <View style={styles.smartContractInfo}>
                  <View style={styles.contractHeader}>
                    <Icon name="code" size={20} color={GOLD_COLORS.primary} />
                    <Text style={styles.contractTitle}>Smart Contract Verification</Text>
                  </View>
                  <View style={styles.contractInfoItem}>
                    <Icon name="check-circle" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.contractInfoText}>
                      Validates transaction parameters
                    </Text>
                  </View>
                  <View style={styles.contractInfoItem}>
                    <Icon name="check-circle" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.contractInfoText}>
                      Ensures sufficient balance
                    </Text>
                  </View>
                  <View style={styles.contractInfoItem}>
                    <Icon name="check-circle" size={14} color={GOLD_COLORS.success} />
                    <Text style={styles.contractInfoText}>
                      Verifies wallet compatibility
                    </Text>
                  </View>
                </View>
              </ScrollView>

              {/* Action Buttons */}
              <View style={styles.actionButtons}>
                <TouchableOpacity
                  style={styles.cancelButton}
                  onPress={onClose}
                  disabled={isLoading}
                >
                  <Icon name="close" size={20} color="#666666" />
                  <Text style={styles.cancelButtonText}>Cancel</Text>
                </TouchableOpacity>

                <TouchableOpacity
                  style={[
                    styles.verifyButton,
                    (!selectedAssetForVerification || !amount || !sourceWallet || isLoading) &&
                    styles.verifyButtonDisabled,
                  ]}
                  onPress={handleVerify}
                  disabled={!selectedAssetForVerification || !amount || !sourceWallet || isLoading}
                >
                  {isLoading ? (
                    <ActivityIndicator size="small" color="#000000" />
                  ) : (
                    <>
                      <Icon name="smart-button" size={20} color="#000000" />
                      <Text style={styles.verifyButtonText}>Verify with Smart Contract</Text>
                    </>
                  )}
                </TouchableOpacity>
              </View>
            </SafeAreaView>
          </Animated.View>
        </Animated.View>
      </KeyboardAvoidingView>
    </Modal>
  );
};

// AssetCard Component (updated to include onCopyAddress prop)
const AssetCard: React.FC<AssetCardProps> = ({
  asset,
  isExpanded,
  isSelected,
  onCardPress,
  onSelectPress,
  onCopyAddress,
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
    outputRange: [80, asset.depositAvailable ? 220 : 140],
  });

  const handleCardPress = () => {
    if (!asset.depositAvailable) {
      Alert.alert(
        'Deposit Not Available',
        `Deposit for ${asset.symbol} is currently unavailable. Please select another asset.`,
        [{ text: 'OK' }]
      );
      return;
    }
    onCardPress(asset.id);
  };

  const handleSelectPress = (e: any) => {
    e.stopPropagation();
    if (!asset.depositAvailable) {
      Alert.alert(
        'Deposit Not Available',
        `Deposit for ${asset.symbol} is currently unavailable. Please select another asset.`,
        [{ text: 'OK' }]
      );
      return;
    }
    onSelectPress(asset);
  };

  const handleCopyAddress = (e: any) => {
    e.stopPropagation();
    if (!asset.depositAvailable) {
      Alert.alert(
        'Deposit Not Available',
        `Deposit for ${asset.symbol} is currently unavailable.`,
        [{ text: 'OK' }]
      );
      return;
    }
    onCopyAddress(asset);
  };

  return (
    <TouchableOpacity
      onPress={handleCardPress}
      activeOpacity={asset.depositAvailable ? 0.7 : 1}
      style={[
        styles.assetCardContainer,
        isSelected && styles.selectedAssetCardContainer,
        !asset.depositAvailable && styles.disabledAssetCardContainer,
      ]}
      disabled={!asset.depositAvailable && !isExpanded}
    >
      <Animated.View style={[styles.assetCard, { height: heightInterpolation }]}>
        <View style={styles.assetHeader}>
          <View style={styles.assetInfo}>
            <View style={[
              styles.assetIcon,
              { backgroundColor: asset.iconColor },
              !asset.depositAvailable && styles.disabledAssetIcon
            ]}>
              <Text style={[
                styles.assetIconText,
                !asset.depositAvailable && styles.disabledAssetIconText
              ]}>
                {asset.symbol.charAt(0)}
              </Text>
            </View>
            <View style={styles.assetTextInfo}>
              <View style={styles.assetTitleRow}>
                <Text style={[
                  styles.assetSymbol,
                  isSelected && styles.selectedAssetSymbol,
                  !asset.depositAvailable && styles.disabledAssetSymbol
                ]}>
                  {asset.symbol}
                </Text>
                {!asset.depositAvailable && (
                  <View style={styles.unavailableBadge}>
                    <Icon name="block" size={10} color="#FFFFFF" />
                    <Text style={styles.unavailableBadgeText}>UNAVAILABLE</Text>
                  </View>
                )}
                {isSelected && asset.depositAvailable && (
                  <View style={styles.selectedBadge}>
                    <Icon name="check-circle" size={14} color="#000000" />
                    <Text style={styles.selectedBadgeText}>Selected</Text>
                  </View>
                )}
              </View>
              <Text style={[
                styles.assetName,
                !asset.depositAvailable && styles.disabledAssetName
              ]}>
                {asset.name}
              </Text>
              <Text style={[
                styles.assetNetwork,
                !asset.depositAvailable && styles.disabledAssetNetwork
              ]}>
                {asset.network}
              </Text>
            </View>
          </View>

          <TouchableOpacity
            onPress={handleSelectPress}
            style={styles.radioButtonContainer}
            activeOpacity={asset.depositAvailable ? 0.7 : 1}
            disabled={!asset.depositAvailable}
          >
            {!asset.depositAvailable ? (
              <View style={styles.radioButtonDisabled}>
                <Icon name="block" size={24} color="#CCCCCC" />
              </View>
            ) : isSelected ? (
              <View style={styles.radioButtonSelected}>
                <Icon name="radio-button-checked" size={24} color={GOLD_COLORS.primary} />
              </View>
            ) : (
              <View style={styles.radioButtonUnselected}>
                <Icon name="radio-button-unchecked" size={24} color="#CCCCCC" />
              </View>
            )}
          </TouchableOpacity>
        </View>

        {isExpanded && (
          <View style={styles.assetDetails}>
            <View style={styles.detailSection}>
              <Text style={styles.detailSectionTitle}>Funding Information</Text>

              {asset.depositAvailable ? (
                <>
                  <View style={styles.infoCard}>
                    <View style={styles.infoRow}>
                      <Icon name="network-check" size={18} color={GOLD_COLORS.dark} />
                      <View style={styles.infoContent}>
                        <Text style={styles.infoLabel}>Network Type</Text>
                        <Text style={styles.infoValue}>{asset.networkType}</Text>
                      </View>
                    </View>

                    <View style={styles.infoRow}>
                      <Icon name="payments" size={18} color={GOLD_COLORS.dark} />
                      <View style={styles.infoContent}>
                        <Text style={styles.infoLabel}>Minimum Deposit</Text>
                        <Text style={styles.infoValue}>{asset.minFundingAmount} {asset.symbol}</Text>
                      </View>
                    </View>
                  </View>

                  <View style={styles.walletAddressSection}>
                    <Text style={styles.walletAddressLabel}>Deposit Address</Text>
                    <View style={styles.walletAddressContainer}>
                      <Text style={styles.walletAddressText} numberOfLines={1}>
                        {asset.walletAddress}
                      </Text>
                      <TouchableOpacity
                        style={styles.copyButton}
                        onPress={handleCopyAddress}
                      >
                        <Icon name="content-copy" size={20} color="#FFFFFF" />
                      </TouchableOpacity>
                    </View>
                    <Text style={styles.walletNote}>
                      Send only {asset.symbol} on {asset.network} to this address
                    </Text>
                  </View>

                  {!isSelected && (
                    <TouchableOpacity
                      onPress={handleSelectPress}
                      style={styles.selectButton}
                      activeOpacity={0.8}
                    >
                      <Icon name="check-circle" size={20} color="#000000" style={styles.selectIcon} />
                      <Text style={styles.selectButtonText}>Select {asset.symbol} for Funding</Text>
                    </TouchableOpacity>
                  )}

                  {isSelected && (
                    <View style={styles.alreadySelected}>
                      <Icon name="verified" size={24} color={GOLD_COLORS.primary} />
                      <Text style={styles.alreadySelectedText}>Selected for Funding</Text>
                    </View>
                  )}
                </>
              ) : (
                <View style={styles.depositUnavailableContainer}>
                  <View style={styles.unavailableIconContainer}>
                    <Icon name="block" size={48} color="#CCCCCC" />
                  </View>
                  <Text style={styles.depositUnavailableTitle}>
                    Deposit Not Available
                  </Text>
                  <Text style={styles.depositUnavailableText}>
                    {asset.symbol} deposits are currently unavailable.
                  </Text>
                </View>
              )}
            </View>
          </View>
        )}
      </Animated.View>
    </TouchableOpacity>
  );
};

// Main FundScreen Component
const FundScreen: React.FC = () => {
  const fundingAssets: Asset[] = [
    {
      id: '1',
      symbol: 'USDT',
      name: 'Tether',
      network: 'Binance Smart Chain',
      networkType: 'BEP20',
      walletAddress: '0x8fB7952eA9fC9eE5c0B8C8c4d7423F1234567890',
      minFundingAmount: 10,
      iconColor: '#26A17B',
      depositAvailable: true,
    },
    {
      id: '2',
      symbol: 'USDC',
      name: 'USD Coin',
      network: 'Sonic Network',
      networkType: 'SONIC',
      walletAddress: '0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed',
      minFundingAmount: 10,
      iconColor: '#2775CA',
      depositAvailable: true,
    },
    {
      id: '3',
      symbol: 'MCGP',
      name: 'MCGP Chain',
      network: 'Sonic Network',
      networkType: 'SONIC',
      walletAddress: '0x3f5CE5FBFe3E9af3971dD833D26bA9b5C936f0bE',
      minFundingAmount: 5,
      iconColor: '#FFD700',
      depositAvailable: false,
    },
  ];

  const [selectedAsset, setSelectedAsset] = useState<Asset | null>(null);
  const [expandedAssetId, setExpandedAssetId] = useState<string | null>(null);
  const [showVerificationModal, setShowVerificationModal] = useState(false);
  const selectionAnimation = useRef(new Animated.Value(1)).current;

  useEffect(() => {
    if (fundingAssets.length > 0 && !selectedAsset) {
      const availableAsset = fundingAssets.find(asset => asset.depositAvailable);
      if (availableAsset) {
        handleAssetSelect(availableAsset, false);
      }
    }
  }, []);

  const handleAssetSelect = (asset: Asset, shouldCollapseList: boolean = false) => {
    if (!asset.depositAvailable) {
      Alert.alert(
        'Deposit Not Available',
        `Deposit for ${asset.symbol} is currently unavailable. Please select another asset.`,
        [{ text: 'OK' }]
      );
      return;
    }

    selectionAnimation.setValue(0);
    Animated.spring(selectionAnimation, {
      toValue: 1,
      friction: 3,
      tension: 40,
      useNativeDriver: true,
    }).start();

    setSelectedAsset(asset);
    setExpandedAssetId(asset.id);
  };

  const handleAssetCardPress = (assetId: string) => {
    const asset = fundingAssets.find(a => a.id === assetId);
    if (asset && !asset.depositAvailable) {
      Alert.alert(
        'Deposit Not Available',
        `Deposit for ${asset.symbol} is currently unavailable. Please select another asset.`,
        [{ text: 'OK' }]
      );
      return;
    }

    if (expandedAssetId === assetId) {
      setExpandedAssetId(null);
    } else {
      setExpandedAssetId(assetId);
    }
  };

  const handleCopyAddress = (asset: Asset) => {
    Clipboard.setString(asset.walletAddress);

    // Show the verification modal after copying
    setTimeout(() => {
      setShowVerificationModal(true);
    }, 300);

    Alert.alert(
      'Address Copied!',
      `${asset.symbol} wallet address copied to clipboard.\n\nPlease complete verification to proceed.`,
      [{ text: 'OK' }]
    );
  };

  const handleVerification = (assetId: string, amount: string, sourceWallet: string) => {
    setShowVerificationModal(false);

    // Find the asset
    const asset = fundingAssets.find(a => a.id === assetId);

    Alert.alert(
      '✅ Smart Contract Verification Complete!',
      `Transaction Details:
      
Asset: ${asset?.symbol}
Amount: ${amount} ${asset?.symbol}
Source: ${sourceWallet.substring(0, 12)}...${sourceWallet.substring(38)}
      
✅ Smart contract verification successful
✅ Transaction parameters validated
✅ Ready for execution
      
You may now send funds to the copied address.`,
      [
        {
          text: 'View Transaction',
          style: 'default',
          onPress: () => {
            console.log('Navigate to transaction details');
          },
        },
        {
          text: 'OK',
          style: 'cancel',
          onPress: () => {
            // Optionally reset selection
            // setSelectedAsset(null);
            // setExpandedAssetId(null);
          }
        },
      ]
    );
  };

  const handleCloseVerificationModal = () => {
    setShowVerificationModal(false);
  };

  const selectionScale = selectionAnimation.interpolate({
    inputRange: [0, 0.5, 1],
    outputRange: [0.95, 1.05, 1],
  });

  const renderAssetIcon = (symbol: string, color: string, size: number = 40) => (
    <View style={[styles.selectorIcon, {
      width: size,
      height: size,
      borderRadius: size / 2,
      backgroundColor: color
    }]}>
      <Text style={[styles.selectorIconText, { fontSize: size / 2 }]}>
        {symbol.charAt(0)}
      </Text>
    </View>
  );

  return (
    <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Fund Wallet</Text>
        <View style={styles.headerRight} />
      </View>

      <View style={styles.content}>
        <View style={styles.instructionsCard}>
          <Icon name="info" size={24} color={GOLD_COLORS.primary} />
          <View style={styles.instructionsContent}>
            <Text style={styles.instructionsTitle}>Smart Contract Fund Verification</Text>
            <Text style={styles.instructionsText}>
              1. Select an asset below{'\n'}
              2. Copy the deposit address{'\n'}
              3. Complete smart contract verification{'\n'}
              4. Send funds from your external wallet
            </Text>
          </View>
        </View>

        {selectedAsset && (
          <Animated.View
            style={[
              styles.selectedAssetCard,
              { transform: [{ scale: selectionScale }] }
            ]}
          >
            <View style={styles.selectedAssetHeader}>
              <Text style={styles.selectedAssetTitle}>Selected for Funding</Text>
              <View style={styles.selectedAssetBadge}>
                <Text style={styles.selectedAssetBadgeText}>ACTIVE</Text>
              </View>
            </View>

            <View style={styles.selectedAssetContent}>
              <View style={styles.selectedAssetInfo}>
                {renderAssetIcon(selectedAsset.symbol, selectedAsset.iconColor, 50)}
                <View style={styles.selectedAssetDetails}>
                  <Text style={styles.selectedAssetSymbol}>{selectedAsset.symbol}</Text>
                  <Text style={styles.selectedAssetName}>{selectedAsset.name}</Text>
                  <Text style={styles.selectedAssetNetwork}>{selectedAsset.network}</Text>
                </View>
              </View>

              <TouchableOpacity
                style={styles.copyMainButton}
                onPress={() => handleCopyAddress(selectedAsset)}
              >
                <Icon name="content-copy" size={20} color="#000000" style={styles.copyIcon} />
                <Text style={styles.copyMainButtonText}>Copy & Verify</Text>
              </TouchableOpacity>
            </View>
          </Animated.View>
        )}

        <View style={styles.section}>
          <View style={styles.sectionHeader}>
            <Text style={styles.sectionTitle}>Select Funding Asset</Text>
            <Text style={styles.sectionSubtitle}>
              Choose an asset and complete smart contract verification
            </Text>
          </View>

          <View style={styles.assetList}>
            {fundingAssets.map(asset => (
              <AssetCard
                key={asset.id}
                asset={asset}
                isExpanded={expandedAssetId === asset.id}
                isSelected={selectedAsset?.id === asset.id}
                onCardPress={handleAssetCardPress}
                onSelectPress={handleAssetSelect}
                onCopyAddress={handleCopyAddress}
              />
            ))}
          </View>
        </View>

        <View style={styles.notesCard}>
          <View style={styles.notesHeader}>
            <Icon name="verified" size={20} color={GOLD_COLORS.primary} />
            <Text style={styles.notesTitle}>Smart Contract Security</Text>
          </View>
          <View style={styles.noteItem}>
            <Icon name="security" size={14} color={GOLD_COLORS.success} />
            <Text style={styles.noteText}>
              All transactions are verified through decentralized smart contracts
            </Text>
          </View>
          <View style={styles.noteItem}>
            <Icon name="code" size={14} color={GOLD_COLORS.primary} />
            <Text style={styles.noteText}>
              Automated parameter validation ensures transaction safety
            </Text>
          </View>
          <View style={styles.noteItem}>
            <Icon name="block" size={14} color={GOLD_COLORS.warning} />
            <Text style={styles.noteText}>
              Invalid transactions are automatically rejected by the smart contract
            </Text>
          </View>
        </View>

        <View style={styles.quickActions}>
          <TouchableOpacity
            style={styles.quickActionButton}
            onPress={() => setShowVerificationModal(true)}
          >
            <Icon name="smart-button" size={24} color="#000000" />
            <Text style={styles.quickActionText}>Start Verification</Text>
          </TouchableOpacity>

          <TouchableOpacity style={styles.quickActionButton}>
            <Icon name="history" size={24} color="#000000" />
            <Text style={styles.quickActionText}>Transaction History</Text>
          </TouchableOpacity>

          <TouchableOpacity style={styles.quickActionButton}>
            <Icon name="help" size={24} color="#000000" />
            <Text style={styles.quickActionText}>Verification Help</Text>
          </TouchableOpacity>
        </View>
      </View>

      <VerificationModal
        visible={showVerificationModal}
        selectedAsset={selectedAsset}
        onClose={handleCloseVerificationModal}
        onVerify={handleVerification}
      />
    </ScrollView>
  );
};

// Styles
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
    borderBottomWidth: 1,
    borderBottomColor: '#E5E5EA',
  },
  headerTitle: {
    fontSize: 22,
    fontWeight: '800',
    color: '#000000',
  },
  headerRight: {
    width: 40,
  },
  content: {
    paddingHorizontal: 20,
    paddingTop: 20,
    paddingBottom: 40,
  },
  instructionsCard: {
    flexDirection: 'row',
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 16,
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  instructionsContent: {
    flex: 1,
    marginLeft: 12,
  },
  instructionsTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 8,
  },
  instructionsText: {
    fontSize: 14,
    color: '#666666',
    lineHeight: 20,
  },
  selectedAssetCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 3 },
    shadowOpacity: 0.08,
    shadowRadius: 6,
    elevation: 4,
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
  },
  selectedAssetHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 16,
  },
  selectedAssetTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  selectedAssetBadge: {
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 10,
    paddingVertical: 6,
    borderRadius: 12,
  },
  selectedAssetBadgeText: {
    fontSize: 11,
    fontWeight: '900',
    color: '#000000',
  },
  selectedAssetContent: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  selectedAssetInfo: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  selectorIcon: {
    justifyContent: 'center',
    alignItems: 'center',
  },
  selectorIconText: {
    color: '#000000',
    fontWeight: '900',
  },
  selectedAssetDetails: {
    marginLeft: 16,
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
  selectedAssetNetwork: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    marginTop: 4,
    fontWeight: '600',
  },
  copyMainButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.primary,
    paddingHorizontal: 16,
    paddingVertical: 12,
    borderRadius: 12,
    marginLeft: 16,
  },
  copyIcon: {
    marginRight: 8,
  },
  copyMainButtonText: {
    fontSize: 14,
    fontWeight: '700',
    color: '#000000',
  },
  section: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  sectionHeader: {
    marginBottom: 20,
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
  },
  assetList: {},
  assetCardContainer: {
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    marginBottom: 12,
    borderWidth: 1,
    borderColor: '#E5E5EA',
    overflow: 'hidden',
  },
  selectedAssetCardContainer: {
    borderColor: GOLD_COLORS.primary,
    borderWidth: 2,
    backgroundColor: GOLD_COLORS.light,
  },
  disabledAssetCardContainer: {
    borderColor: '#CCCCCC',
    backgroundColor: '#F5F5F5',
    opacity: 0.8,
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
  disabledAssetIcon: {
    backgroundColor: '#CCCCCC',
  },
  assetIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 18,
  },
  disabledAssetIconText: {
    color: '#999999',
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
  selectedAssetSymbol: {
    color: GOLD_COLORS.dark,
  },
  disabledAssetSymbol: {
    color: '#999999',
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
  unavailableBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.error,
    paddingHorizontal: 6,
    paddingVertical: 2,
    borderRadius: 8,
    marginLeft: 8,
  },
  unavailableBadgeText: {
    fontSize: 8,
    fontWeight: '900',
    color: '#FFFFFF',
    marginLeft: 2,
  },
  assetName: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 2,
  },
  disabledAssetName: {
    color: '#999999',
  },
  assetNetwork: {
    fontSize: 12,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
  },
  disabledAssetNetwork: {
    color: '#CCCCCC',
  },
  radioButtonContainer: {
    padding: 4,
    marginLeft: 10,
  },
  radioButtonSelected: {},
  radioButtonUnselected: {},
  radioButtonDisabled: {
    opacity: 0.5,
  },
  assetDetails: {
    marginTop: 16,
    paddingTop: 16,
    borderTopWidth: 1,
    borderTopColor: '#E5E5EA',
  },
  detailSection: {},
  detailSectionTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginBottom: 16,
  },
  infoCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 16,
  },
  infoRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
  },
  infoContent: {
    marginLeft: 12,
    flex: 1,
  },
  infoLabel: {
    fontSize: 12,
    color: '#666666',
    marginBottom: 2,
  },
  infoValue: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
  },
  walletAddressSection: {
    marginBottom: 16,
  },
  walletAddressLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#000000',
    marginBottom: 8,
  },
  walletAddressContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F2F2F7',
    borderRadius: 8,
    paddingHorizontal: 12,
    paddingVertical: 12,
    marginBottom: 8,
  },
  walletAddressText: {
    flex: 1,
    fontSize: 12,
    color: '#666666',
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
  },
  copyButton: {
    backgroundColor: GOLD_COLORS.primary,
    width: 36,
    height: 36,
    borderRadius: 8,
    justifyContent: 'center',
    alignItems: 'center',
    marginLeft: 8,
  },
  walletNote: {
    fontSize: 11,
    color: GOLD_COLORS.warning,
    fontStyle: 'italic',
  },
  selectButton: {
    backgroundColor: GOLD_COLORS.primary,
    borderRadius: 12,
    paddingVertical: 14,
    paddingHorizontal: 20,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    marginTop: 8,
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
    marginTop: 8,
  },
  alreadySelectedText: {
    fontSize: 14,
    fontWeight: '600',
    color: GOLD_COLORS.dark,
    marginLeft: 8,
  },
  depositUnavailableContainer: {
    alignItems: 'center',
    paddingVertical: 20,
  },
  unavailableIconContainer: {
    backgroundColor: '#F5F5F5',
    width: 80,
    height: 80,
    borderRadius: 40,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  depositUnavailableTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: GOLD_COLORS.error,
    marginBottom: 8,
    textAlign: 'center',
  },
  depositUnavailableText: {
    fontSize: 14,
    color: '#666666',
    textAlign: 'center',
    lineHeight: 20,
    marginBottom: 20,
    paddingHorizontal: 20,
  },
  notesCard: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  notesHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
  },
  notesTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },
  noteItem: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: 8,
  },
  noteText: {
    fontSize: 13,
    color: '#666666',
    marginLeft: 8,
    flex: 1,
    lineHeight: 18,
  },
  quickActions: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: 40,
  },
  quickActionButton: {
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#FFFFFF',
    borderRadius: 12,
    padding: 16,
    width: (Dimensions.get('window').width - 80) / 3,
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
    marginTop: 8,
    textAlign: 'center',
  },

  // Modal Styles
  modalOverlay: {
    flex: 1,
    justifyContent: 'flex-end',
  },
  modalBackdrop: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
  },
  modalContainer: {
    backgroundColor: '#FFFFFF',
    borderTopLeftRadius: 24,
    borderTopRightRadius: 24,
    maxHeight: '90%',
    minHeight: '70%',
  },
  modalContent: {
    flex: 1,
  },
  modalHeader: {
    padding: 24,
    paddingBottom: 16,
    alignItems: 'center',
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
    position: 'relative',
  },
  modalHeaderIcon: {
    backgroundColor: GOLD_COLORS.light,
    width: 60,
    height: 60,
    borderRadius: 30,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  modalTitle: {
    fontSize: 24,
    fontWeight: '900',
    color: '#000000',
    textAlign: 'center',
    marginBottom: 8,
  },
  modalSubtitle: {
    fontSize: 14,
    color: '#666666',
    textAlign: 'center',
    lineHeight: 20,
  },
  closeButton: {
    position: 'absolute',
    top: 24,
    right: 24,
    padding: 4,
  },
  verificationScrollView: {
    flex: 1,
    paddingHorizontal: 20,
  },
  formSection: {
    marginBottom: 32,
  },
  sectionHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 8,
  },
  stepIndicator: {
    width: 28,
    height: 28,
    borderRadius: 14,
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  stepNumber: {
    fontSize: 14,
    fontWeight: '900',
    color: '#000000',
  },
  sectionTitle: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
  },
  sectionDescription: {
    fontSize: 14,
    color: '#666666',
    marginBottom: 16,
    lineHeight: 20,
  },
  assetSelectionGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 12,
  },
  assetOption: {
    flex: 1,
    minWidth: '45%',
    backgroundColor: '#FAFAFA',
    borderRadius: 12,
    padding: 16,
    alignItems: 'center',
    borderWidth: 2,
    borderColor: '#E5E5EA',
    position: 'relative',
  },
  assetOptionSelected: {
    backgroundColor: GOLD_COLORS.light,
    borderColor: GOLD_COLORS.primary,
  },
  assetOptionIcon: {
    width: 60,
    height: 60,
    borderRadius: 30,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 12,
  },
  assetOptionIconInner: {
    width: 48,
    height: 48,
    borderRadius: 24,
    justifyContent: 'center',
    alignItems: 'center',
  },
  assetOptionIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 18,
  },
  assetOptionSymbol: {
    fontSize: 18,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 4,
  },
  assetOptionSymbolSelected: {
    color: GOLD_COLORS.dark,
  },
  assetOptionName: {
    fontSize: 12,
    color: '#666666',
  },
  selectedIndicator: {
    position: 'absolute',
    top: 8,
    right: 8,
  },
  amountInputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    borderWidth: 2,
    borderColor: '#E5E5EA',
    borderRadius: 12,
    overflow: 'hidden',
  },
  amountInput: {
    flex: 1,
    padding: 16,
    fontSize: 16,
    color: '#000000',
    fontWeight: '600',
  },
  amountCurrency: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 16,
    paddingVertical: 16,
    borderLeftWidth: 2,
    borderLeftColor: '#E5E5EA',
    gap: 8,
  },
  currencyIcon: {
    width: 24,
    height: 24,
    borderRadius: 12,
    justifyContent: 'center',
    alignItems: 'center',
  },
  currencyIconText: {
    color: '#000000',
    fontWeight: '900',
    fontSize: 12,
  },
  currencyText: {
    fontSize: 16,
    fontWeight: '700',
    color: GOLD_COLORS.dark,
  },
  walletInputContainer: {
    position: 'relative',
  },
  walletInput: {
    borderWidth: 2,
    borderColor: '#E5E5EA',
    borderRadius: 12,
    padding: 16,
    fontSize: 14,
    color: '#000000',
    minHeight: 80,
    textAlignVertical: 'top',
    paddingRight: 70,
  },
  pasteButton: {
    position: 'absolute',
    right: 12,
    top: 12,
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 8,
    gap: 4,
  },
  pasteButtonText: {
    fontSize: 12,
    color: GOLD_COLORS.primary,
    fontWeight: '600',
  },
  walletPreview: {
    marginTop: 8,
    backgroundColor: '#F8F8F8',
    padding: 12,
    borderRadius: 8,
  },
  walletPreviewLabel: {
    fontSize: 12,
    fontWeight: '600',
    color: '#666666',
    marginBottom: 4,
  },
  walletPreviewAddress: {
    fontSize: 11,
    color: '#333333',
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
  },
  smartContractInfo: {
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 16,
    marginBottom: 32,
    borderWidth: 1,
    borderColor: GOLD_COLORS.muted,
  },
  contractHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
  },
  contractTitle: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000000',
    marginLeft: 8,
  },
  contractInfoItem: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: 8,
  },
  contractInfoText: {
    fontSize: 13,
    color: '#666666',
    marginLeft: 8,
    flex: 1,
    lineHeight: 18,
  },
  actionButtons: {
    flexDirection: 'row',
    gap: 12,
    padding: 20,
    paddingTop: 16,
    borderTopWidth: 1,
    borderTopColor: '#E5E5EA',
  },
  cancelButton: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 16,
    borderRadius: 12,
    borderWidth: 2,
    borderColor: '#E5E5EA',
    backgroundColor: '#FFFFFF',
  },
  cancelButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#666666',
    marginLeft: 8,
  },
  verifyButton: {
    flex: 2,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 16,
    borderRadius: 12,
    backgroundColor: GOLD_COLORS.primary,
  },
  verifyButtonDisabled: {
    backgroundColor: '#CCCCCC',
  },
  verifyButtonText: {
    fontSize: 16,
    fontWeight: '900',
    color: '#000000',
    marginLeft: 8,
  },
});

export default FundScreen;