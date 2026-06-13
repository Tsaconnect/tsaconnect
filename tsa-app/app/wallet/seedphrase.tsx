// app/wallet/seedphrase.tsx
//
// Seed-phrase setup / backup / verify flow — a single screen with a step machine:
//   loading → setup (create new OR restore from 12 words)
//           → warning → display (reveal phrase) → verify → success
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
//
// SECURITY screen: every reveal gate and the copy behavior are preserved
// VERBATIM — the phrase is only loaded after the user accepts the warning
// (handleProceedToDisplay → loadMnemonic), the import/verify/backup-confirm
// logic, BIP39 validation, secureTextEntry masking, and the
// confirmSeedPhraseBackup / markWalletBackedUp / registerWalletAddress API
// calls are unchanged. This is a visual/structural restyle only.
import React, { useState, useEffect, useRef, useMemo } from 'react';
import {
  View,
  TextInput,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import { ethers } from 'ethers';
import { router, useNavigation } from 'expo-router';
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
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Card,
  Chip,
  Divider,
  Icon,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

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
  const { colors, spacing, radius } = useTheme();
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

  const screen = { flex: 1, backgroundColor: colors.background } as const;
  const scrollContent = { padding: spacing.lg, paddingBottom: spacing['4xl'] } as const;

  // Loading check
  if (step === 'loading') {
    return (
      <SafeAreaView style={screen} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading…" />
        </View>
      </SafeAreaView>
    );
  }

  // Step 0: No wallet found — offer create or import
  if (step === 'setup') {
    const allFilled = filledCount === SEED_LENGTH;
    const hasInvalid = invalidIndices.size > 0;
    const canSubmit = allFilled && !hasInvalid && !loading;

    return (
      <SafeAreaView style={screen} edges={['top']}>
        <KeyboardAvoidingView
          style={{ flex: 1 }}
          behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        >
          <ScrollView contentContainerStyle={scrollContent} keyboardShouldPersistTaps="handled">
            <View style={{ marginBottom: spacing['2xl'] }}>
              <Text variant="title1" weight="700" style={{ marginBottom: spacing.xs }}>
                Set up your wallet
              </Text>
              <Text variant="bodySm" color="textSecondary">
                Restore an existing wallet with your 12-word recovery phrase, or create a brand-new
                one.
              </Text>
            </View>

            <Card padding="lg">
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'flex-start',
                  justifyContent: 'space-between',
                  gap: spacing.md,
                  marginBottom: spacing.md,
                }}
              >
                <View style={{ flex: 1 }}>
                  <Text variant="title3" weight="700" style={{ marginBottom: 2 }}>
                    Restore existing wallet
                  </Text>
                  <Text variant="caption" color="textMuted">
                    Enter the 12 words in order. Tap a cell to start.
                  </Text>
                </View>
                <Badge
                  label={`${filledCount}/${SEED_LENGTH}`}
                  size="sm"
                  color={allFilled && !hasInvalid ? colors.success : colors.textSecondary}
                  background={allFilled && !hasInvalid ? colors.successBg : colors.surfaceAlt}
                />
              </View>

              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm, marginBottom: spacing.md }}>
                <PressableScale
                  onPress={handlePasteAll}
                  disabled={loading}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    gap: spacing.xs,
                    paddingVertical: spacing.xs,
                    paddingHorizontal: spacing.sm,
                    borderRadius: radius.sm,
                    backgroundColor: colors.surfaceAlt,
                  }}
                >
                  <Icon name={pasted ? 'check' : 'copy'} size={16} color={colors.accent} />
                  <Text variant="caption" weight="600" color="accent">
                    {pasted ? 'Pasted' : 'Paste'}
                  </Text>
                </PressableScale>

                <PressableScale
                  onPress={() => setShowWords((s) => !s)}
                  disabled={loading}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    gap: spacing.xs,
                    paddingVertical: spacing.xs,
                    paddingHorizontal: spacing.sm,
                    borderRadius: radius.sm,
                    backgroundColor: colors.surfaceAlt,
                  }}
                >
                  <Icon name={showWords ? 'eyeOff' : 'eye'} size={16} color={colors.accent} />
                  <Text variant="caption" weight="600" color="accent">
                    {showWords ? 'Hide' : 'Show'}
                  </Text>
                </PressableScale>

                {filledCount > 0 && (
                  <PressableScale
                    onPress={handleClearAll}
                    disabled={loading}
                    style={{
                      flexDirection: 'row',
                      alignItems: 'center',
                      gap: spacing.xs,
                      paddingVertical: spacing.xs,
                      paddingHorizontal: spacing.sm,
                      borderRadius: radius.sm,
                      backgroundColor: colors.surfaceAlt,
                    }}
                  >
                    <Icon name="closeCircle" size={16} color={colors.danger} />
                    <Text variant="caption" weight="600" color="danger">
                      Clear
                    </Text>
                  </PressableScale>
                )}
              </View>

              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm, marginBottom: spacing.lg }}>
                {wordInputs.map((word, index) => {
                  const isInvalid = invalidIndices.has(index);
                  const filled = !!word && !isInvalid;
                  const borderColor = isInvalid
                    ? colors.danger
                    : filled
                      ? colors.accent
                      : colors.border;
                  const backgroundColor = isInvalid
                    ? colors.dangerBg
                    : filled
                      ? colors.surface
                      : colors.surfaceAlt;
                  return (
                    <View
                      key={index}
                      style={{
                        flexDirection: 'row',
                        alignItems: 'center',
                        width: '31.5%',
                        borderWidth: 1,
                        borderColor,
                        borderRadius: radius.sm,
                        paddingHorizontal: spacing.sm,
                        paddingVertical: spacing.sm,
                        backgroundColor,
                      }}
                    >
                      <Text variant="caption" weight="700" color="textMuted" style={{ width: 18 }}>
                        {index + 1}
                      </Text>
                      <TextInput
                        ref={(r) => {
                          inputRefs.current[index] = r;
                        }}
                        style={{
                          flex: 1,
                          fontSize: 14,
                          color: colors.textPrimary,
                          paddingVertical: 0,
                          paddingHorizontal: spacing.xs,
                        }}
                        value={word}
                        onChangeText={(t) => handleWordChange(index, t)}
                        onSubmitEditing={() => handleSubmitEditing(index)}
                        onKeyPress={(e) => handleKeyPress(index, e)}
                        placeholderTextColor={colors.textMuted}
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

              {error ? (
                <Text variant="bodySm" color="danger" align="center" style={{ marginBottom: spacing.md }}>
                  {error}
                </Text>
              ) : null}

              <Button
                title="Restore wallet"
                onPress={handleImportWallet}
                disabled={!canSubmit}
                loading={loadingAction === 'import'}
              />
            </Card>

            <View style={{ flexDirection: 'row', alignItems: 'center', marginVertical: spacing['2xl'] }}>
              <View style={{ flex: 1 }}>
                <Divider />
              </View>
              <Text variant="bodySm" color="textMuted" style={{ marginHorizontal: spacing.lg }}>
                or
              </Text>
              <View style={{ flex: 1 }}>
                <Divider />
              </View>
            </View>

            <Button
              title="Create a new wallet"
              variant="secondary"
              onPress={handleCreateNewWallet}
              disabled={loading}
              loading={loadingAction === 'create'}
            />
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    );
  }

  // Step 1: Warning
  if (step === 'warning') {
    return (
      <SafeAreaView style={screen} edges={['top']}>
        <ScrollView contentContainerStyle={scrollContent}>
          <AccentCard
            railColor={colors.danger}
            padding="lg"
            style={{ backgroundColor: colors.dangerBg, marginBottom: spacing['2xl'] }}
          >
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
              <Icon name="warning" size={20} color={colors.danger} />
              <Text variant="title3" weight="700" color="danger">
                Important: Read carefully
              </Text>
            </View>
            <Text variant="bodySm" color="danger" style={{ marginBottom: spacing.md }}>
              Your seed phrase is the only way to recover your wallet if you lose access to your
              device. Anyone with your seed phrase can access your funds.
            </Text>
            <View style={{ gap: spacing.sm }}>
              {[
                'Write it down on paper and store it in a safe place',
                'Never share it with anyone',
                'Never store it digitally (no screenshots, no cloud storage)',
                'TSA Connect will never ask for your seed phrase',
              ].map((item) => (
                <View key={item} style={{ flexDirection: 'row', gap: spacing.sm }}>
                  <Text variant="bodySm" color="danger">
                    {'•'}
                  </Text>
                  <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                    {item}
                  </Text>
                </View>
              ))}
            </View>
          </AccentCard>

          <Button
            title="I Understand, Show My Seed Phrase"
            leftIcon="eye"
            onPress={handleProceedToDisplay}
            loading={loading}
          />

          {error ? (
            <Text variant="bodySm" color="danger" align="center" style={{ marginTop: spacing.md }}>
              {error}
            </Text>
          ) : null}
        </ScrollView>
      </SafeAreaView>
    );
  }

  // Step 2: Display words
  if (step === 'display') {
    return (
      <SafeAreaView style={screen} edges={['top']}>
        <ScrollView contentContainerStyle={scrollContent}>
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing['2xl'] }}>
            Write down these 12 words in order. You will need to verify them next.
          </Text>

          <View
            style={{
              flexDirection: 'row',
              flexWrap: 'wrap',
              gap: spacing.sm,
              justifyContent: 'center',
              marginBottom: spacing['2xl'],
            }}
          >
            {words.map((word, index) => (
              <Card
                key={index}
                padding="md"
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  width: '47%',
                }}
              >
                <Text variant="caption" weight="600" color="textMuted" style={{ width: 24 }}>
                  {index + 1}
                </Text>
                <Text variant="bodyMed" weight="500">
                  {word}
                </Text>
              </Card>
            ))}
          </View>

          <PressableScale
            onPress={async () => {
              await Clipboard.setStringAsync(words.join(' '));
              setCopied(true);
              setTimeout(() => setCopied(false), 2000);
            }}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              gap: spacing.xs,
              paddingVertical: spacing.md,
              marginBottom: spacing.lg,
            }}
          >
            <Icon name={copied ? 'check' : 'copy'} size={18} color={colors.accent} />
            <Text variant="bodySm" weight="600" color="accent">
              {copied ? 'Copied!' : 'Copy to Clipboard'}
            </Text>
          </PressableScale>

          <Button title="I've Written It Down" onPress={handleProceedToVerify} />
        </ScrollView>
      </SafeAreaView>
    );
  }

  // Step 3: Verify
  if (step === 'verify') {
    return (
      <SafeAreaView style={screen} edges={['top']}>
        <ScrollView contentContainerStyle={scrollContent}>
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing['2xl'] }}>
            Select the correct word for each numbered position.
          </Text>

          {verifyIndices.map((idx) => (
            <View key={idx} style={{ marginBottom: spacing.xl }}>
              <Text variant="bodySm" weight="600" style={{ marginBottom: spacing.sm }}>
                Word #{idx + 1}
              </Text>
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
                {(shuffledOptions[idx] || []).map((option) => (
                  <Chip
                    key={option}
                    label={option}
                    selected={userSelections[idx] === option}
                    onPress={() => handleSelectWord(idx, option)}
                  />
                ))}
              </View>
            </View>
          ))}

          {error ? (
            <Text variant="bodySm" color="danger" align="center" style={{ marginBottom: spacing.md }}>
              {error}
            </Text>
          ) : null}

          <Button title="Verify" onPress={handleVerify} loading={loading} disabled={loading} />
        </ScrollView>
      </SafeAreaView>
    );
  }

  // Step 4: Success
  return (
    <SafeAreaView style={screen} edges={['top']}>
      <View
        style={{
          flex: 1,
          justifyContent: 'center',
          alignItems: 'center',
          padding: spacing.lg,
        }}
      >
        <View
          style={{
            width: 72,
            height: 72,
            borderRadius: radius.pill,
            backgroundColor: colors.successBg,
            justifyContent: 'center',
            alignItems: 'center',
            marginBottom: spacing.lg,
          }}
        >
          <Icon name="checkCircle" size={40} color={colors.success} />
        </View>
        <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
          Seed Phrase Backed Up
        </Text>
        <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing['3xl'] }}>
          Your wallet is now fully secured. Keep your seed phrase safe and never share it.
        </Text>
        <Button title="Go to Wallet" onPress={() => router.replace('/tokenization')} />
      </View>
    </SafeAreaView>
  );
};

export default SeedPhrase;
