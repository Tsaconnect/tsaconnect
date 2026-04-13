import React, { useState } from 'react';
import {
  View, Text, TextInput, TouchableOpacity, Modal, Pressable,
  StyleSheet, ActivityIndicator, Alert,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import * as Clipboard from 'expo-clipboard';
import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { readTokenContract } from '../../services/wallet';
import { useTokens, type CustomToken } from '../../hooks/useTokens';
import ChainSelector from './ChainSelector';

const GOLD = '#D4AF37';

interface ImportTokenModalProps {
  visible: boolean;
  onClose: () => void;
}

type Step = 'input' | 'loading' | 'confirm';

function hashColor(str: string): string {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  const hue = Math.abs(hash) % 360;
  return `hsl(${hue}, 55%, 50%)`;
}

const ImportTokenModal = ({ visible, onClose }: ImportTokenModalProps) => {
  const { tokens, importToken } = useTokens();
  const [selectedChain, setSelectedChain] = useState<ChainKey>('sonic');
  const [contractAddress, setContractAddress] = useState('');
  const [step, setStep] = useState<Step>('input');
  const [error, setError] = useState('');
  const [tokenData, setTokenData] = useState<{ name: string; symbol: string; decimals: number } | null>(null);

  const reset = () => {
    setContractAddress('');
    setStep('input');
    setError('');
    setTokenData(null);
    setSelectedChain('sonic');
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  const handlePaste = async () => {
    const text = await Clipboard.getStringAsync();
    if (text) {
      setContractAddress(text.trim());
      setError('');
    }
  };

  const handleLookup = async () => {
    setError('');
    if (!contractAddress.trim() || !contractAddress.startsWith('0x') || contractAddress.length !== 42) {
      setError('Enter a valid contract address (0x...)');
      return;
    }

    setStep('loading');
    const result = await readTokenContract(contractAddress.trim(), selectedChain);
    if (!result) {
      setError('Could not read token data from this address. Make sure it is a valid ERC20 contract.');
      setStep('input');
      return;
    }

    if (tokens[result.symbol]) {
      setError(`${result.symbol} is already a supported token`);
      setStep('input');
      return;
    }

    setTokenData(result);
    setStep('confirm');
  };

  const handleImport = async () => {
    if (!tokenData) return;
    try {
      const customToken: CustomToken = {
        symbol: tokenData.symbol,
        name: tokenData.name,
        decimals: tokenData.decimals,
        iconColor: hashColor(tokenData.symbol),
        chains: [selectedChain],
        contractAddress: contractAddress.trim(),
        chainKey: selectedChain,
        custom: true,
        importedAt: new Date().toISOString(),
      };
      await importToken(customToken);
      Alert.alert('Token Imported', `${tokenData.symbol} has been added to your wallet.`);
      handleClose();
    } catch (err: any) {
      setError(err.message || 'Failed to import token');
      setStep('input');
    }
  };

  return (
    <Modal visible={visible} transparent animationType="slide">
      <Pressable style={styles.overlay} onPress={handleClose}>
        <Pressable style={styles.content} onPress={(e) => e.stopPropagation()}>
          <View style={styles.handle} />
          <Text style={styles.title}>Import Token</Text>

          <Text style={styles.label}>NETWORK</Text>
          <ChainSelector
            availableChains={CHAIN_KEYS}
            selectedChain={selectedChain}
            onSelect={(chain) => { setSelectedChain(chain); setError(''); }}
            size="normal"
          />

          {step === 'input' && (
            <>
              <Text style={[styles.label, { marginTop: 20 }]}>CONTRACT ADDRESS</Text>
              <View style={styles.inputRow}>
                <TextInput
                  style={styles.input}
                  placeholder="0x..."
                  placeholderTextColor="#CCC"
                  value={contractAddress}
                  onChangeText={(t) => { setContractAddress(t); setError(''); }}
                  autoCapitalize="none"
                  autoCorrect={false}
                />
                <TouchableOpacity style={styles.pasteBtn} onPress={handlePaste}>
                  <Ionicons name="clipboard-outline" size={18} color={GOLD} />
                </TouchableOpacity>
              </View>

              {error ? (
                <View style={styles.errorRow}>
                  <Ionicons name="alert-circle" size={14} color="#DC2626" />
                  <Text style={styles.errorText}>{error}</Text>
                </View>
              ) : null}

              <TouchableOpacity
                style={[styles.btn, !contractAddress.trim() && { opacity: 0.4 }]}
                onPress={handleLookup}
                disabled={!contractAddress.trim()}
              >
                <Text style={styles.btnText}>Look Up Token</Text>
              </TouchableOpacity>
            </>
          )}

          {step === 'loading' && (
            <View style={styles.loadingContainer}>
              <ActivityIndicator size="large" color={GOLD} />
              <Text style={styles.loadingText}>Reading contract on {CHAINS[selectedChain].name}...</Text>
            </View>
          )}

          {step === 'confirm' && tokenData && (
            <>
              <View style={styles.tokenCard}>
                <View style={[styles.tokenBadge, { backgroundColor: hashColor(tokenData.symbol) }]}>
                  <Text style={styles.tokenBadgeText}>{tokenData.symbol[0]}</Text>
                </View>
                <View style={{ flex: 1, marginLeft: 12 }}>
                  <Text style={styles.tokenName}>{tokenData.name}</Text>
                  <Text style={styles.tokenSymbol}>{tokenData.symbol}</Text>
                </View>
              </View>

              <View style={styles.detailsCard}>
                <View style={styles.detailRow}>
                  <Text style={styles.detailLabel}>Symbol</Text>
                  <Text style={styles.detailValue}>{tokenData.symbol}</Text>
                </View>
                <View style={styles.detailRow}>
                  <Text style={styles.detailLabel}>Decimals</Text>
                  <Text style={styles.detailValue}>{tokenData.decimals}</Text>
                </View>
                <View style={styles.detailRow}>
                  <Text style={styles.detailLabel}>Network</Text>
                  <Text style={styles.detailValue}>{CHAINS[selectedChain].name}</Text>
                </View>
                <View style={styles.detailRow}>
                  <Text style={styles.detailLabel}>Contract</Text>
                  <Text style={styles.detailValue} numberOfLines={1} ellipsizeMode="middle">
                    {contractAddress}
                  </Text>
                </View>
              </View>

              {error ? (
                <View style={styles.errorRow}>
                  <Ionicons name="alert-circle" size={14} color="#DC2626" />
                  <Text style={styles.errorText}>{error}</Text>
                </View>
              ) : null}

              <TouchableOpacity style={styles.btn} onPress={handleImport}>
                <Text style={styles.btnText}>Import {tokenData.symbol}</Text>
              </TouchableOpacity>
              <TouchableOpacity style={styles.cancelBtn} onPress={() => setStep('input')}>
                <Text style={styles.cancelText}>Back</Text>
              </TouchableOpacity>
            </>
          )}
        </Pressable>
      </Pressable>
    </Modal>
  );
};

export default ImportTokenModal;

const styles = StyleSheet.create({
  overlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'flex-end' },
  content: { backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24, padding: 24, maxHeight: '80%' },
  handle: { width: 40, height: 4, backgroundColor: '#E0E0E0', borderRadius: 2, alignSelf: 'center', marginBottom: 16 },
  title: { fontSize: 20, fontWeight: '700', color: '#1A1A1A', marginBottom: 20, textAlign: 'center' },
  label: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1, marginBottom: 8 },
  inputRow: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  input: { flex: 1, backgroundColor: '#F5F5F5', borderRadius: 12, paddingHorizontal: 16, paddingVertical: 14, fontSize: 15, color: '#1A1A1A', borderWidth: 1, borderColor: '#F0F0F0' },
  pasteBtn: { width: 44, height: 48, backgroundColor: '#FFF8E1', borderRadius: 12, justifyContent: 'center', alignItems: 'center' },
  errorRow: { flexDirection: 'row', alignItems: 'center', gap: 6, marginTop: 10 },
  errorText: { fontSize: 12, color: '#DC2626', flex: 1 },
  btn: { backgroundColor: '#D4AF37', paddingVertical: 16, borderRadius: 14, alignItems: 'center', marginTop: 20 },
  btnText: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  cancelBtn: { paddingVertical: 14, alignItems: 'center', marginTop: 8 },
  cancelText: { fontSize: 15, color: '#888', fontWeight: '600' },
  loadingContainer: { alignItems: 'center', paddingVertical: 40, gap: 16 },
  loadingText: { fontSize: 14, color: '#888' },
  tokenCard: { flexDirection: 'row', alignItems: 'center', backgroundColor: '#F9F9F9', borderRadius: 16, padding: 16, marginTop: 20 },
  tokenBadge: { width: 44, height: 44, borderRadius: 22, justifyContent: 'center', alignItems: 'center' },
  tokenBadgeText: { fontSize: 18, fontWeight: '700', color: '#FFF' },
  tokenName: { fontSize: 16, fontWeight: '700', color: '#1A1A1A' },
  tokenSymbol: { fontSize: 13, color: '#888' },
  detailsCard: { backgroundColor: '#F9F9F9', borderRadius: 12, padding: 14, marginTop: 12 },
  detailRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 6 },
  detailLabel: { fontSize: 13, color: '#888' },
  detailValue: { fontSize: 13, fontWeight: '600', color: '#1A1A1A', maxWidth: '60%' },
});
