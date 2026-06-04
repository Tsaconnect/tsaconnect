// app/p2p/pool.tsx
//
// Seller's P2P pool management — deposit / withdraw a single token's
// available + locked balance. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives); behavior, API calls, and the
// sign → broadcast → confirm flows are unchanged.
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
  Alert,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  View,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  chainsApi,
  formatTokenAmount,
  p2pChainKey,
  p2pTokenDecimals,
  parseTokenAmount,
  poolApi,
  unsignedTxToTxLike,
  type P2PChainInfo,
} from '../../services/p2p';
import { getChainByChainId } from '../../constants/chains';
import {
  broadcastTransaction,
  ensureGas,
  getActiveWallet,
  signTransaction,
} from '../../services/wallet';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  Chip,
  Divider,
  Icon,
  PressableScale,
  SegmentedControl,
  Spinner,
  Text,
} from '@/components/ui';

const TOKENS = ['USDT', 'USDC', 'MCGP'] as const;
type Token = typeof TOKENS[number];

type Mode = 'deposit' | 'withdraw';

// Sonic chain IDs (mainnet + testnet) — the pool screen defaults to Sonic so
// existing single-chain behavior is preserved when the chains endpoint is
// unreachable or hasn't loaded yet.
const SONIC_CHAIN_IDS = [146, 14601];

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
  /** Chain the tx was broadcast on. Confirm must target the same chain even
   *  if the user switches the chain selector before retrying. Old records
   *  lacking this field fall back to the current selectedChainId. */
  chainId?: number;
}

const P2PPoolScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
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
  // Enabled P2P chains, fetched from /p2p/chains. Empty = endpoint failed or
  // not yet loaded → the selector hides and everything defaults to Sonic.
  const [chains, setChains] = useState<P2PChainInfo[]>([]);
  // The chain the pool ops target. Defaults to Sonic; threaded through every
  // poolApi call and the broadcast/gas-guard so funds route to the picked chain.
  const [selectedChainId, setSelectedChainId] = useState<number>(SONIC_CHAIN_IDS[0]);

  const decimals = p2pTokenDecimals(selectedChainId, token);

  // Fetch enabled chains once on mount. Graceful fallback: on failure we leave
  // `chains` empty (selector hidden) and keep the Sonic default — the screen
  // stays fully functional for the current single-chain behavior.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const res = await chainsApi.list();
      if (cancelled || !res.success || !res.data || res.data.length === 0) return;
      const list = res.data;
      setChains(list);
      // Prefer Sonic; otherwise fall back to the first enabled chain so the
      // selected chain is always one the backend actually serves.
      setSelectedChainId((prev) => {
        if (list.some((c) => c.chainId === prev)) return prev;
        const sonic = list.find((c) => SONIC_CHAIN_IDS.includes(c.chainId));
        return (sonic ?? list[0]).chainId;
      });
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Label a chain for the selector — prefer the constants' shortName, else the
  // chains endpoint can't supply a name so fall back to "Chain <id>".
  const chainLabel = useCallback(
    (chainId: number) => getChainByChainId(chainId)?.shortName ?? `Chain ${chainId}`,
    [],
  );

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
    const res = await poolApi.balance(token, w, selectedChainId);
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
  }, [token, wallet, selectedChainId]);

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
      // Show the pending record only when it matches the current (mode, token,
      // chain) so a BSC pending doesn't surface while Sonic is selected.
      // Back-compat: old records lack chainId → treat as matching any chain.
      const chainMatch = !p.chainId || p.chainId === selectedChainId;
      setPending(p.mode === mode && p.token === token && chainMatch ? p : null);
    } catch {
      setPending(null);
    }
  }, [mode, token, selectedChainId]);

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
    // Always confirm against the chain the tx was BROADCAST on, not the
    // current UI selection — the user may have switched chains since the
    // broadcast. Old records lacking chainId fall back to selectedChainId.
    const chainForConfirm = p.chainId ?? selectedChainId;
    const conf =
      p.mode === 'deposit'
        ? await poolApi.confirmDeposit(p.token, p.amount, p.txHash, w, chainForConfirm)
        : await poolApi.confirmWithdraw(p.token, p.amount, p.txHash, w, chainForConfirm);
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
    const aRes = await poolApi.allowance(token, walletAddress, selectedChainId);
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
    const aPrep = await poolApi.prepareApprove(token, amountBaseUnits, walletAddress, selectedChainId);
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
    const signed = await signTransaction(unsignedTxToTxLike(aPrep.data.unsignedTx), walletAddress);
    setStage('Broadcasting approve…');
    await broadcastTransaction(p2pChainKey(selectedChainId), signed);
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

      // Gas guard (Task 7): make sure the wallet holds the selected chain's
      // native token before we sign/broadcast anything — both the approve and
      // the deposit/withdraw tx pay gas on this chain. Bails with a fund-your-
      // wallet prompt rather than letting the tx fail mid-flow.
      setStage('Checking gas…');
      const hasGas = await ensureGas(p2pChainKey(selectedChainId), w);
      if (!hasGas) return; // `finally` resets submitting/stage.

      // For deposits, ensure the seller has approved the escrow first.
      // Withdrawals don't need an allowance.
      await runApproveIfNeeded(amountBaseUnits, w);

      setStage('Preparing transaction…');
      const prep =
        mode === 'deposit'
          ? await poolApi.prepareDeposit(token, amountBaseUnits, w, selectedChainId)
          : await poolApi.prepareWithdraw(token, amountBaseUnits, w, selectedChainId);
      if (!prep.success || !prep.data) {
        throw new Error(statusAwareError(prep.status, prep.message) || 'Prepare failed');
      }

      setStage('Signing…');
      const signed = await signTransaction(unsignedTxToTxLike(prep.data.unsignedTx), w);

      setStage('Broadcasting…');
      const resp = await broadcastTransaction(p2pChainKey(selectedChainId), signed);
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
          chainId: selectedChainId,
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

  const fmt = (v: string) => (v === '—' ? '—' : `${formatTokenAmount(v, decimals, 4)} ${token}`);
  const submitLabel = submitting
    ? stage || 'Working…'
    : mode === 'deposit'
      ? 'Deposit'
      : 'Withdraw';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="title1" weight="700">
            P2P Pool
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
            Funds you deposit here back your published ads. Available balance is
            withdrawable any time; locked balance is reserved against open trades.
          </Text>

          {pending && (
            <AccentCard railColor={colors.danger} padding="md" style={{ marginBottom: spacing.lg }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="warning" size={18} color={colors.danger} />
                <Text variant="bodyMed" weight="700" color="danger">
                  Pending confirmation
                </Text>
              </View>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
                Your last {pending.mode} broadcast on chain but the server
                couldn&apos;t record it. The funds moved correctly; retry to
                update your pool balance.
              </Text>
              <Text variant="caption" color="textMuted" numeric numberOfLines={1} style={{ marginTop: spacing.sm }}>
                tx {pending.txHash}
              </Text>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginTop: spacing.md }}>
                <Button
                  title={retrying ? 'Retrying…' : 'Retry confirm'}
                  size="sm"
                  fullWidth={false}
                  loading={retrying}
                  disabled={retrying}
                  onPress={handleRetryConfirm}
                />
                <Button
                  title="Dismiss"
                  variant="ghost"
                  size="sm"
                  fullWidth={false}
                  disabled={retrying}
                  onPress={handleDiscardPending}
                />
              </View>
            </AccentCard>
          )}

          {chains.length > 1 && (
            <>
              <Text variant="bodySm" color="textSecondary" weight="600" style={{ marginBottom: spacing.sm }}>
                Network
              </Text>
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm, marginBottom: spacing.lg }}>
                {chains.map((c) => (
                  <Chip
                    key={c.chainId}
                    label={chainLabel(c.chainId)}
                    selected={selectedChainId === c.chainId}
                    onPress={() => setSelectedChainId(c.chainId)}
                  />
                ))}
              </View>
            </>
          )}

          <View style={{ flexDirection: 'row', gap: spacing.sm, marginBottom: spacing.lg }}>
            {TOKENS.map((t) => (
              <Chip key={t} label={t} selected={token === t} onPress={() => setToken(t)} />
            ))}
          </View>

          <Card style={{ marginBottom: spacing.lg }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
              <Text variant="bodySm" color="textSecondary" weight="600">
                Available
              </Text>
              {loadingBal ? (
                <Spinner size="small" inline />
              ) : (
                <Text variant="title3" weight="700" numeric>
                  {fmt(available)}
                </Text>
              )}
            </View>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: spacing.xs }}>
              <Text variant="bodySm" color="textSecondary" weight="600">
                Locked
              </Text>
              {loadingBal ? null : (
                <Text variant="bodySm" weight="600" color="textMuted" numeric>
                  {fmt(locked)}
                </Text>
              )}
            </View>
            {balanceError && !loadingBal && (
              <>
                <Divider spacing="sm" />
                <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.sm }}>
                  <Text variant="caption" color="danger" numberOfLines={2} style={{ flex: 1 }}>
                    {balanceError}
                  </Text>
                  <PressableScale onPress={loadBalance} hitSlop={8}>
                    <Text variant="bodySm" weight="700" color="accent">
                      Retry
                    </Text>
                  </PressableScale>
                </View>
              </>
            )}
          </Card>

          <View style={{ marginBottom: spacing.lg }}>
            <SegmentedControl
              segments={[
                { key: 'deposit', label: 'Deposit' },
                { key: 'withdraw', label: 'Withdraw' },
              ]}
              value={mode}
              onChange={(k) => handleModeChange(k as Mode)}
            />
          </View>

          <AmountInput
            label={`Amount (${token})`}
            placeholder="0.00"
            symbol={token}
            symbolPosition="suffix"
            value={amountInput}
            onChangeText={setAmountInput}
            error={
              exceedsAvailable
                ? `Exceeds available (${formatTokenAmount(available, decimals, 4)} ${token}).`
                : undefined
            }
            onMax={
              mode === 'withdraw' && available !== '—'
                ? () => setAmountInput(formatTokenAmount(available, decimals, decimals))
                : undefined
            }
            maxLabel="Use full balance"
          />

          {mode === 'deposit' && (
            <Text variant="caption" color="textMuted" style={{ marginTop: spacing.sm, lineHeight: 16 }}>
              You&apos;ll sign two transactions: an ERC-20 approve (skipped if your
              existing allowance is sufficient) followed by the deposit itself.
            </Text>
          )}

          <Button
            title={submitLabel}
            variant={mode === 'deposit' ? 'primary' : 'secondary'}
            leftIcon={mode === 'deposit' ? 'plus' : 'wallet'}
            loading={submitting}
            disabled={!canSubmit}
            onPress={handleSubmit}
            style={{ marginTop: spacing.xl }}
          />

          <PressableScale
            onPress={() => router.push('/p2p/my-ads')}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'center',
              gap: spacing.xs,
              paddingVertical: spacing.lg,
              marginTop: spacing.md,
            }}
          >
            <Icon name="list" size={16} color={colors.accent} />
            <Text variant="bodySm" weight="600" color="accent">
              Go to my ads
            </Text>
            <Icon name="chevronRight" size={16} color={colors.accent} />
          </PressableScale>
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

export default P2PPoolScreen;
