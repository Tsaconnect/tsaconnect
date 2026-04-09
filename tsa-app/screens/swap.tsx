// screens/swap.tsx
// OTC Marketplace swap screen — buy MCGP with USDC or sell MCGP for USDC
import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  TextInput,
  ScrollView,
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  Pressable,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { COLORS, SIZES, FONTS, SHADOWS } from '@/constants';
import { signTransaction } from '@/services/wallet';
import {
  getSwapPrice,
  prepareSwap,
  submitTransaction,
  getWalletBalances,
  type WalletBalance,
} from '@/services/walletApi';
import { useKycVerification } from '@/hooks/useKycVerification';

const MCGP_DECIMALS = 18;
const USDC_DECIMALS = 6;

type Direction = 'buy' | 'sell';
type ScreenState = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

const GOLD = '#D4AF37';
const DARK_CARD = '#1A1A2E';

function toWei(amount: string, decimals: number): bigint {
  try {
    const [intPart, fracPart = ''] = amount.split('.');
    const padded = fracPart.padEnd(decimals, '0').slice(0, decimals);
    return BigInt(intPart || '0') * BigInt(10 ** decimals) + BigInt(padded || '0');
  } catch {
    return BigInt(0);
  }
}

function fromWei(wei: string, decimals: number, displayDecimals = 6): string {
  try {
    const n = BigInt(wei);
    const factor = BigInt(10 ** decimals);
    const whole = n / factor;
    const frac = n % factor;
    const fracStr = frac.toString().padStart(decimals, '0').slice(0, displayDecimals);
    return `${whole}.${fracStr}`.replace(/\.?0+$/, '') || '0';
  } catch {
    return '0';
  }
}

// Token icon component
const TokenBadge = ({ symbol, size = 36 }: { symbol: string; size?: number }) => {
  const colors: Record<string, { bg: string; text: string }> = {
    MCGP: { bg: '#D4AF37', text: '#FFF' },
    USDC: { bg: '#2775CA', text: '#FFF' },
    USDT: { bg: '#26A17B', text: '#FFF' },
  };
  const c = colors[symbol] || { bg: '#888', text: '#FFF' };
  return (
    <View style={[s.tokenBadge, { width: size, height: size, borderRadius: size / 2, backgroundColor: c.bg }]}>
      <Text style={[s.tokenBadgeText, { fontSize: size * 0.35, color: c.text }]}>
        {symbol === 'USDC' ? '$' : symbol[0]}
      </Text>
    </View>
  );
};

const SwapScreen = () => {
  const { requireKycVerified } = useKycVerification();

  const [direction, setDirection] = useState<Direction>('buy');
  const [amount, setAmount] = useState('');
  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [loadingBalances, setLoadingBalances] = useState(true);

  const [priceLoading, setPriceLoading] = useState(false);
  const [quoteUsdcAmount, setQuoteUsdcAmount] = useState<string | null>(null);
  const [quoteMcgpAmount, setQuoteMcgpAmount] = useState<string | null>(null);
  const [priceError, setPriceError] = useState('');

  const [screenState, setScreenState] = useState<ScreenState>('form');
  const [error, setError] = useState('');
  const [swapTxHash, setSwapTxHash] = useState('');
  const [statusMessage, setStatusMessage] = useState('');

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const result = await getWalletBalances();
        if (result.success && Array.isArray(result.data)) setBalances(result.data);
      } catch (e) {
        console.warn('Failed to load balances', e);
      } finally {
        setLoadingBalances(false);
      }
    })();
  }, []);

  const fetchPrice = useCallback(
    (dir: Direction, inputAmount: string) => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
      if (!inputAmount || isNaN(Number(inputAmount)) || Number(inputAmount) <= 0) {
        setQuoteUsdcAmount(null);
        setQuoteMcgpAmount(null);
        setPriceError('');
        return;
      }
      debounceRef.current = setTimeout(async () => {
        setPriceLoading(true);
        setPriceError('');
        try {
          const mcgpWei = toWei(inputAmount, MCGP_DECIMALS).toString();
          const result = await getSwapPrice(dir, mcgpWei);
          if (result.success && result.data) {
            setQuoteMcgpAmount(result.data.mcgpAmount);
            setQuoteUsdcAmount(result.data.usdcAmount);
          } else {
            setPriceError(result.message || 'Unable to fetch price');
            setQuoteUsdcAmount(null);
            setQuoteMcgpAmount(null);
          }
        } catch (e: any) {
          setPriceError(e.message || 'Price fetch failed');
        } finally {
          setPriceLoading(false);
        }
      }, 500);
    },
    []
  );

  useEffect(() => {
    fetchPrice(direction, amount);
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current); };
  }, [amount, direction, fetchPrice]);

  const getMcgpBalance = (): string => balances.find((b) => b.symbol === 'MCGP')?.balance || '0';
  const getUsdcBalance = (): string => balances.find((b) => b.symbol === 'USDC')?.balance || '0';

  const fromToken = direction === 'buy' ? 'USDC' : 'MCGP';
  const toToken = direction === 'buy' ? 'MCGP' : 'USDC';
  const fromBalance = direction === 'buy' ? getUsdcBalance() : getMcgpBalance();
  const usdcDisplay = quoteUsdcAmount ? fromWei(quoteUsdcAmount, USDC_DECIMALS, 4) : null;

  const toggleDirection = () => {
    setDirection((d) => (d === 'buy' ? 'sell' : 'buy'));
    setAmount('');
    setQuoteUsdcAmount(null);
    setQuoteMcgpAmount(null);
    setPriceError('');
    setError('');
  };

  const validate = (): string | null => {
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    if (direction === 'sell' && Number(amount) > Number(getMcgpBalance())) return 'Insufficient MCGP balance.';
    if (direction === 'buy' && usdcDisplay && Number(usdcDisplay) > Number(getUsdcBalance())) return 'Insufficient USDC balance.';
    return null;
  };

  const handleReview = () => {
    const err = validate();
    if (err) { setError(err); return; }
    if (!quoteUsdcAmount) { setError('Wait for price quote.'); return; }
    setError('');
    setScreenState('confirm');
  };

  const handleSwap = async () => {
    if (!requireKycVerified()) return;
    setScreenState('signing');
    setError('');

    try {
      const mcgpWei = toWei(amount, MCGP_DECIMALS).toString();

      setStatusMessage('Preparing swap...');
      const prepResult = await prepareSwap(direction, mcgpWei, 50);
      if (!prepResult.success || !prepResult.data) throw new Error(prepResult.message || 'Failed to prepare swap');

      const { approveTx, swapTx, usdcAmount, mcgpAmount } = prepResult.data;

      // Step 1: Approve
      setStatusMessage('Approving tokens...');
      const signedApprove = await signTransaction({
        to: approveTx.to, data: approveTx.data, value: approveTx.value,
        gasLimit: approveTx.gasLimit, gasPrice: approveTx.gasPrice,
        nonce: approveTx.nonce, chainId: approveTx.chainId,
      });
      await submitTransaction(signedApprove, 'approve', direction === 'buy' ? 'USDC' : 'MCGP',
        approveTx.to, fromWei(direction === 'buy' ? usdcAmount : mcgpAmount, direction === 'buy' ? USDC_DECIMALS : MCGP_DECIMALS), approveTx.chainId);

      // Step 2: Swap
      setStatusMessage('Executing swap...');
      const signedSwap = await signTransaction({
        to: swapTx.to, data: swapTx.data, value: swapTx.value,
        gasLimit: swapTx.gasLimit, gasPrice: swapTx.gasPrice,
        nonce: swapTx.nonce, chainId: swapTx.chainId,
      });
      const swapResult = await submitTransaction(signedSwap, 'swap', 'MCGP', swapTx.to,
        fromWei(mcgpAmount, MCGP_DECIMALS), swapTx.chainId);

      if (swapResult.success && swapResult.data) {
        setSwapTxHash(swapResult.data.txHash);
        setScreenState('success');
      } else {
        throw new Error(swapResult.message || 'Swap submission failed');
      }
    } catch (e: any) {
      setError(e.message || 'Swap failed.');
      setScreenState('failure');
    }
  };

  // --- Signing screen ---
  if (screenState === 'signing') {
    return (
      <View style={s.container}>
        <View style={s.centeredContainer}>
          <View style={s.signingIconWrap}>
            <ActivityIndicator size="large" color={GOLD} />
          </View>
          <Text style={s.signingTitle}>Processing Swap</Text>
          <Text style={s.signingMessage}>{statusMessage}</Text>
          <View style={s.signingSteps}>
            <Text style={s.signingStep}>1. Approve token spend</Text>
            <Text style={s.signingStep}>2. Execute swap on-chain</Text>
          </View>
        </View>
      </View>
    );
  }

  // --- Success / Failure ---
  if (screenState === 'success' || screenState === 'failure') {
    const isSuccess = screenState === 'success';
    return (
      <View style={s.container}>
        <View style={s.centeredContainer}>
          <View style={[s.resultIconWrap, { backgroundColor: isSuccess ? '#D1FAE5' : '#FEE2E2' }]}>
            <Ionicons name={isSuccess ? 'checkmark-circle' : 'close-circle'} size={48} color={isSuccess ? '#16A34A' : '#DC2626'} />
          </View>
          <Text style={s.resultTitle}>{isSuccess ? 'Swap Complete!' : 'Swap Failed'}</Text>
          <Text style={s.resultMessage}>
            {isSuccess
              ? direction === 'buy'
                ? `You bought ${amount} MCGP`
                : `You sold ${amount} MCGP`
              : error || 'Something went wrong.'}
          </Text>
          {swapTxHash ? (
            <Text style={s.txHash} numberOfLines={1} ellipsizeMode="middle">Tx: {swapTxHash}</Text>
          ) : null}
          <TouchableOpacity style={s.primaryBtn} onPress={() => isSuccess ? router.replace('/tokenization') : setScreenState('form')}>
            <Text style={s.primaryBtnText}>{isSuccess ? 'Back to Wallet' : 'Try Again'}</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  // --- Confirm screen ---
  if (screenState === 'confirm') {
    return (
      <View style={s.container}>
        <ScrollView contentContainerStyle={s.scrollContent}>
          <Pressable onPress={() => setScreenState('form')} style={s.backRow}>
            <Ionicons name="chevron-back" size={22} color={GOLD} />
            <Text style={s.backText}>Back</Text>
          </Pressable>
          <Text style={s.pageTitle}>Confirm Swap</Text>

          <View style={s.confirmCard}>
            {/* From */}
            <View style={s.confirmTokenRow}>
              <TokenBadge symbol={fromToken} />
              <View style={{ flex: 1, marginLeft: 12 }}>
                <Text style={s.confirmTokenLabel}>{direction === 'buy' ? 'You Pay' : 'You Send'}</Text>
                <Text style={s.confirmTokenAmount}>
                  {direction === 'buy' ? `${usdcDisplay} USDC` : `${amount} MCGP`}
                </Text>
              </View>
            </View>

            <View style={s.confirmArrow}>
              <Ionicons name="arrow-down" size={20} color={GOLD} />
            </View>

            {/* To */}
            <View style={s.confirmTokenRow}>
              <TokenBadge symbol={toToken} />
              <View style={{ flex: 1, marginLeft: 12 }}>
                <Text style={s.confirmTokenLabel}>You Receive</Text>
                <Text style={s.confirmTokenAmount}>
                  {direction === 'buy' ? `${amount} MCGP` : `${usdcDisplay} USDC`}
                </Text>
              </View>
            </View>

            <View style={s.confirmDivider} />
            <View style={s.confirmDetailRow}>
              <Text style={s.confirmDetailLabel}>Rate</Text>
              <Text style={s.confirmDetailValue}>
                1 MCGP = {usdcDisplay && Number(amount) > 0 ? (Number(usdcDisplay) / Number(amount)).toFixed(4) : '—'} USDC
              </Text>
            </View>
            <View style={s.confirmDetailRow}>
              <Text style={s.confirmDetailLabel}>Slippage</Text>
              <Text style={s.confirmDetailValue}>0.5%</Text>
            </View>
          </View>

          <Text style={s.disclaimer}>Two transactions will be signed: token approval and the swap.</Text>

          <TouchableOpacity style={s.primaryBtn} onPress={handleSwap}>
            <Ionicons name="swap-vertical" size={18} color="#FFF" style={{ marginRight: 8 }} />
            <Text style={s.primaryBtnText}>Confirm Swap</Text>
          </TouchableOpacity>
          <TouchableOpacity style={s.secondaryBtn} onPress={() => setScreenState('form')}>
            <Text style={s.secondaryBtnText}>Cancel</Text>
          </TouchableOpacity>
        </ScrollView>
      </View>
    );
  }

  // --- Main Form ---
  const rateDisplay = usdcDisplay && Number(amount) > 0 ? (Number(usdcDisplay) / Number(amount)).toFixed(4) : null;

  return (
    <KeyboardAvoidingView style={s.container} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
      <ScrollView contentContainerStyle={s.scrollContent} showsVerticalScrollIndicator={false}>
        {/* Header */}
        <View style={s.headerRow}>
          <View>
            <Text style={s.pageTitle}>Swap</Text>
            <Text style={s.pageSubtitle}>OTC Marketplace</Text>
          </View>
          {rateDisplay && (
            <View style={s.ratePill}>
              <Ionicons name="trending-up" size={14} color={GOLD} />
              <Text style={s.ratePillText}>1 MCGP = ${rateDisplay}</Text>
            </View>
          )}
        </View>

        {/* From Card — what user pays */}
        <View style={s.swapCard}>
          <View style={s.swapCardHeader}>
            <Text style={s.swapCardLabel}>{direction === 'buy' ? 'You Pay' : 'You Send'}</Text>
            <TouchableOpacity onPress={() => {
              if (direction === 'sell') setAmount(getMcgpBalance());
            }}>
              <Text style={s.swapCardBalance}>
                Balance: {fromBalance} {fromToken}
              </Text>
            </TouchableOpacity>
          </View>
          <View style={s.swapCardBody}>
            <TokenBadge symbol={fromToken} size={40} />
            {direction === 'sell' ? (
              <View style={s.swapInputWrap}>
                <TextInput
                  style={s.swapInput}
                  placeholder="0"
                  placeholderTextColor="#666"
                  value={amount}
                  onChangeText={(t) => { setAmount(t); setError(''); }}
                  keyboardType="decimal-pad"
                />
                <Text style={s.swapInputToken}>MCGP</Text>
              </View>
            ) : (
              <View style={s.swapOutputWrap}>
                {priceLoading ? (
                  <ActivityIndicator size="small" color={GOLD} />
                ) : (
                  <Text style={s.swapOutputText}>{usdcDisplay || '0'}</Text>
                )}
                <Text style={s.swapInputToken}>USDC</Text>
              </View>
            )}
            {direction === 'sell' && (
              <TouchableOpacity style={s.maxChip} onPress={() => setAmount(getMcgpBalance())}>
                <Text style={s.maxChipText}>MAX</Text>
              </TouchableOpacity>
            )}
          </View>
        </View>

        {/* Swap Direction Button */}
        <View style={s.swapArrowContainer}>
          <TouchableOpacity style={s.swapArrowBtn} onPress={toggleDirection} activeOpacity={0.7}>
            <Ionicons name="swap-vertical" size={22} color="#FFF" />
          </TouchableOpacity>
        </View>

        {/* To Card — what user receives */}
        <View style={[s.swapCard, { marginTop: -8 }]}>
          <View style={s.swapCardHeader}>
            <Text style={s.swapCardLabel}>You Receive</Text>
            <Text style={s.swapCardBalance}>
              Balance: {direction === 'buy' ? getMcgpBalance() : getUsdcBalance()} {toToken}
            </Text>
          </View>
          <View style={s.swapCardBody}>
            <TokenBadge symbol={toToken} size={40} />
            {direction === 'buy' ? (
              <View style={s.swapInputWrap}>
                <TextInput
                  style={s.swapInput}
                  placeholder="0"
                  placeholderTextColor="#666"
                  value={amount}
                  onChangeText={(t) => { setAmount(t); setError(''); }}
                  keyboardType="decimal-pad"
                />
                <Text style={s.swapInputToken}>MCGP</Text>
              </View>
            ) : (
              <View style={s.swapOutputWrap}>
                {priceLoading ? (
                  <ActivityIndicator size="small" color={GOLD} />
                ) : (
                  <Text style={s.swapOutputText}>{usdcDisplay || '0'}</Text>
                )}
                <Text style={s.swapInputToken}>USDC</Text>
              </View>
            )}
          </View>
        </View>

        {/* Price info */}
        {priceError ? (
          <View style={s.infoBar}>
            <Ionicons name="warning" size={16} color="#DC2626" />
            <Text style={[s.infoBarText, { color: '#DC2626' }]}>{priceError}</Text>
          </View>
        ) : rateDisplay ? (
          <View style={s.infoBar}>
            <Ionicons name="information-circle" size={16} color={GOLD} />
            <Text style={s.infoBarText}>1 MCGP = {rateDisplay} USDC  ·  0.5% slippage</Text>
          </View>
        ) : null}

        {error ? (
          <View style={[s.infoBar, { backgroundColor: '#FEF2F2', borderColor: '#FECACA' }]}>
            <Ionicons name="alert-circle" size={16} color="#DC2626" />
            <Text style={[s.infoBarText, { color: '#DC2626' }]}>{error}</Text>
          </View>
        ) : null}

        {/* Action Button */}
        <TouchableOpacity
          style={[s.primaryBtn, (!amount || priceLoading || !!priceError) && s.disabledBtn]}
          onPress={handleReview}
          disabled={!amount || priceLoading || !!priceError}
        >
          <Text style={s.primaryBtnText}>Review Swap</Text>
        </TouchableOpacity>
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

export default SwapScreen;

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA' },
  scrollContent: { flexGrow: 1, padding: 20, paddingBottom: 40 },
  centeredContainer: { flex: 1, justifyContent: 'center', alignItems: 'center', padding: 24 },

  // Header
  headerRow: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 24 },
  pageTitle: { fontSize: 28, fontWeight: '700', color: '#1A1A1A' },
  pageSubtitle: { fontSize: 13, color: '#888', marginTop: 2 },
  ratePill: {
    flexDirection: 'row', alignItems: 'center', gap: 4,
    backgroundColor: '#FFF8E1', paddingHorizontal: 10, paddingVertical: 6, borderRadius: 20,
  },
  ratePillText: { fontSize: 12, fontWeight: '600', color: GOLD },

  backRow: { flexDirection: 'row', alignItems: 'center', marginBottom: 16, gap: 4 },
  backText: { fontSize: 15, color: GOLD, fontWeight: '600' },

  // Swap cards
  swapCard: {
    backgroundColor: '#FFF', borderRadius: 16, padding: 16,
    borderWidth: 1, borderColor: '#F0F0F0',
    shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.04, shadowRadius: 8,
    elevation: 2,
  },
  swapCardHeader: { flexDirection: 'row', justifyContent: 'space-between', marginBottom: 12 },
  swapCardLabel: { fontSize: 13, fontWeight: '600', color: '#888', textTransform: 'uppercase', letterSpacing: 0.5 },
  swapCardBalance: { fontSize: 12, color: '#AAA' },
  swapCardBody: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  swapInputWrap: { flex: 1, flexDirection: 'row', alignItems: 'center' },
  swapInput: { flex: 1, fontSize: 28, fontWeight: '700', color: '#1A1A1A', padding: 0 },
  swapInputToken: { fontSize: 14, fontWeight: '600', color: '#888', marginLeft: 4 },
  swapOutputWrap: { flex: 1, flexDirection: 'row', alignItems: 'center' },
  swapOutputText: { flex: 1, fontSize: 28, fontWeight: '700', color: '#1A1A1A' },
  swapCardNote: { fontSize: 12, color: '#888', marginTop: 8, marginLeft: 52 },

  maxChip: { backgroundColor: GOLD, paddingHorizontal: 12, paddingVertical: 6, borderRadius: 8 },
  maxChipText: { fontSize: 12, fontWeight: '700', color: '#FFF' },

  // Swap arrow
  swapArrowContainer: { alignItems: 'center', zIndex: 10, marginVertical: -12 },
  swapArrowBtn: {
    width: 40, height: 40, borderRadius: 20, backgroundColor: GOLD,
    justifyContent: 'center', alignItems: 'center',
    shadowColor: GOLD, shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.3, shadowRadius: 4,
    elevation: 4,
  },

  // Info bar
  infoBar: {
    flexDirection: 'row', alignItems: 'center', gap: 6,
    backgroundColor: '#FFF8E1', borderWidth: 1, borderColor: '#FDE68A',
    borderRadius: 10, padding: 10, marginTop: 12,
  },
  infoBarText: { fontSize: 12, color: '#92400E', flex: 1 },

  // Buttons
  primaryBtn: {
    flexDirection: 'row', backgroundColor: GOLD, paddingVertical: 16,
    borderRadius: 14, alignItems: 'center', justifyContent: 'center', marginTop: 20,
    shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.25, shadowRadius: 8,
    elevation: 4,
  },
  primaryBtnText: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  secondaryBtn: { paddingVertical: 14, borderRadius: 14, alignItems: 'center', marginTop: 10 },
  secondaryBtnText: { fontSize: 15, color: '#888', fontWeight: '600' },
  disabledBtn: { opacity: 0.4 },

  // Token badge
  tokenBadge: { justifyContent: 'center', alignItems: 'center' },
  tokenBadgeText: { fontWeight: '700' },

  // Confirm screen
  confirmCard: {
    backgroundColor: '#FFF', borderRadius: 16, padding: 16,
    borderWidth: 1, borderColor: '#F0F0F0', marginBottom: 16,
    shadowColor: '#000', shadowOffset: { width: 0, height: 2 }, shadowOpacity: 0.04, shadowRadius: 8,
    elevation: 2,
  },
  confirmTokenRow: { flexDirection: 'row', alignItems: 'center', paddingVertical: 12 },
  confirmTokenLabel: { fontSize: 12, color: '#888', textTransform: 'uppercase', letterSpacing: 0.5 },
  confirmTokenAmount: { fontSize: 22, fontWeight: '700', color: '#1A1A1A', marginTop: 2 },
  confirmArrow: { alignItems: 'center', paddingVertical: 4 },
  confirmDivider: { height: 1, backgroundColor: '#F0F0F0', marginVertical: 12 },
  confirmDetailRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 6 },
  confirmDetailLabel: { fontSize: 13, color: '#888' },
  confirmDetailValue: { fontSize: 13, fontWeight: '600', color: '#1A1A1A' },
  disclaimer: { fontSize: 12, color: '#AAA', textAlign: 'center', marginBottom: 8 },

  // Signing screen
  signingIconWrap: { width: 80, height: 80, borderRadius: 40, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  signingTitle: { fontSize: 22, fontWeight: '700', color: '#1A1A1A', marginBottom: 8 },
  signingMessage: { fontSize: 15, color: GOLD, fontWeight: '600', marginBottom: 20 },
  signingSteps: { gap: 8 },
  signingStep: { fontSize: 13, color: '#888' },

  // Result screen
  resultIconWrap: { width: 80, height: 80, borderRadius: 40, justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  resultTitle: { fontSize: 22, fontWeight: '700', color: '#1A1A1A', marginBottom: 8 },
  resultMessage: { fontSize: 15, color: '#666', textAlign: 'center', marginBottom: 20, paddingHorizontal: 24 },
  txHash: { fontSize: 12, color: '#AAA', marginBottom: 24, maxWidth: '80%' },
});
