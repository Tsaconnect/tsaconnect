import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  View, Text, TextInput, TouchableOpacity, ScrollView, StyleSheet,
  ActivityIndicator, KeyboardAvoidingView, Platform, Modal, Pressable,
  Switch,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { getChainByChainId, CHAINS } from '@/constants/chains';
import { getWalletBalances, normalizeWalletBalances, submitTransaction, type WalletBalanceWithChain } from '@/services/walletApi';
import { getActiveWallet, fetchPendingNonce, signLiFiTransaction } from '@/services/wallet';
import { ethers } from 'ethers';
import { getLiFiTokens, getLiFiQuote, buildERC20ApproveCalldata, type LiFiToken, type LiFiQuote } from '@/services/lifi';
import TokenPickerModal from '@/components/swap/TokenPickerModal';

const GOLD = '#D4AF37';
const DEFAULT_FROM_CHAIN_ID = 56;
const DEFAULT_TO_CHAIN_ID = 1;

type QuoteStatus = 'idle' | 'quoting' | 'quoted' | 'no_route' | 'error';
type Screen = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

export default function AllSwapScreen() {
  const [fromToken, setFromToken] = useState<LiFiToken | null>(null);
  const [fromChainId, setFromChainId] = useState(DEFAULT_FROM_CHAIN_ID);
  const [toToken, setToToken] = useState<LiFiToken | null>(null);
  const [toChainId, setToChainId] = useState(DEFAULT_TO_CHAIN_ID);
  const [fromAmount, setFromAmount] = useState('');
  const [quote, setQuote] = useState<LiFiQuote | null>(null);
  const [quoteStatus, setQuoteStatus] = useState<QuoteStatus>('idle');
  const [quoteError, setQuoteError] = useState('');
  const [screen, setScreen] = useState<Screen>('form');
  const [pickerMode, setPickerMode] = useState<'from' | 'to' | null>(null);
  const [allTokens, setAllTokens] = useState<LiFiToken[]>([]);
  const [toAllTokens, setToAllTokens] = useState<LiFiToken[]>([]);
  const [balances, setBalances] = useState<WalletBalanceWithChain[]>([]);
  const [signingStatus, setSigningStatus] = useState('');
  const [txHash, setTxHash] = useState('');
  const [error, setError] = useState('');
  const [slippageAuto, setSlippageAuto] = useState(true);
  const [slippage, setSlippage] = useState('0.005');
  const [slippageSheetVisible, setSlippageSheetVisible] = useState(false);
  const [walletAddress, setWalletAddress] = useState('');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    getActiveWallet().then(addr => { if (addr) setWalletAddress(addr); });
  }, []);

  useFocusEffect(useCallback(() => {
    loadBalances();
  }, [fromChainId]));

  useEffect(() => {
    let active = true;
    getLiFiTokens(fromChainId).then(tokens => { if (active) setAllTokens(tokens); }).catch(() => {});
    return () => { active = false; };
  }, [fromChainId]);

  useEffect(() => {
    let active = true;
    getLiFiTokens(toChainId).then(tokens => { if (active) setToAllTokens(tokens); }).catch(() => {});
    return () => { active = false; };
  }, [toChainId]);

  const loadBalances = async () => {
    try {
      const res = await getWalletBalances(fromChainId);
      if (res.success && res.data) {
        setBalances(normalizeWalletBalances(res.data, 'bsc'));
      }
    } catch {}
  };

  useEffect(() => {
    if (timer.current) clearTimeout(timer.current);
    if (!fromToken || !toToken || !fromAmount || isNaN(Number(fromAmount)) || Number(fromAmount) <= 0 || !walletAddress) {
      setQuote(null); setQuoteStatus('idle'); setQuoteError('');
      return;
    }
    setQuoteStatus('quoting');
    timer.current = setTimeout(async () => {
      try {
        const amountWei = ethers.parseUnits(fromAmount, fromToken.decimals).toString();
        const q = await getLiFiQuote({
          fromChain: fromChainId,
          toChain: toChainId,
          fromToken: fromToken.address,
          toToken: toToken.address,
          fromAmount: amountWei,
          fromAddress: walletAddress,
          slippage: slippageAuto ? '0.005' : slippage,
        });
        setQuote(q); setQuoteStatus('quoted'); setQuoteError('');
      } catch (e: any) {
        const msg: string = e?.message ?? '';
        if (msg.toLowerCase().includes('no available quotes') || msg.toLowerCase().includes('not supported')) {
          setQuoteStatus('no_route');
        } else {
          setQuoteStatus('error'); setQuoteError(msg);
        }
        setQuote(null);
      }
    }, 500);
    return () => { if (timer.current) clearTimeout(timer.current); };
  }, [fromToken, toToken, fromAmount, fromChainId, toChainId, walletAddress, slippageAuto, slippage]);

  const swapDirection = () => {
    const [nf, nfc, nt, ntc] = [toToken, toChainId, fromToken, fromChainId];
    setFromToken(nf); setFromChainId(nfc);
    setToToken(nt); setToChainId(ntc);
    setFromAmount(''); setQuote(null); setQuoteStatus('idle');
  };

  const fromBalance = balances.find(b => b.symbol === fromToken?.symbol)?.balance ?? '0';

  const validate = () => {
    if (!fromToken) return 'Select a token to send';
    if (!toToken) return 'Select a token to receive';
    if (!fromAmount || Number(fromAmount) <= 0) return 'Enter an amount';
    try {
      const amountUnits = ethers.parseUnits(fromAmount, fromToken.decimals);
      const balanceUnits = ethers.parseUnits(fromBalance || '0', fromToken.decimals);
      if (amountUnits > balanceUnits) return `Insufficient ${fromToken.symbol} balance`;
    } catch {
      return 'Enter a valid amount';
    }
    if (!quote) return 'Waiting for price...';
    return null;
  };

  const handleSwap = async () => {
    if (!quote || !fromToken || !walletAddress) return;
    setScreen('signing'); setError('');
    try {
      const chainKeyResult = getChainByChainId(quote.transactionRequest.chainId);
      if (!chainKeyResult) throw new Error('Unsupported chain in route');
      const chainKey = chainKeyResult.key;

      let nonce = await fetchPendingNonce(walletAddress, chainKey);
      const fromAmountWei = ethers.parseUnits(fromAmount, fromToken.decimals).toString();
      const humanAmount = String(Number(fromAmountWei) / Math.pow(10, fromToken.decimals));

      if (quote.approvalAddress && quote.approvalToken) {
        setSigningStatus('Step 1 of 2: Approving…');
        const calldata = buildERC20ApproveCalldata(quote.approvalAddress, ethers.MaxUint256.toString());
        const approveTx = {
          to: quote.approvalToken,
          data: calldata,
          value: '0x0',
          chainId: quote.transactionRequest.chainId,
          nonce,
          gasLimit: '65000',
          ...(quote.transactionRequest.maxFeePerGas
            ? { maxFeePerGas: quote.transactionRequest.maxFeePerGas, maxPriorityFeePerGas: quote.transactionRequest.maxPriorityFeePerGas }
            : { gasPrice: quote.transactionRequest.gasPrice }),
        };
        const signedApprove = await signLiFiTransaction(approveTx);
        await submitTransaction(signedApprove, 'approve', fromToken.symbol, quote.approvalToken, humanAmount, quote.transactionRequest.chainId);
        nonce += 1;
      }

      setSigningStatus('Step 2 of 2: Swapping…');
      const swapTx = { ...quote.transactionRequest, nonce };
      const signedSwap = await signLiFiTransaction(swapTx);
      const res = await submitTransaction(signedSwap, 'swap', fromToken.symbol, quote.transactionRequest.to, humanAmount, quote.transactionRequest.chainId);

      if (res.success && res.data) {
        setTxHash(res.data.txHash); setScreen('success');
      } else {
        throw new Error(res.message || 'Swap failed');
      }
    } catch (e: any) {
      setError(e?.message || 'Swap failed'); setScreen('failure');
    }
  };

  const outputAmount = quote && toToken
    ? (() => {
        try {
          const raw = ethers.formatUnits(quote.toAmount, toToken.decimals);
          const n = Number(raw);
          if (isNaN(n) || n === 0) return '0';
          return n.toLocaleString('en-US', { maximumFractionDigits: 6, minimumFractionDigits: 0 });
        } catch { return '0'; }
      })()
    : null;

  const chainName = (chainId: number) => {
    const entry = Object.values(CHAINS).find(c => c.chainId === chainId);
    return entry?.shortName ?? String(chainId);
  };

  const formattedBalance = fromBalance
    ? (() => {
        const n = Number(fromBalance);
        if (isNaN(n) || n === 0) return '0';
        return n.toLocaleString('en-US', { maximumFractionDigits: 6, minimumFractionDigits: 0 });
      })()
    : '0';

  if (screen === 'signing') return (
    <View style={s.container}>
      <View style={s.center}>
        <ActivityIndicator size="large" color={GOLD} style={{ marginBottom: 16 }} />
        <Text style={s.bigTitle}>Processing Swap</Text>
        <Text style={[s.sub, { color: GOLD, marginTop: 8 }]}>{signingStatus}</Text>
      </View>
    </View>
  );

  if (screen === 'success' || screen === 'failure') {
    const ok = screen === 'success';
    return (
      <View style={s.container}>
        <View style={s.center}>
          <View style={[s.resIcon, { backgroundColor: ok ? '#D1FAE5' : '#FEE2E2' }]}>
            <Ionicons name={ok ? 'checkmark-circle' : 'close-circle'} size={48} color={ok ? '#16A34A' : '#DC2626'} />
          </View>
          <Text style={s.bigTitle}>{ok ? 'Swap Complete!' : 'Swap Failed'}</Text>
          <Text style={[s.sub, { textAlign: 'center', marginTop: 8, paddingHorizontal: 24 }]}>
            {ok ? `Swapped ${fromAmount} ${fromToken?.symbol} → ${outputAmount} ${toToken?.symbol}` : error}
          </Text>
          {txHash ? <Text style={s.txHash} numberOfLines={1} ellipsizeMode="middle">Tx: {txHash}</Text> : null}
          <TouchableOpacity style={s.btn} onPress={() => ok ? router.back() : setScreen('form')}>
            <Text style={s.btnT}>{ok ? 'Done' : 'Try Again'}</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  if (screen === 'confirm') return (
    <View style={s.container}>
      <ScrollView contentContainerStyle={s.scroll}>
        <TouchableOpacity onPress={() => setScreen('form')} style={s.backRow}>
          <Ionicons name="chevron-back" size={22} color={GOLD} />
          <Text style={[s.sub, { color: GOLD }]}>Back</Text>
        </TouchableOpacity>
        <Text style={s.bigTitle}>Confirm Swap</Text>
        <View style={s.card}>
          <View style={s.confirmRow}>
            <Text style={s.cardLabel}>YOU PAY</Text>
            <Text style={s.confirmAmount}>{fromAmount} {fromToken?.symbol}</Text>
            <Text style={s.sub}>{fromToken?.name}</Text>
          </View>
          <View style={{ alignItems: 'center', paddingVertical: 8 }}>
            <Ionicons name="arrow-down" size={20} color={GOLD} />
          </View>
          <View style={s.confirmRow}>
            <Text style={s.cardLabel}>YOU RECEIVE</Text>
            <Text style={s.confirmAmount}>{outputAmount} {toToken?.symbol}</Text>
            <Text style={s.sub}>{toToken?.name}</Text>
          </View>
          <View style={s.divider} />
          <View style={s.detRow}><Text style={s.detL}>Route</Text><Text style={s.detV}>{quote?.tool}</Text></View>
          <View style={s.detRow}><Text style={s.detL}>Est. time</Text><Text style={s.detV}>{quote ? Math.ceil(quote.estimatedDuration / 60) : '—'} min</Text></View>
          <View style={s.detRow}><Text style={s.detL}>Gas cost</Text><Text style={s.detV}>${quote?.gasCostUSD}</Text></View>
          <View style={s.detRow}><Text style={s.detL}>Slippage</Text><Text style={s.detV}>{slippageAuto ? '0.5% (auto)' : `${(Number(slippage) * 100).toFixed(1)}%`}</Text></View>
        </View>
        {quote?.approvalAddress && (
          <Text style={s.disc}>Two transactions: token approval + swap.</Text>
        )}
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

  const validationError = validate();
  return (
    <KeyboardAvoidingView style={s.container} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
      <ScrollView contentContainerStyle={s.scroll} keyboardShouldPersistTaps="handled">
        <View style={s.headerRow}>
          <View>
            <Text style={s.bigTitle}>All Swap</Text>
            <Text style={s.sub}>Any token · Any chain</Text>
          </View>
          <TouchableOpacity onPress={() => setSlippageSheetVisible(true)} style={s.settingsBtn}>
            <Ionicons name="settings-outline" size={20} color="#888" />
          </TouchableOpacity>
        </View>

        <View style={s.card}>
          <Text style={s.cardLabel}>YOU PAY</Text>
          <View style={s.cardRow}>
            <TouchableOpacity style={s.tokenBtn} onPress={() => setPickerMode('from')} activeOpacity={0.7}>
              <View>
                <Text style={s.tokenBtnText}>{fromToken?.symbol ?? 'Select'}</Text>
                {fromToken && <Text style={s.tokenChain}>{chainName(fromChainId)}</Text>}
              </View>
              <Ionicons name="chevron-down" size={14} color="#888" />
            </TouchableOpacity>
            <TextInput
              style={s.amountInput}
              placeholder="0.00"
              placeholderTextColor="#CCC"
              value={fromAmount}
              onChangeText={t => { setFromAmount(t); setError(''); }}
              keyboardType="decimal-pad"
            />
          </View>
          <TouchableOpacity onPress={() => fromBalance !== '0' && setFromAmount(fromBalance)}>
            <Text style={s.balanceText}>Balance: {formattedBalance} {fromToken?.symbol}</Text>
          </TouchableOpacity>

          <View style={s.arrowWrap}>
            <TouchableOpacity style={s.arrowBtn} onPress={swapDirection} activeOpacity={0.7}>
              <Ionicons name="swap-vertical" size={20} color="#FFF" />
            </TouchableOpacity>
          </View>

          <Text style={s.cardLabel}>YOU RECEIVE</Text>
          <View style={s.cardRow}>
            <TouchableOpacity style={s.tokenBtn} onPress={() => setPickerMode('to')} activeOpacity={0.7}>
              <View>
                <Text style={s.tokenBtnText}>{toToken?.symbol ?? 'Select'}</Text>
                {toToken && <Text style={s.tokenChain}>{chainName(toChainId)}</Text>}
              </View>
              <Ionicons name="chevron-down" size={14} color="#888" />
            </TouchableOpacity>
            {quoteStatus === 'quoting'
              ? <ActivityIndicator size="small" color={GOLD} style={{ flex: 1 }} />
              : <Text style={s.amountOutput}>{outputAmount ?? '—'}</Text>}
          </View>
        </View>

        {quoteStatus === 'quoted' && quote && (
          <View style={s.infoBar}>
            <Ionicons name="flash" size={14} color={GOLD} />
            <Text style={s.infoText}>via {quote.tool} · ~{Math.ceil(quote.estimatedDuration / 60)} min · ${quote.gasCostUSD} gas</Text>
          </View>
        )}
        {quoteStatus === 'no_route' && (
          <View style={[s.infoBar, s.infoError]}>
            <Ionicons name="warning" size={14} color="#DC2626" />
            <Text style={[s.infoText, { color: '#DC2626' }]}>No route available for this pair. Try a different token or amount.</Text>
          </View>
        )}
        {quoteStatus === 'error' && (
          <View style={[s.infoBar, s.infoError]}>
            <Ionicons name="alert-circle" size={14} color="#DC2626" />
            <Text style={[s.infoText, { color: '#DC2626' }]}>{quoteError || 'Swap pricing unavailable. Try again shortly.'}</Text>
          </View>
        )}

        <TouchableOpacity
          style={[s.btn, !!validationError && { opacity: 0.4 }]}
          onPress={() => { const e = validate(); if (e) { setError(e); return; } setScreen('confirm'); }}
          disabled={!!validationError}
        >
          <Text style={s.btnT}>Review Swap</Text>
        </TouchableOpacity>
      </ScrollView>

      <TokenPickerModal
        visible={pickerMode !== null}
        mode={pickerMode ?? 'from'}
        chainId={pickerMode === 'from' ? fromChainId : toChainId}
        userBalances={balances}
        allTokens={pickerMode === 'from' ? allTokens : toAllTokens}
        onChainChange={(newChainId) => {
          if (pickerMode === 'from') setFromChainId(newChainId);
          else setToChainId(newChainId);
        }}
        onSelect={(token, chainId) => {
          if (pickerMode === 'from') { setFromToken(token); setFromChainId(chainId); }
          else { setToToken(token); setToChainId(chainId); }
          setPickerMode(null);
          setFromAmount(''); setQuote(null); setQuoteStatus('idle');
        }}
        onClose={() => setPickerMode(null)}
      />

      <Modal visible={slippageSheetVisible} transparent animationType="slide">
        <Pressable style={s.overlay} onPress={() => setSlippageSheetVisible(false)}>
          <Pressable style={s.slippageSheet} onPress={() => {}}>
            <Text style={[s.bigTitle, { fontSize: 18 }]}>Slippage Tolerance</Text>
            <View style={s.slippageRow}>
              <Text style={s.detL}>Auto (0.5%)</Text>
              <Switch value={slippageAuto} onValueChange={setSlippageAuto} trackColor={{ true: GOLD }} />
            </View>
            {!slippageAuto && (
              <TextInput
                style={s.slippageInput}
                value={String(Number(slippage) * 100)}
                onChangeText={v => { const n = Number(v); if (!isNaN(n) && n >= 0.1 && n <= 49) setSlippage(String(n / 100)); }}
                keyboardType="decimal-pad"
                placeholder="0.5"
                placeholderTextColor="#CCC"
              />
            )}
          </Pressable>
        </Pressable>
      </Modal>
    </KeyboardAvoidingView>
  );
}

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
  scroll: { flexGrow: 1, padding: 20, paddingBottom: 40 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 24 },
  headerRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 20 },
  bigTitle: { fontSize: 26, fontWeight: '700', color: '#1A1A1A' },
  sub: { fontSize: 13, color: '#888', marginTop: 2 },
  settingsBtn: { padding: 8 },
  card: {
    backgroundColor: '#FFF', borderRadius: 16, padding: 16,
    borderWidth: 1, borderColor: '#F0F0F0',
    elevation: 2, shadowColor: '#000', shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.04, shadowRadius: 8,
  },
  cardLabel: { fontSize: 11, fontWeight: '700', color: '#AAA', letterSpacing: 1, marginBottom: 10 },
  cardRow: { flexDirection: 'row', alignItems: 'center', gap: 12, marginBottom: 6 },
  tokenBtn: { flexDirection: 'row', alignItems: 'center', gap: 4, backgroundColor: '#F5F5F5', paddingHorizontal: 12, paddingVertical: 8, borderRadius: 20 },
  tokenBtnText: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  tokenChain: { fontSize: 10, color: '#888', marginTop: 1 },
  amountInput: { flex: 1, fontSize: 26, fontWeight: '700', color: '#1A1A1A', textAlign: 'right', padding: 0 },
  amountOutput: { flex: 1, fontSize: 26, fontWeight: '700', color: GOLD, textAlign: 'right' },
  balanceText: { fontSize: 12, color: GOLD, fontWeight: '500', textAlign: 'right', marginBottom: 16 },
  arrowWrap: { alignItems: 'center', zIndex: 10, marginVertical: -2 },
  arrowBtn: {
    width: 36, height: 36, borderRadius: 18, backgroundColor: GOLD,
    justifyContent: 'center', alignItems: 'center',
    elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.3, shadowRadius: 4, marginVertical: 8,
  },
  infoBar: { flexDirection: 'row', alignItems: 'flex-start', gap: 6, backgroundColor: '#FFF8E1', borderWidth: 1, borderColor: '#FDE68A', borderRadius: 10, padding: 10, marginTop: 12 },
  infoError: { backgroundColor: '#FEF2F2', borderColor: '#FECACA' },
  infoText: { fontSize: 12, color: '#92400E', flex: 1 },
  btn: { flexDirection: 'row', backgroundColor: GOLD, paddingVertical: 16, borderRadius: 14, alignItems: 'center', justifyContent: 'center', marginTop: 20, elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.25, shadowRadius: 8 },
  btnT: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  btn2: { paddingVertical: 14, borderRadius: 14, alignItems: 'center', marginTop: 10 },
  btn2T: { fontSize: 15, color: '#888', fontWeight: '600' },
  backRow: { flexDirection: 'row', alignItems: 'center', marginBottom: 16, gap: 4 },
  confirmRow: { paddingVertical: 10 },
  confirmAmount: { fontSize: 22, fontWeight: '700', color: '#1A1A1A', marginTop: 4 },
  divider: { height: 1, backgroundColor: '#F0F0F0', marginVertical: 12 },
  detRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 6 },
  detL: { fontSize: 13, color: '#888' },
  detV: { fontSize: 13, fontWeight: '600', color: '#1A1A1A' },
  disc: { fontSize: 12, color: '#AAA', textAlign: 'center', marginBottom: 8, marginTop: 4 },
  resIcon: { width: 80, height: 80, borderRadius: 40, justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  txHash: { fontSize: 12, color: '#AAA', marginBottom: 24, maxWidth: '80%' },
  overlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'flex-end' },
  slippageSheet: { backgroundColor: '#FFF', borderTopLeftRadius: 20, borderTopRightRadius: 20, padding: 24, gap: 16 },
  slippageRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' },
  slippageInput: { borderWidth: 1, borderColor: '#E0E0E0', borderRadius: 10, padding: 12, fontSize: 16, color: '#1A1A1A' },
});
