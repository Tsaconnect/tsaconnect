// app/wallet/manage.tsx
//
// Manage Wallets — list/switch/rename/delete wallets, generate a new wallet, import
// an existing one from a 12-word recovery phrase, and toggle mainnet/testnet.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui).
//
// SECURITY screen: all seed generation/import, BIP39 validation, and persistence is
// preserved VERBATIM — no key material is logged or altered. This is a visual /
// structural restyle only. The recovery-phrase entry grid intentionally keeps raw
// TextInputs (ref-based focus jumping, per-cell secureTextEntry, Backspace handling)
// because those behaviours aren't expressible through the Input primitive.
import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import {
  View,
  TouchableOpacity,
  ActivityIndicator,
  Alert,
  TextInput,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import { ethers } from 'ethers';
import { router, useLocalSearchParams, useNavigation } from 'expo-router';
import {
  generateWallet,
  importWalletFromMnemonic,
  addWallet,
  removeWallet,
  getWalletList,
  getActiveWallet,
  setActiveWallet,
  updateWalletLabel,
  markWalletBackedUp,
  migrateFromSingleWallet,
  isValidMnemonic,
  WalletMeta,
} from '../../services/wallet';
import { registerWalletAddress } from '../../services/walletApi';
import { useNetwork } from '../../hooks/useNetwork';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Badge,
  Button,
  Card,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Sheet,
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

type Mode = 'menu' | 'import';

const WalletManage = () => {
  const { colors, spacing, radius } = useTheme();
  const { mode: initialMode } = useLocalSearchParams<{ mode?: string }>();
  const navigation = useNavigation();
  const [loading, setLoading] = useState(true);
  const [actionLoading, setActionLoading] = useState(false);
  const [mode, setMode] = useState<Mode>(initialMode === 'import' ? 'import' : 'menu');
  const [wordInputs, setWordInputs] = useState<string[]>(() => Array(SEED_LENGTH).fill(''));
  const [showWords, setShowWords] = useState(false);
  const [pasted, setPasted] = useState(false);
  const inputRefs = useRef<(TextInput | null)[]>(Array(SEED_LENGTH).fill(null));
  const [error, setError] = useState('');
  const [wallets, setWallets] = useState<WalletMeta[]>([]);
  const [activeAddress, setActiveAddress] = useState<string | null>(null);
  const [renameModalVisible, setRenameModalVisible] = useState(false);
  const [renameAddress, setRenameAddress] = useState('');
  const [renameValue, setRenameValue] = useState('');
  const { network, switchNetwork } = useNetwork();

  const handleNetworkToggle = () => {
    const target = network === 'mainnet' ? 'testnet' : 'mainnet';
    Alert.alert(
      `Switch to ${target === 'testnet' ? 'Testnet' : 'Mainnet'}?`,
      target === 'testnet'
        ? 'Testnet uses test tokens with no real value. Your mainnet balances are not affected.'
        : 'Switch back to Mainnet to see your real balances.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: `Switch to ${target === 'testnet' ? 'Testnet' : 'Mainnet'}`,
          onPress: () => switchNetwork(target),
        },
      ]
    );
  };

  const loadWallets = useCallback(async () => {
    await migrateFromSingleWallet();
    const list = await getWalletList();
    const active = await getActiveWallet();
    setWallets(list);
    setActiveAddress(active);
    setLoading(false);
  }, []);

  useEffect(() => {
    loadWallets();
  }, [loadWallets]);

  useEffect(() => {
    navigation.setOptions({
      title: mode === 'import' ? 'Import Wallet' : 'Manage Wallets',
    });
  }, [mode, navigation]);

  const handleSwitchWallet = async (address: string) => {
    if (address === activeAddress) return;
    setActionLoading(true);
    try {
      await setActiveWallet(address);
      await registerWalletAddress(address);
      setActiveAddress(address);
    } catch (err: any) {
      Alert.alert('Error', err.message || 'Failed to switch wallet');
    } finally {
      setActionLoading(false);
    }
  };

  const handleGenerateNewWallet = async () => {
    setActionLoading(true);
    setError('');
    try {
      const wallet = await generateWallet();
      const label = `Wallet ${wallets.length + 1}`;
      await addWallet(wallet, label);
      await registerWalletAddress(wallet.address);
      await loadWallets();
      router.push('/wallet/seedphrase');
    } catch (err: any) {
      setError(err.message || 'Failed to generate wallet.');
    } finally {
      setActionLoading(false);
    }
  };

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
    if (index < SEED_LENGTH - 1) inputRefs.current[index + 1]?.focus();
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

  const handleCancelImport = () => {
    setMode('menu');
    setError('');
    setWordInputs(Array(SEED_LENGTH).fill(''));
    setShowWords(false);
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
    setActionLoading(true);
    setError('');
    try {
      const walletInfo = await importWalletFromMnemonic(phrase);
      const label = `Wallet ${wallets.length + 1}`;
      await addWallet(walletInfo, label);
      await markWalletBackedUp(walletInfo.address);
      await registerWalletAddress(walletInfo.address);
      setWordInputs(Array(SEED_LENGTH).fill(''));
      setShowWords(false);
      setMode('menu');
      await loadWallets();
    } catch (err: any) {
      setError(err.message || 'Failed to import wallet.');
    } finally {
      setActionLoading(false);
    }
  };

  const handleDeleteWallet = (wallet: WalletMeta) => {
    const isOnly = wallets.length === 1;
    const message = isOnly
      ? 'This is your only wallet. Deleting it will remove all wallet data. Make sure you have your seed phrase backed up.'
      : `Delete "${wallet.label}"? Make sure you have the seed phrase backed up.`;

    Alert.alert('Delete Wallet', message, [
      { text: 'Cancel', style: 'cancel' },
      {
        text: 'Delete',
        style: 'destructive',
        onPress: async () => {
          await removeWallet(wallet.address);
          if (isOnly) {
            await registerWalletAddress('');
          } else {
            const newActive = await getActiveWallet();
            if (newActive) await registerWalletAddress(newActive);
          }
          await loadWallets();
        },
      },
    ]);
  };

  const handleRenameStart = (wallet: WalletMeta) => {
    setRenameAddress(wallet.address);
    setRenameValue(wallet.label);
    setRenameModalVisible(true);
  };

  const handleRenameSave = async () => {
    if (!renameValue.trim()) return;
    await updateWalletLabel(renameAddress, renameValue.trim());
    setRenameModalVisible(false);
    await loadWallets();
  };

  const handleWalletMenu = (wallet: WalletMeta) => {
    Alert.alert(wallet.label, undefined, [
      { text: 'Rename', onPress: () => handleRenameStart(wallet) },
      { text: 'View Seed Phrase', onPress: () => router.push('/wallet/seedphrase') },
      { text: 'Delete', style: 'destructive', onPress: () => handleDeleteWallet(wallet) },
      { text: 'Cancel', style: 'cancel' },
    ]);
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" />
        </View>
      </SafeAreaView>
    );
  }

  if (mode === 'import') {
    const allFilled = filledCount === SEED_LENGTH;
    const hasInvalid = invalidIndices.size > 0;
    const canSubmit = allFilled && !hasInvalid && !actionLoading;

    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <KeyboardAvoidingView
          style={{ flex: 1 }}
          behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        >
          <ScrollView
            contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
            keyboardShouldPersistTaps="handled"
          >
            <View style={{ alignItems: 'center', marginBottom: spacing.xl }}>
              <View
                style={{
                  width: 56,
                  height: 56,
                  borderRadius: radius.pill,
                  backgroundColor: colors.accentMuted,
                  justifyContent: 'center',
                  alignItems: 'center',
                  marginBottom: spacing.md,
                }}
              >
                <Icon name="upload" size={24} color={colors.accent} />
              </View>
              <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.xs }}>
                Restore a wallet
              </Text>
              <Text variant="bodySm" color="textSecondary" align="center" style={{ paddingHorizontal: spacing.sm }}>
                Enter your 12-word recovery phrase in order. We never store or transmit it.
              </Text>
            </View>

            <Card padding="lg">
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  marginBottom: spacing.md,
                }}
              >
                <Text variant="title3" weight="700">
                  Recovery phrase
                </Text>
                <Badge
                  label={`${filledCount}/${SEED_LENGTH}`}
                  color={allFilled && !hasInvalid ? colors.success : colors.textMuted}
                  background={allFilled && !hasInvalid ? colors.successBg : colors.surfaceAlt}
                />
              </View>

              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm, marginBottom: spacing.md }}>
                <PressableScale
                  onPress={handlePasteAll}
                  disabled={actionLoading}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    gap: spacing.xs,
                    paddingVertical: spacing.xs + 2,
                    paddingHorizontal: spacing.md,
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
                  disabled={actionLoading}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    gap: spacing.xs,
                    paddingVertical: spacing.xs + 2,
                    paddingHorizontal: spacing.md,
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
                    disabled={actionLoading}
                    style={{
                      flexDirection: 'row',
                      alignItems: 'center',
                      gap: spacing.xs,
                      paddingVertical: spacing.xs + 2,
                      paddingHorizontal: spacing.md,
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
                  return (
                    <View
                      key={index}
                      style={{
                        flexDirection: 'row',
                        alignItems: 'center',
                        width: '31.5%',
                        borderWidth: 1,
                        borderColor: isInvalid
                          ? colors.danger
                          : filled
                            ? colors.accent
                            : colors.border,
                        borderRadius: radius.md,
                        paddingHorizontal: spacing.sm,
                        paddingVertical: spacing.sm,
                        backgroundColor: isInvalid
                          ? colors.dangerBg
                          : filled
                            ? colors.surface
                            : colors.surfaceAlt,
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
                          paddingHorizontal: 4,
                        }}
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
                      />
                    </View>
                  );
                })}
              </View>

              {error ? (
                <Text variant="bodySm" color="danger" style={{ marginBottom: spacing.md }}>
                  {error}
                </Text>
              ) : null}

              <Button
                title="Import wallet"
                onPress={handleImportWallet}
                disabled={!canSubmit}
                loading={actionLoading}
              />

              <Button
                title="Cancel"
                variant="ghost"
                onPress={handleCancelImport}
                disabled={actionLoading}
                style={{ marginTop: spacing.sm }}
              />
            </Card>
          </ScrollView>
        </KeyboardAvoidingView>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.xl }}>
          Manage your wallets, back up seed phrases, or add a new wallet.
        </Text>

        {error ? (
          <Text variant="bodySm" color="danger" style={{ marginBottom: spacing.lg }}>
            {error}
          </Text>
        ) : null}

        {/* Wallet list */}
        {wallets.length > 0 ? (
          <View style={{ marginBottom: spacing['2xl'] }}>
            <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
              Your Wallets
            </Text>
            {wallets.map((wallet) => {
              const isActive = wallet.address === activeAddress;
              return (
                <PressableScale
                  key={wallet.address}
                  onPress={() => handleSwitchWallet(wallet.address)}
                  style={{ marginBottom: spacing.sm }}
                >
                  <Card padding="lg" style={isActive ? { borderWidth: 2, borderColor: colors.accent } : undefined}>
                    <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.md }}>
                      <View
                        style={{
                          width: 44,
                          height: 44,
                          borderRadius: radius.pill,
                          backgroundColor: colors.accentMuted,
                          justifyContent: 'center',
                          alignItems: 'center',
                          marginRight: spacing.md,
                        }}
                      >
                        <Icon name="wallet" size={22} color={colors.accent} />
                      </View>
                      <View style={{ flex: 1 }}>
                        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: 2 }}>
                          <Text variant="bodyMed" weight="600">
                            {wallet.label}
                          </Text>
                          {isActive && (
                            <Badge label="Active" color={colors.accent} background={colors.accentMuted} size="sm" />
                          )}
                        </View>
                        <Text
                          variant="caption"
                          color="textMuted"
                          numeric
                          numberOfLines={1}
                          ellipsizeMode="middle"
                        >
                          {wallet.address}
                        </Text>
                      </View>
                      <IconButton
                        icon="dots"
                        accessibilityLabel="Wallet options"
                        onPress={() => handleWalletMenu(wallet)}
                        size={20}
                        color={colors.textMuted}
                      />
                    </View>
                    <View style={{ flexDirection: 'row' }}>
                      {wallet.backedUp ? (
                        <Badge label="Backed Up" icon="checkCircle" color={colors.success} background={colors.successBg} />
                      ) : (
                        <Button
                          title="Back Up"
                          size="sm"
                          leftIcon="shield"
                          fullWidth={false}
                          onPress={() => router.push('/wallet/seedphrase')}
                        />
                      )}
                    </View>
                  </Card>
                </PressableScale>
              );
            })}
          </View>
        ) : (
          <AccentCard padding="md" railColor={colors.info} style={{ marginBottom: spacing['2xl'] }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Icon name="info" size={20} color={colors.info} />
              <Text variant="bodySm" weight="500" color="info" style={{ flex: 1 }}>
                No wallet found. Create or import one below.
              </Text>
            </View>
          </AccentCard>
        )}

        {/* Add Wallet */}
        <View style={{ marginBottom: spacing['2xl'] }}>
          <Text variant="title3" weight="700" style={{ marginBottom: spacing.md }}>
            Add Wallet
          </Text>
          <Button
            title="Generate New Wallet"
            leftIcon="plus"
            onPress={handleGenerateNewWallet}
            disabled={actionLoading}
            loading={actionLoading}
            style={{ marginBottom: spacing.md }}
          />
          <Button
            title="Import Existing Wallet"
            variant="secondary"
            leftIcon="upload"
            onPress={() => {
              setError('');
              setWordInputs(Array(SEED_LENGTH).fill(''));
              setShowWords(false);
              setMode('import');
            }}
            disabled={actionLoading}
          />
        </View>

        {/* Network Toggle */}
        <Card padding="lg">
          <PressableScale onPress={handleNetworkToggle}>
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                <Icon name="bolt" size={20} color={colors.accent} />
                <View>
                  <Text variant="bodySm" weight="600">
                    Network
                  </Text>
                  <Text variant="caption" color="textMuted">
                    {network === 'mainnet' ? 'Mainnet' : 'Testnet'}
                  </Text>
                </View>
              </View>
              <Badge
                label={network === 'mainnet' ? 'Live' : 'Test'}
                color={network === 'mainnet' ? colors.success : colors.warning}
                background={network === 'mainnet' ? colors.successBg : colors.warningBg}
              />
            </View>
          </PressableScale>
        </Card>
      </ScrollView>

      {/* Rename Sheet */}
      <Sheet visible={renameModalVisible} onClose={() => setRenameModalVisible(false)} title="Rename Wallet">
        <View style={{ padding: spacing.lg }}>
          <Input
            value={renameValue}
            onChangeText={setRenameValue}
            placeholder="Wallet name"
            autoFocus
            containerStyle={{ marginBottom: spacing.lg }}
          />
          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <Button
              title="Cancel"
              variant="ghost"
              onPress={() => setRenameModalVisible(false)}
              style={{ flex: 1 }}
            />
            <Button title="Save" onPress={handleRenameSave} style={{ flex: 1 }} />
          </View>
        </View>
      </Sheet>
    </SafeAreaView>
  );
};

export default WalletManage;
