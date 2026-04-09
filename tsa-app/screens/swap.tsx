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
} from 'react-native';
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

// MCGP: 18 decimals, USDC: 6 decimals
const MCGP_DECIMALS = 18;
const USDC_DECIMALS = 6;

type Direction = 'buy' | 'sell';
type ScreenState = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

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
  const [approveTxHash, setApproveTxHash] = useState('');
  const [swapTxHash, setSwapTxHash] = useState('');
  const [statusMessage, setStatusMessage] = useState('');

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Load balances on mount
  useEffect(() => {
    (async () => {
      try {
        const result = await getWalletBalances();
        if (result.success && result.data) setBalances(result.data);
      } catch (e) {
        console.warn('Failed to load balances', e);
      } finally {
        setLoadingBalances(false);
      }
    })();
  }, []);

  // Debounced price fetch when amount or direction changes
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
          // For buy: user enters MCGP amount they want
          // For sell: user enters MCGP amount they want to sell
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
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [amount, direction, fetchPrice]);

  const getMcgpBalance = (): string => {
    return balances.find((b) => b.symbol === 'MCGP')?.balance || '0';
  };

  const getUsdcBalance = (): string => {
    return balances.find((b) => b.symbol === 'USDC')?.balance || '0';
  };

  const toggleDirection = () => {
    setDirection((d) => (d === 'buy' ? 'sell' : 'buy'));
    setAmount('');
    setQuoteUsdcAmount(null);
    setQuoteMcgpAmount(null);
    setPriceError('');
    setError('');
  };

  const validate = (): string | null => {
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) {
      return 'Please enter a valid MCGP amount.';
    }
    const num = Number(amount);
    if (direction === 'sell') {
      if (num > Number(getMcgpBalance())) return 'Insufficient MCGP balance.';
    } else {
      if (quoteUsdcAmount) {
        const usdcNeeded = Number(fromWei(quoteUsdcAmount, USDC_DECIMALS));
        if (usdcNeeded > Number(getUsdcBalance())) return 'Insufficient USDC balance.';
      }
    }
    return null;
  };

  const handleReview = () => {
    const err = validate();
    if (err) { setError(err); return; }
    if (!quoteUsdcAmount) { setError('Please wait for price quote.'); return; }
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
      if (!prepResult.success || !prepResult.data) {
        throw new Error(prepResult.message || 'Failed to prepare swap');
      }

      const { approveTx, swapTx, usdcAmount, mcgpAmount } = prepResult.data;
      const chainId = approveTx.chainId;

      // Step 1: Sign & broadcast approve tx
      setStatusMessage('Signing approval...');
      const signedApproveTx = await signTransaction({
        to: approveTx.to,
        data: approveTx.data,
        value: approveTx.value,
        gasLimit: approveTx.gasLimit,
        gasPrice: approveTx.gasPrice,
        nonce: approveTx.nonce,
        chainId: approveTx.chainId,
      });

      setStatusMessage('Submitting approval...');
      const approveResult = await submitTransaction(
        signedApproveTx,
        'approve',
        direction === 'buy' ? 'USDC' : 'MCGP',
        approveTx.to,
        fromWei(direction === 'buy' ? usdcAmount : mcgpAmount, direction === 'buy' ? USDC_DECIMALS : MCGP_DECIMALS),
        chainId
      );
      if (approveResult.success && approveResult.data) {
        setApproveTxHash(approveResult.data.txHash);
      }

      // Step 2: Sign & broadcast swap tx
      setStatusMessage('Signing swap...');
      const signedSwapTx = await signTransaction({
        to: swapTx.to,
        data: swapTx.data,
        value: swapTx.value,
        gasLimit: swapTx.gasLimit,
        gasPrice: swapTx.gasPrice,
        nonce: swapTx.nonce,
        chainId: swapTx.chainId,
      });

      setStatusMessage('Submitting swap...');
      const swapResult = await submitTransaction(
        signedSwapTx,
        'swap',
        'MCGP',
        swapTx.to,
        fromWei(mcgpAmount, MCGP_DECIMALS),
        chainId
      );

      if (swapResult.success && swapResult.data) {
        setSwapTxHash(swapResult.data.txHash);
        setScreenState('success');
      } else {
        throw new Error(swapResult.message || 'Swap submission failed');
      }
    } catch (e: any) {
      console.error('Swap error:', e);
      setError(e.message || 'Swap failed. Please try again.');
      setScreenState('failure');
    }
  };

  // --- Signing/Loading screen ---
  if (screenState === 'signing') {
    return (
      <View style={styles.container}>
        <View style={styles.centeredContainer}>
          <ActivityIndicator size="large" color={COLORS.primary} />
          <Text style={[styles.resultTitle, { marginTop: 16 }]}>Processing Swap</Text>
          <Text style={styles.resultMessage}>{statusMessage}</Text>
        </View>
      </View>
    );
  }

  // --- Success / Failure screen ---
  if (screenState === 'success' || screenState === 'failure') {
    const isSuccess = screenState === 'success';
    return (
      <View style={styles.container}>
        <View style={styles.centeredContainer}>
          <View
            style={[
              styles.resultIcon,
              { backgroundColor: isSuccess ? '#D1FAE5' : '#FEE2E2' },
            ]}
          >
            <Text
              style={[
                styles.resultIconText,
                { color: isSuccess ? COLORS.success : COLORS.danger },
              ]}
            >
              {isSuccess ? '✓' : '✕'}
            </Text>
          </View>
          <Text style={styles.resultTitle}>
            {isSuccess ? 'Swap Complete' : 'Swap Failed'}
          </Text>
          <Text style={styles.resultMessage}>
            {isSuccess
              ? `Swapped ${amount} MCGP ${direction === 'buy' ? 'purchased' : 'sold'} successfully.`
              : error || 'Something went wrong.'}
          </Text>
          {swapTxHash ? (
            <Text style={styles.txHashText} numberOfLines={1} ellipsizeMode="middle">
              Tx: {swapTxHash}
            </Text>
          ) : null}
          <TouchableOpacity
            style={styles.primaryButton}
            onPress={() => {
              if (isSuccess) {
                router.replace('/tokenization');
              } else {
                setScreenState('form');
              }
            }}
          >
            <Text style={styles.primaryButtonText}>
              {isSuccess ? 'Back to Wallet' : 'Try Again'}
            </Text>
          </TouchableOpacity>
          {isSuccess && (
            <TouchableOpacity
              style={[styles.secondaryButton, { marginTop: 12 }]}
              onPress={() => setScreenState('form')}
            >
              <Text style={styles.secondaryButtonText}>Swap Again</Text>
            </TouchableOpacity>
          )}
        </View>
      </View>
    );
  }

  // --- Confirm screen ---
  if (screenState === 'confirm') {
    const mcgpDisplay = amount;
    const usdcDisplay = quoteUsdcAmount
      ? fromWei(quoteUsdcAmount, USDC_DECIMALS, 4)
      : '—';

    return (
      <View style={styles.container}>
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header}>
            <TouchableOpacity onPress={() => setScreenState('form')} style={styles.backButton}>
              <Text style={styles.backButtonText}>← Back</Text>
            </TouchableOpacity>
            <Text style={styles.title}>Confirm Swap</Text>
          </View>

          <View style={styles.swapSummaryCard}>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>Direction</Text>
              <Text style={[styles.confirmValue, { color: COLORS.primary }]}>
                {direction === 'buy' ? 'Buy MCGP' : 'Sell MCGP'}
              </Text>
            </View>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>
                {direction === 'buy' ? 'You Pay' : 'You Sell'}
              </Text>
              <Text style={styles.confirmValue}>
                {direction === 'buy' ? `${usdcDisplay} USDC` : `${mcgpDisplay} MCGP`}
              </Text>
            </View>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>
                {direction === 'buy' ? 'You Receive' : 'You Receive'}
              </Text>
              <Text style={styles.confirmValue}>
                {direction === 'buy' ? `${mcgpDisplay} MCGP` : `${usdcDisplay} USDC`}
              </Text>
            </View>
            <View style={[styles.confirmRow, { borderBottomWidth: 0 }]}>
              <Text style={styles.confirmLabel}>Slippage</Text>
              <Text style={styles.confirmValue}>0.5%</Text>
            </View>
          </View>

          <Text style={styles.disclaimerText}>
            Two transactions will be signed: an approval and the swap. Please review carefully.
          </Text>

          <TouchableOpacity style={styles.primaryButton} onPress={handleSwap}>
            <Text style={styles.primaryButtonText}>Confirm Swap</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={[styles.secondaryButton, { marginTop: 12 }]}
            onPress={() => setScreenState('form')}
          >
            <Text style={styles.secondaryButtonText}>Cancel</Text>
          </TouchableOpacity>
        </ScrollView>
      </View>
    );
  }

  // --- Main form ---
  const usdcDisplay = quoteUsdcAmount ? fromWei(quoteUsdcAmount, USDC_DECIMALS, 4) : null;

  return (
    <KeyboardAvoidingView
      style={styles.container}
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
    >
      <ScrollView contentContainerStyle={styles.scrollContent}>
        <View style={styles.header}>
          <Text style={styles.title}>EasySwap</Text>
          <Text style={styles.subtitle}>OTC Marketplace</Text>
        </View>

        {/* Direction Toggle */}
        <View style={styles.directionContainer}>
          <TouchableOpacity
            style={[
              styles.directionButton,
              direction === 'buy' && styles.directionButtonActive,
            ]}
            onPress={() => direction !== 'buy' && toggleDirection()}
          >
            <Text
              style={[
                styles.directionButtonText,
                direction === 'buy' && styles.directionButtonTextActive,
              ]}
            >
              Buy MCGP
            </Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={[
              styles.directionButton,
              direction === 'sell' && styles.directionButtonActive,
            ]}
            onPress={() => direction !== 'sell' && toggleDirection()}
          >
            <Text
              style={[
                styles.directionButtonText,
                direction === 'sell' && styles.directionButtonTextActive,
              ]}
            >
              Sell MCGP
            </Text>
          </TouchableOpacity>
        </View>

        {/* Balances */}
        {!loadingBalances && (
          <View style={styles.balancesRow}>
            <Text style={styles.balanceChip}>MCGP: {getMcgpBalance()}</Text>
            <Text style={styles.balanceChip}>USDC: {getUsdcBalance()}</Text>
          </View>
        )}

        {/* Amount Input */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>MCGP Amount</Text>
          <View style={styles.inputRow}>
            <TextInput
              style={[styles.input, { flex: 1 }]}
              placeholder="0.00"
              placeholderTextColor={COLORS.gray}
              value={amount}
              onChangeText={(text) => {
                setAmount(text);
                setError('');
              }}
              keyboardType="decimal-pad"
            />
            {direction === 'sell' && (
              <TouchableOpacity
                style={styles.maxButton}
                onPress={() => setAmount(getMcgpBalance())}
              >
                <Text style={styles.maxText}>MAX</Text>
              </TouchableOpacity>
            )}
          </View>
        </View>

        {/* Price Quote */}
        <View style={styles.quoteCard}>
          {priceLoading ? (
            <ActivityIndicator size="small" color={COLORS.primary} />
          ) : priceError ? (
            <Text style={styles.priceErrorText}>{priceError}</Text>
          ) : usdcDisplay ? (
            <View>
              <View style={styles.quoteRow}>
                <Text style={styles.quoteLabel}>
                  {direction === 'buy' ? 'You Pay' : 'You Receive'}
                </Text>
                <Text style={styles.quoteValue}>{usdcDisplay} USDC</Text>
              </View>
              <View style={styles.quoteRow}>
                <Text style={styles.quoteLabel}>
                  {direction === 'buy' ? 'You Receive' : 'You Send'}
                </Text>
                <Text style={styles.quoteValue}>{amount} MCGP</Text>
              </View>
              <View style={[styles.quoteRow, { borderBottomWidth: 0 }]}>
                <Text style={styles.quoteLabel}>Rate</Text>
                <Text style={styles.quoteValue}>
                  1 MCGP ={' '}
                  {amount && Number(amount) > 0
                    ? (Number(usdcDisplay) / Number(amount)).toFixed(4)
                    : '—'}{' '}
                  USDC
                </Text>
              </View>
            </View>
          ) : (
            <Text style={styles.quotePlaceholder}>Enter an amount to see the price</Text>
          )}
        </View>

        {error ? <Text style={styles.errorText}>{error}</Text> : null}

        <TouchableOpacity
          style={[
            styles.primaryButton,
            (!amount || priceLoading || !!priceError) && styles.disabledButton,
          ]}
          onPress={handleReview}
          disabled={!amount || priceLoading || !!priceError}
        >
          <Text style={styles.primaryButtonText}>Review Swap</Text>
        </TouchableOpacity>
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

export default SwapScreen;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
  scrollContent: {
    flexGrow: 1,
    padding: SIZES.padding3,
  },
  centeredContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: SIZES.padding3,
  },
  header: {
    marginBottom: 24,
  },
  backButton: {
    marginBottom: 8,
  },
  backButtonText: {
    ...FONTS.body3,
    color: COLORS.primary,
    fontWeight: '600',
  },
  title: {
    ...FONTS.h1,
    color: COLORS.dark,
  },
  subtitle: {
    ...FONTS.body4,
    color: COLORS.gray,
    marginTop: 2,
  },
  directionContainer: {
    flexDirection: 'row',
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 12,
    overflow: 'hidden',
    marginBottom: 16,
  },
  directionButton: {
    flex: 1,
    paddingVertical: 14,
    alignItems: 'center',
    backgroundColor: COLORS.white,
  },
  directionButtonActive: {
    backgroundColor: COLORS.primary,
  },
  directionButtonText: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.gray,
  },
  directionButtonTextActive: {
    color: COLORS.white,
  },
  balancesRow: {
    flexDirection: 'row',
    gap: 8,
    marginBottom: 20,
  },
  balanceChip: {
    ...FONTS.body5,
    color: COLORS.gray,
    backgroundColor: COLORS.lightGray,
    paddingHorizontal: 10,
    paddingVertical: 4,
    borderRadius: 20,
  },
  fieldGroup: {
    marginBottom: 20,
  },
  label: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 8,
  },
  inputRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
  },
  input: {
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 12,
    padding: 16,
    ...FONTS.body3,
    color: COLORS.dark,
  },
  maxButton: {
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    paddingHorizontal: 16,
    borderRadius: 12,
  },
  maxText: {
    ...FONTS.body4,
    color: COLORS.white,
    fontWeight: '700',
  },
  quoteCard: {
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 12,
    padding: 16,
    marginBottom: 20,
    minHeight: 60,
    justifyContent: 'center',
  },
  quoteRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    paddingVertical: 8,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  quoteLabel: {
    ...FONTS.body4,
    color: COLORS.gray,
  },
  quoteValue: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
  },
  quotePlaceholder: {
    ...FONTS.body4,
    color: COLORS.gray,
    textAlign: 'center',
  },
  priceErrorText: {
    ...FONTS.body4,
    color: COLORS.danger,
    textAlign: 'center',
  },
  errorText: {
    ...FONTS.body4,
    color: COLORS.danger,
    marginBottom: 8,
  },
  primaryButton: {
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    marginTop: 4,
    ...SHADOWS.medium,
  },
  primaryButtonText: {
    ...FONTS.h4,
    color: COLORS.white,
    fontWeight: '600',
  },
  secondaryButton: {
    paddingVertical: 14,
    borderRadius: 12,
    alignItems: 'center',
    borderWidth: 1,
    borderColor: COLORS.lightGray,
  },
  secondaryButtonText: {
    ...FONTS.body3,
    color: COLORS.gray,
    fontWeight: '600',
  },
  disabledButton: {
    opacity: 0.5,
  },
  // Confirm screen
  swapSummaryCard: {
    backgroundColor: COLORS.white,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    marginBottom: 16,
    overflow: 'hidden',
  },
  confirmRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: 16,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  confirmLabel: {
    ...FONTS.body4,
    color: COLORS.gray,
  },
  confirmValue: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
  },
  disclaimerText: {
    ...FONTS.body5,
    color: COLORS.gray,
    marginBottom: 20,
    textAlign: 'center',
  },
  // Result screen
  resultIcon: {
    width: 72,
    height: 72,
    borderRadius: 36,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  resultIconText: {
    fontSize: 32,
    fontWeight: '700',
  },
  resultTitle: {
    ...FONTS.h2,
    color: COLORS.dark,
    fontWeight: '600',
    marginBottom: 8,
  },
  resultMessage: {
    ...FONTS.body3,
    color: COLORS.gray,
    textAlign: 'center',
    marginBottom: 16,
  },
  txHashText: {
    ...FONTS.body5,
    color: COLORS.gray,
    marginBottom: 24,
    maxWidth: '80%',
  },
});
