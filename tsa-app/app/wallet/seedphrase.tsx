import React, { useState, useEffect, useRef, useMemo } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  ActivityIndicator,
  TextInput,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import * as Clipboard from 'expo-clipboard';
import { ethers } from 'ethers';
import { router, useNavigation } from 'expo-router';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';
import {
  getMnemonic,
  generateWallet,
  addWallet,
  importWalletFromMnemonic,
  getWalletList,
  markWalletBackedUp,
  getActiveWallet,
  isValidMnemonic,
} from '../../services/wallet';
import { confirmSeedPhraseBackup, registerWalletAddress } from '../../services/walletApi';

const SEED_LENGTH = 12;
const enWordlist = ethers.wordlists.en;
const isBip39Word = (w: string) => {
  if (!w) return false;
  try {
    return enWordlist.getWordIndex(w.toLowerCase().trim()) >= 0;
  } catch {
    return false;
  }
};

type Step = 'loading' | 'setup' | 'warning' | 'display' | 'verify' | 'success';

const SeedPhrase = () => {
  const navigation = useNavigation();
  const [step, setStep] = useState<Step>('loading');
  const [words, setWords] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadingAction, setLoadingAction] = useState<'create' | 'import' | null>(null);
  const [error, setError] = useState('');
  const [wordInputs, setWordInputs] = useState<string[]>(() => Array(SEED_LENGTH).fill(''));
  const [showWords, setShowWords] = useState(false);
  const [pasted, setPasted] = useState(false);
  const inputRefs = useRef<(TextInput | null)[]>(Array(SEED_LENGTH).fill(null));

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
      if (mnemonic && isValidMnemonic(mnemonic)) {
        setStep('warning');
      } else {
        setStep('setup');
      }
    })();
  }, []);

  const filledCount = useMemo(
    () => wordInputs.filter((w) => w.trim().length > 0).length,
    [wordInputs],
  );

  const invalidIndices = useMemo(() => {
    const set = new Set<number>();
    wordInputs.forEach((w, i) => {
      const trimmed = w.trim();
      if (trimmed.length > 0 && !isBip39Word(trimmed)) set.add(i);
    });
    return set;
  }, [wordInputs]);

  const distributeWords = (startIndex: number, parts: string[]) => {
    setWordInputs((prev) => {
      const next = [...prev];
      parts.forEach((word, i) => {
        const target = startIndex + i;
        if (target < SEED_LENGTH) next[target] = word;
      });
      return next;
    });
    const focusIdx = Math.min(startIndex + parts.length, SEED_LENGTH - 1);
    requestAnimationFrame(() => inputRefs.current[focusIdx]?.focus());
  };

  const handleWordChange = (index: number, text: string) => {
    setError('');
    const cleaned = text.toLowerCase().replace(/[^a-z\s]/g, '');
    const parts = cleaned.split(/\s+/).filter(Boolean);

    if (parts.length > 1) {
      distributeWords(index, parts);
      return;
    }
    setWordInputs((prev) => {
      const next = [...prev];
      next[index] = cleaned.replace(/\s+/g, '');
      return next;
    });
  };

  const handleSubmitEditing = (index: number) => {
    if (index < SEED_LENGTH - 1) {
      inputRefs.current[index + 1]?.focus();
    }
  };

  const handleKeyPress = (index: number, e: any) => {
    if (e?.nativeEvent?.key === 'Backspace' && !wordInputs[index] && index > 0) {
      inputRefs.current[index - 1]?.focus();
    }
  };

  const handlePasteAll = async () => {
    const clip = await Clipboard.getStringAsync();
    if (!clip) return;
    const parts = clip
      .trim()
      .toLowerCase()
      .replace(/[^a-z\s]/g, ' ')
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, SEED_LENGTH);
    if (!parts.length) return;
    const next: string[] = Array(SEED_LENGTH).fill('');
    parts.forEach((w, i) => (next[i] = w));
    setWordInputs(next);
    setError('');
    setPasted(true);
    setTimeout(() => setPasted(false), 1500);
  };

  const handleClearAll = () => {
    setWordInputs(Array(SEED_LENGTH).fill(''));
    setError('');
    requestAnimationFrame(() => inputRefs.current[0]?.focus());
  };

  const handleCreateNewWallet = async () => {
    setLoading(true);
    setLoadingAction('create');
    setError('');
    try {
      const wallet = await generateWallet();
      const list = await getWalletList();
      await addWallet(wallet, `Wallet ${list.length + 1}`);
      try {
        await registerWalletAddress(wallet.address);
      } catch (_) {}
      setWords(wallet.mnemonic.split(' '));
      setStep('display');
    } catch (err: any) {
      setError(err.message || 'Failed to create wallet.');
    } finally {
      setLoading(false);
      setLoadingAction(null);
    }
  };

  const handleImportWallet = async () => {
    const trimmed = wordInputs.map((w) => w.trim());
    if (trimmed.some((w) => !w)) {
      setError(`Please fill all ${SEED_LENGTH} words.`);
      return;
    }
    if (invalidIndices.size > 0) {
      setError('Some words are not in the BIP39 wordlist. Check the highlighted cells.');
      return;
    }
    const phrase = trimmed.join(' ');
    if (!isValidMnemonic(phrase)) {
      setError('Invalid recovery phrase. Double-check the order and spelling.');
      return;
    }
    setLoading(true);
    setLoadingAction('import');
    setError('');
    try {
      const walletInfo = await importWalletFromMnemonic(phrase);
      const list = await getWalletList();
      await addWallet(walletInfo, `Wallet ${list.length + 1}`);
      await markWalletBackedUp(walletInfo.address);
      try {
        await registerWalletAddress(walletInfo.address);
      } catch (_) {}
      router.replace('/tokenization');
    } catch (err: any) {
      setError(err.message || 'Failed to import wallet. Check your seed phrase.');
    } finally {
      setLoading(false);
      setLoadingAction(null);
    }
  };

  const loadMnemonic = async () => {
    setLoading(true);
    try {
      const mnemonic = await getMnemonic();
      if (mnemonic && isValidMnemonic(mnemonic)) {
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
      const activeAddr = await getActiveWallet();
      if (activeAddr) await markWalletBackedUp(activeAddr);
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
    const allFilled = filledCount === SEED_LENGTH;
    const hasInvalid = invalidIndices.size > 0;
    const canSubmit = allFilled && !hasInvalid && !loading;

    return (
      <KeyboardAvoidingView
        style={styles.container}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      >
        <ScrollView
          contentContainerStyle={styles.scrollContent}
          keyboardShouldPersistTaps="handled"
        >
          <View style={styles.header}>
            <Text style={styles.title}>Set up your wallet</Text>
            <Text style={styles.subtitle}>
              Restore an existing wallet with your 12-word recovery phrase, or create a brand-new one.
            </Text>
          </View>

          <View style={styles.importCard}>
            <View style={styles.importCardHeader}>
              <View style={styles.importCardHeading}>
                <Text style={styles.importCardTitle}>Restore existing wallet</Text>
                <Text style={styles.importCardSubtitle}>
                  Enter the 12 words in order. Tap a cell to start.
                </Text>
              </View>
              <View style={styles.counterPill}>
                <Text
                  style={[
                    styles.counterPillText,
                    allFilled && !hasInvalid && styles.counterPillTextDone,
                  ]}
                >
                  {filledCount}/{SEED_LENGTH}
                </Text>
              </View>
            </View>

            <View style={styles.toolbar}>
              <TouchableOpacity
                style={styles.toolbarBtn}
                onPress={handlePasteAll}
                disabled={loading}
              >
                <Ionicons
                  name={pasted ? 'checkmark' : 'clipboard-outline'}
                  size={16}
                  color={COLORS.primary}
                />
                <Text style={styles.toolbarBtnText}>{pasted ? 'Pasted' : 'Paste'}</Text>
              </TouchableOpacity>

              <TouchableOpacity
                style={styles.toolbarBtn}
                onPress={() => setShowWords((s) => !s)}
                disabled={loading}
              >
                <Ionicons
                  name={showWords ? 'eye-off-outline' : 'eye-outline'}
                  size={16}
                  color={COLORS.primary}
                />
                <Text style={styles.toolbarBtnText}>{showWords ? 'Hide' : 'Show'}</Text>
              </TouchableOpacity>

              {filledCount > 0 && (
                <TouchableOpacity
                  style={styles.toolbarBtn}
                  onPress={handleClearAll}
                  disabled={loading}
                >
                  <Ionicons name="close-circle-outline" size={16} color={COLORS.danger} />
                  <Text style={[styles.toolbarBtnText, { color: COLORS.danger }]}>Clear</Text>
                </TouchableOpacity>
              )}
            </View>

            <View style={styles.wordInputGrid}>
              {wordInputs.map((word, index) => {
                const isInvalid = invalidIndices.has(index);
                return (
                  <View
                    key={index}
                    style={[
                      styles.wordInputCell,
                      word && !isInvalid && styles.wordInputCellFilled,
                      isInvalid && styles.wordInputCellInvalid,
                    ]}
                  >
                    <Text style={styles.wordInputNumber}>{index + 1}</Text>
                    <TextInput
                      ref={(r) => {
                        inputRefs.current[index] = r;
                      }}
                      style={styles.wordInputField}
                      value={word}
                      onChangeText={(t) => handleWordChange(index, t)}
                      onSubmitEditing={() => handleSubmitEditing(index)}
                      onKeyPress={(e) => handleKeyPress(index, e)}
                      autoCapitalize="none"
                      autoCorrect={false}
                      autoComplete="off"
                      spellCheck={false}
                      secureTextEntry={!showWords && !!word}
                      returnKeyType={index === SEED_LENGTH - 1 ? 'done' : 'next'}
                      blurOnSubmit={index === SEED_LENGTH - 1}
                      selectTextOnFocus
                      placeholder=""
                    />
                  </View>
                );
              })}
            </View>

            {error ? <Text style={styles.errorText}>{error}</Text> : null}

            <TouchableOpacity
              style={[styles.primaryButton, !canSubmit && styles.disabledButton]}
              onPress={handleImportWallet}
              disabled={!canSubmit}
            >
              {loadingAction === 'import' ? (
                <ActivityIndicator color={COLORS.white} />
              ) : (
                <Text style={styles.primaryButtonText}>Restore wallet</Text>
              )}
            </TouchableOpacity>
          </View>

          <View style={styles.separatorRow}>
            <View style={styles.separatorLine} />
            <Text style={styles.separatorText}>or</Text>
            <View style={styles.separatorLine} />
          </View>

          <TouchableOpacity
            style={[styles.secondaryButton, loading && styles.disabledButton]}
            onPress={handleCreateNewWallet}
            disabled={loading}
          >
            {loadingAction === 'create' ? (
              <ActivityIndicator color={COLORS.primary} />
            ) : (
              <Text style={styles.secondaryButtonText}>Create a new wallet</Text>
            )}
          </TouchableOpacity>
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
  title: {
    ...FONTS.h2,
    color: COLORS.dark,
    fontWeight: '700',
    marginBottom: 6,
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
  importCard: {
    backgroundColor: COLORS.white,
    borderRadius: 16,
    padding: 16,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    ...SHADOWS.light,
  },
  importCardHeader: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    justifyContent: 'space-between',
    marginBottom: 12,
    gap: 12,
  },
  importCardHeading: {
    flex: 1,
  },
  importCardTitle: {
    ...FONTS.h3,
    color: COLORS.dark,
    fontWeight: '700',
    marginBottom: 4,
  },
  importCardSubtitle: {
    ...FONTS.body5,
    color: COLORS.gray,
    lineHeight: 18,
  },
  counterPill: {
    backgroundColor: COLORS.lightGray,
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: 999,
  },
  counterPillText: {
    ...FONTS.body5,
    color: COLORS.gray,
    fontWeight: '700',
  },
  counterPillTextDone: {
    color: COLORS.success,
  },
  toolbar: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
    marginBottom: 12,
  },
  toolbarBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    paddingVertical: 6,
    paddingHorizontal: 10,
    borderRadius: 8,
    backgroundColor: '#F3F4F6',
  },
  toolbarBtnText: {
    ...FONTS.body5,
    color: COLORS.primary,
    fontWeight: '600',
  },
  wordInputGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
    marginBottom: 16,
  },
  wordInputCell: {
    flexDirection: 'row',
    alignItems: 'center',
    width: '31.5%',
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 10,
    paddingHorizontal: 8,
    paddingVertical: 8,
    backgroundColor: '#FAFAFA',
  },
  wordInputCellFilled: {
    backgroundColor: COLORS.white,
    borderColor: COLORS.primary,
  },
  wordInputCellInvalid: {
    backgroundColor: '#FEF2F2',
    borderColor: COLORS.danger,
  },
  wordInputNumber: {
    ...FONTS.body5,
    color: COLORS.gray,
    fontWeight: '700',
    width: 18,
  },
  wordInputField: {
    flex: 1,
    ...FONTS.body4,
    color: COLORS.dark,
    paddingVertical: 0,
    paddingHorizontal: 4,
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
