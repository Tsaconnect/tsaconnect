import React, { useState, useEffect, useCallback } from 'react';
import {
  View, Text, TouchableOpacity, StyleSheet, TextInput, ScrollView,
  ActivityIndicator, KeyboardAvoidingView, Platform, Pressable, Modal, FlatList,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import { CHAINS, type ChainKey } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { isValidAddress, signTransaction } from '../../services/wallet';
import {
  getWalletBalances, prepareSendTransaction, submitTransaction, WalletBalance,
} from '../../services/walletApi';
import { useKycVerification } from '../../hooks/useKycVerification';

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';
const GOLD = '#D4AF37';

const TOKEN_COLORS: Record<string, string> = {
  MCGP: '#D4AF37', USDC: '#2775CA', USDT: '#26A17B', S: '#5B21B6', BNB: '#F0B90B',
};

const TokenBadge = ({ symbol, size = 36 }: { symbol: string; size?: number }) => (
  <View style={[st.badge, { width: size, height: size, borderRadius: size / 2, backgroundColor: TOKEN_COLORS[symbol] || '#888' }]}>
    <Text style={{ fontSize: size * 0.38, fontWeight: '700', color: '#FFF' }}>{symbol[0]}</Text>
  </View>
);

const SendToken = () => {
  const { tokens, tokenList, getChainsForToken } = useTokens();
  const { requireKycVerified } = useKycVerification();
  const [selectedToken, setSelectedToken] = useState('USDT');
  const [selectedChain, setSelectedChain] = useState<ChainKey>('sonic');
  const [toAddress, setToAddress] = useState('');
  const [amount, setAmount] = useState('');
  const [inputMode, setInputMode] = useState<'token' | 'usd'>('token');
  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [showTokenPicker, setShowTokenPicker] = useState(false);

  useEffect(() => {
    const chains = tokens[selectedToken]?.chains ?? [];
    if (!chains.includes(selectedChain)) setSelectedChain(chains[0]);
  }, [selectedToken]);

  const loadBalances = useCallback(async () => {
    try {
      setLoading(true);
      const chainId = CHAINS[selectedChain]?.chainId;
      const result = await getWalletBalances(chainId);
      if (result.success && result.data) {
        const d = result.data as any;
        let fetched: WalletBalance[] = [];
        if (Array.isArray(d)) { fetched = d; }
        else if (d.balances) {
          for (const [, cb] of Object.entries(d.balances as Record<string, any>)) {
            for (const [sym, info] of Object.entries(cb as Record<string, any>)) {
              if (info && typeof info === 'object') {
                fetched.push({ symbol: sym, name: sym, balance: info.balance || '0', usdValue: String(info.usdValue || 0), contractAddress: '', decimals: sym === 'MCGP' ? 18 : 6 });
              }
            }
          }
        }
        // Merge with tokenList so all tokens appear
        const balanceMap = new Map(fetched.map(b => [b.symbol, b]));
        const merged: WalletBalance[] = tokenList.map(t =>
          balanceMap.get(t.symbol) ?? { symbol: t.symbol, name: t.name, balance: '0', usdValue: '0.00', contractAddress: '', decimals: t.decimals }
        );
        for (const b of fetched) {
          if (!merged.some(m => m.symbol === b.symbol)) merged.push(b);
        }
        setBalances(merged);
      }
    } catch (err) {
      console.error('Send: load balances error:', err);
    } finally { setLoading(false); }
  }, [selectedChain, tokenList]);

  useEffect(() => { loadBalances(); }, [loadBalances]);

  const getBalance = (): string => balances.find(b => b.symbol === selectedToken)?.balance || '0';
  const getUsdPrice = (): number => {
    const b = balances.find(b => b.symbol === selectedToken);
    if (!b) return selectedToken === 'USDC' || selectedToken === 'USDT' ? 1 : 0;
    const bal = parseFloat(b.balance || '0');
    const usd = parseFloat(b.usdValue || '0');
    return bal > 0 ? usd / bal : (selectedToken === 'USDC' || selectedToken === 'USDT' ? 1 : 0);
  };

  const tokenAmount = inputMode === 'token' ? amount : (
    amount && getUsdPrice() > 0 ? (parseFloat(amount) / getUsdPrice()).toFixed(6) : '0'
  );
  const usdAmount = inputMode === 'usd' ? amount : (
    amount && getUsdPrice() > 0 ? (parseFloat(amount) * getUsdPrice()).toFixed(2) : '0'
  );

  const activeChain = CHAINS[selectedChain];

  const handlePaste = async () => {
    try {
      const text = await Clipboard.getStringAsync();
      if (text) setToAddress(text.trim());
    } catch {}
  };

  const validate = (): string | null => {
    if (!toAddress.trim()) return 'Enter a recipient address.';
    if (!isValidAddress(toAddress.trim())) return 'Invalid wallet address.';
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    const sendAmount = parseFloat(tokenAmount);
    if (sendAmount > parseFloat(getBalance())) return 'Insufficient balance.';
    return null;
  };

  const handleReview = async () => {
    const e = validate(); if (e) { setError(e); return; }
    setError('');
    try {
      setLoading(true);
      const r = await prepareSendTransaction(selectedToken, toAddress.trim(), tokenAmount, activeChain.chainId);
      if (r.success && r.data) {
        const gp = BigInt(r.data.gasPrice || '0'), gl = BigInt(r.data.gasLimit || '21000');
        setGasEstimate((Number(gp * gl) / 1e18).toFixed(6));
      }
      setScreen('confirm');
    } catch { setError('Failed to estimate gas.'); }
    finally { setLoading(false); }
  };

  const handleSend = async () => {
    if (!requireKycVerified()) return;
    setScreen('sending'); setError('');
    try {
      const r = await prepareSendTransaction(selectedToken, toAddress.trim(), tokenAmount, activeChain.chainId);
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare');
      const signed = await signTransaction({
        to: r.data.to, data: r.data.data, value: r.data.value,
        gasLimit: r.data.gasLimit, gasPrice: r.data.gasPrice,
        nonce: r.data.nonce, chainId: r.data.chainId,
      });
      const res = await submitTransaction(signed, 'send', selectedToken, toAddress.trim(), tokenAmount, activeChain.chainId);
      if (res.success && res.data) { setTxHash(res.data.txHash); setScreen('success'); }
      else throw new Error(res.message || 'Failed');
    } catch (e: any) { setError(e.message || 'Failed.'); setScreen('failure'); }
  };

  // ── Result ──
  if (screen === 'success' || screen === 'failure') {
    const ok = screen === 'success';
    return (
      <View style={st.container}>
        <View style={st.center}>
          <View style={[st.resIcon, { backgroundColor: ok ? '#D1FAE5' : '#FEE2E2' }]}>
            <Ionicons name={ok ? 'checkmark-circle' : 'close-circle'} size={48} color={ok ? '#16A34A' : '#DC2626'} />
          </View>
          <Text style={st.bigTitle}>{ok ? 'Sent!' : 'Failed'}</Text>
          <Text style={st.resMsg}>
            {ok ? `${tokenAmount} ${selectedToken} sent` : error}
          </Text>
          {ok && <Text style={st.resUsd}>≈ ${usdAmount}</Text>}
          {txHash ? <Text style={st.txH} numberOfLines={1} ellipsizeMode="middle">Tx: {txHash}</Text> : null}
          <TouchableOpacity style={st.btn} onPress={() => router.replace('/tokenization')}>
            <Text style={st.btnT}>Back to Wallet</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  // ── Sending ──
  if (screen === 'sending') return (
    <View style={st.container}>
      <View style={st.center}>
        <View style={st.signingIcon}><ActivityIndicator size="large" color={GOLD} /></View>
        <Text style={st.bigTitle}>Sending</Text>
        <Text style={st.signingMsg}>{tokenAmount} {selectedToken} → {toAddress.slice(0, 8)}...</Text>
      </View>
    </View>
  );

  // ── Confirm ──
  if (screen === 'confirm') return (
    <View style={st.container}>
      <ScrollView contentContainerStyle={st.scroll}>
        <Pressable onPress={() => setScreen('form')} style={st.backRow}>
          <Ionicons name="chevron-back" size={22} color={GOLD} />
          <Text style={st.backT}>Back</Text>
        </Pressable>
        <Text style={st.bigTitle}>Confirm Send</Text>

        <View style={st.card}>
          <View style={st.detRow}>
            <Text style={st.detL}>Amount</Text>
            <View style={{ alignItems: 'flex-end' }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
                <TokenBadge symbol={selectedToken} size={24} />
                <Text style={st.detVBold}>{tokenAmount} {selectedToken}</Text>
              </View>
              <Text style={st.detUsd}>≈ ${usdAmount}</Text>
            </View>
          </View>
          <View style={st.divider} />
          <View style={st.detRow}>
            <Text style={st.detL}>To</Text>
            <Text style={[st.detV, { maxWidth: 180 }]} numberOfLines={1} ellipsizeMode="middle">{toAddress}</Text>
          </View>
          <View style={st.detRow}>
            <Text style={st.detL}>Network</Text>
            <Text style={st.detV}>{activeChain.name}</Text>
          </View>
          <View style={st.detRow}>
            <Text style={st.detL}>Gas Fee</Text>
            <Text style={st.detV}>{gasEstimate} {activeChain.nativeCurrency.symbol}</Text>
          </View>
        </View>

        <TouchableOpacity style={st.btn} onPress={handleSend}>
          <Ionicons name="send" size={18} color="#FFF" style={{ marginRight: 8 }} />
          <Text style={st.btnT}>Confirm & Send</Text>
        </TouchableOpacity>
        <TouchableOpacity style={st.btn2} onPress={() => setScreen('form')}>
          <Text style={st.btn2T}>Cancel</Text>
        </TouchableOpacity>
      </ScrollView>
    </View>
  );

  // ── Form ──
  const price = getUsdPrice();

  return (
    <KeyboardAvoidingView style={st.container} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
      <ScrollView contentContainerStyle={st.scroll} showsVerticalScrollIndicator={false} keyboardShouldPersistTaps="handled">
        <Text style={st.bigTitle}>Send</Text>
        <Text style={st.sub}>Transfer tokens to any wallet</Text>

        {/* Amount card */}
        <View style={st.card}>
          <View style={st.amountHeader}>
            <Text style={st.cardLabel}>AMOUNT</Text>
            <TouchableOpacity onPress={() => {
              setInputMode('token');
              setAmount(getBalance());
            }}>
              <Text style={st.balText}>Balance: {getBalance()} {selectedToken}</Text>
            </TouchableOpacity>
          </View>

          {/* Token selector + Amount input */}
          <View style={st.amountRow}>
            <TouchableOpacity style={st.tokenChip} onPress={() => setShowTokenPicker(true)}>
              <TokenBadge symbol={selectedToken} size={28} />
              <Text style={st.tokenChipText}>{selectedToken}</Text>
              <Ionicons name="chevron-down" size={16} color="#888" />
            </TouchableOpacity>
            <TextInput
              style={st.amountInput}
              placeholder="0.00"
              placeholderTextColor="#CCC"
              value={amount}
              onChangeText={t => { setAmount(t); setError(''); }}
              keyboardType="decimal-pad"
            />
          </View>

          {/* USD equivalent + toggle */}
          <View style={st.usdRow}>
            <Text style={st.usdText}>
              {inputMode === 'token'
                ? `≈ $${amount && price > 0 ? (parseFloat(amount) * price).toFixed(2) : '0.00'}`
                : `≈ ${amount && price > 0 ? (parseFloat(amount) / price).toFixed(6) : '0'} ${selectedToken}`}
            </Text>
            <TouchableOpacity
              style={st.toggleBtn}
              onPress={() => {
                if (inputMode === 'token' && price > 0 && amount) {
                  setAmount((parseFloat(amount) * price).toFixed(2));
                } else if (inputMode === 'usd' && price > 0 && amount) {
                  setAmount((parseFloat(amount) / price).toFixed(6));
                }
                setInputMode(m => m === 'token' ? 'usd' : 'token');
              }}
            >
              <Ionicons name="swap-vertical" size={16} color={GOLD} />
              <Text style={st.toggleText}>{inputMode === 'token' ? 'USD' : selectedToken}</Text>
            </TouchableOpacity>
          </View>
        </View>

        {/* Recipient */}
        <View style={st.card}>
          <Text style={st.cardLabel}>RECIPIENT</Text>
          <View style={st.addressRow}>
            <TextInput
              style={st.addressInput}
              placeholder="0x... wallet address"
              placeholderTextColor="#CCC"
              value={toAddress}
              onChangeText={t => { setToAddress(t); setError(''); }}
              autoCapitalize="none"
              autoCorrect={false}
            />
            <TouchableOpacity style={st.pasteBtn} onPress={handlePaste}>
              <Ionicons name="clipboard-outline" size={18} color={GOLD} />
            </TouchableOpacity>
          </View>
          {toAddress && !isValidAddress(toAddress) && (
            <Text style={st.addrHint}>Invalid address format</Text>
          )}
        </View>

        {/* Network info */}
        <View style={st.networkRow}>
          <Ionicons name="globe-outline" size={16} color="#888" />
          <Text style={st.networkText}>{activeChain.name}</Text>
        </View>

        {error ? (
          <View style={st.errorBar}>
            <Ionicons name="alert-circle" size={16} color="#DC2626" />
            <Text style={st.errorText}>{error}</Text>
          </View>
        ) : null}

        <TouchableOpacity
          style={[st.btn, loading && { opacity: 0.4 }]}
          onPress={handleReview}
          disabled={loading}
        >
          {loading ? <ActivityIndicator color="#FFF" /> : (
            <>
              <Ionicons name="send" size={18} color="#FFF" style={{ marginRight: 8 }} />
              <Text style={st.btnT}>Review</Text>
            </>
          )}
        </TouchableOpacity>
      </ScrollView>

      {/* Token Picker */}
      <Modal visible={showTokenPicker} transparent animationType="slide">
        <Pressable style={st.modalOverlay} onPress={() => setShowTokenPicker(false)}>
          <View style={st.modalContent}>
            <Text style={st.modalTitle}>Select Token</Text>
            <FlatList
              data={tokenList}
              keyExtractor={t => t.symbol}
              renderItem={({ item }) => (
                <TouchableOpacity
                  style={[st.modalItem, item.symbol === selectedToken && st.modalItemActive]}
                  onPress={() => { setSelectedToken(item.symbol); setShowTokenPicker(false); setInputMode('token'); setAmount(''); }}
                >
                  <TokenBadge symbol={item.symbol} size={36} />
                  <View style={{ flex: 1, marginLeft: 12 }}>
                    <Text style={st.modalItemSym}>{item.symbol}</Text>
                    <Text style={st.modalItemName}>{item.name}</Text>
                  </View>
                  <Text style={st.modalItemBal}>{balances.find(b => b.symbol === item.symbol)?.balance || '0'}</Text>
                </TouchableOpacity>
              )}
            />
          </View>
        </Pressable>
      </Modal>
    </KeyboardAvoidingView>
  );
};

export default SendToken;

const st = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
  scroll: { flexGrow: 1, padding: 20, paddingBottom: 40 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 24 },

  bigTitle: { fontSize: 26, fontWeight: '700', color: '#1A1A1A' },
  sub: { fontSize: 13, color: '#888', marginTop: 4, marginBottom: 20 },

  card: {
    backgroundColor: '#FFF', borderRadius: 16, padding: 16, marginBottom: 12,
    borderWidth: 1, borderColor: '#F0F0F0',
    elevation: 2, shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.04, shadowRadius: 8,
  },
  cardLabel: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1, marginBottom: 12 },
  amountHeader: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 },
  balText: { fontSize: 12, color: GOLD, fontWeight: '500' },

  amountRow: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  tokenChip: { flexDirection: 'row', alignItems: 'center', gap: 6, backgroundColor: '#F5F5F5', paddingHorizontal: 10, paddingVertical: 8, borderRadius: 24 },
  tokenChipText: { fontSize: 15, fontWeight: '700', color: '#1A1A1A' },
  amountInput: { flex: 1, fontSize: 28, fontWeight: '700', color: '#1A1A1A', textAlign: 'right', padding: 0 },

  usdRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginTop: 10 },
  usdText: { fontSize: 14, color: '#888' },
  toggleBtn: { flexDirection: 'row', alignItems: 'center', gap: 4, backgroundColor: '#FFF8E1', paddingHorizontal: 10, paddingVertical: 6, borderRadius: 16 },
  toggleText: { fontSize: 12, fontWeight: '600', color: GOLD },

  addressRow: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  addressInput: { flex: 1, fontSize: 15, color: '#1A1A1A', backgroundColor: '#F9F9F9', height: 48, paddingHorizontal: 12, borderRadius: 12 },
  pasteBtn: { width: 44, height: 44, borderRadius: 12, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center' },
  addrHint: { fontSize: 12, color: '#DC2626', marginTop: 6 },

  networkRow: { flexDirection: 'row', alignItems: 'center', gap: 6, marginBottom: 12, marginLeft: 4 },
  networkText: { fontSize: 13, color: '#888' },

  errorBar: { flexDirection: 'row', alignItems: 'center', gap: 6, backgroundColor: '#FEF2F2', borderWidth: 1, borderColor: '#FECACA', borderRadius: 10, padding: 10, marginBottom: 8 },
  errorText: { fontSize: 12, color: '#DC2626', flex: 1 },

  btn: { flexDirection: 'row', backgroundColor: GOLD, paddingVertical: 16, borderRadius: 14, alignItems: 'center', justifyContent: 'center', marginTop: 16, elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.25, shadowRadius: 8 },
  btnT: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  btn2: { paddingVertical: 14, borderRadius: 14, alignItems: 'center', marginTop: 10 },
  btn2T: { fontSize: 15, color: '#888', fontWeight: '600' },

  badge: { justifyContent: 'center', alignItems: 'center' },
  backRow: { flexDirection: 'row', alignItems: 'center', marginBottom: 16, gap: 4 },
  backT: { fontSize: 15, color: GOLD, fontWeight: '600' },

  // Confirm
  detRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: 8 },
  detL: { fontSize: 13, color: '#888' },
  detV: { fontSize: 13, fontWeight: '600', color: '#1A1A1A' },
  detVBold: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },
  detUsd: { fontSize: 12, color: '#888', marginTop: 2 },
  divider: { height: 1, backgroundColor: '#F0F0F0', marginVertical: 4 },

  // Signing
  signingIcon: { width: 80, height: 80, borderRadius: 40, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  signingMsg: { fontSize: 15, color: GOLD, fontWeight: '600' },

  // Result
  resIcon: { width: 80, height: 80, borderRadius: 40, justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  resMsg: { fontSize: 15, color: '#666', textAlign: 'center', marginBottom: 4, paddingHorizontal: 24 },
  resUsd: { fontSize: 14, color: '#888', marginBottom: 20 },
  txH: { fontSize: 12, color: '#AAA', marginBottom: 24, maxWidth: '80%' },

  // Token modal
  modalOverlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'flex-end' },
  modalContent: { backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24, padding: 20, maxHeight: '60%' },
  modalTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginBottom: 16, textAlign: 'center' },
  modalItem: { flexDirection: 'row', alignItems: 'center', paddingVertical: 14, paddingHorizontal: 8, borderRadius: 12 },
  modalItemActive: { backgroundColor: '#FFF8E1' },
  modalItemSym: { fontSize: 16, fontWeight: '700', color: '#1A1A1A' },
  modalItemName: { fontSize: 13, color: '#888' },
  modalItemBal: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
});
