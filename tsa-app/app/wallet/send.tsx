import React, { useState, useEffect, useCallback } from 'react';
import {
  View, Text, TouchableOpacity, StyleSheet, TextInput, ScrollView,
  ActivityIndicator, KeyboardAvoidingView, Platform, Pressable, Modal, FlatList,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router, useLocalSearchParams } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { isValidAddress, signTransaction } from '../../services/wallet';
import {
  getWalletBalances,
  normalizeWalletBalances,
  prepareSendTransaction,
  submitTransaction,
  type WalletBalanceWithChain,
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

const SendToken = () => {
  const { tokens, tokenList } = useTokens();

  // Discovered-token params (set when arriving from the wallet/token detail
  // screen for an auto-discovered ERC-20). When present, we skip the
  // per-chain balance scan and prepare the tx with explicit contract +
  // decimals, since the token isn't in supported_tokens.
  const navParams = useLocalSearchParams<{
    token?: string;
    chainKey?: string;
    contractAddress?: string;
    discovered?: string;
    decimals?: string;
    balance?: string;
    usdValue?: string;
    name?: string;
  }>();
  const isDiscovered = navParams.discovered === '1' && !!navParams.contractAddress;
  const discoveredDecimals = navParams.decimals ? parseInt(navParams.decimals, 10) : undefined;

  const [selectedToken, setSelectedToken] = useState(navParams.token || 'USDT');
  const [selectedChain, setSelectedChain] = useState<ChainKey>(
    (navParams.chainKey as ChainKey) || 'sonic',
  );
  const [toAddress, setToAddress] = useState('');
  const [amount, setAmount] = useState('');
  const [inputMode, setInputMode] = useState<'token' | 'usd'>('token');
  const [balances, setBalances] = useState<BalanceEntry[]>([]);
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

  useEffect(() => {
    // Discovered tokens come pre-pinned to a single chain via nav params;
    // don't let the per-chain auto-correct overwrite that selection.
    if (isDiscovered) return;
    const chains = getSupportedChainsForToken(selectedToken);
    if (chains.length === 0) return;
    if (!chains.includes(selectedChain)) {
      setSelectedChain(chains[0]);
    }
  }, [getSupportedChainsForToken, selectedChain, selectedToken, isDiscovered]);

  const loadBalances = useCallback(async () => {
    // Discovered token: skip the multi-chain scan entirely (the token
    // isn't in supported_tokens, so it wouldn't appear) and seed a
    // single synthetic entry from the nav params.
    if (isDiscovered) {
      setBalances([{
        symbol: navParams.token || '',
        name: navParams.name || navParams.token || '',
        balance: navParams.balance || '0',
        usdValue: navParams.usdValue || '0',
        contractAddress: navParams.contractAddress || '',
        decimals: discoveredDecimals ?? 18,
        chainKey: (navParams.chainKey as ChainKey) || 'sonic',
        chainName: CHAINS[(navParams.chainKey as ChainKey) || 'sonic']?.shortName || (navParams.chainKey as string) || '',
      }]);
      setLoading(false);
      return;
    }
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

      // Add 0-balance entries for supported tokens not returned by API
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
      console.error('Send: load balances error:', err);
    } finally { setLoading(false); }
  }, [tokenList, isDiscovered, navParams.token, navParams.name, navParams.balance, navParams.usdValue, navParams.contractAddress, navParams.chainKey, discoveredDecimals]);

  useEffect(() => { loadBalances(); }, [loadBalances]);

  const getBalance = (): string => {
    const entry = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain);
    return entry?.balance || '0';
  };
  const getUsdPrice = (): number => {
    const b = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain);
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
      const r = await prepareSendTransaction(
        selectedToken,
        toAddress.trim(),
        tokenAmount,
        activeChain?.chainId || CHAINS.sonic.chainId,
        isDiscovered
          ? {
              tokenAddress: navParams.contractAddress,
              decimals: discoveredDecimals,
            }
          : undefined,
      );
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare transaction.');
      try {
        const gpStr = r.data.gasPrice || '0';
        const glStr = r.data.gasLimit || '21000';
        const gp = BigInt(gpStr);
        const gl = BigInt(glStr);
        const gasFee = Number(gp * gl) / 1e18;
        setGasEstimate(gasFee > 0 ? gasFee.toFixed(6) : '~0.001');
      } catch {
        setGasEstimate('~0.001');
      }
      setScreen('confirm');
    } catch (err: any) { setError(err.message || 'Failed to estimate gas.'); }
    finally { setLoading(false); }
  };

  const handleSend = async () => {

    setScreen('sending'); setError('');
    try {
      const r = await prepareSendTransaction(
        selectedToken,
        toAddress.trim(),
        tokenAmount,
        activeChain?.chainId || CHAINS.sonic.chainId,
        isDiscovered
          ? {
              tokenAddress: navParams.contractAddress,
              decimals: discoveredDecimals,
            }
          : undefined,
      );
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare');
      const signed = await signTransaction({
        type: 0, // legacy transaction format
        to: r.data.to, data: r.data.data, value: r.data.value,
        gasLimit: r.data.gasLimit, gasPrice: r.data.gasPrice,
        nonce: r.data.nonce, chainId: r.data.chainId,
      });
      const res = await submitTransaction(signed, 'send', selectedToken, toAddress.trim(), tokenAmount, activeChain?.chainId || CHAINS.sonic.chainId);
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
            <Text style={st.detV}>{activeChain?.name || selectedChain}</Text>
          </View>
          <View style={st.detRow}>
            <Text style={st.detL}>Gas Fee</Text>
            <Text style={st.detV}>{gasEstimate} {activeChain?.nativeCurrency?.symbol || 'S'}</Text>
          </View>
        </View>

        <TouchableOpacity style={st.btn} onPress={() => setShowAuthModal(true)}>
          <Ionicons name="send" size={18} color="#FFF" style={{ marginRight: 8 }} />
          <Text style={st.btnT}>Confirm & Send</Text>
        </TouchableOpacity>
        <TouchableOpacity style={st.btn2} onPress={() => setScreen('form')}>
          <Text style={st.btn2T}>Cancel</Text>
        </TouchableOpacity>
      </ScrollView>

      <AuthorizationModal
        visible={showAuthModal}
        title="Authorize Transfer"
        description={`Send ${tokenAmount} ${selectedToken} to ${toAddress.slice(0, 8)}...${toAddress.slice(-4)}`}
        onAuthorized={() => { setShowAuthModal(false); handleSend(); }}
        onCancel={() => setShowAuthModal(false)}
      />
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
          <Text style={st.networkText}>{activeChain?.name || selectedChain}</Text>
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
                const bal = parseFloat(item.balance);
                return (
                  <TouchableOpacity
                    style={[st.modalItem, active && st.modalItemActive]}
                    onPress={() => {
                      setSelectedToken(item.symbol);
                      setSelectedChain(item.chainKey);
                      setShowTokenPicker(false);
                      setInputMode('token');
                      setAmount('');
                    }}
                  >
                    <TokenBadge symbol={item.symbol} size={36} />
                    <View style={{ flex: 1, marginLeft: 12 }}>
                      <Text style={st.modalItemSym}>{item.symbol}</Text>
                      <Text style={st.modalItemName}>{item.name} · {item.chainName}</Text>
                    </View>
                    <View style={{ alignItems: 'flex-end' }}>
                      <Text style={st.modalItemBal}>
                        {bal > 0 ? bal.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '0'}
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

  btn: { flexDirection: 'row', backgroundColor: GOLD, paddingVertical: 16, paddingHorizontal: 32, borderRadius: 14, alignItems: 'center', justifyContent: 'center', alignSelf: 'stretch', marginTop: 16, marginHorizontal: 20, elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.25, shadowRadius: 8 },
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
