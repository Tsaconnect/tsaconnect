import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  ActivityIndicator,
  Alert,
  TextInput,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { router, useNavigation } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';
import { ethers } from 'ethers';
import {
  getMnemonic,
  generateWallet,
  storePrivateKey,
  storeMnemonic,
  importWalletFromMnemonic,
} from '../../services/wallet';
import { confirmSeedPhraseBackup, registerWalletAddress } from '../../services/walletApi';

type Step = 'loading' | 'setup' | 'warning' | 'display' | 'verify' | 'success';

const SeedPhrase = () => {
  const navigation = useNavigation();
  const [step, setStep] = useState<Step>('loading');
  const [words, setWords] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [mnemonicInput, setMnemonicInput] = useState('');

  // Verification state: 4 random word indices to verify
  const [verifyIndices, setVerifyIndices] = useState<number[]>([]);
  const [userSelections, setUserSelections] = useState<Record<number, string>>({});
  const [shuffledOptions, setShuffledOptions] = useState<Record<number, string[]>>({});
  const [copied, setCopied] = useState(false);

  React.useEffect(() => {
    const titles: Record<Step, string> = {
      loading: 'Seed Phrase',
      setup: 'Set Up Wallet',
      warning: 'Back Up Seed Phrase',
      display: 'Your Seed Phrase',
      verify: 'Verify Seed Phrase',
      success: 'Seed Phrase',
    };
    navigation.setOptions({ title: titles[step] });
  }, [step, navigation]);

  // On mount, check if a valid wallet already exists
  useEffect(() => {
    (async () => {
      const mnemonic = await getMnemonic();
      if (mnemonic && ethers.Mnemonic.isValidMnemonic(mnemonic)) {
        setStep('warning');
      } else {
        setStep('setup');
      }
    })();
  }, []);

  const handleCreateNewWallet = async () => {
    setLoading(true);
    setError('');
    try {
      const wallet = await generateWallet();
      await storePrivateKey(wallet.privateKey);
      await storeMnemonic(wallet.mnemonic);
      await AsyncStorage.setItem('walletAddress', wallet.address);
      await AsyncStorage.setItem('seedPhraseBackedUp', 'false');
      try {
        await registerWalletAddress(wallet.address);
      } catch (_) {}
      setWords(wallet.mnemonic.split(' '));
      setStep('display');
    } catch (err: any) {
      setError(err.message || 'Failed to create wallet.');
    } finally {
      setLoading(false);
    }
  };

  const handleImportWallet = async () => {
    if (!mnemonicInput.trim()) {
      setError('Please enter your seed phrase.');
      return;
    }
    const inputWords = mnemonicInput.trim().split(/\s+/);
    if (inputWords.length !== 12) {
      setError('Seed phrase must be exactly 12 words.');
      return;
    }
    setLoading(true);
    setError('');
    try {
      const { address } = await importWalletFromMnemonic(mnemonicInput);
      await AsyncStorage.setItem('walletAddress', address);
      await AsyncStorage.setItem('seedPhraseBackedUp', 'true');
      try {
        await registerWalletAddress(address);
      } catch (_) {}
      router.replace('/tokenization');
    } catch (err: any) {
      setError(err.message || 'Failed to import wallet. Check your seed phrase.');
    } finally {
      setLoading(false);
    }
  };

  const loadMnemonic = async () => {
    setLoading(true);
    try {
      const mnemonic = await getMnemonic();
      if (mnemonic && ethers.Mnemonic.isValidMnemonic(mnemonic)) {
        setWords(mnemonic.split(' '));
      } else {
        // Mnemonic missing or corrupted — show setup
        setStep('setup');
      }
    } catch (err: any) {
      setError(err.message || 'Failed to load seed phrase.');
    } finally {
      setLoading(false);
    }
  };

  const setupVerification = () => {
    if (words.length !== 12) return;

    // Pick 4 random unique indices
    const indices: number[] = [];
    while (indices.length < 4) {
      const idx = Math.floor(Math.random() * 12);
      if (!indices.includes(idx)) {
        indices.push(idx);
      }
    }
    indices.sort((a, b) => a - b);
    setVerifyIndices(indices);

    // For each index, create shuffled options (correct word + 3 random decoys)
    const options: Record<number, string[]> = {};
    indices.forEach((idx) => {
      const correctWord = words[idx];
      const decoys: string[] = [];
      while (decoys.length < 3) {
        const randomIdx = Math.floor(Math.random() * 12);
        const randomWord = words[randomIdx];
        if (randomWord !== correctWord && !decoys.includes(randomWord)) {
          decoys.push(randomWord);
        }
      }
      // Shuffle correct word with decoys
      const allOptions = [correctWord, ...decoys].sort(() => Math.random() - 0.5);
      options[idx] = allOptions;
    });
    setShuffledOptions(options);
    setUserSelections({});
  };

  const handleProceedToDisplay = async () => {
    await loadMnemonic();
    if (!error) {
      setStep('display');
    }
  };

  const handleProceedToVerify = () => {
    setupVerification();
    setStep('verify');
  };

  const handleSelectWord = (index: number, word: string) => {
    setUserSelections((prev) => ({ ...prev, [index]: word }));
    setError('');
  };

  const handleVerify = async () => {
    // Check all selections
    for (const idx of verifyIndices) {
      if (!userSelections[idx]) {
        setError('Please select a word for each position.');
        return;
      }
      if (userSelections[idx] !== words[idx]) {
        setError('One or more words are incorrect. Please try again.');
        setUserSelections({});
        return;
      }
    }

    // All correct
    setLoading(true);
    try {
      await AsyncStorage.setItem('seedPhraseBackedUp', 'true');
      await confirmSeedPhraseBackup();
      setStep('success');
    } catch (err: any) {
      console.error('Confirm backup error:', err);
      // Still mark locally even if API fails
      setStep('success');
    } finally {
      setLoading(false);
    }
  };

  // Loading check
  if (step === 'loading') {
    return (
      <View style={[styles.container, { justifyContent: 'center', alignItems: 'center' }]}>
        <ActivityIndicator size="large" color={COLORS.primary} />
      </View>
    );
  }

  // Step 0: No wallet found — offer create or import
  if (step === 'setup') {
    return (
      <KeyboardAvoidingView
        style={styles.container}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      >
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header}>
            <Text style={styles.subtitle}>
              No wallet found on this device. Create a new wallet or import an existing one using your seed phrase.
            </Text>
          </View>

          <TouchableOpacity
            style={[styles.primaryButton, loading && styles.disabledButton]}
            onPress={handleCreateNewWallet}
            disabled={loading}
          >
            {loading && !mnemonicInput.trim() ? (
              <ActivityIndicator color={COLORS.white} />
            ) : (
              <Text style={styles.primaryButtonText}>Create New Wallet</Text>
            )}
          </TouchableOpacity>

          <View style={styles.separatorRow}>
            <View style={styles.separatorLine} />
            <Text style={styles.separatorText}>OR</Text>
            <View style={styles.separatorLine} />
          </View>

          <Text style={styles.label}>Import Existing Wallet</Text>
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

          <TouchableOpacity
            style={[styles.secondaryButton, loading && styles.disabledButton]}
            onPress={handleImportWallet}
            disabled={loading}
          >
            {loading && mnemonicInput.trim() ? (
              <ActivityIndicator color={COLORS.primary} />
            ) : (
              <Text style={styles.secondaryButtonText}>Restore Wallet</Text>
            )}
          </TouchableOpacity>

          {error ? <Text style={styles.errorText}>{error}</Text> : null}
        </ScrollView>
      </KeyboardAvoidingView>
    );
  }

  // Step 1: Warning
  if (step === 'warning') {
    return (
      <View style={styles.container}>
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header} />

          <View style={styles.warningCard}>
            <Text style={styles.warningTitle}>Important: Read carefully</Text>
            <Text style={styles.warningBody}>
              Your seed phrase is the only way to recover your wallet if you lose access to your
              device. Anyone with your seed phrase can access your funds.
            </Text>
            <View style={styles.warningList}>
              <Text style={styles.warningItem}>
                {'\u2022'} Write it down on paper and store it in a safe place
              </Text>
              <Text style={styles.warningItem}>
                {'\u2022'} Never share it with anyone
              </Text>
              <Text style={styles.warningItem}>
                {'\u2022'} Never store it digitally (no screenshots, no cloud storage)
              </Text>
              <Text style={styles.warningItem}>
                {'\u2022'} TSA Connect will never ask for your seed phrase
              </Text>
            </View>
          </View>

          <TouchableOpacity style={styles.primaryButton} onPress={handleProceedToDisplay}>
            {loading ? (
              <ActivityIndicator color={COLORS.white} />
            ) : (
              <Text style={styles.primaryButtonText}>I Understand, Show My Seed Phrase</Text>
            )}
          </TouchableOpacity>

          {error ? <Text style={styles.errorText}>{error}</Text> : null}
        </ScrollView>
      </View>
    );
  }

  // Step 2: Display words
  if (step === 'display') {
    return (
      <View style={styles.container}>
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header}>
            <Text style={styles.subtitle}>
              Write down these 12 words in order. You will need to verify them next.
            </Text>
          </View>

          <View style={styles.wordGrid}>
            {words.map((word, index) => (
              <View key={index} style={styles.wordCard}>
                <Text style={styles.wordNumber}>{index + 1}</Text>
                <Text style={styles.wordText}>{word}</Text>
              </View>
            ))}
          </View>

          <TouchableOpacity
            style={styles.copyButton}
            onPress={async () => {
              await Clipboard.setStringAsync(words.join(' '));
              setCopied(true);
              setTimeout(() => setCopied(false), 2000);
            }}
          >
            <Text style={styles.copyButtonText}>
              {copied ? 'Copied!' : 'Copy to Clipboard'}
            </Text>
          </TouchableOpacity>

          <TouchableOpacity style={styles.primaryButton} onPress={handleProceedToVerify}>
            <Text style={styles.primaryButtonText}>I've Written It Down</Text>
          </TouchableOpacity>
        </ScrollView>
      </View>
    );
  }

  // Step 3: Verify
  if (step === 'verify') {
    return (
      <View style={styles.container}>
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header}>
            <Text style={styles.subtitle}>
              Select the correct word for each numbered position.
            </Text>
          </View>

          {verifyIndices.map((idx) => (
            <View key={idx} style={styles.verifyGroup}>
              <Text style={styles.verifyLabel}>Word #{idx + 1}</Text>
              <View style={styles.optionsRow}>
                {(shuffledOptions[idx] || []).map((option) => (
                  <TouchableOpacity
                    key={option}
                    style={[
                      styles.optionButton,
                      userSelections[idx] === option && styles.optionButtonSelected,
                    ]}
                    onPress={() => handleSelectWord(idx, option)}
                  >
                    <Text
                      style={[
                        styles.optionText,
                        userSelections[idx] === option && styles.optionTextSelected,
                      ]}
                    >
                      {option}
                    </Text>
                  </TouchableOpacity>
                ))}
              </View>
            </View>
          ))}

          {error ? <Text style={styles.errorText}>{error}</Text> : null}

          <TouchableOpacity
            style={[styles.primaryButton, loading && styles.disabledButton]}
            onPress={handleVerify}
            disabled={loading}
          >
            {loading ? (
              <ActivityIndicator color={COLORS.white} />
            ) : (
              <Text style={styles.primaryButtonText}>Verify</Text>
            )}
          </TouchableOpacity>
        </ScrollView>
      </View>
    );
  }

  // Step 4: Success
  return (
    <View style={styles.container}>
      <View style={styles.successContainer}>
        <View style={styles.successIcon}>
          <Text style={styles.successIconText}>!</Text>
        </View>
        <Text style={styles.successTitle}>Seed Phrase Backed Up</Text>
        <Text style={styles.successMessage}>
          Your wallet is now fully secured. Keep your seed phrase safe and never share it.
        </Text>
        <TouchableOpacity
          style={[styles.primaryButton, { width: '100%' }]}
          onPress={() => router.replace('/tokenization')}
        >
          <Text style={styles.primaryButtonText}>Go to Wallet</Text>
        </TouchableOpacity>
      </View>
    </View>
  );
};

export default SeedPhrase;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
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
  warningCard: {
    backgroundColor: '#FEF2F2',
    borderRadius: 12,
    padding: 20,
    marginBottom: 24,
    borderWidth: 1,
    borderColor: '#FECACA',
  },
  warningTitle: {
    ...FONTS.h4,
    color: COLORS.danger,
    fontWeight: '600',
    marginBottom: 12,
  },
  warningBody: {
    ...FONTS.body3,
    color: '#7F1D1D',
    lineHeight: 22,
    marginBottom: 12,
  },
  warningList: {
    gap: 8,
  },
  warningItem: {
    ...FONTS.body4,
    color: '#7F1D1D',
    lineHeight: 20,
    paddingLeft: 4,
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
  disabledButton: {
    opacity: 0.6,
  },
  errorText: {
    ...FONTS.body4,
    color: COLORS.danger,
    marginTop: 12,
    textAlign: 'center',
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
  separatorRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginVertical: 24,
  },
  separatorLine: {
    flex: 1,
    height: 1,
    backgroundColor: COLORS.lightGray,
  },
  separatorText: {
    ...FONTS.body4,
    color: COLORS.gray,
    marginHorizontal: 16,
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
  // Word grid
  wordGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
    marginBottom: 32,
    justifyContent: 'center',
  },
  wordCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: COLORS.white,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 10,
    paddingVertical: 10,
    paddingHorizontal: 14,
    width: '47%',
  },
  wordNumber: {
    ...FONTS.body5,
    color: COLORS.gray,
    fontWeight: '600',
    width: 24,
  },
  wordText: {
    ...FONTS.body3,
    color: COLORS.dark,
    fontWeight: '500',
  },
  copyButton: {
    alignItems: 'center',
    paddingVertical: 12,
    marginBottom: 16,
  },
  copyButtonText: {
    ...FONTS.body4,
    color: COLORS.primary,
    fontWeight: '600',
  },
  // Verify
  verifyGroup: {
    marginBottom: 20,
  },
  verifyLabel: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 8,
  },
  optionsRow: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
  },
  optionButton: {
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 10,
    paddingVertical: 10,
    paddingHorizontal: 16,
    backgroundColor: COLORS.white,
  },
  optionButtonSelected: {
    borderColor: COLORS.primary,
    backgroundColor: COLORS.primary,
  },
  optionText: {
    ...FONTS.body4,
    color: COLORS.dark,
  },
  optionTextSelected: {
    color: COLORS.white,
    fontWeight: '600',
  },
  // Success
  successContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: SIZES.padding3,
  },
  successIcon: {
    width: 72,
    height: 72,
    borderRadius: 36,
    backgroundColor: '#D1FAE5',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  successIconText: {
    fontSize: 32,
    fontWeight: '700',
    color: COLORS.success,
  },
  successTitle: {
    ...FONTS.h2,
    color: COLORS.dark,
    fontWeight: '600',
    marginBottom: 8,
  },
  successMessage: {
    ...FONTS.body3,
    color: COLORS.gray,
    textAlign: 'center',
    marginBottom: 32,
    lineHeight: 22,
  },
});
