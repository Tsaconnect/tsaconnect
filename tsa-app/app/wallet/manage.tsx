import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  ActivityIndicator,
  Alert,
  TextInput,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router, useLocalSearchParams, useNavigation } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';
import {
  generateWallet,
  storePrivateKey,
  storeMnemonic,
  importWalletFromMnemonic,
} from '../../services/wallet';
import { registerWalletAddress } from '../../services/walletApi';

type Mode = 'menu' | 'import';

const WalletManage = () => {
  const { mode: initialMode } = useLocalSearchParams<{ mode?: string }>();
  const navigation = useNavigation();
  const [loading, setLoading] = useState(false);
  const [mode, setMode] = useState<Mode>(initialMode === 'import' ? 'import' : 'menu');
  const [mnemonicInput, setMnemonicInput] = useState('');
  const [error, setError] = useState('');
  const [walletAddress, setWalletAddress] = useState<string | null>(null);
  const [seedBackedUp, setSeedBackedUp] = useState(false);

  useEffect(() => {
    (async () => {
      const address = await AsyncStorage.getItem('walletAddress');
      const backedUp = await AsyncStorage.getItem('seedPhraseBackedUp');
      setWalletAddress(address);
      setSeedBackedUp(backedUp === 'true');
    })();
  }, []);

  React.useEffect(() => {
    navigation.setOptions({
      title: mode === 'import' ? 'Import Wallet' : 'Manage Wallet',
    });
  }, [mode, navigation]);

  const handleGenerateNewWallet = () => {
    Alert.alert(
      'Replace Current Wallet',
      'This will replace your current wallet. Make sure you\'ve backed up your seed phrase.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Generate New Wallet',
          style: 'destructive',
          onPress: async () => {
            setLoading(true);
            setError('');
            try {
              const wallet = await generateWallet();
              await storePrivateKey(wallet.privateKey);
              await storeMnemonic(wallet.mnemonic);
              await AsyncStorage.setItem('walletAddress', wallet.address);
              await AsyncStorage.setItem('seedPhraseBackedUp', 'false');

              // Register with backend
              const result = await registerWalletAddress(wallet.address);
              if (!result.success) {
                console.warn('Backend wallet registration failed:', result.message);
              }

              router.replace('/tokenization');
            } catch (err: any) {
              console.error('Generate wallet error:', err);
              setError(err.message || 'Failed to generate wallet. Please try again.');
            } finally {
              setLoading(false);
            }
          },
        },
      ]
    );
  };

  const handleImportWallet = async () => {
    if (!mnemonicInput.trim()) {
      setError('Please enter your seed phrase.');
      return;
    }

    const words = mnemonicInput.trim().split(/\s+/);
    if (words.length !== 12) {
      setError('Seed phrase must be exactly 12 words.');
      return;
    }

    setLoading(true);
    setError('');
    try {
      const { address } = await importWalletFromMnemonic(mnemonicInput);
      await AsyncStorage.setItem('walletAddress', address);
      await AsyncStorage.setItem('seedPhraseBackedUp', 'true');

      // Register with backend
      const result = await registerWalletAddress(address);
      if (!result.success) {
        console.warn('Backend wallet registration failed:', result.message);
      }

      router.replace('/tokenization');
    } catch (err: any) {
      console.error('Import wallet error:', err);
      setError(err.message || 'Failed to import wallet. Check your seed phrase.');
    } finally {
      setLoading(false);
    }
  };

  if (mode === 'import') {
    return (
      <KeyboardAvoidingView
        style={styles.container}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      >
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header}>
            <Text style={styles.subtitle}>
              Enter your 12-word seed phrase to restore your wallet. This will replace your current wallet.
            </Text>
          </View>

          <View style={styles.importSection}>
            <Text style={styles.label}>Seed Phrase</Text>
            <TextInput
              style={styles.mnemonicInput}
              placeholder="Enter your 12-word seed phrase separated by spaces"
              placeholderTextColor={COLORS.gray}
              value={mnemonicInput}
              onChangeText={(text) => {
                setMnemonicInput(text);
                setError('');
              }}
              multiline
              autoCapitalize="none"
              autoCorrect={false}
              textAlignVertical="top"
            />

            {error ? <Text style={styles.errorText}>{error}</Text> : null}

            <TouchableOpacity
              style={[styles.primaryButton, loading && styles.disabledButton]}
              onPress={handleImportWallet}
              disabled={loading}
            >
              {loading ? (
                <ActivityIndicator color={COLORS.white} />
              ) : (
                <Text style={styles.primaryButtonText}>Restore Wallet</Text>
              )}
            </TouchableOpacity>
          </View>
        </ScrollView>
      </KeyboardAvoidingView>
    );
  }

  return (
    <ScrollView style={styles.container} contentContainerStyle={styles.scrollContent}>
      <View style={styles.header}>
        <Text style={styles.subtitle}>
          Manage your wallets, back up your seed phrase, or add a new wallet.
        </Text>
      </View>

      {error ? <Text style={styles.errorText}>{error}</Text> : null}

      {/* Current Wallet(s) Section */}
      {walletAddress ? (
        <View style={styles.walletsSection}>
          <Text style={styles.walletsSectionTitle}>Your Wallets</Text>
          <View style={styles.walletCard}>
            <View style={styles.walletCardHeader}>
              <View style={styles.walletIconCircle}>
                <Ionicons name="wallet" size={22} color={COLORS.primary} />
              </View>
              <View style={styles.walletCardInfo}>
                <Text style={styles.walletCardLabel}>Primary Wallet</Text>
                <Text style={styles.walletCardAddress} numberOfLines={1} ellipsizeMode="middle">
                  {walletAddress}
                </Text>
              </View>
            </View>
            <View style={styles.walletCardActions}>
              {seedBackedUp ? (
                <View style={styles.backedUpBadge}>
                  <Ionicons name="checkmark-circle" size={16} color={COLORS.success} />
                  <Text style={styles.backedUpText}>Backed Up</Text>
                </View>
              ) : (
                <TouchableOpacity
                  style={styles.backupButton}
                  onPress={() => router.push('/wallet/seedphrase')}
                >
                  <Ionicons name="shield-checkmark-outline" size={16} color={COLORS.white} />
                  <Text style={styles.backupButtonText}>Back Up</Text>
                </TouchableOpacity>
              )}
              <TouchableOpacity
                style={styles.viewSeedButton}
                onPress={() => router.push('/wallet/seedphrase')}
              >
                <Ionicons name="eye-outline" size={16} color={COLORS.primary} />
                <Text style={styles.viewSeedButtonText}>View Seed Phrase</Text>
              </TouchableOpacity>
            </View>
          </View>
        </View>
      ) : (
        <View style={styles.noWalletBanner}>
          <Ionicons name="information-circle-outline" size={20} color="#1E40AF" />
          <Text style={styles.noWalletBannerText}>
            No wallet found. Create or import one below.
          </Text>
        </View>
      )}

      {/* Wallet Actions */}
      <View style={styles.walletsSection}>
        <Text style={styles.walletsSectionTitle}>Add Wallet</Text>
        <View style={styles.buttonContainer}>
          <TouchableOpacity
            style={[styles.primaryButton, loading && styles.disabledButton]}
            onPress={handleGenerateNewWallet}
            disabled={loading}
          >
            {loading ? (
              <ActivityIndicator color={COLORS.white} />
            ) : (
              <>
                <Ionicons name="add-circle-outline" size={20} color={COLORS.white} style={{ marginRight: 8 }} />
                <Text style={styles.primaryButtonText}>Generate New Wallet</Text>
              </>
            )}
          </TouchableOpacity>

          <TouchableOpacity
            style={styles.secondaryButton}
            onPress={() => {
              setError('');
              setMnemonicInput('');
              setMode('import');
            }}
            disabled={loading}
          >
            <Ionicons name="download-outline" size={20} color={COLORS.primary} style={{ marginRight: 8 }} />
            <Text style={styles.secondaryButtonText}>Import Existing Wallet</Text>
          </TouchableOpacity>
        </View>
      </View>

      <View style={styles.infoBox}>
        <Text style={styles.infoTitle}>Warning</Text>
        <Text style={styles.infoText}>
          Generating a new wallet or importing one will replace your current wallet. Make sure
          you have backed up your current seed phrase before proceeding.
        </Text>
      </View>
    </ScrollView>
  );
};

export default WalletManage;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#F5F5F7',
  },
  scrollContent: {
    flexGrow: 1,
    padding: SIZES.padding3,
  },
  header: {
    marginBottom: 24,
  },
  subtitle: {
    ...FONTS.body3,
    color: COLORS.gray,
    lineHeight: 22,
  },
  walletsSection: {
    marginBottom: 24,
  },
  walletsSectionTitle: {
    ...FONTS.body3,
    fontWeight: '700',
    color: COLORS.dark,
    marginBottom: 12,
  },
  walletCard: {
    backgroundColor: COLORS.white,
    borderRadius: 14,
    padding: 16,
    ...SHADOWS.light,
  },
  walletCardHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 14,
  },
  walletIconCircle: {
    width: 44,
    height: 44,
    borderRadius: 22,
    backgroundColor: `${COLORS.primary}15`,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 12,
  },
  walletCardInfo: {
    flex: 1,
  },
  walletCardLabel: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 2,
  },
  walletCardAddress: {
    ...FONTS.body5,
    color: COLORS.gray,
  },
  walletCardActions: {
    flexDirection: 'row',
    gap: 10,
  },
  backupButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: COLORS.primary,
    paddingVertical: 10,
    paddingHorizontal: 16,
    borderRadius: 10,
    gap: 6,
    flex: 1,
    justifyContent: 'center',
  },
  backupButtonText: {
    ...FONTS.body4,
    color: COLORS.white,
    fontWeight: '600',
  },
  backedUpBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#D1FAE5',
    paddingVertical: 10,
    paddingHorizontal: 16,
    borderRadius: 10,
    gap: 6,
    flex: 1,
    justifyContent: 'center',
  },
  backedUpText: {
    ...FONTS.body4,
    color: COLORS.success,
    fontWeight: '600',
  },
  viewSeedButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: COLORS.white,
    paddingVertical: 10,
    paddingHorizontal: 16,
    borderRadius: 10,
    borderWidth: 1.5,
    borderColor: COLORS.primary,
    gap: 6,
    flex: 1,
    justifyContent: 'center',
  },
  viewSeedButtonText: {
    ...FONTS.body4,
    color: COLORS.primary,
    fontWeight: '600',
  },
  noWalletBanner: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#EFF6FF',
    borderRadius: 12,
    padding: 14,
    borderWidth: 1,
    borderColor: '#BFDBFE',
    gap: 10,
    marginBottom: 24,
  },
  noWalletBannerText: {
    ...FONTS.body4,
    color: '#1E40AF',
    fontWeight: '500',
    flex: 1,
  },
  buttonContainer: {
    gap: 12,
    marginBottom: 24,
  },
  primaryButton: {
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    flexDirection: 'row',
    justifyContent: 'center',
    ...SHADOWS.medium,
  },
  primaryButtonText: {
    ...FONTS.h4,
    color: COLORS.white,
    fontWeight: '600',
  },
  secondaryButton: {
    backgroundColor: COLORS.white,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    flexDirection: 'row',
    justifyContent: 'center',
    borderWidth: 2,
    borderColor: COLORS.primary,
  },
  secondaryButtonText: {
    ...FONTS.h4,
    color: COLORS.primary,
    fontWeight: '600',
  },
  disabledButton: {
    opacity: 0.6,
  },
  infoBox: {
    backgroundColor: '#FEF2F2',
    borderRadius: 12,
    padding: 16,
    borderWidth: 1,
    borderColor: '#FECACA',
  },
  infoTitle: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.danger,
    marginBottom: 8,
  },
  infoText: {
    ...FONTS.body4,
    color: '#7F1D1D',
    lineHeight: 20,
  },
  importSection: {
    flex: 1,
  },
  label: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 8,
  },
  mnemonicInput: {
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 12,
    padding: 16,
    ...FONTS.body3,
    color: COLORS.dark,
    minHeight: 120,
    marginBottom: 16,
  },
  errorText: {
    ...FONTS.body4,
    color: COLORS.danger,
    marginBottom: 16,
  },
});
