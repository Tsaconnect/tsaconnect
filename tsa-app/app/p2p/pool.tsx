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
// Recovery: if broadcast lands but the server-side confirm POST fails, the
// txHash is stashed in AsyncStorage so the user can retry the confirm step
// without re-broadcasting (the on-chain tx already happened). Confirms are
// idempotent server-side (same txHash → same result).
//
// ERC20 allowance: deposit calls `transferFrom` inside the escrow contract,
// so the seller must approve the escrow for `amount` first. This screen
// only handles the deposit / withdraw step; the approve flow lives in the
// wallet manager (or the seller approves manually via their wallet UI).

import { router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
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
import { P2P_GOLD } from '../../constants/p2p';
import {
  formatTokenAmount,
  parseTokenAmount,
  poolApi,
  TOKEN_DECIMALS,
  unsignedTxToTxLike,
} from '../../services/p2p';
import {
  broadcastTransaction,
  getActiveWallet,
  signTransaction,
} from '../../services/wallet';

const GOLD = P2P_GOLD;

const TOKENS = ['USDT', 'USDC', 'MCGP'] as const;
type Token = typeof TOKENS[number];

type Mode = 'deposit' | 'withdraw';

// AsyncStorage key for stashing a tx that broadcast successfully but whose
// server-side confirm failed. One record per (mode, token); a new broadcast
// for the same pair overwrites it (the previous tx already happened on
// chain, so the user can manually retry the confirm at any time).
const PENDING_KEY = 'p2p_pool_pending_v1';

interface PendingConfirm {
  mode: Mode;
  token: Token;
  amount: string; // base units
  txHash: string;
  createdAt: number;
  /** Wallet that signed the broadcast. Confirm must reference this same
   *  wallet so the backend's on-chain `seller` cross-check passes. */
  walletAddress?: string;
}

const P2PPoolScreen: React.FC = () => {
  const [mode, setMode] = useState<Mode>('deposit');
  const [token, setToken] = useState<Token>('USDT');
  const [amountInput, setAmountInput] = useState('');
  const [available, setAvailable] = useState<string>('0');
  const [locked, setLocked] = useState<string>('0');
  const [loadingBal, setLoadingBal] = useState(true);
  const [balanceError, setBalanceError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [stage, setStage] = useState('');
  const [pending, setPending] = useState<PendingConfirm | null>(null);
  const [retrying, setRetrying] = useState(false);
  // Active wallet — read once on mount + whenever token/mode changes; all
  // poolApi calls thread this through so the backend gas-estimates against
  // the right balance even if the user has multiple in-app HD wallets.
  const [wallet, setWallet] = useState<string>('');

  const decimals = TOKEN_DECIMALS[token] ?? 18;

  const loadBalance = useCallback(async () => {
    setLoadingBal(true);
    setBalanceError(null);
    const w = wallet || (await getActiveWallet());
    if (!w) {
      setAvailable('—');
      setLocked('—');
      setBalanceError('No active wallet. Set one up from the wallet screen first.');
      setLoadingBal(false);
      return;
    }
    if (!wallet) setWallet(w);
    const res = await poolApi.balance(token, w);
    if (res.success && res.data) {
      setAvailable(res.data.available);
      setLocked(res.data.locked);
    } else {
      // Treat as unknown — do NOT show falsy zeros, which would lead a
      // seller with locked funds to assume their money is gone.
      setAvailable('—');
      setLocked('—');
      setBalanceError(res.message || 'Could not read pool balance');
    }
    setLoadingBal(false);
  }, [token, wallet]);

  // Restore any pending confirm for the current (mode, token) so the
  // recovery banner re-appears if the user navigated away mid-failure.
  const loadPending = useCallback(async () => {
    try {
      const raw = await AsyncStorage.getItem(PENDING_KEY);
      if (!raw) {
        setPending(null);
        return;
      }
      const p: PendingConfirm = JSON.parse(raw);
      setPending(p.mode === mode && p.token === token ? p : null);
    } catch {
      setPending(null);
    }
  }, [mode, token]);

  useEffect(() => {
    loadBalance();
    loadPending();
  }, [loadBalance, loadPending]);

  // Switching mode is a user intent change — clear the typed amount so a
  // deposit-side value isn't silently submitted as a withdraw.
  const handleModeChange = (next: Mode) => {
    if (next === mode) return;
    setMode(next);
    setAmountInput('');
  };

  const amountValid = useMemo(() => {
    const trimmed = amountInput.trim();
    if (!trimmed) return false;
    if (!/^\d+(\.\d+)?$/.test(trimmed)) return false;
    return parseFloat(trimmed) > 0;
  }, [amountInput]);

  // Pre-check: withdraw exceeding the on-chain available balance is the
  // most common cause of a confusing post-broadcast revert. Catch it here.
  const exceedsAvailable = useMemo(() => {
    if (mode !== 'withdraw' || !amountValid) return false;
    if (available === '—') return false; // unknown balance — let the server gate it
    try {
      const want = BigInt(parseTokenAmount(amountInput.trim(), decimals));
      const have = BigInt(available);
      return want > have;
    } catch {
      return false;
    }
  }, [mode, amountValid, amountInput, decimals, available]);

  const canSubmit = amountValid && !submitting && !exceedsAvailable;

  const persistPending = async (p: PendingConfirm | null) => {
    if (p) {
      await AsyncStorage.setItem(PENDING_KEY, JSON.stringify(p));
    } else {
      await AsyncStorage.removeItem(PENDING_KEY);
    }
    setPending(p);
  };

  const runConfirm = async (p: PendingConfirm): Promise<void> => {
    // Confirm must reference the wallet that signed the original tx — the
    // backend cross-checks against the on-chain event's `seller` field, so a
    // mismatch here means "you're trying to confirm someone else's deposit".
    const w = p.walletAddress || wallet || (await getActiveWallet());
    if (!w) throw new Error('No active wallet to confirm against.');
    const conf =
      p.mode === 'deposit'
        ? await poolApi.confirmDeposit(p.token, p.amount, p.txHash, w)
        : await poolApi.confirmWithdraw(p.token, p.amount, p.txHash, w);
    if (!conf.success) {
      throw new Error(conf.message || `Confirm returned ${conf.status ?? 'no status'}`);
    }
  };

  const handleRetryConfirm = async () => {
    if (!pending || retrying) return;
    setRetrying(true);
    try {
      await runConfirm(pending);
      await persistPending(null);
      Alert.alert('Confirmed', 'Your previous transaction has been recorded.');
      loadBalance();
    } catch (err: any) {
      Alert.alert('Retry failed', err?.message || 'Server still cannot record this tx.');
    } finally {
      setRetrying(false);
    }
  };

  const handleDiscardPending = async () => {
    Alert.alert(
      'Discard pending confirmation?',
      'The on-chain transaction still happened. Keep the tx hash; you may need it for support.',
      [
        { text: 'Keep', style: 'cancel' },
        {
          text: 'Discard',
          style: 'destructive',
          onPress: async () => {
            await persistPending(null);
          },
        },
      ],
    );
  };

  // runApproveIfNeeded: deposit's first stage. Reads the current allowance,
  // and if it's short of `amountBaseUnits`, broadcasts an approve tx so the
  // subsequent deposit's transferFrom succeeds. Withdrawals skip this
  // entirely (no allowance needed for the seller's own withdrawal).
  const runApproveIfNeeded = async (amountBaseUnits: string, walletAddress: string): Promise<void> => {
    if (mode !== 'deposit') return;
    setStage('Checking allowance…');
    const aRes = await poolApi.allowance(token, walletAddress);
    if (aRes.success && aRes.data) {
      try {
        if (BigInt(aRes.data.allowance) >= BigInt(amountBaseUnits)) {
          return; // Allowance already covers the deposit; skip approve.
        }
      } catch {
        // Allowance read returned a non-numeric — fall through to the
        // approve flow rather than silently skipping it.
      }
    }
    setStage('Preparing approve…');
    const aPrep = await poolApi.prepareApprove(token, amountBaseUnits, walletAddress);
    if (!aPrep.success || !aPrep.data) {
      throw new Error(statusAwareError(aPrep.status, aPrep.message) || 'Prepare approve failed');
    }
    // Server may have observed a sufficient allowance between the read
    // above and the prepare call — skip if it now reports we're covered.
    try {
      if (BigInt(aPrep.data.currentAllowance) >= BigInt(amountBaseUnits)) {
        return;
      }
    } catch {}
    setStage('Signing approve…');
    const signed = await signTransaction(unsignedTxToTxLike(aPrep.data.unsignedTx));
    setStage('Broadcasting approve…');
    await broadcastTransaction('sonic' as ChainKey, signed);
    // No backend confirm step for approve — it's a token-contract allowance
    // bump. The subsequent deposit's transferFrom is what actually moves
    // funds; if approve hasn't mined by the time deposit broadcasts, the
    // deposit will revert on chain and we surface that in the catch below.
  };

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    let broadcastHash: string | null = null;
    let amountBaseUnits = '';
    try {
      const w = await getActiveWallet();
      if (!w) throw new Error('No active wallet. Set one up from the wallet screen first.');
      // Keep the screen-level wallet state in sync with the signer we're
      // actually about to use, so a subsequent loadBalance() reflects this.
      if (w !== wallet) setWallet(w);
      amountBaseUnits = parseTokenAmount(amountInput.trim(), decimals);
      if (amountBaseUnits === '0') throw new Error('Amount must be greater than zero.');

      // For deposits, ensure the seller has approved the escrow first.
      // Withdrawals don't need an allowance.
      await runApproveIfNeeded(amountBaseUnits, w);

      setStage('Preparing transaction…');
      const prep =
        mode === 'deposit'
          ? await poolApi.prepareDeposit(token, amountBaseUnits, w)
          : await poolApi.prepareWithdraw(token, amountBaseUnits, w);
      if (!prep.success || !prep.data) {
        throw new Error(statusAwareError(prep.status, prep.message) || 'Prepare failed');
      }

      setStage('Signing…');
      const signed = await signTransaction(unsignedTxToTxLike(prep.data.unsignedTx));

      setStage('Broadcasting…');
      const resp = await broadcastTransaction('sonic' as ChainKey, signed);
      broadcastHash = resp.hash;

      setStage('Confirming…');
      await runConfirm({
        mode,
        token,
        amount: amountBaseUnits,
        txHash: resp.hash,
        createdAt: Date.now(),
        walletAddress: w,
      });

      Alert.alert(
        mode === 'deposit' ? 'Deposit confirmed' : 'Withdrawal confirmed',
        `${amountInput} ${token} ${mode === 'deposit' ? 'added to' : 'removed from'} your pool.`,
      );
      setAmountInput('');
      await persistPending(null);
      loadBalance();
    } catch (err: any) {
      const detail = err?.message || String(err);
      if (broadcastHash) {
        // The on-chain side happened. Stash so the user can retry the
        // confirm without re-broadcasting. There's no automatic deposit
        // reconciler on the backend — only trades are reconciled — so
        // recovery is user-initiated via the banner at the top of the screen.
        await persistPending({
          mode,
          token,
          amount: amountBaseUnits,
          txHash: broadcastHash,
          createdAt: Date.now(),
          walletAddress: wallet || undefined,
        });
        Alert.alert(
          'On-chain succeeded, server confirm failed',
          `Tx ${broadcastHash}\n\n${detail}\n\nUse the "Retry confirm" button on this screen to record it. Funds are safe in the contract.`,
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

          {pending && (
            <View style={styles.pendingCard}>
              <View style={styles.pendingRow}>
                <Icon name="warning" size={18} color={GOLD.error} />
                <Text style={styles.pendingTitle}>Pending confirmation</Text>
              </View>
              <Text style={styles.pendingBody}>
                Your last {pending.mode} broadcast on chain but the server
                couldn't record it. The funds moved correctly; retry to
                update your pool balance.
              </Text>
              <Text style={styles.pendingHash} numberOfLines={1}>
                tx {pending.txHash}
              </Text>
              <View style={styles.pendingActions}>
                <TouchableOpacity
                  style={[styles.pendingBtn, retrying && styles.btnDisabled]}
                  onPress={handleRetryConfirm}
                  disabled={retrying}
                  activeOpacity={0.85}
                >
                  <Text style={styles.pendingBtnText}>
                    {retrying ? 'Retrying…' : 'Retry confirm'}
                  </Text>
                </TouchableOpacity>
                <TouchableOpacity
                  style={styles.pendingDiscard}
                  onPress={handleDiscardPending}
                  disabled={retrying}
                >
                  <Text style={styles.pendingDiscardText}>Dismiss</Text>
                </TouchableOpacity>
              </View>
            </View>
          )}

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
                  {available === '—'
                    ? '—'
                    : `${formatTokenAmount(available, decimals, 4)} ${token}`}
                </Text>
              )}
            </View>
            <View style={styles.balanceRow}>
              <Text style={styles.balanceLabel}>Locked</Text>
              {loadingBal ? null : (
                <Text style={styles.balanceLocked}>
                  {locked === '—' ? '—' : `${formatTokenAmount(locked, decimals, 4)} ${token}`}
                </Text>
              )}
            </View>
            {balanceError && !loadingBal && (
              <View style={styles.balanceErrorRow}>
                <Text style={styles.balanceErrorText} numberOfLines={2}>
                  {balanceError}
                </Text>
                <TouchableOpacity onPress={loadBalance} hitSlop={8}>
                  <Text style={styles.balanceRetry}>Retry</Text>
                </TouchableOpacity>
              </View>
            )}
          </View>

          <View style={styles.modeRow}>
            {(['deposit', 'withdraw'] as const).map((m) => (
              <TouchableOpacity
                key={m}
                style={[styles.modeChip, mode === m && styles.modeChipActive]}
                onPress={() => handleModeChange(m)}
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
          <View
            style={[styles.input, exceedsAvailable && styles.inputError]}
          >
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
          {exceedsAvailable && (
            <Text style={styles.fieldError}>
              Exceeds available ({formatTokenAmount(available, decimals, 4)} {token}).
            </Text>
          )}
          {mode === 'deposit' ? (
            <Text style={styles.hint}>
              You'll sign two transactions: an ERC-20 approve (skipped if your
              existing allowance is sufficient) followed by the deposit itself.
            </Text>
          ) : (
            available !== '—' && (
              <TouchableOpacity
                onPress={() => setAmountInput(formatTokenAmount(available, decimals, decimals))}
                hitSlop={8}
              >
                <Text style={styles.maxLink}>Use full available balance</Text>
              </TouchableOpacity>
            )
          )}

          <TouchableOpacity
            style={[styles.submit, !canSubmit && styles.submitDisabled]}
            onPress={handleSubmit}
            disabled={!canSubmit}
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

// statusAwareError turns a status code + raw server message into an
// actionable phrase. The backend returns 410 Gone for legacy migration
// paths a stale client hits; 503 for stale rate feeds; everything else
// passes through unchanged.
function statusAwareError(status: number | undefined, message: string | undefined): string | undefined {
  if (status === 410) return 'Your app version is out of date — please update and retry.';
  if (status === 503) return 'Live rate temporarily unavailable. Refresh and try again.';
  return message;
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: GOLD.background },
  scroll: { padding: 20, paddingBottom: 60 },
  heading: { fontSize: 24, fontWeight: '800', color: '#000', marginBottom: 6 },
  subheading: { fontSize: 13, color: '#666', marginBottom: 18, lineHeight: 19 },
  pendingCard: {
    backgroundColor: '#FFF6E5',
    borderRadius: 10,
    padding: 14,
    marginBottom: 18,
    borderWidth: 1,
    borderColor: GOLD.error,
  },
  pendingRow: { flexDirection: 'row', alignItems: 'center', marginBottom: 6 },
  pendingTitle: { marginLeft: 6, fontSize: 14, fontWeight: '700', color: GOLD.error },
  pendingBody: { fontSize: 13, color: '#444', lineHeight: 18, marginBottom: 4 },
  pendingHash: { fontSize: 11, color: '#777', marginBottom: 10, fontFamily: 'monospace' },
  pendingActions: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  pendingBtn: {
    backgroundColor: GOLD.primary,
    paddingVertical: 8,
    paddingHorizontal: 16,
    borderRadius: 8,
  },
  pendingBtnText: { fontWeight: '700', color: '#000' },
  pendingDiscard: { paddingVertical: 8 },
  pendingDiscardText: { color: '#666', fontWeight: '600' },
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
  balanceErrorRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginTop: 10,
    paddingTop: 10,
    borderTopWidth: 1,
    borderTopColor: '#F0F0F0',
  },
  balanceErrorText: { flex: 1, fontSize: 12, color: GOLD.error, marginRight: 8 },
  balanceRetry: { color: GOLD.dark, fontWeight: '700', fontSize: 13 },
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
  inputError: { borderColor: GOLD.error },
  inputField: { flex: 1, fontSize: 16, color: '#000', paddingVertical: 0 },
  inputUnit: { fontSize: 14, color: GOLD.dark, fontWeight: '700', marginLeft: 8 },
  fieldError: { color: GOLD.error, fontSize: 12, marginTop: 6 },
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
  btnDisabled: { opacity: 0.55 },
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
