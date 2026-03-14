import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  TextInput,
  ScrollView,
  ActivityIndicator,
  Alert,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { router } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import { COLORS, SIZES, FONTS, SHADOWS } from '../../constants';
import { isValidAddress, signTransaction } from '../../services/wallet';
import {
  getWalletBalances,
  prepareSendTransaction,
  submitTransaction,
  WalletBalance,
} from '../../services/walletApi';

const TOKENS = ['MCGP', 'USDT', 'USDC'];

type ScreenState = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

const SendToken = () => {
  const [selectedToken, setSelectedToken] = useState('MCGP');
  const [toAddress, setToAddress] = useState('');
  const [amount, setAmount] = useState('');
  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [screenState, setScreenState] = useState<ScreenState>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [showTokenPicker, setShowTokenPicker] = useState(false);

  useEffect(() => {
    loadBalances();
  }, []);

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

  const handlePaste = async () => {
    try {
      const text = await Clipboard.getStringAsync();
      if (text) setToAddress(text.trim());
    } catch (err) {
      console.warn('Paste error:', err);
    }
  };

  const handleMax = () => {
    setAmount(getSelectedBalance());
  };

  const validateForm = (): string | null => {
    if (!toAddress.trim()) return 'Please enter a recipient address.';
    if (!isValidAddress(toAddress.trim())) return 'Invalid wallet address.';
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

    // Get gas estimate from backend
    try {
      setLoading(true);
      const prepResult = await prepareSendTransaction(selectedToken, toAddress.trim(), amount);
      if (prepResult.success && prepResult.data) {
        const gasPrice = BigInt(prepResult.data.gasPrice || '0');
        const gasLimit = BigInt(prepResult.data.gasLimit || '21000');
        const gasCostWei = gasPrice * gasLimit;
        // Convert from wei to a readable value (rough estimate)
        const gasCostEth = Number(gasCostWei) / 1e18;
        setGasEstimate(gasCostEth.toFixed(6));
      }
      setScreenState('confirm');
    } catch (err) {
      setError('Failed to estimate gas. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleSend = async () => {
    setScreenState('sending');
    setError('');

    try {
      // Prepare transaction from backend
      const prepResult = await prepareSendTransaction(
        selectedToken,
        toAddress.trim(),
        amount
      );

      if (!prepResult.success || !prepResult.data) {
        throw new Error(prepResult.message || 'Failed to prepare transaction');
      }

      const unsignedTx = prepResult.data;

      // Sign transaction (triggers biometric)
      const signedTx = await signTransaction({
        to: unsignedTx.to,
        data: unsignedTx.data,
        value: unsignedTx.value,
        gasLimit: unsignedTx.gasLimit,
        gasPrice: unsignedTx.gasPrice,
        nonce: unsignedTx.nonce,
        chainId: unsignedTx.chainId,
      });

      // Submit signed transaction
      const submitResult = await submitTransaction(
        signedTx,
        'send',
        selectedToken,
        toAddress.trim(),
        amount
      );

      if (submitResult.success && submitResult.data) {
        setTxHash(submitResult.data.txHash);
        setScreenState('success');
      } else {
        throw new Error(submitResult.message || 'Transaction submission failed');
      }
    } catch (err: any) {
      console.error('Send transaction error:', err);
      setError(err.message || 'Transaction failed. Please try again.');
      setScreenState('failure');
    }
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
            <Text
              style={[
                styles.resultIconText,
                { color: isSuccess ? COLORS.success : COLORS.danger },
              ]}
            >
              {isSuccess ? '!' : 'X'}
            </Text>
          </View>
          <Text style={styles.resultTitle}>
            {isSuccess ? 'Transaction Sent' : 'Transaction Failed'}
          </Text>
          <Text style={styles.resultMessage}>
            {isSuccess
              ? `${amount} ${selectedToken} sent successfully.`
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
            Please wait while your transaction is being processed.
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
            <Text style={styles.title}>Confirm Transaction</Text>
          </View>

          <View style={styles.confirmCard}>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>Token</Text>
              <Text style={styles.confirmValue}>{selectedToken}</Text>
            </View>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>Amount</Text>
              <Text style={styles.confirmValue}>{amount}</Text>
            </View>
            <View style={styles.confirmRow}>
              <Text style={styles.confirmLabel}>To</Text>
              <Text
                style={[styles.confirmValue, { fontSize: 12 }]}
                numberOfLines={1}
                ellipsizeMode="middle"
              >
                {toAddress}
              </Text>
            </View>
            <View style={[styles.confirmRow, { borderBottomWidth: 0 }]}>
              <Text style={styles.confirmLabel}>Est. Gas Fee</Text>
              <Text style={styles.confirmValue}>{gasEstimate} S</Text>
            </View>
          </View>

          <TouchableOpacity style={styles.primaryButton} onPress={handleSend}>
            <Text style={styles.primaryButtonText}>Confirm & Send</Text>
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
          <Text style={styles.title}>Send</Text>
        </View>

        {/* Token selector */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Token</Text>
          <TouchableOpacity
            style={styles.tokenSelector}
            onPress={() => setShowTokenPicker(!showTokenPicker)}
          >
            <Text style={styles.tokenSelectorText}>{selectedToken}</Text>
            <Text style={styles.tokenSelectorArrow}>
              {showTokenPicker ? 'v' : '>'}
            </Text>
          </TouchableOpacity>
          {showTokenPicker && (
            <View style={styles.tokenPickerDropdown}>
              {TOKENS.map((token) => (
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

        {/* Recipient address */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Recipient Address</Text>
          <View style={styles.inputRow}>
            <TextInput
              style={[styles.input, { flex: 1 }]}
              placeholder="0x..."
              placeholderTextColor={COLORS.gray}
              value={toAddress}
              onChangeText={(text) => {
                setToAddress(text);
                setError('');
              }}
              autoCapitalize="none"
              autoCorrect={false}
            />
            <TouchableOpacity style={styles.pasteButton} onPress={handlePaste}>
              <Text style={styles.pasteText}>Paste</Text>
            </TouchableOpacity>
          </View>
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
          style={[styles.primaryButton, loading && styles.disabledButton]}
          onPress={handleReview}
          disabled={loading}
        >
          {loading ? (
            <ActivityIndicator color={COLORS.white} />
          ) : (
            <Text style={styles.primaryButtonText}>Review</Text>
          )}
        </TouchableOpacity>
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

export default SendToken;

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
  fieldGroup: {
    marginBottom: 20,
  },
  label: {
    ...FONTS.body3,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 8,
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
  tokenSelectorArrow: {
    ...FONTS.body3,
    color: COLORS.gray,
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
  pasteButton: {
    backgroundColor: COLORS.lightGray,
    paddingVertical: 16,
    paddingHorizontal: 16,
    borderRadius: 12,
  },
  pasteText: {
    ...FONTS.body4,
    color: COLORS.primary,
    fontWeight: '600',
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
  disabledButton: {
    opacity: 0.6,
  },
  errorText: {
    ...FONTS.body4,
    color: COLORS.danger,
    marginBottom: 8,
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
