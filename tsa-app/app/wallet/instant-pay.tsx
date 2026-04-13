import React, { useState, useEffect, useCallback } from 'react';
import {
  View, Text, TouchableOpacity, StyleSheet, TextInput, ScrollView,
  ActivityIndicator, KeyboardAvoidingView, Platform, Pressable, Modal, FlatList,
} from 'react-native';
import { router } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import { useFocusEffect } from '@react-navigation/native';
import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { signTransaction } from '../../services/wallet';
import {
  getWalletBalances,
  normalizeWalletBalances,
  prepareSendTransaction,
  submitTransaction,
  resolveUsername,
  type WalletBalanceWithChain,
  type ResolvedUser,
} from '../../services/walletApi';
import AuthorizationModal from '../../components/common/AuthorizationModal';

type BalanceEntry = WalletBalanceWithChain;

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

const InstantPay = () => {
  const { tokens, tokenList } = useTokens();


  const [username, setUsername] = useState('');
  const [amount, setAmount] = useState('');
  const [selectedToken, setSelectedToken] = useState('USDT');
  const [selectedChain, setSelectedChain] = useState<ChainKey>('sonic');
  const [balances, setBalances] = useState<BalanceEntry[]>([]);
  const [resolvedUser, setResolvedUser] = useState<ResolvedUser | null>(null);
  const [resolving, setResolving] = useState(false);
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [showTokenPicker, setShowTokenPicker] = useState(false);
  const [showAuthModal, setShowAuthModal] = useState(false);

  const getSupportedChainsForToken = useCallback((symbol: string): ChainKey[] => {
    const registryChains = tokens[symbol]?.chains ?? [];
    if (registryChains.length > 0) {
      return registryChains;
    }

    const inferredChains = Array.from(
      new Set(
        balances
          .filter((entry) => entry.symbol === symbol)
          .map((entry) => entry.chainKey)
      )
    );
    return inferredChains;
  }, [balances, tokens]);

  const loadBalances = useCallback(async () => {
    try {
      setLoading(true);
      const chainResults = await Promise.all(
        CHAIN_KEYS.map(async (chainKey) => {
          const result = await getWalletBalances(CHAINS[chainKey].chainId);
          return { chainKey, result };
        })
      );

      const allBalances: BalanceEntry[] = [];
      const seen = new Set<string>();

      for (const { chainKey, result } of chainResults) {
        if (!result.success || !result.data) continue;
        for (const entry of normalizeWalletBalances(result.data, chainKey)) {
          allBalances.push(entry);
          seen.add(`${entry.symbol}-${entry.chainKey}`);
        }
      }

      for (const t of tokenList) {
        for (const chain of t.chains) {
          const key = `${t.symbol}-${chain}`;
          if (!seen.has(key)) {
            allBalances.push({
              symbol: t.symbol, name: t.name, balance: '0', usdValue: '0.00',
              contractAddress: '', decimals: t.decimals,
              chainKey: chain as ChainKey, chainName: CHAINS[chain as ChainKey]?.shortName || chain,
            });
          }
        }
      }

      setBalances(allBalances);
    } catch (err) {
      console.error('InstantPay: load balances error:', err);
    } finally { setLoading(false); }
  }, [tokenList]);

  useFocusEffect(useCallback(() => { loadBalances(); }, [loadBalances]));

  useEffect(() => {
    const chains = getSupportedChainsForToken(selectedToken);
    if (chains.length === 0) return;
    if (!chains.includes(selectedChain)) {
      setSelectedChain(chains[0]);
    }
  }, [getSupportedChainsForToken, selectedChain, selectedToken]);

  const bal = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain)?.balance || '0';
  const activeChain = CHAINS[selectedChain];

  // Debounced username lookup
  useEffect(() => {
    if (username.trim().length < 3) { setResolvedUser(null); return; }
    const t = setTimeout(async () => {
      setResolving(true); setError('');
      try {
        const r = await resolveUsername(username.trim());
        if (r.success && r.data) setResolvedUser(r.data);
        else { setResolvedUser(null); setError(r.message || 'User not found'); }
      } catch { setResolvedUser(null); setError('Failed to look up user'); }
      finally { setResolving(false); }
    }, 600);
    return () => clearTimeout(t);
  }, [username]);

  const validate = (): string | null => {
    if (!username.trim()) return 'Enter a username.';
    if (!resolvedUser) return 'User not found.';
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    if (Number(amount) > Number(bal)) return 'Insufficient balance.';
    return null;
  };

  const handleReview = async () => {
    const e = validate(); if (e) { setError(e); return; }
    setError('');
    try {
      setLoading(true);
      const r = await prepareSendTransaction(selectedToken, resolvedUser!.walletAddress, amount, activeChain?.chainId || CHAINS.sonic.chainId);
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare transaction.');
      const gp = BigInt(r.data.gasPrice || '0');
      const gl = BigInt(r.data.gasLimit || '21000');
      setGasEstimate((Number(gp * gl) / 1e18).toFixed(6));
      setScreen('confirm');
    } catch (err: any) { setError(err.message || 'Failed to estimate gas.'); }
    finally { setLoading(false); }
  };

  const handleSendRequest = () => {
    if (!resolvedUser) return;
    setShowAuthModal(true);
  };

  const handleSend = async () => {
    setShowAuthModal(false);
    setScreen('sending'); setError('');
    try {
      const r = await prepareSendTransaction(selectedToken, resolvedUser!.walletAddress, amount, activeChain?.chainId || CHAINS.sonic.chainId);
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare');
      const signed = await signTransaction({
        type: 0,
        to: r.data.to, data: r.data.data, value: r.data.value,
        gasLimit: r.data.gasLimit, gasPrice: r.data.gasPrice,
        nonce: r.data.nonce, chainId: r.data.chainId,
      });
      const res = await submitTransaction(signed, 'send', selectedToken, resolvedUser!.walletAddress, amount, activeChain?.chainId || CHAINS.sonic.chainId);
      if (res.success && res.data?.txHash) { setTxHash(res.data.txHash); setScreen('success'); }
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
          <Text style={st.bigTitle}>{ok ? 'Payment Sent!' : 'Payment Failed'}</Text>
          <Text style={st.resMsg}>
            {ok ? `${amount} ${selectedToken} sent to @${resolvedUser?.username}` : error}
          </Text>
          {txHash ? <Text style={st.txH} numberOfLines={1} ellipsizeMode="middle">Tx: {txHash}</Text> : null}
          <TouchableOpacity style={st.btn} onPress={() => router.replace('/tokenization')}>
            <Text style={st.btnT}>Back to Wallet</Text>
          </TouchableOpacity>
          {ok && (
            <TouchableOpacity style={st.btn2} onPress={() => { setScreen('form'); setAmount(''); setUsername(''); setResolvedUser(null); }}>
              <Text style={st.btn2T}>Send Again</Text>
            </TouchableOpacity>
          )}
        </View>
      </View>
    );
  }

  // ── Sending ──
  if (screen === 'sending') return (
    <View style={st.container}>
      <View style={st.center}>
        <View style={st.signingIcon}><ActivityIndicator size="large" color={GOLD} /></View>
        <Text style={st.bigTitle}>Sending Payment</Text>
        <Text style={st.signingMsg}>Sending {amount} {selectedToken} to @{resolvedUser?.username}</Text>
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
        <Text style={st.bigTitle}>Confirm Payment</Text>

        {/* Recipient */}
        <View style={st.recipientCard}>
          <View style={st.recipientAvatar}>
            <Ionicons name="person" size={24} color={GOLD} />
          </View>
          <View style={{ flex: 1, marginLeft: 12 }}>
            <Text style={st.recipientName}>{resolvedUser?.name}</Text>
            <Text style={st.recipientUser}>@{resolvedUser?.username}</Text>
          </View>
          {resolvedUser?.verificationStatus === 'verified' && (
            <View style={st.verifiedChip}>
              <Ionicons name="checkmark-circle" size={14} color="#16A34A" />
              <Text style={st.verifiedText}>Verified</Text>
            </View>
          )}
        </View>

        {/* Details */}
        <View style={st.card}>
          <View style={st.detRow}>
            <Text style={st.detL}>Amount</Text>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
              <TokenBadge symbol={selectedToken} size={24} />
              <Text style={st.detVBold}>{amount} {selectedToken}</Text>
            </View>
          </View>
          <View style={st.divider} />
          <View style={st.detRow}>
            <Text style={st.detL}>Network</Text>
            <Text style={st.detV}>{activeChain?.name || selectedChain}</Text>
          </View>
          <View style={st.detRow}>
            <Text style={st.detL}>Gas Fee</Text>
            <Text style={st.detV}>{gasEstimate} {activeChain?.nativeCurrency?.symbol || 'S'}</Text>
          </View>
        </View>

        <TouchableOpacity style={st.btn} onPress={handleSendRequest}>
          <Ionicons name="flash" size={18} color="#FFF" style={{ marginRight: 8 }} />
          <Text style={st.btnT}>Confirm & Pay</Text>
        </TouchableOpacity>
        <TouchableOpacity style={st.btn2} onPress={() => setScreen('form')}>
          <Text style={st.btn2T}>Cancel</Text>
        </TouchableOpacity>
      </ScrollView>

      <AuthorizationModal
        visible={showAuthModal}
        title="Authorize Transfer"
        description={`Send ${amount} ${selectedToken} to @${resolvedUser?.username}`}
        onAuthorized={handleSend}
        onCancel={() => setShowAuthModal(false)}
      />
    </View>
  );

  // ── Form ──
  return (
    <KeyboardAvoidingView style={st.container} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
      <ScrollView contentContainerStyle={st.scroll} showsVerticalScrollIndicator={false} keyboardShouldPersistTaps="handled">
        {/* Header */}
        <View style={st.headerIllustration}>
          <View style={st.headerIconCircle}>
            <Ionicons name="flash" size={28} color={GOLD} />
          </View>
          <Text style={st.bigTitle}>Instant Pay</Text>
          <Text style={st.sub}>Send crypto to any TSA Connect user</Text>
        </View>

        {/* Recipient */}
        <View style={st.card}>
          <Text style={st.cardLabel}>RECIPIENT</Text>
          <View style={st.usernameRow}>
            <View style={st.atBadge}><Text style={st.atText}>@</Text></View>
            <TextInput
              style={st.usernameInput}
              placeholder="username"
              placeholderTextColor="#CCC"
              value={username}
              onChangeText={t => { setUsername(t.replace(/\s/g, '')); setError(''); }}
              autoCapitalize="none"
              autoCorrect={false}
            />
            {resolving && <ActivityIndicator size="small" color={GOLD} />}
          </View>

          {resolvedUser && (
            <View style={st.resolvedRow}>
              <View style={st.resolvedAvatar}>
                <Ionicons name="person" size={16} color={GOLD} />
              </View>
              <View style={{ flex: 1 }}>
                <Text style={st.resolvedName}>{resolvedUser.name}</Text>
                <Text style={st.resolvedAddr} numberOfLines={1} ellipsizeMode="middle">{resolvedUser.walletAddress}</Text>
              </View>
              {resolvedUser.verificationStatus === 'verified' && (
                <Ionicons name="checkmark-circle" size={18} color="#16A34A" />
              )}
            </View>
          )}
        </View>

        {/* Amount + Token */}
        <View style={st.card}>
          <View style={st.amountHeader}>
            <Text style={st.cardLabel}>AMOUNT</Text>
            <TouchableOpacity onPress={() => setAmount(bal)}>
              <Text style={st.balText}>Balance: {bal}</Text>
            </TouchableOpacity>
          </View>
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
        </View>

        {/* Error */}
        {error ? (
          <View style={st.errorBar}>
            <Ionicons name="alert-circle" size={16} color="#DC2626" />
            <Text style={st.errorText}>{error}</Text>
          </View>
        ) : null}

        {/* Action */}
        <TouchableOpacity
          style={[st.btn, (loading || resolving) && { opacity: 0.4 }]}
          onPress={handleReview}
          disabled={loading || resolving}
        >
          {loading ? <ActivityIndicator color="#FFF" /> : (
            <>
              <Ionicons name="flash" size={18} color="#FFF" style={{ marginRight: 8 }} />
              <Text style={st.btnT}>Review Payment</Text>
            </>
          )}
        </TouchableOpacity>
      </ScrollView>

      {/* Token + Chain Picker */}
      <Modal visible={showTokenPicker} transparent animationType="slide">
        <Pressable style={st.modalOverlay} onPress={() => setShowTokenPicker(false)}>
          <View style={st.modalContent}>
            <Text style={st.modalTitle}>Select Token</Text>
            <FlatList
              data={[...(balances as BalanceEntry[])].sort((a, b) => parseFloat(b.balance) - parseFloat(a.balance))}
              keyExtractor={item => `${item.symbol}-${item.chainKey}`}
              renderItem={({ item }) => {
                const active = item.symbol === selectedToken && item.chainKey === selectedChain;
                const balVal = parseFloat(item.balance);
                return (
                  <TouchableOpacity
                    style={[st.modalItem, active && st.modalItemActive]}
                    onPress={() => {
                      setSelectedToken(item.symbol);
                      setSelectedChain(item.chainKey);
                      setShowTokenPicker(false);
                    }}
                  >
                    <TokenBadge symbol={item.symbol} size={36} />
                    <View style={{ flex: 1, marginLeft: 12 }}>
                      <Text style={st.modalItemSym}>{item.symbol}</Text>
                      <Text style={st.modalItemName}>{item.name} · {item.chainName}</Text>
                    </View>
                    <View style={{ alignItems: 'flex-end' }}>
                      <Text style={st.modalItemBal}>
                        {balVal > 0 ? balVal.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '0'}
                      </Text>
                      {parseFloat(item.usdValue) > 0 && (
                        <Text style={{ fontSize: 11, color: '#AAA' }}>${parseFloat(item.usdValue).toFixed(2)}</Text>
                      )}
                    </View>
                  </TouchableOpacity>
                );
              }}
            />
          </View>
        </Pressable>
      </Modal>
    </KeyboardAvoidingView>
  );
};

export default InstantPay;

const st = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
  scroll: { flexGrow: 1, padding: 20, paddingBottom: 40 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 24 },

  // Header
  headerIllustration: { alignItems: 'center', marginBottom: 24, paddingTop: 8 },
  headerIconCircle: { width: 56, height: 56, borderRadius: 28, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center', marginBottom: 12 },
  bigTitle: { fontSize: 26, fontWeight: '700', color: '#1A1A1A' },
  sub: { fontSize: 13, color: '#888', marginTop: 4 },

  // Cards
  card: {
    backgroundColor: '#FFF', borderRadius: 16, padding: 16, marginBottom: 12,
    borderWidth: 1, borderColor: '#F0F0F0',
    elevation: 2, shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.04, shadowRadius: 8,
  },
  cardLabel: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1, marginBottom: 12 },

  // Username
  usernameRow: { flexDirection: 'row', alignItems: 'center', gap: 0 },
  atBadge: { width: 40, height: 48, backgroundColor: '#FFF8E1', borderRadius: 12, borderTopRightRadius: 0, borderBottomRightRadius: 0, justifyContent: 'center', alignItems: 'center' },
  atText: { fontSize: 18, fontWeight: '700', color: GOLD },
  usernameInput: { flex: 1, fontSize: 18, fontWeight: '600', color: '#1A1A1A', backgroundColor: '#F9F9F9', height: 48, paddingHorizontal: 12, borderRadius: 12, borderTopLeftRadius: 0, borderBottomLeftRadius: 0 },

  resolvedRow: { flexDirection: 'row', alignItems: 'center', marginTop: 12, backgroundColor: '#F0FDF4', borderRadius: 12, padding: 10, gap: 8 },
  resolvedAvatar: { width: 32, height: 32, borderRadius: 16, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center' },
  resolvedName: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  resolvedAddr: { fontSize: 11, color: '#888', maxWidth: 180 },

  // Amount
  amountHeader: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 },
  balText: { fontSize: 12, color: GOLD, fontWeight: '500' },
  amountRow: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  tokenChip: { flexDirection: 'row', alignItems: 'center', gap: 6, backgroundColor: '#F5F5F5', paddingHorizontal: 10, paddingVertical: 8, borderRadius: 24 },
  tokenChipText: { fontSize: 15, fontWeight: '700', color: '#1A1A1A' },
  amountInput: { flex: 1, fontSize: 28, fontWeight: '700', color: '#1A1A1A', textAlign: 'right', padding: 0 },

  // Error
  errorBar: { flexDirection: 'row', alignItems: 'center', gap: 6, backgroundColor: '#FEF2F2', borderWidth: 1, borderColor: '#FECACA', borderRadius: 10, padding: 10, marginBottom: 8 },
  errorText: { fontSize: 12, color: '#DC2626', flex: 1 },

  // Buttons
  btn: { flexDirection: 'row', backgroundColor: GOLD, paddingVertical: 16, paddingHorizontal: 32, borderRadius: 14, alignItems: 'center', justifyContent: 'center', alignSelf: 'stretch', marginTop: 16, marginHorizontal: 20, elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.25, shadowRadius: 8 },
  btnT: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  btn2: { paddingVertical: 14, borderRadius: 14, alignItems: 'center', marginTop: 10 },
  btn2T: { fontSize: 15, color: '#888', fontWeight: '600' },

  backRow: { flexDirection: 'row', alignItems: 'center', marginBottom: 16, gap: 4 },
  backT: { fontSize: 15, color: GOLD, fontWeight: '600' },

  // Badge
  badge: { justifyContent: 'center', alignItems: 'center' },

  // Confirm
  recipientCard: { flexDirection: 'row', alignItems: 'center', backgroundColor: '#FFF', borderRadius: 16, padding: 16, marginBottom: 12, borderWidth: 1, borderColor: '#F0F0F0', elevation: 2, shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.04, shadowRadius: 8 },
  recipientAvatar: { width: 44, height: 44, borderRadius: 22, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center' },
  recipientName: { fontSize: 16, fontWeight: '700', color: '#1A1A1A' },
  recipientUser: { fontSize: 13, color: '#888' },
  verifiedChip: { flexDirection: 'row', alignItems: 'center', gap: 4, backgroundColor: '#F0FDF4', paddingHorizontal: 8, paddingVertical: 4, borderRadius: 12 },
  verifiedText: { fontSize: 11, fontWeight: '600', color: '#16A34A' },
  detRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: 8 },
  detL: { fontSize: 13, color: '#888' },
  detV: { fontSize: 13, fontWeight: '600', color: '#1A1A1A' },
  detVBold: { fontSize: 18, fontWeight: '700', color: '#1A1A1A' },
  divider: { height: 1, backgroundColor: '#F0F0F0', marginVertical: 4 },

  // Signing
  signingIcon: { width: 80, height: 80, borderRadius: 40, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  signingMsg: { fontSize: 15, color: GOLD, fontWeight: '600' },

  // Result
  resIcon: { width: 80, height: 80, borderRadius: 40, justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  resMsg: { fontSize: 15, color: '#666', textAlign: 'center', marginBottom: 20, paddingHorizontal: 24 },
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
