import React, { useState, useEffect, useCallback } from 'react';
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
import { Ionicons } from '@expo/vector-icons';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';
import { CHAINS, type ChainKey } from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { signTransaction } from '../../services/wallet';
import {
  getWalletBalances,
  prepareSendTransaction,
  submitTransaction,
  resolveUsername,
  WalletBalance,
  ResolvedUser,
} from '../../services/walletApi';
import { useKycVerification } from '../../hooks/useKycVerification';

type ScreenState = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

const InstantPay = () => {
  const { tokens, tokenList, getChainsForToken } = useTokens();
  const { requireKycVerified } = useKycVerification();

  const [username, setUsername] = useState('');
  const [amount, setAmount] = useState('');
  const [selectedToken, setSelectedToken] = useState('USDT');
  const [selectedChain, setSelectedChain] = useState<ChainKey>('sonic');
  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [resolvedUser, setResolvedUser] = useState<ResolvedUser | null>(null);
  const [resolving, setResolving] = useState(false);
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [screenState, setScreenState] = useState<ScreenState>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [showTokenPicker, setShowTokenPicker] = useState(false);

  useEffect(() => {
    loadBalances();
  }, []);

  // Auto-select chain when token changes
  useEffect(() => {
    const tokenChains = tokens[selectedToken]?.chains ?? [];
    if (!tokenChains.includes(selectedChain)) {
      setSelectedChain(tokenChains[0]);
    }
  }, [selectedToken]);

  const loadBalances = async () => {
    try {
      const result = await getWalletBalances();
      if (result.success && result.data) {
        setBalances(result.data);
      }
    } catch (err) {
      console.error('Load balances error:', err);
    } finally {
      setLoading(false);
    }
  };

  const getSelectedBalance = (): string => {
    const token = balances.find((b) => b.symbol === selectedToken);
    return token?.balance || '0';
  };

  // Debounced username lookup
  useEffect(() => {
    if (username.trim().length < 3) {
      setResolvedUser(null);
      return;
    }

    const timeout = setTimeout(async () => {
      setResolving(true);
      setError('');
      try {
        const result = await resolveUsername(username.trim());
        if (result.success && result.data) {
          setResolvedUser(result.data);
        } else {
          setResolvedUser(null);
          setError(result.message || 'User not found');
        }
      } catch {
        setResolvedUser(null);
        setError('Failed to look up user');
      } finally {
        setResolving(false);
      }
    }, 600);

    return () => clearTimeout(timeout);
  }, [username]);

  const activeChain = CHAINS[selectedChain];

  const validateForm = (): string | null => {
    if (!username.trim()) return 'Please enter a username.';
    if (!resolvedUser) return 'User not found. Check the username.';
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) {
      return 'Please enter a valid amount.';
    }
    if (Number(amount) > Number(getSelectedBalance())) {
      return 'Insufficient balance.';
    }
    return null;
  };

  const handleReview = async () => {
    const validationError = validateForm();
    if (validationError) {
      setError(validationError);
      return;
    }
    setError('');

    try {
      setLoading(true);
      const prepResult = await prepareSendTransaction(
        selectedToken,
        resolvedUser!.walletAddress,
        amount,
        activeChain.chainId
      );
      if (prepResult.success && prepResult.data) {
        const gasPrice = BigInt(prepResult.data.gasPrice || '0');
        const gasLimit = BigInt(prepResult.data.gasLimit || '21000');
        const gasCostWei = gasPrice * gasLimit;
        const gasCostEth = Number(gasCostWei) / 1e18;
        setGasEstimate(gasCostEth.toFixed(6));
      }
      setScreenState('confirm');
    } catch {
      setError('Failed to estimate gas. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleSend = async () => {
    if (!requireKycVerified()) return;
    if (!resolvedUser) return;

    setScreenState('sending');
    setError('');

    try {
      const prepResult = await prepareSendTransaction(
        selectedToken,
        resolvedUser.walletAddress,
        amount,
        activeChain.chainId
      );

      if (!prepResult.success || !prepResult.data) {
        throw new Error(prepResult.message || 'Failed to prepare transaction');
      }

      const unsignedTx = prepResult.data;

      const signedTx = await signTransaction({
        to: unsignedTx.to,
        data: unsignedTx.data,
        value: unsignedTx.value,
        gasLimit: unsignedTx.gasLimit,
        gasPrice: unsignedTx.gasPrice,
        nonce: unsignedTx.nonce,
        chainId: unsignedTx.chainId,
      });

      const submitResult = await submitTransaction(
        signedTx,
        'instant_pay',
        selectedToken,
        resolvedUser.walletAddress,
        amount,
        activeChain.chainId
      );

      if (submitResult.success && submitResult.data) {
        setTxHash(submitResult.data.txHash);
        setScreenState('success');
      } else {
        throw new Error(submitResult.message || 'Transaction submission failed');
      }
    } catch (err: any) {
      console.error('Instant pay error:', err);
      setError(err.message || 'Transaction failed. Please try again.');
      setScreenState('failure');
    }
  };

  const handleMax = () => {
    setAmount(getSelectedBalance());
  };

  // Result screen (success or failure)
  if (screenState === 'success' || screenState === 'failure') {
    const isSuccess = screenState === 'success';
    return (
      <View style={styles.container}>
        <View style={styles.resultContainer}>
          <View
            style={[
              styles.resultIcon,
              { backgroundColor: isSuccess ? '#D1FAE5' : '#FEE2E2' },
            ]}
          >
            <Ionicons
              name={isSuccess ? 'checkmark-circle' : 'close-circle'}
              size={40}
              color={isSuccess ? '#16A34A' : '#DC2626'}
            />
          </View>
          <Text style={styles.resultTitle}>
            {isSuccess ? 'Payment Sent!' : 'Payment Failed'}
          </Text>
          <Text style={styles.resultMessage}>
            {isSuccess
              ? `${amount} ${selectedToken} sent to @${resolvedUser?.username}`
              : error || 'Something went wrong.'}
          </Text>
          {txHash ? (
            <Text style={styles.txHashText} numberOfLines={1} ellipsizeMode="middle">
              Tx: {txHash}
            </Text>
          ) : null}
          <TouchableOpacity
            style={styles.primaryButton}
            onPress={() => router.replace('/wallet/home')}
          >
            <Text style={styles.primaryButtonText}>Back to Wallet</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  // Sending screen
  if (screenState === 'sending') {
    return (
      <View style={styles.container}>
        <View style={styles.resultContainer}>
          <ActivityIndicator size="large" color={COLORS.primary} />
          <Text style={[styles.resultTitle, { marginTop: 16 }]}>Sending...</Text>
          <Text style={styles.resultMessage}>
            Sending {amount} {selectedToken} to @{resolvedUser?.username}
          </Text>
        </View>
      </View>
    );
  }

  // Confirmation screen
  if (screenState === 'confirm') {
    return (
      <View style={styles.container}>
        <ScrollView contentContainerStyle={styles.scrollContent}>
          <View style={styles.header}>
            <Text style={styles.title}>Confirm Payment</Text>
          </View>

          <View style={styles.recipientCard}>
            <Ionicons name="person-circle" size={48} color={COLORS.primary} />
            <View style={{ marginLeft: 12 }}>
              <Text style={styles.recipientName}>{resolvedUser?.name}</Text>
              <Text style={styles.recipientUsername}>@{resolvedUser?.username}</Text>
            </View>
            {resolvedUser?.verificationStatus === 'verified' && (
              <Ionicons name="checkmark-circle" size={20} color="#16A34A" style={{ marginLeft: 'auto' }} />
            )}
          </View>

          <View style={styles.confirmCard}>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>Token</Text>
              <Text style={styles.confirmValue}>{selectedToken}</Text>
            </View>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>Network</Text>
              <Text style={styles.confirmValue}>{activeChain.name}</Text>
            </View>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>Amount</Text>
              <Text style={styles.confirmValue}>{amount} {selectedToken}</Text>
            </View>
            <View style={[styles.confirmRow, { borderBottomWidth: 0 }]}>
              <Text style={styles.confirmLabel}>Est. Gas Fee</Text>
              <Text style={styles.confirmValue}>
                {gasEstimate} {activeChain.nativeCurrency.symbol}
              </Text>
            </View>
          </View>

          <TouchableOpacity style={styles.primaryButton} onPress={handleSend}>
            <Text style={styles.primaryButtonText}>Confirm & Pay</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={styles.secondaryButton}
            onPress={() => setScreenState('form')}
          >
            <Text style={styles.secondaryButtonText}>Go Back</Text>
          </TouchableOpacity>
        </ScrollView>
      </View>
    );
  }

  // Main form
  return (
    <KeyboardAvoidingView
      style={styles.container}
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
    >
      <ScrollView contentContainerStyle={styles.scrollContent}>
        <View style={styles.header}>
          <Text style={styles.title}>Instant Pay</Text>
          <Text style={styles.subtitle}>Send to any TSA Connect user by username</Text>
        </View>

        {/* Username input */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Recipient Username</Text>
          <View style={styles.inputRow}>
            <View style={styles.atSymbol}>
              <Text style={styles.atSymbolText}>@</Text>
            </View>
            <TextInput
              style={[styles.input, { flex: 1, borderTopLeftRadius: 0, borderBottomLeftRadius: 0 }]}
              placeholder="username"
              placeholderTextColor={COLORS.gray}
              value={username}
              onChangeText={(text) => {
                setUsername(text.replace(/\s/g, ''));
                setError('');
              }}
              autoCapitalize="none"
              autoCorrect={false}
            />
            {resolving && (
              <ActivityIndicator size="small" color={COLORS.primary} style={{ marginLeft: 8 }} />
            )}
          </View>

          {/* Resolved user badge */}
          {resolvedUser && (
            <View style={styles.resolvedBadge}>
              <Ionicons name="person-circle" size={24} color={COLORS.primary} />
              <View style={{ marginLeft: 8, flex: 1 }}>
                <Text style={styles.resolvedName}>{resolvedUser.name}</Text>
                <Text style={styles.resolvedAddress} numberOfLines={1} ellipsizeMode="middle">
                  {resolvedUser.walletAddress}
                </Text>
              </View>
              {resolvedUser.verificationStatus === 'verified' && (
                <Ionicons name="checkmark-circle" size={18} color="#16A34A" />
              )}
            </View>
          )}
        </View>

        {/* Token selector */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Token</Text>
          <TouchableOpacity
            style={styles.tokenSelector}
            onPress={() => setShowTokenPicker(!showTokenPicker)}
          >
            <Text style={styles.tokenSelectorText}>{selectedToken}</Text>
            <Ionicons name={showTokenPicker ? 'chevron-up' : 'chevron-down'} size={18} color={COLORS.gray} />
          </TouchableOpacity>
          {showTokenPicker && (
            <View style={styles.tokenPickerDropdown}>
              {tokenList.map(({ symbol: token }) => (
                <TouchableOpacity
                  key={token}
                  style={[
                    styles.tokenPickerItem,
                    token === selectedToken && styles.tokenPickerItemActive,
                  ]}
                  onPress={() => {
                    setSelectedToken(token);
                    setShowTokenPicker(false);
                  }}
                >
                  <Text
                    style={[
                      styles.tokenPickerItemText,
                      token === selectedToken && styles.tokenPickerItemTextActive,
                    ]}
                  >
                    {token}
                  </Text>
                </TouchableOpacity>
              ))}
            </View>
          )}
          <Text style={styles.balanceHint}>
            Balance: {getSelectedBalance()} {selectedToken}
          </Text>
        </View>

        {/* Amount */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Amount</Text>
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
            <TouchableOpacity style={styles.maxButton} onPress={handleMax}>
              <Text style={styles.maxText}>MAX</Text>
            </TouchableOpacity>
          </View>
        </View>

        {error ? <Text style={styles.errorText}>{error}</Text> : null}

        <TouchableOpacity
          style={[styles.primaryButton, (loading || resolving) && styles.disabledButton]}
          onPress={handleReview}
          disabled={loading || resolving}
        >
          {loading ? (
            <ActivityIndicator color={COLORS.white} />
          ) : (
            <Text style={styles.primaryButtonText}>Review Payment</Text>
          )}
        </TouchableOpacity>
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

export default InstantPay;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
  scrollContent: {
    flexGrow: 1,
    padding: SIZES.padding3,
  },
  header: {
    marginBottom: 24,
  },
  title: {
    ...FONTS.h1,
    color: COLORS.dark,
  },
  subtitle: {
    ...FONTS.body4,
    color: COLORS.gray,
    marginTop: 4,
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
    gap: 0,
  },
  atSymbol: {
    backgroundColor: COLORS.lightGray,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderTopLeftRadius: 12,
    borderBottomLeftRadius: 12,
    paddingHorizontal: 14,
    paddingVertical: 16,
    justifyContent: 'center',
  },
  atSymbolText: {
    ...FONTS.body3,
    fontWeight: '700',
    color: COLORS.primary,
  },
  input: {
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 12,
    padding: 16,
    ...FONTS.body3,
    color: COLORS.dark,
  },
  resolvedBadge: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F0FDF4',
    borderWidth: 1,
    borderColor: '#BBF7D0',
    borderRadius: 12,
    padding: 12,
    marginTop: 8,
  },
  resolvedName: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
  },
  resolvedAddress: {
    ...FONTS.body5,
    color: COLORS.gray,
    maxWidth: 200,
  },
  tokenSelector: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 12,
    padding: 16,
  },
  tokenSelectorText: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
  },
  tokenPickerDropdown: {
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 12,
    marginTop: 4,
    overflow: 'hidden',
  },
  tokenPickerItem: {
    padding: 14,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  tokenPickerItemActive: {
    backgroundColor: COLORS.lightGray,
  },
  tokenPickerItemText: {
    ...FONTS.body3,
    color: COLORS.dark,
  },
  tokenPickerItemTextActive: {
    color: COLORS.primary,
    fontWeight: '600',
  },
  balanceHint: {
    ...FONTS.body5,
    color: COLORS.gray,
    marginTop: 6,
  },
  maxButton: {
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    paddingHorizontal: 16,
    borderRadius: 12,
    marginLeft: 8,
  },
  maxText: {
    ...FONTS.body4,
    color: COLORS.white,
    fontWeight: '700',
  },
  primaryButton: {
    backgroundColor: COLORS.primary,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    marginTop: 16,
    ...SHADOWS.medium,
  },
  primaryButtonText: {
    ...FONTS.h4,
    color: COLORS.white,
    fontWeight: '600',
  },
  secondaryButton: {
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    marginTop: 8,
  },
  secondaryButtonText: {
    ...FONTS.body3,
    color: COLORS.gray,
    fontWeight: '600',
  },
  disabledButton: {
    opacity: 0.6,
  },
  errorText: {
    ...FONTS.body4,
    color: COLORS.danger,
    marginBottom: 8,
  },
  // Recipient card on confirm screen
  recipientCard: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FAFAFA',
    borderRadius: 12,
    padding: 16,
    marginBottom: 16,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
  },
  recipientName: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
  },
  recipientUsername: {
    ...FONTS.body4,
    color: COLORS.gray,
  },
  // Confirm screen
  confirmCard: {
    backgroundColor: COLORS.white,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    marginBottom: 24,
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
    maxWidth: '60%',
    textAlign: 'right',
  },
  // Result screen
  resultContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: SIZES.padding3,
  },
  resultIcon: {
    width: 72,
    height: 72,
    borderRadius: 36,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
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
