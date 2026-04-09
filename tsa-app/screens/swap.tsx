// screens/swap.tsx
// Token swap screen — extensible for multiple pairs, currently MCGP/USDC
import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  View, Text, TouchableOpacity, StyleSheet, TextInput,
  ScrollView, ActivityIndicator, KeyboardAvoidingView, Platform,
  Pressable, Modal, FlatList,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { COLORS } from '@/constants';
import { signTransaction } from '@/services/wallet';
import {
  getSwapPrice, prepareSwap, submitTransaction, getWalletBalances,
  type WalletBalance,
} from '@/services/walletApi';
import { useKycVerification } from '@/hooks/useKycVerification';

// ── Token config (extend this list as new tokens are added) ──
interface TokenInfo {
  symbol: string;
  name: string;
  decimals: number;
  color: string;
  icon: string; // first letter or symbol
}

const TOKENS: Record<string, TokenInfo> = {
  MCGP: { symbol: 'MCGP', name: 'MCG Protocol', decimals: 18, color: '#D4AF37', icon: 'M' },
  USDC: { symbol: 'USDC', name: 'USD Coin', decimals: 6, color: '#2775CA', icon: '$' },
  USDT: { symbol: 'USDT', name: 'Tether', decimals: 6, color: '#26A17B', icon: '₮' },
};

// Available swap pairs: [fromToken, toToken]
// Add new pairs here as contracts support them
const SWAP_PAIRS: Array<{ from: string; to: string; label: string }> = [
  { from: 'USDC', to: 'MCGP', label: 'Buy MCGP with USDC' },
  { from: 'MCGP', to: 'USDC', label: 'Sell MCGP for USDC' },
  // Future: { from: 'USDT', to: 'MCGP', label: 'Buy MCGP with USDT' },
];

const GOLD = '#D4AF37';
type Screen = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

function toWei(amount: string, d: number): bigint {
  try {
    const [int, frac = ''] = amount.split('.');
    return BigInt(int || '0') * BigInt(10 ** d) + BigInt(frac.padEnd(d, '0').slice(0, d) || '0');
  } catch { return BigInt(0); }
}

function fromWei(wei: string, d: number, show = 6): string {
  try {
    const n = BigInt(wei), f = BigInt(10 ** d);
    const frac = (n % f).toString().padStart(d, '0').slice(0, show);
    return `${n / f}.${frac}`.replace(/\.?0+$/, '') || '0';
  } catch { return '0'; }
}

// ── Token Badge ──
const TokenBadge = ({ token, size = 40 }: { token: TokenInfo; size?: number }) => (
  <View style={[s.badge, { width: size, height: size, borderRadius: size / 2, backgroundColor: token.color }]}>
    <Text style={{ fontSize: size * 0.38, fontWeight: '700', color: '#FFF' }}>{token.icon}</Text>
  </View>
);

// ── Token Selector Button ──
const TokenSelector = ({ token, onPress, disabled }: { token: TokenInfo; onPress: () => void; disabled?: boolean }) => (
  <TouchableOpacity style={s.tokenSelector} onPress={onPress} disabled={disabled} activeOpacity={0.7}>
    <TokenBadge token={token} size={32} />
    <Text style={s.tokenSelectorText}>{token.symbol}</Text>
    {!disabled && <Ionicons name="chevron-down" size={16} color="#888" />}
  </TouchableOpacity>
);

// ── Token Picker Modal ──
const TokenPicker = ({
  visible, tokens, selected, onSelect, onClose,
}: {
  visible: boolean;
  tokens: TokenInfo[];
  selected: string;
  onSelect: (symbol: string) => void;
  onClose: () => void;
}) => (
  <Modal visible={visible} transparent animationType="slide">
    <Pressable style={s.modalOverlay} onPress={onClose}>
      <View style={s.modalContent}>
        <Text style={s.modalTitle}>Select Token</Text>
        <FlatList
          data={tokens}
          keyExtractor={t => t.symbol}
          renderItem={({ item }) => (
            <TouchableOpacity
              style={[s.modalItem, item.symbol === selected && s.modalItemActive]}
              onPress={() => { onSelect(item.symbol); onClose(); }}
            >
              <TokenBadge token={item} size={36} />
              <View style={{ flex: 1, marginLeft: 12 }}>
                <Text style={s.modalItemSymbol}>{item.symbol}</Text>
                <Text style={s.modalItemName}>{item.name}</Text>
              </View>
              {item.symbol === selected && <Ionicons name="checkmark-circle" size={22} color={GOLD} />}
            </TouchableOpacity>
          )}
        />
      </View>
    </Pressable>
  </Modal>
);

// ── Main Screen ──
const SwapScreen = () => {
  const { requireKycVerified } = useKycVerification();
  const [fromSymbol, setFromSymbol] = useState('USDC');
  const [toSymbol, setToSymbol] = useState('MCGP');
  const [amount, setAmount] = useState('');
  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [loadingBal, setLoadingBal] = useState(true);
  const [priceLoading, setPriceLoading] = useState(false);
  const [quote, setQuote] = useState<{ mcgp: string; usdc: string; rate: string } | null>(null);
  const [priceErr, setPriceErr] = useState('');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [status, setStatus] = useState('');
  const [pickerFor, setPickerFor] = useState<'from' | 'to' | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fromToken = TOKENS[fromSymbol];
  const toToken = TOKENS[toSymbol];
  const direction = fromSymbol === 'MCGP' ? 'sell' as const : 'buy' as const;

  useEffect(() => {
    (async () => {
      try {
        const r = await getWalletBalances();
        if (r.success && Array.isArray(r.data)) setBalances(r.data);
      } catch {} finally { setLoadingBal(false); }
    })();
  }, []);

  const bal = (sym: string) => balances.find(b => b.symbol === sym)?.balance || '0';

  const fetchPrice = useCallback((dir: 'buy' | 'sell', input: string, fromDec: number) => {
    if (timer.current) clearTimeout(timer.current);
    if (!input || isNaN(Number(input)) || Number(input) <= 0) {
      setQuote(null); setPriceErr(''); return;
    }
    timer.current = setTimeout(async () => {
      setPriceLoading(true); setPriceErr('');
      try {
        const params = dir === 'buy'
          ? { usdcAmount: toWei(input, fromDec).toString() }
          : { mcgpAmount: toWei(input, fromDec).toString() };
        const r = await getSwapPrice(dir, params);
        if (r.success && r.data) {
          setQuote({ mcgp: r.data.mcgpAmount, usdc: r.data.usdcAmount, rate: r.data.pricePerMCGP });
        } else {
          setPriceErr(r.message || 'Price unavailable'); setQuote(null);
        }
      } catch (e: any) { setPriceErr(e.message || 'Failed'); }
      finally { setPriceLoading(false); }
    }, 500);
  }, []);

  useEffect(() => {
    fetchPrice(direction, amount, fromToken.decimals);
    return () => { if (timer.current) clearTimeout(timer.current); };
  }, [amount, direction, fromToken.decimals, fetchPrice]);

  const rawOutput = quote
    ? direction === 'buy' ? fromWei(quote.mcgp, TOKENS.MCGP.decimals, 4) : fromWei(quote.usdc, TOKENS.USDC.decimals, 4)
    : null;
  const outputDisplay = rawOutput && rawOutput !== '0' ? rawOutput : null;
  const rawRate = quote ? fromWei(quote.rate, TOKENS.USDC.decimals, 6) : null;
  const rateDisplay = rawRate && rawRate !== '0' ? rawRate : null;

  const swapTokens = () => {
    const newFrom = toSymbol;
    const newTo = fromSymbol;
    setFromSymbol(newFrom);
    setToSymbol(newTo);
    setAmount(''); setQuote(null); setPriceErr(''); setError('');
  };

  const selectFrom = (sym: string) => {
    if (sym === toSymbol) setToSymbol(fromSymbol); // swap if same
    setFromSymbol(sym);
    setAmount(''); setQuote(null); setError('');
  };

  const selectTo = (sym: string) => {
    if (sym === fromSymbol) setFromSymbol(toSymbol);
    setToSymbol(sym);
    setAmount(''); setQuote(null); setError('');
  };

  // Available tokens for picker (based on existing pairs)
  const fromTokenOptions = [...new Set(SWAP_PAIRS.map(p => p.from))].map(s => TOKENS[s]);
  const toTokenOptions = [...new Set(SWAP_PAIRS.map(p => p.to))].map(s => TOKENS[s]);

  const validate = (): string | null => {
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    if (Number(amount) > Number(bal(fromSymbol))) return `Insufficient ${fromSymbol} balance.`;
    if (!quote) return 'Waiting for price...';
    return null;
  };

  const handleSwap = async () => {
    if (!requireKycVerified()) return;
    setScreen('signing'); setError('');
    try {
      const params = direction === 'buy'
        ? { usdcAmount: toWei(amount, fromToken.decimals).toString() }
        : { mcgpAmount: toWei(amount, fromToken.decimals).toString() };

      setStatus('Preparing swap...');
      const prep = await prepareSwap(direction, { ...params, slippageBps: 50 });
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Failed to prepare');

      const { approveTx, swapTx } = prep.data;

      setStatus('Approving tokens...');
      const signedA = await signTransaction({
        to: approveTx.to, data: approveTx.data, value: approveTx.value,
        gasLimit: approveTx.gasLimit, gasPrice: approveTx.gasPrice,
        nonce: approveTx.nonce, chainId: approveTx.chainId,
      });
      await submitTransaction(signedA, 'approve', fromSymbol, approveTx.to, amount, approveTx.chainId);

      setStatus('Executing swap...');
      const signedS = await signTransaction({
        to: swapTx.to, data: swapTx.data, value: swapTx.value,
        gasLimit: swapTx.gasLimit, gasPrice: swapTx.gasPrice,
        nonce: swapTx.nonce, chainId: swapTx.chainId,
      });
      const res = await submitTransaction(signedS, 'swap', 'MCGP', swapTx.to, amount, swapTx.chainId);

      if (res.success && res.data) { setTxHash(res.data.txHash); setScreen('success'); }
      else throw new Error(res.message || 'Swap failed');
    } catch (e: any) { setError(e.message || 'Swap failed.'); setScreen('failure'); }
  };

  // ── Signing ──
  if (screen === 'signing') return (
    <View style={s.container}>
      <View style={s.center}>
        <View style={s.signingIcon}><ActivityIndicator size="large" color={GOLD} /></View>
        <Text style={s.bigTitle}>Processing Swap</Text>
        <Text style={s.signingMsg}>{status}</Text>
        <View style={{ gap: 6, marginTop: 12 }}>
          <Text style={s.step}>1. Approve {fromSymbol} spend</Text>
          <Text style={s.step}>2. Execute swap on-chain</Text>
        </View>
      </View>
    </View>
  );

  // ── Result ──
  if (screen === 'success' || screen === 'failure') {
    const ok = screen === 'success';
    return (
      <View style={s.container}>
        <View style={s.center}>
          <View style={[s.resIcon, { backgroundColor: ok ? '#D1FAE5' : '#FEE2E2' }]}>
            <Ionicons name={ok ? 'checkmark-circle' : 'close-circle'} size={48} color={ok ? '#16A34A' : '#DC2626'} />
          </View>
          <Text style={s.bigTitle}>{ok ? 'Swap Complete!' : 'Swap Failed'}</Text>
          <Text style={s.resMsg}>
            {ok ? `Swapped ${amount} ${fromSymbol} → ${outputDisplay || ''} ${toSymbol}` : error}
          </Text>
          {txHash ? <Text style={s.txH} numberOfLines={1} ellipsizeMode="middle">Tx: {txHash}</Text> : null}
          <TouchableOpacity style={s.btn} onPress={() => ok ? router.replace('/tokenization') : setScreen('form')}>
            <Text style={s.btnT}>{ok ? 'Back to Wallet' : 'Try Again'}</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  // ── Confirm ──
  if (screen === 'confirm') return (
    <View style={s.container}>
      <ScrollView contentContainerStyle={s.scroll}>
        <Pressable onPress={() => setScreen('form')} style={s.backRow}>
          <Ionicons name="chevron-back" size={22} color={GOLD} />
          <Text style={s.backT}>Back</Text>
        </Pressable>
        <Text style={s.bigTitle}>Confirm Swap</Text>

        <View style={s.card}>
          <View style={s.cRow}>
            <TokenBadge token={fromToken} />
            <View style={{ flex: 1, marginLeft: 12 }}>
              <Text style={s.cLabel}>You Pay</Text>
              <Text style={s.cAmount}>{amount} {fromSymbol}</Text>
            </View>
          </View>
          <View style={{ alignItems: 'center', paddingVertical: 4 }}>
            <Ionicons name="arrow-down" size={20} color={GOLD} />
          </View>
          <View style={s.cRow}>
            <TokenBadge token={toToken} />
            <View style={{ flex: 1, marginLeft: 12 }}>
              <Text style={s.cLabel}>You Receive</Text>
              <Text style={s.cAmount}>{outputDisplay || '—'} {toSymbol}</Text>
            </View>
          </View>
          <View style={s.divider} />
          <View style={s.detRow}><Text style={s.detL}>Rate</Text><Text style={s.detV}>1 MCGP = {rateDisplay || '—'} USDC</Text></View>
          <View style={s.detRow}><Text style={s.detL}>Slippage</Text><Text style={s.detV}>0.5%</Text></View>
        </View>

        <Text style={s.disc}>Two transactions: token approval + swap execution.</Text>
        <TouchableOpacity style={s.btn} onPress={handleSwap}>
          <Ionicons name="swap-vertical" size={18} color="#FFF" style={{ marginRight: 8 }} />
          <Text style={s.btnT}>Confirm Swap</Text>
        </TouchableOpacity>
        <TouchableOpacity style={s.btn2} onPress={() => setScreen('form')}>
          <Text style={s.btn2T}>Cancel</Text>
        </TouchableOpacity>
      </ScrollView>
    </View>
  );

  // ── Form ──
  return (
    <KeyboardAvoidingView style={s.container} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
      <ScrollView contentContainerStyle={s.scroll} showsVerticalScrollIndicator={false} keyboardShouldPersistTaps="handled">
        {/* Header */}
        <View style={s.header}>
          <View>
            <Text style={s.bigTitle}>Swap</Text>
            <Text style={s.sub}>OTC Marketplace</Text>
          </View>
          {rateDisplay ? (
            <View style={s.pill}>
              <Ionicons name="trending-up" size={14} color={GOLD} />
              <Text style={s.pillT}>1 MCGP = ${rateDisplay}</Text>
            </View>
          ) : null}
        </View>

        {/* From card */}
        <View style={s.card}>
          <View style={s.cardHead}>
            <Text style={s.cardLabel}>YOU PAY</Text>
            <TouchableOpacity onPress={() => setAmount(bal(fromSymbol))}>
              <Text style={s.cardBal}>Balance: {bal(fromSymbol)}</Text>
            </TouchableOpacity>
          </View>
          <View style={s.cardBody}>
            <TokenSelector
              token={fromToken}
              onPress={() => setPickerFor('from')}
              disabled={fromTokenOptions.length <= 1}
            />
            <TextInput
              style={s.amountInput}
              placeholder="0.00"
              placeholderTextColor="#CCC"
              value={amount}
              onChangeText={t => { setAmount(t); setError(''); }}
              keyboardType="decimal-pad"
            />
          </View>
        </View>

        {/* Swap arrow */}
        <View style={s.arrowWrap}>
          <TouchableOpacity style={s.arrowBtn} onPress={swapTokens} activeOpacity={0.7}>
            <Ionicons name="swap-vertical" size={22} color="#FFF" />
          </TouchableOpacity>
        </View>

        {/* To card */}
        <View style={[s.card, { marginTop: -8 }]}>
          <View style={s.cardHead}>
            <Text style={s.cardLabel}>YOU RECEIVE</Text>
            <Text style={s.cardBal}>Balance: {bal(toSymbol)}</Text>
          </View>
          <View style={s.cardBody}>
            <TokenSelector
              token={toToken}
              onPress={() => setPickerFor('to')}
              disabled={toTokenOptions.length <= 1}
            />
            {priceLoading ? (
              <ActivityIndicator size="small" color={GOLD} style={{ flex: 1 }} />
            ) : (
              <Text style={s.amountOutput}>{outputDisplay || '—'}</Text>
            )}
          </View>
        </View>

        {/* Info bar */}
        {priceErr ? (
          <View style={[s.info, s.infoError]}>
            <Ionicons name="warning" size={16} color="#DC2626" />
            <Text style={[s.infoT, { color: '#DC2626' }]}>{priceErr}</Text>
          </View>
        ) : rateDisplay ? (
          <View style={s.info}>
            <Ionicons name="information-circle" size={16} color={GOLD} />
            <Text style={s.infoT}>1 MCGP = {rateDisplay} USDC  ·  0.5% slippage</Text>
          </View>
        ) : null}

        {error ? (
          <View style={[s.info, s.infoError]}>
            <Ionicons name="alert-circle" size={16} color="#DC2626" />
            <Text style={[s.infoT, { color: '#DC2626' }]}>{error}</Text>
          </View>
        ) : null}

        {/* Action */}
        <TouchableOpacity
          style={[s.btn, (!amount || priceLoading || !!priceErr) && { opacity: 0.4 }]}
          onPress={() => { const e = validate(); if (e) setError(e); else { setError(''); setScreen('confirm'); } }}
          disabled={!amount || priceLoading || !!priceErr}
        >
          <Text style={s.btnT}>Review Swap</Text>
        </TouchableOpacity>
      </ScrollView>

      {/* Token picker modals */}
      <TokenPicker
        visible={pickerFor === 'from'}
        tokens={fromTokenOptions}
        selected={fromSymbol}
        onSelect={selectFrom}
        onClose={() => setPickerFor(null)}
      />
      <TokenPicker
        visible={pickerFor === 'to'}
        tokens={toTokenOptions}
        selected={toSymbol}
        onSelect={selectTo}
        onClose={() => setPickerFor(null)}
      />
    </KeyboardAvoidingView>
  );
};

export default SwapScreen;

// ── Styles ──
const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
  scroll: { flexGrow: 1, padding: 20, paddingBottom: 40 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 24 },

  header: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 24 },
  bigTitle: { fontSize: 28, fontWeight: '700', color: '#1A1A1A' },
  sub: { fontSize: 13, color: '#888', marginTop: 2 },
  pill: { flexDirection: 'row', alignItems: 'center', gap: 4, backgroundColor: '#FFF8E1', paddingHorizontal: 10, paddingVertical: 6, borderRadius: 20 },
  pillT: { fontSize: 12, fontWeight: '600', color: GOLD },

  // Cards
  card: {
    backgroundColor: '#FFF', borderRadius: 16, padding: 16,
    borderWidth: 1, borderColor: '#F0F0F0',
    elevation: 2, shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.04, shadowRadius: 8,
  },
  cardHead: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 },
  cardLabel: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1 },
  cardBal: { fontSize: 12, color: GOLD, fontWeight: '500' },
  cardBody: { flexDirection: 'row', alignItems: 'center', gap: 12 },

  // Token selector
  tokenSelector: {
    flexDirection: 'row', alignItems: 'center', gap: 6,
    backgroundColor: '#F5F5F5', paddingHorizontal: 10, paddingVertical: 8, borderRadius: 24,
  },
  tokenSelectorText: { fontSize: 15, fontWeight: '700', color: '#1A1A1A' },

  // Amount input/output
  amountInput: { flex: 1, fontSize: 28, fontWeight: '700', color: '#1A1A1A', textAlign: 'right', padding: 0 },
  amountOutput: { flex: 1, fontSize: 28, fontWeight: '700', color: '#1A1A1A', textAlign: 'right' },

  // Swap arrow
  arrowWrap: { alignItems: 'center', zIndex: 10, marginVertical: -12 },
  arrowBtn: {
    width: 40, height: 40, borderRadius: 20, backgroundColor: GOLD,
    justifyContent: 'center', alignItems: 'center',
    elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.3, shadowRadius: 4,
  },

  // Info bar
  info: {
    flexDirection: 'row', alignItems: 'center', gap: 6,
    backgroundColor: '#FFF8E1', borderWidth: 1, borderColor: '#FDE68A',
    borderRadius: 10, padding: 10, marginTop: 12,
  },
  infoError: { backgroundColor: '#FEF2F2', borderColor: '#FECACA' },
  infoT: { fontSize: 12, color: '#92400E', flex: 1 },

  // Buttons
  btn: {
    flexDirection: 'row', backgroundColor: GOLD, paddingVertical: 16,
    borderRadius: 14, alignItems: 'center', justifyContent: 'center', marginTop: 20,
    elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.25, shadowRadius: 8,
  },
  btnT: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  btn2: { paddingVertical: 14, borderRadius: 14, alignItems: 'center', marginTop: 10 },
  btn2T: { fontSize: 15, color: '#888', fontWeight: '600' },

  // Badge
  badge: { justifyContent: 'center', alignItems: 'center' },

  // Back
  backRow: { flexDirection: 'row', alignItems: 'center', marginBottom: 16, gap: 4 },
  backT: { fontSize: 15, color: GOLD, fontWeight: '600' },

  // Confirm
  cRow: { flexDirection: 'row', alignItems: 'center', paddingVertical: 12 },
  cLabel: { fontSize: 12, color: '#888', textTransform: 'uppercase', letterSpacing: 0.5 },
  cAmount: { fontSize: 22, fontWeight: '700', color: '#1A1A1A', marginTop: 2 },
  divider: { height: 1, backgroundColor: '#F0F0F0', marginVertical: 12 },
  detRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 6 },
  detL: { fontSize: 13, color: '#888' },
  detV: { fontSize: 13, fontWeight: '600', color: '#1A1A1A' },
  disc: { fontSize: 12, color: '#AAA', textAlign: 'center', marginBottom: 8 },

  // Signing
  signingIcon: { width: 80, height: 80, borderRadius: 40, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  signingMsg: { fontSize: 15, color: GOLD, fontWeight: '600', marginBottom: 20 },
  step: { fontSize: 13, color: '#888' },

  // Result
  resIcon: { width: 80, height: 80, borderRadius: 40, justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  resMsg: { fontSize: 15, color: '#666', textAlign: 'center', marginBottom: 20, paddingHorizontal: 24 },
  txH: { fontSize: 12, color: '#AAA', marginBottom: 24, maxWidth: '80%' },

  // Token picker modal
  modalOverlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'flex-end' },
  modalContent: { backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24, padding: 20, maxHeight: '60%' },
  modalTitle: { fontSize: 18, fontWeight: '700', color: '#1A1A1A', marginBottom: 16, textAlign: 'center' },
  modalItem: { flexDirection: 'row', alignItems: 'center', paddingVertical: 14, paddingHorizontal: 8, borderRadius: 12 },
  modalItemActive: { backgroundColor: '#FFF8E1' },
  modalItemSymbol: { fontSize: 16, fontWeight: '700', color: '#1A1A1A' },
  modalItemName: { fontSize: 13, color: '#888' },
});
