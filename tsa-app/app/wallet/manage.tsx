import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
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
  Modal,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import * as Clipboard from 'expo-clipboard';
import { ethers } from 'ethers';
import { router, useLocalSearchParams, useNavigation } from 'expo-router';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';
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
      <View style={[styles.container, { justifyContent: 'center', alignItems: 'center' }]}>
        <ActivityIndicator size="large" color={COLORS.primary} />
      </View>
    );
  }

  if (mode === 'import') {
    const allFilled = filledCount === SEED_LENGTH;
    const hasInvalid = invalidIndices.size > 0;
    const canSubmit = allFilled && !hasInvalid && !actionLoading;

    return (
      <KeyboardAvoidingView
        style={styles.container}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      >
        <ScrollView
          contentContainerStyle={styles.scrollContent}
          keyboardShouldPersistTaps="handled"
        >
          <View style={styles.importIntro}>
            <View style={styles.importIconCircle}>
              <Ionicons name="download-outline" size={22} color={COLORS.primary} />
            </View>
            <Text style={styles.importIntroTitle}>Restore a wallet</Text>
            <Text style={styles.importIntroSubtitle}>
              Enter your 12-word recovery phrase in order. We never store or transmit it.
            </Text>
          </View>

          <View style={styles.importCard}>
            <View style={styles.importCardHeader}>
              <Text style={styles.importCardTitle}>Recovery phrase</Text>
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
                disabled={actionLoading}
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
                disabled={actionLoading}
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
                  disabled={actionLoading}
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
              {actionLoading ? (
                <ActivityIndicator color={COLORS.white} />
              ) : (
                <Text style={styles.primaryButtonText}>Import wallet</Text>
              )}
            </TouchableOpacity>

            <TouchableOpacity
              style={[styles.ghostButton, actionLoading && styles.disabledButton]}
              onPress={handleCancelImport}
              disabled={actionLoading}
            >
              <Text style={styles.ghostButtonText}>Cancel</Text>
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
          Manage your wallets, back up seed phrases, or add a new wallet.
        </Text>
      </View>

      {error ? <Text style={styles.errorText}>{error}</Text> : null}

      {/* Wallet list */}
      {wallets.length > 0 ? (
        <View style={styles.walletsSection}>
          <Text style={styles.walletsSectionTitle}>Your Wallets</Text>
          {wallets.map((wallet) => {
            const isActive = wallet.address === activeAddress;
            return (
              <TouchableOpacity
                key={wallet.address}
                style={[styles.walletCard, isActive && styles.walletCardActive]}
                onPress={() => handleSwitchWallet(wallet.address)}
                activeOpacity={0.7}
              >
                <View style={styles.walletCardHeader}>
                  <View style={styles.walletIconCircle}>
                    <Ionicons name="wallet" size={22} color={COLORS.primary} />
                  </View>
                  <View style={styles.walletCardInfo}>
                    <View style={styles.walletLabelRow}>
                      <Text style={styles.walletCardLabel}>{wallet.label}</Text>
                      {isActive && (
                        <View style={styles.activeBadge}>
                          <Text style={styles.activeBadgeText}>Active</Text>
                        </View>
                      )}
                    </View>
                    <Text style={styles.walletCardAddress} numberOfLines={1} ellipsizeMode="middle">
                      {wallet.address}
                    </Text>
                  </View>
                  <TouchableOpacity
                    style={styles.menuButton}
                    onPress={() => handleWalletMenu(wallet)}
                    hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}
                  >
                    <Ionicons name="ellipsis-vertical" size={20} color={COLORS.gray} />
                  </TouchableOpacity>
                </View>
                <View style={styles.walletCardActions}>
                  {wallet.backedUp ? (
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
                </View>
              </TouchableOpacity>
            );
          })}
        </View>
      ) : (
        <View style={styles.noWalletBanner}>
          <Ionicons name="information-circle-outline" size={20} color="#1E40AF" />
          <Text style={styles.noWalletBannerText}>
            No wallet found. Create or import one below.
          </Text>
        </View>
      )}

      {/* Add Wallet */}
      <View style={styles.walletsSection}>
        <Text style={styles.walletsSectionTitle}>Add Wallet</Text>
        <View style={styles.buttonContainer}>
          <TouchableOpacity
            style={[styles.primaryButton, actionLoading && styles.disabledButton]}
            onPress={handleGenerateNewWallet}
            disabled={actionLoading}
          >
            {actionLoading ? (
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
              setWordInputs(Array(SEED_LENGTH).fill(''));
              setShowWords(false);
              setMode('import');
            }}
            disabled={actionLoading}
          >
            <Ionicons name="download-outline" size={20} color={COLORS.primary} style={{ marginRight: 8 }} />
            <Text style={styles.secondaryButtonText}>Import Existing Wallet</Text>
          </TouchableOpacity>
        </View>
      </View>

      {/* Network Toggle */}
      <View style={styles.sectionCard}>
        <TouchableOpacity style={styles.networkRow} onPress={handleNetworkToggle}>
          <View style={styles.networkLeft}>
            <Ionicons name="globe-outline" size={20} color="#D4AF37" />
            <View>
              <Text style={styles.networkLabel}>Network</Text>
              <Text style={styles.networkValue}>
                {network === 'mainnet' ? 'Mainnet' : 'Testnet'}
              </Text>
            </View>
          </View>
          <View style={[styles.networkBadge, network === 'testnet' && styles.networkBadgeTestnet]}>
            <Text style={[styles.networkBadgeText, network === 'testnet' && styles.networkBadgeTextTestnet]}>
              {network === 'mainnet' ? 'Live' : 'Test'}
            </Text>
          </View>
        </TouchableOpacity>
      </View>

      {/* Rename Modal */}
      <Modal visible={renameModalVisible} transparent animationType="fade">
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <Text style={styles.modalTitle}>Rename Wallet</Text>
            <TextInput
              style={styles.modalInput}
              value={renameValue}
              onChangeText={setRenameValue}
              placeholder="Wallet name"
              autoFocus
            />
            <View style={styles.modalButtons}>
              <TouchableOpacity
                style={styles.modalCancelButton}
                onPress={() => setRenameModalVisible(false)}
              >
                <Text style={styles.modalCancelText}>Cancel</Text>
              </TouchableOpacity>
              <TouchableOpacity style={styles.modalSaveButton} onPress={handleRenameSave}>
                <Text style={styles.modalSaveText}>Save</Text>
              </TouchableOpacity>
            </View>
          </View>
        </View>
      </Modal>
    </ScrollView>
  );
};

export default WalletManage;

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#F5F5F7' },
  scrollContent: { flexGrow: 1, padding: SIZES.padding3 },
  header: { marginBottom: 24 },
  subtitle: { ...FONTS.body3, color: COLORS.gray, lineHeight: 22 },
  walletsSection: { marginBottom: 24 },
  walletsSectionTitle: { ...FONTS.body3, fontWeight: '700', color: COLORS.dark, marginBottom: 12 },
  walletCard: {
    backgroundColor: COLORS.white, borderRadius: 14, padding: 16,
    marginBottom: 10, ...SHADOWS.light,
  },
  walletCardActive: { borderWidth: 2, borderColor: COLORS.primary },
  walletCardHeader: { flexDirection: 'row', alignItems: 'center', marginBottom: 14 },
  walletIconCircle: {
    width: 44, height: 44, borderRadius: 22,
    backgroundColor: `${COLORS.primary}15`, justifyContent: 'center', alignItems: 'center', marginRight: 12,
  },
  walletCardInfo: { flex: 1 },
  walletLabelRow: { flexDirection: 'row', alignItems: 'center', gap: 8, marginBottom: 2 },
  walletCardLabel: { ...FONTS.body3, fontWeight: '600', color: COLORS.dark },
  walletCardAddress: { ...FONTS.body5, color: COLORS.gray },
  activeBadge: {
    backgroundColor: `${COLORS.primary}20`, paddingHorizontal: 8, paddingVertical: 2, borderRadius: 6,
  },
  activeBadgeText: { ...FONTS.body5, color: COLORS.primary, fontWeight: '600' },
  menuButton: { padding: 4 },
  walletCardActions: { flexDirection: 'row', gap: 10 },
  backupButton: {
    flexDirection: 'row', alignItems: 'center', backgroundColor: COLORS.primary,
    paddingVertical: 10, paddingHorizontal: 16, borderRadius: 10, gap: 6,
  },
  backupButtonText: { ...FONTS.body4, color: COLORS.white, fontWeight: '600' },
  backedUpBadge: {
    flexDirection: 'row', alignItems: 'center', backgroundColor: '#D1FAE5',
    paddingVertical: 10, paddingHorizontal: 16, borderRadius: 10, gap: 6,
  },
  backedUpText: { ...FONTS.body4, color: COLORS.success, fontWeight: '600' },
  noWalletBanner: {
    flexDirection: 'row', alignItems: 'center', backgroundColor: '#EFF6FF',
    borderRadius: 12, padding: 14, borderWidth: 1, borderColor: '#BFDBFE', gap: 10, marginBottom: 24,
  },
  noWalletBannerText: { ...FONTS.body4, color: '#1E40AF', fontWeight: '500', flex: 1 },
  buttonContainer: { gap: 12, marginBottom: 24 },
  primaryButton: {
    backgroundColor: COLORS.primary, paddingVertical: 16, borderRadius: 12,
    alignItems: 'center', flexDirection: 'row', justifyContent: 'center', ...SHADOWS.medium,
  },
  primaryButtonText: { ...FONTS.h4, color: COLORS.white, fontWeight: '600' },
  secondaryButton: {
    backgroundColor: COLORS.white, paddingVertical: 16, borderRadius: 12,
    alignItems: 'center', flexDirection: 'row', justifyContent: 'center',
    borderWidth: 2, borderColor: COLORS.primary,
  },
  secondaryButtonText: { ...FONTS.h4, color: COLORS.primary, fontWeight: '600' },
  disabledButton: { opacity: 0.6 },
  label: { ...FONTS.body3, fontWeight: '600', color: COLORS.dark, marginBottom: 8 },
  errorText: { ...FONTS.body4, color: COLORS.danger, marginBottom: 16 },
  importIntro: {
    alignItems: 'center',
    marginBottom: 20,
  },
  importIconCircle: {
    width: 56,
    height: 56,
    borderRadius: 28,
    backgroundColor: `${COLORS.primary}15`,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 12,
  },
  importIntroTitle: {
    ...FONTS.h2,
    color: COLORS.dark,
    fontWeight: '700',
    marginBottom: 6,
  },
  importIntroSubtitle: {
    ...FONTS.body4,
    color: COLORS.gray,
    textAlign: 'center',
    lineHeight: 20,
    paddingHorizontal: 8,
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
    alignItems: 'center',
    justifyContent: 'space-between',
    marginBottom: 12,
  },
  importCardTitle: {
    ...FONTS.h4,
    color: COLORS.dark,
    fontWeight: '700',
  },
  counterPill: {
    backgroundColor: '#F3F4F6',
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
  ghostButton: {
    paddingVertical: 14,
    alignItems: 'center',
    marginTop: 8,
  },
  ghostButtonText: {
    ...FONTS.body3,
    color: COLORS.gray,
    fontWeight: '600',
  },
  modalOverlay: {
    flex: 1, backgroundColor: 'rgba(0,0,0,0.5)', justifyContent: 'center', alignItems: 'center',
  },
  modalContent: {
    backgroundColor: COLORS.white, borderRadius: 16, padding: 24, width: '85%',
  },
  modalTitle: { ...FONTS.h4, fontWeight: '600', color: COLORS.dark, marginBottom: 16 },
  modalInput: {
    borderWidth: 1, borderColor: COLORS.lightGray, borderRadius: 10,
    padding: 12, ...FONTS.body3, color: COLORS.dark, marginBottom: 16,
  },
  modalButtons: { flexDirection: 'row', justifyContent: 'flex-end', gap: 12 },
  modalCancelButton: { paddingVertical: 10, paddingHorizontal: 16 },
  modalCancelText: { ...FONTS.body3, color: COLORS.gray },
  modalSaveButton: {
    backgroundColor: COLORS.primary, paddingVertical: 10, paddingHorizontal: 20, borderRadius: 8,
  },
  modalSaveText: { ...FONTS.body3, color: COLORS.white, fontWeight: '600' },
  sectionCard: {
    backgroundColor: '#FFF',
    borderRadius: 16,
    marginTop: 16,
    borderWidth: 1,
    borderColor: '#F0F0F0',
    overflow: 'hidden',
  },
  networkRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: 16,
  },
  networkLeft: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
  },
  networkLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#1A1A1A',
  },
  networkValue: {
    fontSize: 12,
    color: '#888',
    marginTop: 2,
  },
  networkBadge: {
    backgroundColor: '#D1FAE5',
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: 10,
  },
  networkBadgeTestnet: {
    backgroundColor: '#FEF3C7',
  },
  networkBadgeText: {
    fontSize: 12,
    fontWeight: '700',
    color: '#16A34A',
  },
  networkBadgeTextTestnet: {
    color: '#D97706',
  },
});
