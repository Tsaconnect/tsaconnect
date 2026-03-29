import React, { useState, useEffect, useCallback } from 'react';
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
  WalletMeta,
} from '../../services/wallet';
import { registerWalletAddress } from '../../services/walletApi';

type Mode = 'menu' | 'import';

const WalletManage = () => {
  const { mode: initialMode } = useLocalSearchParams<{ mode?: string }>();
  const navigation = useNavigation();
  const [loading, setLoading] = useState(true);
  const [actionLoading, setActionLoading] = useState(false);
  const [mode, setMode] = useState<Mode>(initialMode === 'import' ? 'import' : 'menu');
  const [mnemonicInput, setMnemonicInput] = useState('');
  const [error, setError] = useState('');
  const [wallets, setWallets] = useState<WalletMeta[]>([]);
  const [activeAddress, setActiveAddress] = useState<string | null>(null);
  const [renameModalVisible, setRenameModalVisible] = useState(false);
  const [renameAddress, setRenameAddress] = useState('');
  const [renameValue, setRenameValue] = useState('');

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
    setActionLoading(true);
    setError('');
    try {
      const walletInfo = await importWalletFromMnemonic(mnemonicInput);
      const label = `Wallet ${wallets.length + 1}`;
      await addWallet(walletInfo, label);
      await markWalletBackedUp(walletInfo.address);
      await registerWalletAddress(walletInfo.address);
      setMnemonicInput('');
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
    return (
      <KeyboardAvoidingView
        style={styles.container}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      >
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header}>
            <Text style={styles.subtitle}>
              Enter your 12-word seed phrase to add a wallet.
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
              style={[styles.primaryButton, actionLoading && styles.disabledButton]}
              onPress={handleImportWallet}
              disabled={actionLoading}
            >
              {actionLoading ? (
                <ActivityIndicator color={COLORS.white} />
              ) : (
                <Text style={styles.primaryButtonText}>Import Wallet</Text>
              )}
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.secondaryButton}
              onPress={() => { setMode('menu'); setError(''); setMnemonicInput(''); }}
            >
              <Text style={styles.secondaryButtonText}>Cancel</Text>
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
              setMnemonicInput('');
              setMode('import');
            }}
            disabled={actionLoading}
          >
            <Ionicons name="download-outline" size={20} color={COLORS.primary} style={{ marginRight: 8 }} />
            <Text style={styles.secondaryButtonText}>Import Existing Wallet</Text>
          </TouchableOpacity>
        </View>
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
  importSection: { flex: 1 },
  label: { ...FONTS.body3, fontWeight: '600', color: COLORS.dark, marginBottom: 8 },
  mnemonicInput: {
    borderWidth: 1, borderColor: COLORS.lightGray, borderRadius: 12,
    padding: 16, ...FONTS.body3, color: COLORS.dark, minHeight: 120, marginBottom: 16,
  },
  errorText: { ...FONTS.body4, color: COLORS.danger, marginBottom: 16 },
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
});
