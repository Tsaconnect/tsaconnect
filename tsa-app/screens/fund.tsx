import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  Platform,
  Alert,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import AsyncStorage from '@react-native-async-storage/async-storage';
import * as Clipboard from 'expo-clipboard';
import QRCode from 'react-native-qrcode-svg';
import {
  getSupportedNetworkNames,
  type ChainKey,
  type ChainConfig,
  type TokenConfig,
} from '../constants/chains';
import { useTokens, type ChainWithKey } from '../hooks/useTokens';

// Gold color palette
const GOLD_COLORS = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  success: '#34C759',
  warning: '#FF9500',
};

const FundScreen: React.FC = () => {
  const { tokenList, getChainsForToken } = useTokens();
  const [selectedAsset, setSelectedAsset] = useState<TokenConfig | null>(null);
  const [selectedChain, setSelectedChain] = useState<ChainWithKey | null>(null);
  const [walletAddress, setWalletAddress] = useState('');
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    const loadAddress = async () => {
      const address = await AsyncStorage.getItem('walletAddress');
      if (address) setWalletAddress(address);
    };
    loadAddress();
  }, []);

  const handleAssetSelect = (asset: TokenConfig) => {
    setSelectedAsset(asset);
    const chains = getChainsForToken(asset.symbol);
    if (chains.length === 1) {
      setSelectedChain(chains[0]);
    } else {
      setSelectedChain(null);
    }
  };

  const handleNetworkSelect = (chain: ChainWithKey) => {
    setSelectedChain(chain);
  };

  const handleCopyAddress = async () => {
    if (!walletAddress) {
      Alert.alert('No Wallet', 'Please set up your wallet first.');
      return;
    }
    await Clipboard.setStringAsync(walletAddress);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const availableChains = selectedAsset ? getChainsForToken(selectedAsset.symbol) : [];
  const showDepositInfo = selectedAsset && selectedChain && walletAddress;

  return (
    <ScrollView
      style={styles.container}
      contentContainerStyle={{ paddingBottom: 100 }}
      showsVerticalScrollIndicator={false}
    >
      <View style={styles.content}>
        {/* Instructions */}
        <View style={styles.instructionsCard}>
          <Icon name="info" size={24} color={GOLD_COLORS.primary} />
          <View style={styles.instructionsContent}>
            <Text style={styles.instructionsTitle}>Fund Your Wallet</Text>
            <Text style={styles.instructionsText}>
              1. Select an asset below{'\n'}
              2. Choose the network{'\n'}
              3. Copy or scan the deposit address{'\n'}
              4. Send funds from your external wallet
            </Text>
          </View>
        </View>

        {/* Step 1: Select Asset */}
        <View style={styles.section}>
          <View style={styles.sectionHeader}>
            <View style={styles.stepBadge}>
              <Text style={styles.stepBadgeText}>1</Text>
            </View>
            <Text style={styles.sectionTitle}>Select Asset</Text>
          </View>

          <View style={styles.assetGrid}>
            {tokenList.map(asset => {
              const isSelected = selectedAsset?.symbol === asset.symbol;
              return (
                <TouchableOpacity
                  key={asset.symbol}
                  style={[styles.assetCard, isSelected && styles.assetCardSelected]}
                  onPress={() => handleAssetSelect(asset)}
                  activeOpacity={0.7}
                >
                  <View style={[styles.assetIcon, { backgroundColor: asset.iconColor }]}>
                    <Text style={styles.assetIconText}>{asset.symbol.charAt(0)}</Text>
                  </View>
                  <Text style={[styles.assetSymbol, isSelected && styles.assetSymbolSelected]}>
                    {asset.symbol}
                  </Text>
                  <Text style={styles.assetName}>{asset.name}</Text>
                  {isSelected && (
                    <View style={styles.checkBadge}>
                      <Icon name="check-circle" size={18} color={GOLD_COLORS.primary} />
                    </View>
                  )}
                </TouchableOpacity>
              );
            })}
          </View>
        </View>

        {/* Step 2: Select Network */}
        {selectedAsset && (
          <View style={styles.section}>
            <View style={styles.sectionHeader}>
              <View style={styles.stepBadge}>
                <Text style={styles.stepBadgeText}>2</Text>
              </View>
              <Text style={styles.sectionTitle}>Select Network</Text>
            </View>

            {availableChains.map(chain => {
              const isSelected = selectedChain?.key === chain.key;
              return (
                <TouchableOpacity
                  key={chain.key}
                  style={[styles.networkCard, isSelected && styles.networkCardSelected]}
                  onPress={() => handleNetworkSelect(chain)}
                  activeOpacity={0.7}
                >
                  <View style={styles.networkInfo}>
                    <Icon
                      name="hub"
                      size={22}
                      color={isSelected ? GOLD_COLORS.dark : '#666666'}
                    />
                    <View style={styles.networkText}>
                      <Text style={[styles.networkName, isSelected && styles.networkNameSelected]}>
                        {chain.name}
                      </Text>
                      <Text style={styles.networkType}>{chain.shortName}</Text>
                    </View>
                  </View>
                  {isSelected ? (
                    <Icon name="radio-button-checked" size={24} color={GOLD_COLORS.primary} />
                  ) : (
                    <Icon name="radio-button-unchecked" size={24} color="#CCCCCC" />
                  )}
                </TouchableOpacity>
              );
            })}
          </View>
        )}

        {/* Step 3: Deposit Address */}
        {showDepositInfo && (
          <View style={styles.depositSection}>
            <View style={styles.sectionHeader}>
              <View style={styles.stepBadge}>
                <Text style={styles.stepBadgeText}>3</Text>
              </View>
              <Text style={styles.sectionTitle}>Deposit Address</Text>
            </View>

            <View style={styles.depositCard}>
              {/* Selected summary */}
              <View style={styles.depositSummary}>
                <View style={[styles.summaryIcon, { backgroundColor: selectedAsset.iconColor }]}>
                  <Text style={styles.summaryIconText}>{selectedAsset.symbol.charAt(0)}</Text>
                </View>
                <View style={styles.summaryText}>
                  <Text style={styles.summaryAsset}>{selectedAsset.symbol}</Text>
                  <Text style={styles.summaryNetwork}>
                    {selectedChain.name} ({selectedChain.shortName})
                  </Text>
                </View>
              </View>

              {/* QR Code */}
              <View style={styles.qrContainer}>
                <View style={styles.qrWrapper}>
                  <QRCode
                    value={walletAddress}
                    size={180}
                    color="#000000"
                    backgroundColor="#FFFFFF"
                  />
                </View>
              </View>

              {/* Address */}
              <View style={styles.addressContainer}>
                <Text style={styles.addressLabel}>Your Deposit Address</Text>
                <View style={styles.addressRow}>
                  <Text style={styles.addressText} selectable numberOfLines={2}>
                    {walletAddress}
                  </Text>
                  <TouchableOpacity style={styles.copyButton} onPress={handleCopyAddress}>
                    <Icon
                      name={copied ? 'check' : 'content-copy'}
                      size={20}
                      color="#000000"
                    />
                  </TouchableOpacity>
                </View>
                {copied && (
                  <Text style={styles.copiedText}>Address copied!</Text>
                )}
              </View>

              {/* Warning */}
              <View style={styles.warningBox}>
                <Icon name="warning" size={18} color="#92400E" />
                <Text style={styles.warningText}>
                  Only send {selectedAsset.symbol} on the{' '}
                  <Text style={styles.warningBold}>{selectedChain.name}</Text> to this address.
                  Sending tokens on other networks may result in permanent loss.
                </Text>
              </View>
            </View>
          </View>
        )}

        {/* No wallet warning */}
        {selectedAsset && selectedChain && !walletAddress && (
          <View style={styles.noWalletCard}>
            <Icon name="account-balance-wallet" size={40} color="#CCCCCC" />
            <Text style={styles.noWalletTitle}>No Wallet Set Up</Text>
            <Text style={styles.noWalletText}>
              Please create or import a wallet first to get your deposit address.
            </Text>
          </View>
        )}

        {/* Notes */}
        <View style={styles.notesCard}>
          <View style={styles.notesHeader}>
            <Icon name="security" size={20} color={GOLD_COLORS.primary} />
            <Text style={styles.notesTitle}>Important Notes</Text>
          </View>
          <View style={styles.noteItem}>
            <Icon name="check-circle" size={14} color={GOLD_COLORS.success} />
            <Text style={styles.noteText}>
              Always double-check the network before sending funds
            </Text>
          </View>
          <View style={styles.noteItem}>
            <Icon name="check-circle" size={14} color={GOLD_COLORS.success} />
            <Text style={styles.noteText}>
              Deposits are credited after network confirmations
            </Text>
          </View>
          <View style={styles.noteItem}>
            <Icon name="check-circle" size={14} color={GOLD_COLORS.success} />
            <Text style={styles.noteText}>
              Supported networks: {getSupportedNetworkNames()}
            </Text>
          </View>
        </View>
      </View>
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD_COLORS.background,
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
    lineHeight: 22,
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
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
  },
  stepBadge: {
    width: 28,
    height: 28,
    borderRadius: 14,
    backgroundColor: GOLD_COLORS.primary,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  stepBadgeText: {
    fontSize: 14,
    fontWeight: '900',
    color: '#000000',
  },
  sectionTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000000',
  },
  assetGrid: {
    flexDirection: 'row',
    gap: 12,
  },
  assetCard: {
    flex: 1,
    backgroundColor: '#FAFAFA',
    borderRadius: 12,
    padding: 16,
    alignItems: 'center',
    borderWidth: 2,
    borderColor: '#E5E5EA',
    position: 'relative',
  },
  assetCardSelected: {
    backgroundColor: GOLD_COLORS.light,
    borderColor: GOLD_COLORS.primary,
  },
  assetIcon: {
    width: 44,
    height: 44,
    borderRadius: 22,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 10,
  },
  assetIconText: {
    color: '#FFFFFF',
    fontWeight: '900',
    fontSize: 18,
  },
  assetSymbol: {
    fontSize: 16,
    fontWeight: '800',
    color: '#000000',
    marginBottom: 2,
  },
  assetSymbolSelected: {
    color: GOLD_COLORS.dark,
  },
  assetName: {
    fontSize: 11,
    color: '#666666',
  },
  checkBadge: {
    position: 'absolute',
    top: 8,
    right: 8,
  },
  networkCard: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    backgroundColor: '#FAFAFA',
    borderRadius: 12,
    padding: 16,
    marginBottom: 10,
    borderWidth: 2,
    borderColor: '#E5E5EA',
  },
  networkCardSelected: {
    backgroundColor: GOLD_COLORS.light,
    borderColor: GOLD_COLORS.primary,
  },
  networkInfo: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  networkText: {
    marginLeft: 12,
  },
  networkName: {
    fontSize: 15,
    fontWeight: '600',
    color: '#000000',
  },
  networkNameSelected: {
    color: GOLD_COLORS.dark,
    fontWeight: '700',
  },
  networkType: {
    fontSize: 12,
    color: '#666666',
    marginTop: 2,
  },
  depositSection: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
    borderWidth: 2,
    borderColor: GOLD_COLORS.primary,
  },
  depositCard: {},
  depositSummary: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: GOLD_COLORS.light,
    borderRadius: 12,
    padding: 14,
    marginBottom: 20,
  },
  summaryIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    justifyContent: 'center',
    alignItems: 'center',
  },
  summaryIconText: {
    color: '#FFFFFF',
    fontWeight: '900',
    fontSize: 16,
  },
  summaryText: {
    marginLeft: 12,
  },
  summaryAsset: {
    fontSize: 16,
    fontWeight: '800',
    color: '#000000',
  },
  summaryNetwork: {
    fontSize: 13,
    color: GOLD_COLORS.dark,
    fontWeight: '600',
    marginTop: 2,
  },
  qrContainer: {
    alignItems: 'center',
    marginBottom: 20,
  },
  qrWrapper: {
    padding: 16,
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    borderWidth: 1,
    borderColor: '#E5E5EA',
  },
  addressContainer: {
    marginBottom: 16,
  },
  addressLabel: {
    fontSize: 13,
    fontWeight: '600',
    color: '#666666',
    marginBottom: 8,
  },
  addressRow: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F2F2F7',
    borderRadius: 10,
    paddingLeft: 14,
    paddingVertical: 4,
  },
  addressText: {
    flex: 1,
    fontSize: 13,
    color: '#333333',
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
    paddingVertical: 10,
  },
  copyButton: {
    backgroundColor: GOLD_COLORS.primary,
    width: 40,
    height: 40,
    borderRadius: 10,
    justifyContent: 'center',
    alignItems: 'center',
    marginLeft: 8,
    marginRight: 4,
  },
  copiedText: {
    fontSize: 12,
    color: GOLD_COLORS.success,
    fontWeight: '600',
    marginTop: 6,
  },
  warningBox: {
    flexDirection: 'row',
    backgroundColor: '#FEF3C7',
    borderRadius: 12,
    padding: 14,
    gap: 10,
  },
  warningText: {
    flex: 1,
    fontSize: 13,
    color: '#92400E',
    lineHeight: 20,
  },
  warningBold: {
    fontWeight: '700',
  },
  noWalletCard: {
    backgroundColor: '#FFFFFF',
    borderRadius: 16,
    padding: 32,
    marginBottom: 20,
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 4,
    elevation: 2,
  },
  noWalletTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: '#333333',
    marginTop: 12,
    marginBottom: 8,
  },
  noWalletText: {
    fontSize: 14,
    color: '#666666',
    textAlign: 'center',
    lineHeight: 20,
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
    gap: 8,
  },
  noteText: {
    fontSize: 13,
    color: '#666666',
    flex: 1,
    lineHeight: 18,
  },
});

export default FundScreen;
