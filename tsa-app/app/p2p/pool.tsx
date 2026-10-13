// app/p2p/pool.tsx
//
// Seller's escrow funds / reclaim screen — view a single token's available +
// locked on-chain balance and WITHDRAW (reclaim) unsold `available` funds.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives); behavior, API calls, and the sign → broadcast → confirm flow are
// unchanged.
//
// "Hide the pool" (spec 2026-06-05, Architecture §3): seller funding now happens
// inline during ad creation (services/escrowFunding.ts), so this screen no longer
// offers Deposit as a primary action. It narrows to "your escrow balance &
// reclaim" — viewing available/locked and withdrawing available back to the
// wallet. The deposit/withdraw mode toggle collapses to withdraw-only.
//
// Flow:
//   1. GET /p2p/balance — current (available, locked) for the active wallet.
//   2. Enter an amount to reclaim, confirm.
//   3. Backend builds an unsigned withdraw tx; we sign locally + broadcast on the
//      selected chain.
//   4. POST /p2p/withdrawals/confirm with the txHash → audit row.
//
// Recovery: if a withdraw broadcast lands but the server-side confirm POST fails,
// the record is stashed in the shared, non-overwriting pending store
// (services/p2pPending, mode 'withdraw') so the user can retry the confirm step
// without re-broadcasting (the on-chain tx already happened). Confirms are
// idempotent server-side (same txHash → same result). The store is a LIST keyed
// by txHash so a second stranded withdraw can't orphan a prior one.

import { router, useLocalSearchParams } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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
  p2pNetwork,
  p2pTokenDecimals,
  parseTokenAmount,
  poolApi,
  unsignedTxToTxLike,
  type P2PChainInfo,
} from '../../services/p2p';
import { ensureP2PGas } from '../../services/p2pGas';
import {
  addPending,
  listPending,
  removePending,
  type PendingEscrowConfirm,
} from '../../services/p2pPending';
import { getChain, getChainByChainId } from '../../constants/chains';
import {
  broadcastTransaction,
  getActiveWallet,
  signTransaction,
} from '../../services/wallet';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import { reclaimChainFor } from '@/utils/reclaimChain';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  Chip,
  Divider,
  Icon,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

// MCGP stays here on purpose even though it is no longer a P2P asset: this
// screen is reclaim-only, and sellers must still be able to withdraw MCGP they
// deposited before it was delisted. New MCGP deposits are refused by the API.
const TOKENS = ['USDT', 'USDC', 'MCGP'] as const;
type Token = typeof TOKENS[number];

// Sonic chain IDs (mainnet + testnet) — the screen defaults to Sonic so existing
// single-chain behavior is preserved when the chains endpoint is unreachable or
// hasn't loaded yet.

const P2PPoolScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const kbHeight = useKeyboardHeight();
  // `?chainId=&token=` opens the screen on a given escrow — the TSA Liquidity
  // dashboard's Withdraw sends the pool chain, which only listAll() returns.
  const { chainId: chainParam, token: tokenParam } = useLocalSearchParams<{ chainId?: string; token?: string }>();
  const [token, setToken] = useState<Token>(() =>
    TOKENS.find((t) => t === tokenParam?.toUpperCase()) ?? 'USDT',
  );
  const [amountInput, setAmountInput] = useState('');
  const [available, setAvailable] = useState<string>('0');
  const [locked, setLocked] = useState<string>('0');
  const [loadingBal, setLoadingBal] = useState(true);
  const [balanceError, setBalanceError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [stage, setStage] = useState('');
  // Pending withdraw-confirm record (from the shared store) for the active
  // wallet, if any. The recovery banner keys off the RECORD's own fields, not
  // the live form, so a stranded confirm is always visible.
  const [pending, setPending] = useState<PendingEscrowConfirm | null>(null);
  const [retrying, setRetrying] = useState(false);
  // Active wallet — read once on mount + whenever token changes; all poolApi
  // calls thread this through so the backend gas-estimates against the right
  // balance even if the user has multiple in-app HD wallets.
  const [wallet, setWallet] = useState<string>('');
  // Enabled P2P chains, fetched from /p2p/chains. Empty = endpoint failed or
  // not yet loaded → the selector hides and everything defaults to Sonic.
  const [chains, setChains] = useState<P2PChainInfo[]>([]);
  // The chain the reclaim ops target. Defaults to the ACTIVE network's Sonic
  // chain (14601 on testnet, 146 on mainnet) — NOT a hardcoded mainnet id — so
  // the first balance read on mount targets a chain the backend actually serves
  // (a hardcoded 146 on a testnet backend yields "no escrow configured for
  // chain 146"). `/p2p/chains` may still refine this once it loads. Threaded
  // through every poolApi call and the broadcast/gas-guard so funds route to
  // the picked chain.
  const [selectedChainId, setSelectedChainId] = useState<number>(
    () => Number(chainParam) || getChain('sonic').chainId,
  );

  const decimals = p2pTokenDecimals(selectedChainId, token);

  // Monotonic id for in-flight balance reads; see the guard in loadBalance.
  const balReqRef = useRef(0);

  // Fetch enabled chains once on mount. Graceful fallback: on failure we leave
  // `chains` empty (selector hidden) and keep the Sonic default — the screen
  // stays fully functional for the current single-chain behavior.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      // listAll keeps the pool chain (sent to an LP bound to it, paused too)
      // so an LP can reclaim there; it also records each chain's token decimals.
      const res = await chainsApi.listAll();
      if (cancelled || !res.success || !res.data || res.data.length === 0) return;
      const list = res.data;
      setChains(list);
      // Keep the chain the screen was opened on (an LP's Withdraw); otherwise
      // prefer Sonic, then the first enabled chain (reclaimChainFor).
      setSelectedChainId((prev) => reclaimChainFor(list, prev, Number(chainParam) || undefined));
    })();
    return () => {
      cancelled = true;
    };
  }, [chainParam]);

  // Label a chain for the selector — prefer the constants' shortName, else the
  // chains endpoint can't supply a name so fall back to "Chain <id>".
  const chainLabel = useCallback(
    (chainId: number) => getChainByChainId(chainId)?.shortName ?? `Chain ${chainId}`,
    [],
  );

  const loadBalance = useCallback(async () => {
    // Stale-response guard: a chain switch (or the /p2p/chains refinement)
    // fires a fresh load while a prior one is still in flight. Without this,
    // the slower (often wrong-chain) response can land last and clobber the
    // good result — e.g. an initial Sonic read overwriting a just-loaded BSC
    // balance with a stale error. Only the latest request may touch state.
    const reqId = ++balReqRef.current;
    setLoadingBal(true);
    setBalanceError(null);
    const w = wallet || (await getActiveWallet());
    if (balReqRef.current !== reqId) return;
    if (!w) {
      setAvailable('—');
      setLocked('—');
      setBalanceError('No active wallet. Set one up from the wallet screen first.');
      setLoadingBal(false);
      return;
    }
    if (!wallet) setWallet(w);
    const res = await poolApi.balance(token, w, selectedChainId);
    if (balReqRef.current !== reqId) return;
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

  // Restore any pending withdraw-confirm for the active wallet so the recovery
  // banner re-appears if the user navigated away mid-failure. Keys off the
  // RECORD (token/chainId/amount/txHash), NOT the current form selection, so a
  // stranded confirm is always visible. Surfaces the most recent one matching
  // the currently selected chain (else the most recent across chains) so a
  // user on the right chain sees the actionable record first.
  const loadPending = useCallback(async () => {
    const w = wallet || (await getActiveWallet());
    if (!w) {
      setPending(null);
      return;
    }
    const list = await listPending('withdraw', w);
    if (list.length === 0) {
      setPending(null);
      return;
    }
    const sorted = [...list].sort((a, b) => b.createdAt - a.createdAt);
    const onChain = sorted.find((r) => r.chainId === selectedChainId);
    setPending(onChain ?? sorted[0]);
  }, [wallet, selectedChainId]);

  useEffect(() => {
    loadBalance();
    loadPending();
  }, [loadBalance, loadPending]);

  const amountValid = useMemo(() => {
    const trimmed = amountInput.trim();
    if (!trimmed) return false;
    if (!/^\d+(\.\d+)?$/.test(trimmed)) return false;
    return parseFloat(trimmed) > 0;
  }, [amountInput]);

  // Pre-check: withdrawing more than the on-chain available balance is the
  // most common cause of a confusing post-broadcast revert. Catch it here.
  const exceedsAvailable = useMemo(() => {
    if (!amountValid) return false;
    if (available === '—') return false; // unknown balance — let the server gate it
    try {
      const want = BigInt(parseTokenAmount(amountInput.trim(), decimals));
      const have = BigInt(available);
      return want > have;
    } catch {
      return false;
    }
  }, [amountValid, amountInput, decimals, available]);

  const canSubmit = amountValid && !submitting && !exceedsAvailable;

  // Run a withdraw-confirm against a stashed record's own fields. The confirm
  // must reference the wallet that signed the original tx — the backend
  // cross-checks against the on-chain event's `seller` field — and the chain
  // the tx was BROADCAST on, not the current UI selection.
  const runConfirm = async (p: PendingEscrowConfirm): Promise<void> => {
    const w = p.walletAddress || wallet || (await getActiveWallet());
    if (!w) throw new Error('No active wallet to confirm against.');
    const conf = await poolApi.confirmWithdraw(p.token, p.amount, p.txHash, w, p.chainId);
    if (!conf.success) {
      throw new Error(conf.message || `Confirm returned ${conf.status ?? 'no status'}`);
    }
  };

  const handleRetryConfirm = async () => {
    if (!pending || retrying) return;
    setRetrying(true);
    try {
      await runConfirm(pending);
      await removePending(pending.txHash);
      await loadPending();
      Alert.alert('Confirmed', 'Your previous transaction has been recorded.');
      loadBalance();
    } catch (err: any) {
      Alert.alert('Retry failed', err?.message || 'Server still cannot record this tx.');
    } finally {
      setRetrying(false);
    }
  };

  const handleDiscardPending = async () => {
    if (!pending) return;
    const txHash = pending.txHash;
    Alert.alert(
      'Discard pending confirmation?',
      'The on-chain transaction still happened. Keep the tx hash; you may need it for support.',
      [
        { text: 'Keep', style: 'cancel' },
        {
          text: 'Discard',
          style: 'destructive',
          onPress: async () => {
            await removePending(txHash);
            await loadPending();
          },
        },
      ],
    );
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

      // Gas guard: make sure the wallet holds the selected chain's native token
      // before we sign/broadcast — the withdraw tx pays gas on this chain. Bails
      // with a fund-your-wallet prompt rather than letting the tx fail mid-flow.
      setStage('Checking gas…');
      // Only Sonic is sponsored; on any other chain (the LP pool chain
      // included) ensureP2PGas is the plain signer balance check.
      const hasGas = await ensureP2PGas({ chainId: selectedChainId, walletAddress: w, action: 'withdraw', onStage: setStage });
      if (!hasGas) return; // `finally` resets submitting/stage.

      setStage('Preparing transaction…');
      const prep = await poolApi.prepareWithdraw(token, amountBaseUnits, w, selectedChainId);
      if (!prep.success || !prep.data) {
        throw new Error(statusAwareError(prep.status, prep.message) || 'Prepare failed');
      }

      setStage('Signing…');
      const signed = await signTransaction(unsignedTxToTxLike(prep.data.unsignedTx), w);

      setStage('Broadcasting…');
      const resp = await broadcastTransaction(p2pChainKey(selectedChainId), signed, p2pNetwork(selectedChainId));
      broadcastHash = resp.hash;

      setStage('Confirming…');
      await runConfirm({
        mode: 'withdraw',
        token,
        chainId: selectedChainId,
        amount: amountBaseUnits,
        txHash: resp.hash,
        walletAddress: w,
        createdAt: Date.now(),
      });

      Alert.alert(
        'Withdrawal confirmed',
        `${amountInput} ${token} removed from your escrow pool.`,
      );
      setAmountInput('');
      loadBalance();
    } catch (err: any) {
      const detail = err?.message || String(err);
      if (broadcastHash) {
        // The on-chain side happened. Stash (non-overwriting, keyed by txHash)
        // so the user can retry the confirm without re-broadcasting. There's no
        // automatic withdraw reconciler on the backend — only trades are
        // reconciled — so recovery is user-initiated via the banner at the top
        // of the screen.
        await addPending({
          mode: 'withdraw',
          token,
          chainId: selectedChainId,
          amount: amountBaseUnits,
          txHash: broadcastHash,
          walletAddress: wallet || (await getActiveWallet()) || '',
          createdAt: Date.now(),
        });
        await loadPending();
        Alert.alert(
          'On-chain succeeded, server confirm failed',
          `Tx ${broadcastHash}\n\n${detail}\n\nUse the "Retry confirm" button on this screen to record it. Funds are safe in the contract.`,
        );
      } else {
        Alert.alert('Withdrawal failed', detail);
      }
    } finally {
      setSubmitting(false);
      setStage('');
    }
  };

  const fmt = (v: string) => (v === '—' ? '—' : `${formatTokenAmount(v, decimals, 4)} ${token}`);
  const submitLabel = submitting ? stage || 'Working…' : 'Reclaim funds';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
            Reclaim funds from your escrow pool. Available is withdrawable anytime;
            locked is reserved against open trades.
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
                Your last withdrawal broadcast on chain but the server
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

          <AmountInput
            label={`Amount to reclaim (${token})`}
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
              available !== '—'
                ? () => setAmountInput(formatTokenAmount(available, decimals, decimals))
                : undefined
            }
            maxLabel="Use full balance"
          />

          <Text variant="caption" color="textMuted" style={{ marginTop: spacing.sm, lineHeight: 16 }}>
            Reclaiming withdraws from your available escrow balance back to your
            wallet. Locked funds are reserved against open trades and can&apos;t be
            withdrawn until those trades settle. To fund a new ad, deposit happens
            automatically when you publish it.
          </Text>

          <Button
            title={submitLabel}
            variant="primary"
            leftIcon="wallet"
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
