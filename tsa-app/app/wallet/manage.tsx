import React, { useState } from 'react';
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
    <View style={styles.container}>
      <View style={styles.content}>
        <View style={styles.header}>
          <Text style={styles.subtitle}>
            Import an existing wallet, generate a new one, or view your seed phrase.
          </Text>
        </View>

        {error ? <Text style={styles.errorText}>{error}</Text> : null}

        <View style={styles.buttonContainer}>
          <TouchableOpacity
            style={styles.secondaryButton}
            onPress={() => {
              setError('');
              setMnemonicInput('');
              setMode('import');
            }}
            disabled={loading}
          >
            <Text style={styles.secondaryButtonText}>Import Existing Wallet</Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={[styles.primaryButton, loading && styles.disabledButton]}
            onPress={handleGenerateNewWallet}
            disabled={loading}
          >
            {loading ? (
              <ActivityIndicator color={COLORS.white} />
            ) : (
              <Text style={styles.primaryButtonText}>Generate New Wallet</Text>
            )}
          </TouchableOpacity>

          <TouchableOpacity
            style={styles.secondaryButton}
            onPress={() => router.push('/wallet/seedphrase')}
            disabled={loading}
          >
            <Text style={styles.secondaryButtonText}>View Seed Phrase</Text>
          </TouchableOpacity>
        </View>

        <View style={styles.infoBox}>
          <Text style={styles.infoTitle}>Warning</Text>
          <Text style={styles.infoText}>
            Generating a new wallet or importing one will replace your current wallet. Make sure
            you have backed up your current seed phrase before proceeding.
          </Text>
        </View>
      </View>
    </View>
  );
};

export default WalletManage;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
  scrollContent: {
    flexGrow: 1,
    padding: SIZES.padding3,
  },
  content: {
    flex: 1,
    padding: SIZES.padding3,
  },
  header: {
    marginBottom: 32,
  },
  subtitle: {
    ...FONTS.body3,
    color: COLORS.gray,
    lineHeight: 22,
  },
  buttonContainer: {
    gap: 16,
    marginBottom: 32,
  },
  primaryButton: {
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
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
