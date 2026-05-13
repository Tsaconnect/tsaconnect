import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  View, Text, TextInput, TouchableOpacity, ScrollView, StyleSheet,
  ActivityIndicator, KeyboardAvoidingView, Platform, Modal, Pressable,
  Switch, Image,
} from 'react-native';
import Animated, {
  useSharedValue, useAnimatedStyle, withSpring, withTiming,
  withDelay, withSequence,
} from 'react-native-reanimated';
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
const DARK = '#1A1A1A';
const FEE_PCT = 3;
const DEFAULT_FROM_CHAIN_ID = 56;
const DEFAULT_TO_CHAIN_ID = 1;

type QuoteStatus = 'idle' | 'quoting' | 'quoted' | 'no_route' | 'error' | 'expired';
type Screen = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

function TokenIcon({ uri, symbol, size = 32 }: { uri?: string; symbol?: string; size?: number }) {
  if (uri) return <Image source={{ uri }} style={{ width: size, height: size, borderRadius: size / 2 }} />;
  if (!symbol) return null;
  return (
    <View style={{ width: size, height: size, borderRadius: size / 2, backgroundColor: '#E0E0E0', alignItems: 'center', justifyContent: 'center' }}>
      <Text style={{ fontSize: size * 0.38, fontWeight: '700', color: '#555' }}>{symbol[0]}</Text>
    </View>
  );
}

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
  const [tokensLoading, setTokensLoading] = useState(false);
  const [toTokensLoading, setToTokensLoading] = useState(false);
  const [balances, setBalances] = useState<WalletBalanceWithChain[]>([]);
  const [signingStatus, setSigningStatus] = useState('');
  const [txHash, setTxHash] = useState('');
  const [error, setError] = useState('');
  const [slippageAuto, setSlippageAuto] = useState(true);
  const [slippage, setSlippage] = useState('0.005');
  const [slippageSheetVisible, setSlippageSheetVisible] = useState(false);
  const [walletAddress, setWalletAddress] = useState('');
  const [quoteSecondsLeft, setQuoteSecondsLeft] = useState(0);
  const [refreshKey, setRefreshKey] = useState(0);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    getActiveWallet().then(addr => { if (addr) setWalletAddress(addr); });
  }, []);

  useFocusEffect(useCallback(() => {
    loadBalances();
  }, [fromChainId]));

  useEffect(() => {
    let active = true;
    setTokensLoading(true);
    getLiFiTokens(fromChainId)
      .then(tokens => { if (active) setAllTokens(tokens); })
      .catch(() => {})
      .finally(() => { if (active) setTokensLoading(false); });
    return () => { active = false; };
  }, [fromChainId]);

  useEffect(() => {
    let active = true;
    setToTokensLoading(true);
    getLiFiTokens(toChainId)
      .then(tokens => { if (active) setToAllTokens(tokens); })
      .catch(() => {})
      .finally(() => { if (active) setToTokensLoading(false); });
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
        // Start expiry countdown — use validUntil from LiFi if present, else 30s
        const expiresAt = q.validUntil
          ? q.validUntil * 1000
          : Date.now() + 30_000;
        if (countdownRef.current) clearInterval(countdownRef.current);
        countdownRef.current = setInterval(() => {
          const left = Math.max(0, Math.round((expiresAt - Date.now()) / 1000));
          setQuoteSecondsLeft(left);
          if (left === 0) {
            clearInterval(countdownRef.current!);
            setQuoteStatus('expired');
          }
        }, 1000);
        setQuoteSecondsLeft(Math.round((expiresAt - Date.now()) / 1000));
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
    return () => {
      if (timer.current) clearTimeout(timer.current);
      if (countdownRef.current) clearInterval(countdownRef.current);
    };
  }, [fromToken, toToken, fromAmount, fromChainId, toChainId, walletAddress, slippageAuto, slippage, refreshKey]);

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
    if (quoteStatus === 'expired') return 'Quote expired — refresh first';
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
        setSigningStatus('Step 1 of 2: Approving token…');
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

      setSigningStatus('Step 2 of 2: Executing swap…');
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

  const refreshQuote = () => {
    if (countdownRef.current) clearInterval(countdownRef.current);
    setQuote(null); setQuoteStatus('quoting'); setQuoteSecondsLeft(0);
    setRefreshKey(k => k + 1);
  };

  const fromAmountUSD = fromToken?.priceUSD && fromAmount && Number(fromAmount) > 0
    ? Number(fromAmount) * Number(fromToken.priceUSD)
    : null;
  const toAmountUSD = quote?.toAmountUSD ? Number(quote.toAmountUSD) : null;
  const priceImpactPct = fromAmountUSD && toAmountUSD && fromAmountUSD > 0
    ? ((toAmountUSD - fromAmountUSD) / fromAmountUSD) * 100
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

  // ── Signing ──────────────────────────────────────────────────
  if (screen === 'signing') return (
    <View style={s.container}>
      <View style={s.center}>
        <View style={s.signingRing}>
          <ActivityIndicator size="large" color={GOLD} />
        </View>
        <Text style={s.bigTitle}>Processing Swap</Text>
        <Text style={s.signingStep}>{signingStatus}</Text>
        <Text style={s.signingHint}>Please keep the app open</Text>
      </View>
    </View>
  );

  // ── Success / Failure ─────────────────────────────────────────
  if (screen === 'success') return (
    <SwapSuccessScreen
      fromToken={fromToken}
      toToken={toToken}
      fromAmount={fromAmount}
      outputAmount={outputAmount}
      txHash={txHash}
      onDone={() => router.back()}
    />
  );

  if (screen === 'failure') return (
    <SwapFailureScreen
      error={error}
      onRetry={() => setScreen('form')}
    />
  );

  // ── Confirm ───────────────────────────────────────────────────
  if (screen === 'confirm') return (
    <View style={s.container}>
      <ScrollView contentContainerStyle={s.scroll}>
        <TouchableOpacity onPress={() => setScreen('form')} style={s.backRow}>
          <Ionicons name="chevron-back" size={20} color={GOLD} />
          <Text style={[s.sub, { color: GOLD }]}>Back</Text>
        </TouchableOpacity>
        <Text style={s.bigTitle}>Confirm Swap</Text>

        {/* From → To summary */}
        <View style={s.card}>
          <View style={s.confirmSection}>
            <Text style={s.cardLabel}>YOU PAY</Text>
            <View style={s.confirmTokenRow}>
              <TokenIcon uri={fromToken?.logoURI} symbol={fromToken?.symbol} size={40} />
              <View style={{ flex: 1 }}>
                <Text style={s.confirmAmount}>{fromAmount} {fromToken?.symbol}</Text>
                <Text style={s.confirmMeta}>{fromToken?.name} · {chainName(fromChainId)}</Text>
              </View>
            </View>
          </View>

          <View style={s.arrowDivider}>
            <View style={s.arrowDividerLine} />
            <View style={s.arrowCircle}>
              <Ionicons name="arrow-down" size={16} color="#FFF" />
            </View>
            <View style={s.arrowDividerLine} />
          </View>

          <View style={s.confirmSection}>
            <Text style={s.cardLabel}>YOU RECEIVE</Text>
            <View style={s.confirmTokenRow}>
              <TokenIcon uri={toToken?.logoURI} symbol={toToken?.symbol} size={40} />
              <View style={{ flex: 1 }}>
                <Text style={[s.confirmAmount, { color: GOLD }]}>{outputAmount} {toToken?.symbol}</Text>
                <Text style={s.confirmMeta}>{toToken?.name} · {chainName(toChainId)}</Text>
              </View>
            </View>
          </View>
        </View>

        {/* Details */}
        <View style={[s.card, { marginTop: 12 }]}>
          <DetailRow label="Route" value={quote?.tool ?? '—'} />
          <DetailRow label="Est. time" value={quote ? `~${Math.ceil(quote.estimatedDuration / 60)} min` : '—'} />
          <DetailRow label="Gas cost" value={`$${quote?.gasCostUSD ?? '0'}`} />
          <DetailRow label="Fee" value={`${FEE_PCT}%`} highlight />
          <DetailRow label="Slippage" value={slippageAuto ? '0.5% (auto)' : `${(Number(slippage) * 100).toFixed(1)}%`} last />
        </View>

        {quote?.approvalAddress && (
          <View style={s.approvalNote}>
            <Ionicons name="information-circle-outline" size={14} color="#888" />
            <Text style={s.disc}>Two transactions required: token approval + swap</Text>
          </View>
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

  // ── Form ──────────────────────────────────────────────────────
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
          {/* From */}
          <Text style={s.cardLabel}>YOU PAY</Text>
          <View style={s.cardRow}>
            <TouchableOpacity style={s.tokenBtn} onPress={() => setPickerMode('from')} activeOpacity={0.7}>
              <TokenIcon uri={fromToken?.logoURI} symbol={fromToken?.symbol} size={24} />
              <View>
                <Text style={s.tokenBtnText}>{fromToken?.symbol ?? 'Select'}</Text>
                {fromToken && <Text style={s.tokenChain}>{chainName(fromChainId)}</Text>}
              </View>
              <Ionicons name="chevron-down" size={13} color="#888" />
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
          <View style={s.amountMeta}>
            {fromAmountUSD != null && (
              <Text style={s.usdValue}>≈ ${fromAmountUSD.toLocaleString('en-US', { maximumFractionDigits: 2, minimumFractionDigits: 2 })}</Text>
            )}
            <TouchableOpacity onPress={() => fromBalance !== '0' && setFromAmount(fromBalance)}>
              <Text style={s.balanceText}>Balance: {formattedBalance} {fromToken?.symbol}</Text>
            </TouchableOpacity>
          </View>

          {/* Swap direction button */}
          <View style={s.arrowWrap}>
            <TouchableOpacity style={s.arrowBtn} onPress={swapDirection} activeOpacity={0.7}>
              <Ionicons name="swap-vertical" size={18} color="#FFF" />
            </TouchableOpacity>
          </View>

          {/* To */}
          <Text style={s.cardLabel}>YOU RECEIVE</Text>
          <View style={s.cardRow}>
            <TouchableOpacity style={s.tokenBtn} onPress={() => setPickerMode('to')} activeOpacity={0.7}>
              <TokenIcon uri={toToken?.logoURI} symbol={toToken?.symbol} size={24} />
              <View>
                <Text style={s.tokenBtnText}>{toToken?.symbol ?? 'Select'}</Text>
                {toToken && <Text style={s.tokenChain}>{chainName(toChainId)}</Text>}
              </View>
              <Ionicons name="chevron-down" size={13} color="#888" />
            </TouchableOpacity>
            {quoteStatus === 'quoting'
              ? <ActivityIndicator size="small" color={GOLD} style={{ flex: 1 }} />
              : <View style={{ flex: 1, alignItems: 'flex-end' }}>
                  <Text style={s.amountOutput}>{outputAmount ?? '—'}</Text>
                  {toAmountUSD != null && (
                    <Text style={s.usdValue}>≈ ${toAmountUSD.toLocaleString('en-US', { maximumFractionDigits: 2, minimumFractionDigits: 2 })}</Text>
                  )}
                </View>
            }
          </View>
        </View>

        {/* Quote info / errors */}
        {(quoteStatus === 'quoted' || quoteStatus === 'expired') && quote && (
          <View style={s.infoBar}>
            <Ionicons name="flash" size={13} color={GOLD} />
            <Text style={s.infoText}>
              {quote.tool} · ~{Math.ceil(quote.estimatedDuration / 60)} min · {FEE_PCT}% fee · ${quote.gasCostUSD} gas
            </Text>
            {quoteStatus === 'expired' ? (
              <TouchableOpacity onPress={refreshQuote} style={s.refreshBtn}>
                <Ionicons name="refresh" size={13} color={GOLD} />
                <Text style={s.refreshText}>Refresh</Text>
              </TouchableOpacity>
            ) : (
              <View style={s.countdown}>
                <Text style={s.countdownText}>{quoteSecondsLeft}s</Text>
              </View>
            )}
          </View>
        )}
        {/* Price impact warning */}
        {priceImpactPct != null && priceImpactPct < -5 && (
          <View style={[s.infoBar, priceImpactPct < -15 ? s.infoError : s.infoWarn]}>
            <Ionicons name="warning" size={13} color={priceImpactPct < -15 ? '#DC2626' : '#D97706'} />
            <Text style={[s.infoText, { color: priceImpactPct < -15 ? '#DC2626' : '#92400E' }]}>
              {priceImpactPct < -15 ? 'High' : 'Moderate'} price impact ({Math.abs(priceImpactPct).toFixed(1)}%) — check liquidity before swapping
            </Text>
          </View>
        )}
        {quoteStatus === 'no_route' && (
          <View style={[s.infoBar, s.infoError]}>
            <Ionicons name="warning" size={13} color="#DC2626" />
            <Text style={[s.infoText, { color: '#DC2626' }]}>No route available for this pair. Try a different token or amount.</Text>
          </View>
        )}
        {quoteStatus === 'error' && (
          <View style={[s.infoBar, s.infoError]}>
            <Ionicons name="alert-circle" size={13} color="#DC2626" />
            <Text style={[s.infoText, { color: '#DC2626' }]}>{quoteError || 'Swap pricing unavailable. Try again shortly.'}</Text>
          </View>
        )}
        {quoteStatus === 'expired' && (
          <View style={[s.infoBar, s.infoWarn]}>
            <Ionicons name="time-outline" size={13} color="#D97706" />
            <Text style={[s.infoText, { color: '#92400E' }]}>Quote expired — tap Refresh for a new price</Text>
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
        tokensLoading={pickerMode === 'from' ? tokensLoading : toTokensLoading}
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
            <View style={s.slippageHandle} />
            <Text style={s.slippageTitle}>Slippage Tolerance</Text>
            <View style={s.slippageRow}>
              <View>
                <Text style={s.detL}>Auto</Text>
                <Text style={s.slippageHint}>0.5% — recommended</Text>
              </View>
              <Switch value={slippageAuto} onValueChange={setSlippageAuto} trackColor={{ true: GOLD }} thumbColor="#FFF" />
            </View>
            {!slippageAuto && (
              <View style={s.slippageInputWrap}>
                <TextInput
                  style={s.slippageInput}
                  value={String(Number(slippage) * 100)}
                  onChangeText={v => { const n = Number(v); if (!isNaN(n) && n >= 0.1 && n <= 49) setSlippage(String(n / 100)); }}
                  keyboardType="decimal-pad"
                  placeholder="Enter %"
                  placeholderTextColor="#CCC"
                />
                <Text style={s.slippagePct}>%</Text>
              </View>
            )}
          </Pressable>
        </Pressable>
      </Modal>
    </KeyboardAvoidingView>
  );
}

function SwapSuccessScreen({ fromToken, toToken, fromAmount, outputAmount, txHash, onDone }: {
  fromToken: LiFiToken | null; toToken: LiFiToken | null;
  fromAmount: string; outputAmount: string | null;
  txHash: string; onDone: () => void;
}) {
  const iconScale = useSharedValue(0);
  const ringScale = useSharedValue(1);
  const ringOpacity = useSharedValue(0);
  const contentOpacity = useSharedValue(0);
  const contentTranslateY = useSharedValue(28);
  const btnOpacity = useSharedValue(0);
  const btnTranslateY = useSharedValue(20);

  useEffect(() => {
    iconScale.value = withSpring(1, { damping: 11, stiffness: 160 });
    ringOpacity.value = withDelay(80, withSequence(
      withTiming(0.5, { duration: 100 }),
      withTiming(0, { duration: 550 }),
    ));
    ringScale.value = withDelay(80, withTiming(2.2, { duration: 650 }));
    contentOpacity.value = withDelay(320, withTiming(1, { duration: 380 }));
    contentTranslateY.value = withDelay(320, withSpring(0, { damping: 15 }));
    btnOpacity.value = withDelay(560, withTiming(1, { duration: 300 }));
    btnTranslateY.value = withDelay(560, withSpring(0, { damping: 14 }));
  }, []);

  const iconStyle = useAnimatedStyle(() => ({ transform: [{ scale: iconScale.value }] }));
  const ringStyle = useAnimatedStyle(() => ({ transform: [{ scale: ringScale.value }], opacity: ringOpacity.value }));
  const contentStyle = useAnimatedStyle(() => ({ opacity: contentOpacity.value, transform: [{ translateY: contentTranslateY.value }] }));
  const btnStyle = useAnimatedStyle(() => ({ opacity: btnOpacity.value, transform: [{ translateY: btnTranslateY.value }] }));

  return (
    <View style={[s.container, { justifyContent: 'center', padding: 24 }]}>
      {/* Animated checkmark */}
      <View style={{ alignItems: 'center', marginBottom: 32 }}>
        <View style={{ width: 100, height: 100, alignItems: 'center', justifyContent: 'center' }}>
          <Animated.View style={[{
            position: 'absolute', width: 100, height: 100, borderRadius: 50,
            backgroundColor: '#D1FAE5',
          }, ringStyle]} />
          <Animated.View style={[{
            width: 72, height: 72, borderRadius: 36,
            backgroundColor: '#16A34A',
            alignItems: 'center', justifyContent: 'center',
            shadowColor: '#16A34A', shadowOffset: { width: 0, height: 6 },
            shadowOpacity: 0.3, shadowRadius: 12, elevation: 8,
          }, iconStyle]}>
            <Ionicons name="checkmark" size={38} color="#FFF" />
          </Animated.View>
        </View>
      </View>

      {/* Title + swap summary */}
      <Animated.View style={[{ alignItems: 'center' }, contentStyle]}>
        <Text style={[s.bigTitle, { textAlign: 'center', marginBottom: 24 }]}>Swap Complete</Text>

        <View style={[s.card, { width: '100%', gap: 0 }]}>
          <View style={s.successTokenRow}>
            <TokenIcon uri={fromToken?.logoURI} symbol={fromToken?.symbol} size={36} />
            <View style={{ flex: 1 }}>
              <Text style={s.successAmt}>{fromAmount} {fromToken?.symbol}</Text>
              <Text style={s.successTokenName}>{fromToken?.name}</Text>
            </View>
          </View>
          <View style={s.successArrowRow}>
            <View style={s.successArrowLine} />
            <Ionicons name="arrow-down" size={14} color="#AAA" />
            <View style={s.successArrowLine} />
          </View>
          <View style={s.successTokenRow}>
            <TokenIcon uri={toToken?.logoURI} symbol={toToken?.symbol} size={36} />
            <View style={{ flex: 1 }}>
              <Text style={[s.successAmt, { color: GOLD }]}>{outputAmount} {toToken?.symbol}</Text>
              <Text style={s.successTokenName}>{toToken?.name}</Text>
            </View>
          </View>
        </View>

        {txHash ? (
          <Text style={s.txHash} numberOfLines={1} ellipsizeMode="middle">Tx: {txHash}</Text>
        ) : null}
      </Animated.View>

      {/* Done button */}
      <Animated.View style={[{ width: '100%', marginTop: 28 }, btnStyle]}>
        <TouchableOpacity style={s.btn} onPress={onDone}>
          <Text style={s.btnT}>Done</Text>
        </TouchableOpacity>
      </Animated.View>
    </View>
  );
}

function SwapFailureScreen({ error, onRetry }: { error: string; onRetry: () => void }) {
  const iconScale = useSharedValue(0);
  const contentOpacity = useSharedValue(0);

  useEffect(() => {
    iconScale.value = withSpring(1, { damping: 11, stiffness: 160 });
    contentOpacity.value = withDelay(280, withTiming(1, { duration: 350 }));
  }, []);

  const iconStyle = useAnimatedStyle(() => ({ transform: [{ scale: iconScale.value }] }));
  const contentStyle = useAnimatedStyle(() => ({ opacity: contentOpacity.value }));

  return (
    <View style={[s.container, { justifyContent: 'center', padding: 24 }]}>
      <View style={{ alignItems: 'center', marginBottom: 32 }}>
        <Animated.View style={[{
          width: 72, height: 72, borderRadius: 36,
          backgroundColor: '#DC2626',
          alignItems: 'center', justifyContent: 'center',
          shadowColor: '#DC2626', shadowOffset: { width: 0, height: 6 },
          shadowOpacity: 0.25, shadowRadius: 12, elevation: 8,
        }, iconStyle]}>
          <Ionicons name="close" size={36} color="#FFF" />
        </Animated.View>
      </View>

      <Animated.View style={[{ alignItems: 'center', width: '100%' }, contentStyle]}>
        <Text style={[s.bigTitle, { textAlign: 'center', marginBottom: 12 }]}>Swap Failed</Text>
        {error ? (
          <View style={[s.card, { width: '100%', backgroundColor: '#FEF2F2' }]}>
            <Text style={{ fontSize: 13, color: '#DC2626', textAlign: 'center', lineHeight: 20 }}>{error}</Text>
          </View>
        ) : null}
        <TouchableOpacity style={[s.btn, { marginTop: 28, width: '100%' }]} onPress={onRetry}>
          <Text style={s.btnT}>Try Again</Text>
        </TouchableOpacity>
      </Animated.View>
    </View>
  );
}

function DetailRow({ label, value, highlight, last }: { label: string; value: string; highlight?: boolean; last?: boolean }) {
  return (
    <View style={[s.detRow, !last && s.detRowBorder]}>
      <Text style={s.detL}>{label}</Text>
      <Text style={[s.detV, highlight && { color: GOLD }]}>{value}</Text>
    </View>
  );
}

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
  scroll: { flexGrow: 1, padding: 20, paddingBottom: 48 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 24 },

  // Header
  headerRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 20 },
  bigTitle: { fontSize: 26, fontWeight: '700', color: DARK },
  sub: { fontSize: 13, color: '#888', marginTop: 2 },
  settingsBtn: { padding: 8 },

  // Card
  card: {
    backgroundColor: '#FFF', borderRadius: 20, padding: 18,
    borderWidth: 1, borderColor: '#F0F0F0',
    shadowColor: '#000', shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05, shadowRadius: 10, elevation: 2,
  },
  cardLabel: { fontSize: 10, fontWeight: '700', color: '#BBB', letterSpacing: 1.2, marginBottom: 10 },
  cardRow: { flexDirection: 'row', alignItems: 'center', gap: 10, marginBottom: 6 },

  // Token button
  tokenBtn: {
    flexDirection: 'row', alignItems: 'center', gap: 6,
    backgroundColor: '#F5F5F5', paddingHorizontal: 10, paddingVertical: 8, borderRadius: 20,
    maxWidth: '50%',
  },
  tokenBtnText: { fontSize: 15, fontWeight: '700', color: DARK },
  tokenChain: { fontSize: 10, color: '#AAA', marginTop: 1 },

  // Amount
  amountInput: { flex: 1, fontSize: 28, fontWeight: '700', color: DARK, textAlign: 'right', padding: 0 },
  amountOutput: { flex: 1, fontSize: 28, fontWeight: '700', color: GOLD, textAlign: 'right' },
  amountMeta: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 },
  usdValue: { fontSize: 12, color: '#AAA', fontWeight: '400' },
  balanceText: { fontSize: 12, color: GOLD, fontWeight: '500' },

  // Swap arrow
  arrowWrap: { alignItems: 'center', marginVertical: 4 },
  arrowBtn: {
    width: 38, height: 38, borderRadius: 19, backgroundColor: DARK,
    justifyContent: 'center', alignItems: 'center',
    shadowColor: '#000', shadowOffset: { width: 0, height: 3 },
    shadowOpacity: 0.15, shadowRadius: 6, elevation: 4,
  },

  // Info bar
  infoBar: {
    flexDirection: 'row', alignItems: 'center', gap: 6,
    backgroundColor: '#FFFBEB', borderWidth: 1, borderColor: '#FDE68A',
    borderRadius: 12, padding: 10, marginTop: 12,
  },
  infoError: { backgroundColor: '#FEF2F2', borderColor: '#FECACA' },
  infoWarn: { backgroundColor: '#FFFBEB', borderColor: '#FCD34D' },
  infoText: { fontSize: 12, color: '#92400E', flex: 1 },
  countdown: { backgroundColor: '#F0F0F0', borderRadius: 8, paddingHorizontal: 6, paddingVertical: 2 },
  countdownText: { fontSize: 11, fontWeight: '700', color: '#888' },
  refreshBtn: { flexDirection: 'row', alignItems: 'center', gap: 3 },
  refreshText: { fontSize: 12, fontWeight: '700', color: GOLD },

  // Buttons
  btn: {
    flexDirection: 'row', backgroundColor: GOLD, paddingVertical: 16,
    borderRadius: 16, alignItems: 'center', justifyContent: 'center', marginTop: 20,
    shadowColor: GOLD, shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3, shadowRadius: 10, elevation: 4,
  },
  btnT: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  btn2: { paddingVertical: 14, borderRadius: 16, alignItems: 'center', marginTop: 8 },
  btn2T: { fontSize: 15, color: '#AAA', fontWeight: '600' },

  // Confirm screen
  backRow: { flexDirection: 'row', alignItems: 'center', marginBottom: 16, gap: 2 },
  confirmSection: { paddingVertical: 12 },
  confirmTokenRow: { flexDirection: 'row', alignItems: 'center', gap: 12, marginTop: 8 },
  confirmAmount: { fontSize: 22, fontWeight: '700', color: DARK },
  confirmMeta: { fontSize: 12, color: '#AAA', marginTop: 2 },
  arrowDivider: { flexDirection: 'row', alignItems: 'center', gap: 8, marginVertical: 4 },
  arrowDividerLine: { flex: 1, height: 1, backgroundColor: '#F0F0F0' },
  arrowCircle: {
    width: 28, height: 28, borderRadius: 14, backgroundColor: GOLD,
    alignItems: 'center', justifyContent: 'center',
  },

  // Details table
  detRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: 11 },
  detRowBorder: { borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: '#F0F0F0' },
  detL: { fontSize: 13, color: '#888' },
  detV: { fontSize: 13, fontWeight: '600', color: DARK },

  // Approval note
  approvalNote: { flexDirection: 'row', alignItems: 'center', gap: 6, marginTop: 12, marginBottom: 4 },
  disc: { fontSize: 12, color: '#AAA', flex: 1 },

  // Signing screen
  signingRing: {
    width: 80, height: 80, borderRadius: 40,
    borderWidth: 2, borderColor: '#F0F0F0',
    alignItems: 'center', justifyContent: 'center', marginBottom: 24,
  },
  signingStep: { fontSize: 14, color: GOLD, fontWeight: '600', marginTop: 8 },
  signingHint: { fontSize: 12, color: '#AAA', marginTop: 6 },

  // Success / failure
  successTokenRow: { flexDirection: 'row', alignItems: 'center', gap: 12, paddingVertical: 12 },
  successAmt: { fontSize: 16, fontWeight: '700', color: DARK },
  successTokenName: { fontSize: 12, color: '#AAA', marginTop: 2 },
  successArrowRow: { flexDirection: 'row', alignItems: 'center', gap: 8, paddingVertical: 2 },
  successArrowLine: { flex: 1, height: 1, backgroundColor: '#F0F0F0' },
  txHash: { fontSize: 11, color: '#BBB', marginTop: 16, maxWidth: '90%', textAlign: 'center' },

  // Slippage sheet
  overlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'flex-end' },
  slippageSheet: { backgroundColor: '#FFF', borderTopLeftRadius: 24, borderTopRightRadius: 24, padding: 24, paddingTop: 16, gap: 20 },
  slippageHandle: { width: 36, height: 4, borderRadius: 2, backgroundColor: '#E0E0E0', alignSelf: 'center', marginBottom: 8 },
  slippageTitle: { fontSize: 17, fontWeight: '700', color: DARK },
  slippageRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' },
  slippageHint: { fontSize: 12, color: '#AAA', marginTop: 2 },
  slippageInputWrap: { flexDirection: 'row', alignItems: 'center', borderWidth: 1, borderColor: '#E0E0E0', borderRadius: 12, paddingHorizontal: 14, paddingVertical: 12 },
  slippageInput: { flex: 1, fontSize: 16, color: DARK },
  slippagePct: { fontSize: 15, fontWeight: '600', color: '#AAA' },
});
