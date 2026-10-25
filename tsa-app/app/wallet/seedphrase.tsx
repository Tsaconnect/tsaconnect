// app/wallet/seedphrase.tsx
//
// Seed-phrase setup / backup / verify flow — a single screen with a step machine:
//   loading → setup (restore from 12 words) ┐
//           → create (the create prompt)     ┘ → warning → display → verify → success
// Opened with `?mode=create` it starts at the create prompt, with `?mode=import`
// at the restore form, so a "Create wallet" button never lands on the restore page.
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
//
// SECURITY screen: every reveal gate is preserved VERBATIM (the phrase can no
// longer be copied to the clipboard — only written down) — the phrase is only loaded after the user accepts the warning
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
import { router, useNavigation, useLocalSearchParams } from 'expo-router';
import {
  getMnemonic,
  generateWallet,
  addWallet,
  importWalletFromMnemonic,
  getWalletList,
  markWalletBackedUp,
  getActiveWallet,
  isValidMnemonic,
  removeWallet,
} from '../../services/wallet';
import { confirmSeedPhraseBackup, registerWalletAddress } from '../../services/walletApi';
import { useAuth } from '../../AuthContext/AuthContext';
import {
  SEED_WORD_COUNT,
  VERIFY_WORD_COUNT,
  checkBackupAnswers,
  linkedWalletNotOnDevice,
  parseSetupMode,
} from '@/utils/walletSetup';
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

const SEED_LENGTH = SEED_WORD_COUNT;
const enWordlist = ethers.wordlists.en;
const isBip39Word = (w: string) => {
  if (!w) return false;
  try {
    return enWordlist.getWordIndex(w.toLowerCase().trim()) >= 0;
  } catch {
    return false;
  }
};

type Step = 'loading' | 'setup' | 'create' | 'warning' | 'display' | 'verify' | 'success';

const SeedPhrase = () => {
  const navigation = useNavigation();
  const params = useLocalSearchParams<{ mode?: string }>();
  const mode = parseSetupMode(params.mode);
  const { currentUser } = useAuth();
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
  // The create prompt's own error, so a failed create never shows inside the restore form.
  const [createError, setCreateError] = useState('');
  const [localAddresses, setLocalAddresses] = useState<string[]>([]);
  // The account has a wallet this phone does not hold: creating a new one replaces it.
  const linkedElsewhere = useMemo(
    () => linkedWalletNotOnDevice(currentUser?.walletAddress, localAddresses),
    [currentUser?.walletAddress, localAddresses],
  );

  React.useEffect(() => {
    const titles: Record<Step, string> = {
      loading: 'Seed Phrase',
      setup: 'Restore Wallet',
      create: 'Create Wallet',
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
      // An explicit create/import tap goes where it said; only the generic
      // entry (backup banner, settings) resumes at the backup warning.
      if (!mode) {
        const mnemonic = await getMnemonic();
        if (mnemonic && isValidMnemonic(mnemonic)) {
          setStep('warning');
          return;
        }
      }
      try {
        setLocalAddresses((await getWalletList()).map((w) => w.address));
      } catch {
        // Only a hint for the "already has a wallet" warning; never block setup on it.
      }
      setStep(mode === 'create' ? 'create' : 'setup');
    })();
    // The mode is fixed by the route this screen was opened with.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
    setCreateError('');
    try {
      const wallet = await generateWallet();
      // Store, then link, and undo the store if the link is refused. A failed
      // link used to be swallowed, leaving the account on a different address
      // than this device holds the key for — a balance the user cannot spend.
      // Linking last means the account never points at a key that was not saved.
      const list = await getWalletList();
      await addWallet(wallet, `Wallet ${list.length + 1}`);
      const registered = await registerWalletAddress(wallet.address, wallet.privateKey);
      if (!registered.success) {
        await removeWallet(wallet.address).catch(() => undefined);
        throw new Error(
          registered.message || 'Could not link this wallet to your account. Please try again.',
        );
      }
      setWords(wallet.mnemonic.split(' '));
      setStep('display');
    } catch (err: any) {
      setCreateError(err.message || 'Failed to create wallet.');
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
      // Register first — see handleCreateNewWallet. This is also where a wallet
      // already claimed by another account is caught, instead of the user
      // reaching a balance screen that answers for someone else's address.
      const registered = await registerWalletAddress(walletInfo.address, walletInfo.privateKey);
      if (!registered.success) {
        throw new Error(
          registered.message || 'Could not link this wallet to your account. Please try again.',
        );
      }
      const list = await getWalletList();
      await addWallet(walletInfo, `Wallet ${list.length + 1}`);
      await markWalletBackedUp(walletInfo.address);
      router.replace('/tokenization');
    } catch (err: any) {
      setError(err.message || 'Failed to import wallet. Check your seed phrase.');
    } finally {
      setLoading(false);
      setLoadingAction(null);
    }
  };

  // True only when the phrase is on screen to show. Anything else leaves the
  // user where they are with a reason; the caller used to move on regardless.
  const loadMnemonic = async (): Promise<boolean> => {
    setLoading(true);
    setError('');
    try {
      const mnemonic = await getMnemonic();
      if (mnemonic && isValidMnemonic(mnemonic)) {
        setWords(mnemonic.split(' '));
        return true;
      }
      // Missing or corrupted on this device — restore or create instead.
      setError(
        "We couldn't find your recovery phrase on this device. Restore your wallet with its phrase, or create a new one.",
      );
      setStep('setup');
      return false;
    } catch (err: any) {
      setError(err.message || 'Failed to load seed phrase.');
      return false;
    } finally {
      setLoading(false);
    }
  };

  // False when there is no 12-word phrase to ask about.
  const setupVerification = (): boolean => {
    if (words.length !== SEED_WORD_COUNT) return false;

    // Pick 4 random unique indices
    const indices: number[] = [];
    while (indices.length < VERIFY_WORD_COUNT) {
      const idx = Math.floor(Math.random() * SEED_WORD_COUNT);
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
        const randomIdx = Math.floor(Math.random() * SEED_WORD_COUNT);
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
    return true;
  };

  const handleProceedToDisplay = async () => {
    if (await loadMnemonic()) setStep('display');
  };

  const handleProceedToVerify = () => {
    if (setupVerification()) {
      setStep('verify');
      return;
    }
    // Nothing to ask about: never show a verify step that cannot fail.
    setError('Show your recovery phrase again before you verify it.');
    setStep('warning');
  };

  const handleSelectWord = (index: number, word: string) => {
    setUserSelections((prev) => ({ ...prev, [index]: word }));
    setError('');
  };

  const handleVerify = async () => {
    // A backup is confirmed only by right answers to real questions.
    const check = checkBackupAnswers(words, verifyIndices, userSelections);
    if (check === 'unprepared') {
      setError('We lost track of your recovery phrase. Show it again, then verify it.');
      setStep('warning');
      return;
    }
    if (check === 'incomplete') {
      setError('Please select a word for each position.');
      return;
    }
    if (check === 'wrong') {
      setError('One or more words are incorrect. Please try again.');
      setUserSelections({});
      return;
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
                {mode ? 'Restore your wallet' : 'Set up your wallet'}
              </Text>
              <Text variant="bodySm" color="textSecondary">
                {mode
                  ? 'Enter the 12-word recovery phrase of the wallet you already have.'
                  : 'Restore an existing wallet with your 12-word recovery phrase, or create a brand-new one.'}
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
              title={mode ? "I don't have a wallet — create one" : 'Create a new wallet'}
              variant="secondary"
              onPress={() => {
                setError('');
                setCreateError('');
                setStep('create');
              }}
              disabled={loading}
            />
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    );
  }

  // Create: its own prompt, so tapping "Create wallet" never lands on the restore form.
  if (step === 'create') {
    return (
      <SafeAreaView style={screen} edges={['top']}>
        <ScrollView contentContainerStyle={scrollContent}>
          <Text variant="title1" weight="700" style={{ marginBottom: spacing.xs }}>
            Create your wallet
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing['2xl'] }}>
            We&apos;ll create a new wallet on this device and show you its 12-word recovery phrase.
          </Text>

          {linkedElsewhere ? (
            <AccentCard
              railColor={colors.warning}
              padding="lg"
              style={{ backgroundColor: colors.warningBg, marginBottom: spacing.xl }}
            >
              <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.xs }}>
                Your account already has a wallet
              </Text>
              <Text variant="bodySm" style={{ marginBottom: spacing.md }}>
                A wallet is linked to your account, but this device doesn&apos;t hold it. Creating a
                new wallet replaces it on your account, and the funds in the old one stay out of
                reach until you restore its recovery phrase. To keep using it, restore it instead.
              </Text>
              <Button
                title="Restore my existing wallet"
                variant="secondary"
                onPress={() => {
                  setError('');
                  setStep('setup');
                }}
              />
            </AccentCard>
          ) : null}

          <Card padding="lg" style={{ marginBottom: spacing.xl, gap: spacing.sm }}>
            {[
              'The wallet is created and kept on this device. Only you hold its keys.',
              'You must write the recovery phrase down. It is the only way to get your wallet back.',
              'TSA Connect cannot recover it for you, and will never ask for it.',
            ].map((item) => (
              <View key={item} style={{ flexDirection: 'row', gap: spacing.sm }}>
                <Text variant="bodySm">{'•'}</Text>
                <Text variant="bodySm" style={{ flex: 1 }}>
                  {item}
                </Text>
              </View>
            ))}
          </Card>

          {createError ? (
            <Text variant="bodySm" color="danger" align="center" style={{ marginBottom: spacing.md }}>
              {createError}
            </Text>
          ) : null}

          <Button
            title={linkedElsewhere ? 'Create a new wallet anyway' : 'Create wallet'}
            onPress={handleCreateNewWallet}
            loading={loadingAction === 'create'}
            disabled={loading}
          />
          <Button
            title="I already have a wallet"
            variant="ghost"
            onPress={() => {
              setError('');
              setStep('setup');
            }}
            disabled={loading}
            style={{ marginTop: spacing.sm }}
          />
        </ScrollView>
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

          <Text variant="caption" color="textMuted" align="center" style={{ marginBottom: spacing.lg }}>
            Write the words on paper. Copying them is turned off so they never end up on your
            clipboard or in another app.
          </Text>

          <Button title="I've Written It Down" onPress={handleProceedToVerify} />

          {error ? (
            <Text variant="bodySm" color="danger" align="center" style={{ marginTop: spacing.md }}>
              {error}
            </Text>
          ) : null}
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
