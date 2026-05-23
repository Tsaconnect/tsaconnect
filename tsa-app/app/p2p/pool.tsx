// app/p2p/pool.tsx
//
// Seller's P2P pool management — deposit / withdraw a single token's
// available + locked balance.
//
// Flow:
//   1. GET /p2p/balance — current (available, locked) for the active wallet.
//   2. Pick deposit or withdraw, enter amount, confirm.
//   3. Backend builds an unsigned tx; we sign locally + broadcast on Sonic.
//   4. POST /p2p/{deposits|withdrawals}/confirm with the txHash → audit row.
//
// ERC20 allowance: deposit calls `transferFrom` inside the escrow contract,
// so the seller must approve the escrow for `amount` first. This screen
// only handles the deposit / withdraw step; the approve flow lives in the
// wallet manager (or the seller approves manually via their wallet UI).

import { Stack, router } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { SafeAreaView } from 'react-native-safe-area-context';
import type { ChainKey } from '../../constants/chains';
import {
  formatTokenAmount,
  parseTokenAmount,
  poolApi,
  type UnsignedTx,
} from '../../services/p2p';
import {
  broadcastTransaction,
  getActiveWallet,
  signTransaction,
} from '../../services/wallet';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  background: '#FAF9F6',
  error: '#DC3545',
  success: '#2E8B57',
};

const TOKEN_DECIMALS: Record<string, number> = { USDT: 6, USDC: 6, MCGP: 18 };
const TOKENS = ['USDT', 'USDC', 'MCGP'] as const;
type Token = typeof TOKENS[number];

type Mode = 'deposit' | 'withdraw';

const P2PPoolScreen: React.FC = () => {
  const [mode, setMode] = useState<Mode>('deposit');
  const [token, setToken] = useState<Token>('USDT');
  const [amountInput, setAmountInput] = useState('');
  const [available, setAvailable] = useState<string>('0');
  const [locked, setLocked] = useState<string>('0');
  const [loadingBal, setLoadingBal] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [stage, setStage] = useState('');

  const decimals = TOKEN_DECIMALS[token] ?? 18;

  const loadBalance = useCallback(async () => {
    setLoadingBal(true);
    const res = await poolApi.balance(token);
    if (res.success && res.data) {
      setAvailable(res.data.available);
      setLocked(res.data.locked);
    } else {
      setAvailable('0');
      setLocked('0');
    }
    setLoadingBal(false);
  }, [token]);

  useEffect(() => {
    loadBalance();
  }, [loadBalance]);

  const amountValid = useMemo(() => {
    const v = parseFloat(amountInput.trim());
    return isFinite(v) && v > 0;
  }, [amountInput]);

  const handleSubmit = async () => {
    if (!amountValid || submitting) return;
    setSubmitting(true);
    let broadcastHash: string | null = null;
    try {
      const wallet = await getActiveWallet();
      if (!wallet) throw new Error('No wallet found');
      const amountBaseUnits = parseTokenAmount(amountInput.trim(), decimals);

      setStage('Preparing transaction…');
      const prep =
        mode === 'deposit'
          ? await poolApi.prepareDeposit(token, amountBaseUnits)
          : await poolApi.prepareWithdraw(token, amountBaseUnits);
      if (!prep.success || !prep.data) {
        throw new Error(prep.message || 'Prepare failed');
      }

      setStage('Signing…');
      const signed = await signTransaction(toTxLike(prep.data.unsignedTx));

      setStage('Broadcasting…');
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;

      setStage('Confirming…');
      const conf =
        mode === 'deposit'
          ? await poolApi.confirmDeposit(token, amountBaseUnits, resp.hash)
          : await poolApi.confirmWithdraw(token, amountBaseUnits, resp.hash);
      if (!conf.success) throw new Error(conf.message || 'Confirm failed');

      Alert.alert(
        mode === 'deposit' ? 'Deposit confirmed' : 'Withdrawal confirmed',
        `${amountInput} ${token} ${mode === 'deposit' ? 'added to' : 'removed from'} your pool.`,
      );
      setAmountInput('');
      loadBalance();
    } catch (err: any) {
      const detail = err?.message || String(err);
      if (broadcastHash) {
        Alert.alert(
          'Submitted, server confirm failed',
          `Tx ${broadcastHash} was broadcast on chain but the server couldn't record it ` +
            `(${detail}). Wait a minute and refresh your balance; contact support if it stays off.`,
        );
      } else {
        Alert.alert(mode === 'deposit' ? 'Deposit failed' : 'Withdrawal failed', detail);
      }
    } finally {
      setSubmitting(false);
      setStage('');
    }
  };

  return (
    <SafeAreaView style={styles.container}>
      <Stack.Screen options={{ title: 'P2P Pool' }} />
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView contentContainerStyle={styles.scroll} keyboardShouldPersistTaps="handled">
          <Text style={styles.heading}>P2P Pool</Text>
          <Text style={styles.subheading}>
            Funds you deposit here back your published ads. Available balance is
            withdrawable any time; locked balance is reserved against open trades.
          </Text>

          <View style={styles.tokenRow}>
            {TOKENS.map((t) => (
              <TouchableOpacity
                key={t}
                style={[styles.tokenChip, token === t && styles.tokenChipActive]}
                onPress={() => setToken(t)}
                activeOpacity={0.85}
              >
                <Text style={[styles.tokenChipText, token === t && styles.tokenChipTextActive]}>
                  {t}
                </Text>
              </TouchableOpacity>
            ))}
          </View>

          <View style={styles.balanceCard}>
            <View style={styles.balanceRow}>
              <Text style={styles.balanceLabel}>Available</Text>
              {loadingBal ? (
                <ActivityIndicator size="small" color={GOLD.dark} />
              ) : (
                <Text style={styles.balanceValue}>
                  {formatTokenAmount(available, decimals, 4)} {token}
                </Text>
              )}
            </View>
            <View style={styles.balanceRow}>
              <Text style={styles.balanceLabel}>Locked</Text>
              {loadingBal ? null : (
                <Text style={styles.balanceLocked}>
                  {formatTokenAmount(locked, decimals, 4)} {token}
                </Text>
              )}
            </View>
          </View>

          <View style={styles.modeRow}>
            {(['deposit', 'withdraw'] as const).map((m) => (
              <TouchableOpacity
                key={m}
                style={[styles.modeChip, mode === m && styles.modeChipActive]}
                onPress={() => setMode(m)}
                activeOpacity={0.85}
              >
                <Icon
                  name={m === 'deposit' ? 'arrow-downward' : 'arrow-upward'}
                  size={16}
                  color={mode === m ? '#000' : GOLD.dark}
                />
                <Text
                  style={[styles.modeChipText, mode === m && styles.modeChipTextActive]}
                >
                  {m === 'deposit' ? 'Deposit' : 'Withdraw'}
                </Text>
              </TouchableOpacity>
            ))}
          </View>

          <Text style={styles.label}>Amount ({token})</Text>
          <View style={styles.input}>
            <TextInput
              style={styles.inputField}
              keyboardType="decimal-pad"
              placeholder="0.00"
              placeholderTextColor="#999"
              value={amountInput}
              onChangeText={(v) => setAmountInput(v.replace(/[^0-9.]/g, ''))}
            />
            <Text style={styles.inputUnit}>{token}</Text>
          </View>
          {mode === 'deposit' ? (
            <Text style={styles.hint}>
              Make sure you have approved the P2P escrow contract to spend {token}
              from your wallet before depositing.
            </Text>
          ) : (
            <TouchableOpacity
              onPress={() => setAmountInput(formatTokenAmount(available, decimals, 6))}
              hitSlop={8}
            >
              <Text style={styles.maxLink}>Use full available balance</Text>
            </TouchableOpacity>
          )}

          <TouchableOpacity
            style={[styles.submit, (!amountValid || submitting) && styles.submitDisabled]}
            onPress={handleSubmit}
            disabled={!amountValid || submitting}
            activeOpacity={0.85}
          >
            <Text style={styles.submitText}>
              {submitting ? stage || 'Working…' : mode === 'deposit' ? 'Deposit' : 'Withdraw'}
            </Text>
          </TouchableOpacity>

          <TouchableOpacity
            style={styles.adsLink}
            onPress={() => router.push('/p2p/my-ads')}
            activeOpacity={0.7}
          >
            <Icon name="article" size={16} color={GOLD.dark} />
            <Text style={styles.adsLinkText}>Go to my ads</Text>
          </TouchableOpacity>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

function toTxLike(tx: UnsignedTx) {
  const ensure0x = (v?: string) => (!v ? undefined : v.startsWith('0x') ? v : `0x${v}`);
  return {
    to: tx.to,
    value: tx.value || '0',
    data: ensure0x(tx.data),
    nonce: tx.nonce,
    gasLimit: tx.gasLimit,
    gasPrice: tx.gasPrice,
    chainId: tx.chainId ? Number(tx.chainId) : undefined,
  };
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 24, fontWeight: '800', color: '#000', marginBottom: 6 },
  subheading: { fontSize: 13, color: '#666', marginBottom: 18, lineHeight: 19 },
  tokenRow: { flexDirection: 'row', gap: 8, marginBottom: 16 },
  tokenChip: {
    flex: 1,
    paddingVertical: 10,
    borderRadius: 10,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
    alignItems: 'center',
  },
  tokenChipActive: {
    backgroundColor: GOLD.primary,
    borderColor: GOLD.dark,
  },
  tokenChipText: { fontWeight: '700', color: GOLD.dark },
  tokenChipTextActive: { color: '#000' },
  balanceCard: {
    backgroundColor: '#FFF',
    borderRadius: 12,
    padding: 16,
    borderWidth: 1,
    borderColor: GOLD.muted,
    marginBottom: 18,
  },
  balanceRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingVertical: 4,
  },
  balanceLabel: { fontSize: 14, color: '#666', fontWeight: '600' },
  balanceValue: { fontSize: 18, color: '#000', fontWeight: '800' },
  balanceLocked: { fontSize: 14, color: '#999', fontWeight: '600' },
  modeRow: { flexDirection: 'row', gap: 10, marginBottom: 18 },
  modeChip: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 12,
    borderRadius: 10,
    backgroundColor: '#FFF',
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  modeChipActive: {
    backgroundColor: GOLD.primary,
    borderColor: GOLD.dark,
  },
  modeChipText: { marginLeft: 6, fontWeight: '700', color: GOLD.dark },
  modeChipTextActive: { color: '#000' },
  label: { fontSize: 14, fontWeight: '700', color: '#333', marginTop: 6, marginBottom: 6 },
  input: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF',
    borderRadius: 10,
    paddingHorizontal: 14,
    paddingVertical: 12,
    borderWidth: 1,
    borderColor: GOLD.muted,
  },
  inputField: { flex: 1, fontSize: 16, color: '#000', paddingVertical: 0 },
  inputUnit: { fontSize: 14, color: GOLD.dark, fontWeight: '700', marginLeft: 8 },
  hint: { fontSize: 12, color: '#888', marginTop: 6, lineHeight: 16 },
  maxLink: { fontSize: 13, color: GOLD.dark, fontWeight: '600', marginTop: 6 },
  submit: {
    marginTop: 22,
    backgroundColor: GOLD.primary,
    paddingVertical: 16,
    borderRadius: 12,
    alignItems: 'center',
    shadowColor: GOLD.dark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.25,
    shadowRadius: 6,
    elevation: 4,
  },
  submitDisabled: { opacity: 0.55 },
  submitText: { fontWeight: '800', color: '#000', fontSize: 16 },
  adsLink: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 14,
    marginTop: 18,
  },
  adsLinkText: { marginLeft: 6, color: GOLD.dark, fontWeight: '600' },
});

export default P2PPoolScreen;
