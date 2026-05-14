// app/wallet/import-token.tsx
//
// "Import crypto" screen — full-page variant of the existing
// components/wallet/ImportTokenModal flow. Auto-fills name/symbol/decimals
// by reading the ERC-20 contract on the selected chain, then persists the
// custom token via useTokens().importToken (AsyncStorage, per-network).

import React, { useCallback, useMemo, useState } from 'react';
import {
  View,
  Text,
  TextInput,
  Pressable,
  StyleSheet,
  ScrollView,
  ActivityIndicator,
  Alert,
  KeyboardAvoidingView,
  Platform,
  Modal,
  Image,
} from 'react-native';
import { router, Stack } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { MaterialIcons as Icon } from '@expo/vector-icons';
import * as Clipboard from 'expo-clipboard';

import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { readTokenContract, isValidAddress } from '../../services/wallet';
import { useTokens, type CustomToken } from '../../hooks/useTokens';

const GOLD = '#D4AF37';

type TabKey = 'token' | 'network';

function hashColor(str: string): string {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  return `hsl(${Math.abs(hash) % 360}, 55%, 50%)`;
}

const ChainPickerModal: React.FC<{
  visible: boolean;
  selected: ChainKey;
  onSelect: (key: ChainKey) => void;
  onClose: () => void;
}> = ({ visible, selected, onSelect, onClose }) => {
  const insets = useSafeAreaInsets();
  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose}>
      <Pressable style={styles.sheetOverlay} onPress={onClose}>
        <Pressable
          style={[styles.sheetContent, { paddingBottom: Math.max(insets.bottom, 16) }]}
          onPress={e => e.stopPropagation()}
        >
          <View style={styles.sheetHandle} />
          <Text style={styles.sheetTitle}>Select network</Text>
          <ScrollView style={{ maxHeight: 420 }}>
            {CHAIN_KEYS.map(key => {
              const chain = CHAINS[key];
              const active = selected === key;
              return (
                <Pressable
                  key={key}
                  style={styles.chainRow}
                  onPress={() => {
                    onSelect(key);
                    onClose();
                  }}
                >
                  {chain.iconUrl ? (
                    <Image source={{ uri: chain.iconUrl }} style={styles.chainRowIcon} />
                  ) : (
                    <View style={[styles.chainRowDot, { backgroundColor: chain.iconColor }]} />
                  )}
                  <Text style={styles.chainRowText}>{chain.name}</Text>
                  {active && <Icon name="check" size={20} color={GOLD} />}
                </Pressable>
              );
            })}
          </ScrollView>
        </Pressable>
      </Pressable>
    </Modal>
  );
};

const ImportTokenScreen: React.FC = () => {
  const { importToken } = useTokens();
  const insets = useSafeAreaInsets();

  const [tab, setTab] = useState<TabKey>('token');
  const [chainKey, setChainKey] = useState<ChainKey>('ethereum');
  const [address, setAddress] = useState('');
  const [name, setName] = useState('');
  const [symbol, setSymbol] = useState('');
  const [decimals, setDecimals] = useState('');
  const [fetching, setFetching] = useState(false);
  const [error, setError] = useState('');
  const [showChainPicker, setShowChainPicker] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const selectedChain = CHAINS[chainKey];

  const isAddressValid = useMemo(() => isValidAddress(address.trim()), [address]);

  const lookup = useCallback(async (addr: string, chain: ChainKey) => {
    setFetching(true);
    setError('');
    try {
      const result = await readTokenContract(addr, chain);
      if (!result) {
        setError('Could not read this contract. Make sure it is a valid ERC-20 on the selected network.');
        return;
      }
      setName(result.name);
      setSymbol(result.symbol);
      setDecimals(String(result.decimals));
    } catch {
      setError('Failed to fetch token data.');
    } finally {
      setFetching(false);
    }
  }, []);

  const handleAddressChange = (text: string) => {
    const trimmed = text.trim();
    setAddress(trimmed);
    setError('');
    if (isValidAddress(trimmed)) {
      void lookup(trimmed, chainKey);
    } else {
      setName('');
      setSymbol('');
      setDecimals('');
    }
  };

  const handleChainChange = (next: ChainKey) => {
    setChainKey(next);
    if (isValidAddress(address.trim())) {
      void lookup(address.trim(), next);
    }
  };

  const handlePaste = async () => {
    const text = await Clipboard.getStringAsync();
    if (text) handleAddressChange(text.trim());
  };

  const handleSave = async () => {
    setError('');
    const addr = address.trim();
    if (!isValidAddress(addr)) {
      setError('Enter a valid contract address.');
      return;
    }
    if (!name.trim() || !symbol.trim() || !decimals.trim()) {
      setError('Name, symbol and decimals are required.');
      return;
    }
    const dec = Number(decimals);
    if (!Number.isInteger(dec) || dec < 0 || dec > 36) {
      setError('Decimals must be a whole number between 0 and 36.');
      return;
    }
    setSubmitting(true);
    try {
      const token: CustomToken = {
        symbol: symbol.trim(),
        name: name.trim(),
        decimals: dec,
        iconColor: hashColor(symbol.trim()),
        chains: [chainKey],
        contractAddress: addr,
        chainKey,
        custom: true,
        importedAt: new Date().toISOString(),
      };
      await importToken(token);
      Alert.alert('Token imported', `${token.symbol} on ${selectedChain.name} has been added.`);
      router.back();
    } catch (err: any) {
      setError(err?.message || 'Failed to import token.');
    } finally {
      setSubmitting(false);
    }
  };

  const canSave = isAddressValid && !!name && !!symbol && !!decimals && !submitting;

  return (
    <View style={[styles.screen, { paddingBottom: insets.bottom }]}>
      <Stack.Screen
        options={{
          title: 'Import crypto',
          headerRight: () => (
            <Pressable hitSlop={12} onPress={() => Alert.alert(
              'Import crypto',
              'Anyone can create a token on a chain, including fake versions of well-known assets. Verify the contract address before importing.',
            )}>
              <Icon name="info-outline" size={22} color="#1A1A1A" />
            </Pressable>
          ),
        }}
      />

      {/* Tabs */}
      <View style={styles.tabsRow}>
        <Pressable style={styles.tab} onPress={() => setTab('token')}>
          <Text style={[styles.tabLabel, tab === 'token' && styles.tabLabelActive]}>Token</Text>
          {tab === 'token' && <View style={styles.tabUnderline} />}
        </Pressable>
        <Pressable style={styles.tab} onPress={() => setTab('network')}>
          <Text style={[styles.tabLabel, tab === 'network' && styles.tabLabelActive]}>Network</Text>
          {tab === 'network' && <View style={styles.tabUnderline} />}
        </Pressable>
      </View>

      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView
          contentContainerStyle={styles.body}
          keyboardShouldPersistTaps="handled"
          showsVerticalScrollIndicator={false}
        >
          {tab === 'token' ? (
            <>
              <View style={styles.warning}>
                <Icon name="info" size={18} color="#B98900" />
                <Text style={styles.warningText}>
                  Anyone can create a token, including fake versions of existing tokens.
                  Verify the contract address before importing.
                </Text>
              </View>

              <Text style={styles.label}>Network</Text>
              <Pressable style={styles.networkField} onPress={() => setShowChainPicker(true)}>
                {selectedChain.iconUrl ? (
                  <Image source={{ uri: selectedChain.iconUrl }} style={styles.networkIcon} />
                ) : (
                  <View style={[styles.networkIcon, { backgroundColor: selectedChain.iconColor }]} />
                )}
                <Text style={styles.networkText}>{selectedChain.name}</Text>
                <Icon name="keyboard-arrow-down" size={22} color="#888" />
              </Pressable>

              <Text style={styles.label}>Address</Text>
              <View style={styles.addressField}>
                <TextInput
                  style={styles.addressInput}
                  placeholder="0x..."
                  placeholderTextColor="#BBB"
                  value={address}
                  onChangeText={handleAddressChange}
                  autoCapitalize="none"
                  autoCorrect={false}
                  multiline
                />
                <Pressable style={styles.pasteBtn} onPress={handlePaste} hitSlop={8}>
                  <Text style={styles.pasteText}>Paste</Text>
                </Pressable>
              </View>

              {fetching && (
                <View style={styles.statusRow}>
                  <ActivityIndicator size="small" color={GOLD} />
                  <Text style={styles.statusText}>Reading contract on {selectedChain.name}…</Text>
                </View>
              )}

              <Text style={styles.label}>Name</Text>
              <View style={styles.input}>
                <TextInput
                  style={styles.inputText}
                  value={name}
                  onChangeText={setName}
                  placeholder="Token name"
                  placeholderTextColor="#BBB"
                  autoCapitalize="words"
                />
              </View>

              <Text style={styles.label}>Symbol</Text>
              <View style={styles.input}>
                <TextInput
                  style={styles.inputText}
                  value={symbol}
                  onChangeText={setSymbol}
                  placeholder="e.g. USDC"
                  placeholderTextColor="#BBB"
                  autoCapitalize="characters"
                  autoCorrect={false}
                />
              </View>

              <Text style={styles.label}>Decimals</Text>
              <View style={styles.input}>
                <TextInput
                  style={styles.inputText}
                  value={decimals}
                  onChangeText={setDecimals}
                  placeholder="e.g. 18"
                  placeholderTextColor="#BBB"
                  keyboardType="number-pad"
                />
              </View>

              {error ? (
                <View style={styles.errorRow}>
                  <Icon name="error-outline" size={16} color="#DC2626" />
                  <Text style={styles.errorText}>{error}</Text>
                </View>
              ) : null}

              <Pressable
                style={[styles.saveBtn, !canSave && styles.saveBtnDisabled]}
                onPress={handleSave}
                disabled={!canSave}
              >
                {submitting ? (
                  <ActivityIndicator color="#FFF" />
                ) : (
                  <Text style={styles.saveText}>Import {symbol || 'token'}</Text>
                )}
              </Pressable>
            </>
          ) : (
            <View style={styles.networkTabEmpty}>
              <Icon name="hub" size={42} color="#DDD" />
              <Text style={styles.emptyTitle}>Custom networks</Text>
              <Text style={styles.emptyDesc}>
                Adding custom RPC networks isn&apos;t supported yet. Use the supported networks listed in the Token tab.
              </Text>
            </View>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      <ChainPickerModal
        visible={showChainPicker}
        selected={chainKey}
        onSelect={handleChainChange}
        onClose={() => setShowChainPicker(false)}
      />
    </View>
  );
};

export default ImportTokenScreen;

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: '#FFF' },

  tabsRow: {
    flexDirection: 'row',
    paddingHorizontal: 20,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: '#EFEFEF',
  },
  tab: { flex: 1, alignItems: 'center', paddingVertical: 14 },
  tabLabel: { fontSize: 15, fontWeight: '500', color: '#888' },
  tabLabelActive: { color: '#1A1A1A', fontWeight: '700' },
  tabUnderline: {
    position: 'absolute',
    bottom: 0,
    height: 3,
    width: 60,
    backgroundColor: '#2F44FF',
    borderRadius: 2,
  },

  body: { padding: 20, paddingBottom: 40 },

  warning: {
    flexDirection: 'row',
    gap: 10,
    backgroundColor: '#FFF4D6',
    borderRadius: 10,
    padding: 14,
    marginBottom: 24,
  },
  warningText: { flex: 1, fontSize: 13, color: '#1A1A1A', lineHeight: 18 },

  label: { fontSize: 14, color: '#1A1A1A', marginBottom: 6, marginTop: 14, fontWeight: '500' },

  networkField: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 10,
    borderWidth: 1,
    borderColor: '#E0E0E0',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: 14,
  },
  networkIcon: { width: 22, height: 22, borderRadius: 11 },
  networkText: { flex: 1, fontSize: 15, color: '#1A1A1A', fontWeight: '500' },

  addressField: {
    flexDirection: 'row',
    alignItems: 'center',
    borderWidth: 1,
    borderColor: '#E0E0E0',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: Platform.OS === 'ios' ? 12 : 6,
  },
  addressInput: {
    flex: 1,
    fontSize: 14,
    color: '#1A1A1A',
    minHeight: 40,
    paddingVertical: 4,
  },
  pasteBtn: { paddingHorizontal: 10, paddingVertical: 6 },
  pasteText: { color: '#2F44FF', fontSize: 14, fontWeight: '600' },

  input: {
    borderWidth: 1,
    borderColor: '#E0E0E0',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: Platform.OS === 'ios' ? 14 : 10,
  },
  inputText: { fontSize: 15, color: '#1A1A1A' },

  statusRow: { flexDirection: 'row', alignItems: 'center', gap: 8, marginTop: 10 },
  statusText: { fontSize: 13, color: '#666' },

  errorRow: { flexDirection: 'row', alignItems: 'center', gap: 6, marginTop: 14 },
  errorText: { flex: 1, fontSize: 13, color: '#DC2626' },

  saveBtn: {
    marginTop: 28,
    backgroundColor: GOLD,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
  },
  saveBtnDisabled: { opacity: 0.4 },
  saveText: { color: '#FFF', fontSize: 16, fontWeight: '700' },

  networkTabEmpty: { alignItems: 'center', paddingVertical: 60, paddingHorizontal: 30, gap: 12 },
  emptyTitle: { fontSize: 17, fontWeight: '700', color: '#1A1A1A' },
  emptyDesc: { fontSize: 13, color: '#888', textAlign: 'center', lineHeight: 19 },

  sheetOverlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.45)', justifyContent: 'flex-end' },
  sheetContent: {
    backgroundColor: '#FFF',
    borderTopLeftRadius: 22,
    borderTopRightRadius: 22,
    paddingHorizontal: 20,
    paddingTop: 10,
  },
  sheetHandle: { width: 40, height: 4, borderRadius: 2, backgroundColor: '#E0E0E0', alignSelf: 'center', marginBottom: 14 },
  sheetTitle: { fontSize: 17, fontWeight: '700', color: '#1A1A1A', marginBottom: 12 },
  chainRow: { flexDirection: 'row', alignItems: 'center', gap: 12, paddingVertical: 14 },
  chainRowIcon: { width: 26, height: 26, borderRadius: 13 },
  chainRowDot: { width: 26, height: 26, borderRadius: 13 },
  chainRowText: { flex: 1, fontSize: 15, color: '#1A1A1A' },
});
