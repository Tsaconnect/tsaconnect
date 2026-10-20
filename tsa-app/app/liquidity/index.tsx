// app/liquidity/index.tsx — TSA Liquidity, shared pool (spec 2026-10-14 §6).
// Everyone is in one pool: every buyer's spread is shared pro-rata to
// principal. Activate is instant; add, withdraw and claim are the LP's own txs
// (services/liquidityTx.ts). Withdraw and claim are never disabled for a pause
// or a closed pool: that is the custody promise. A withdrawal the pool cannot
// pay in full pays part now and leaves the rest pending; the copy stays neutral.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useFocusEffect } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  EmptyState,
  ScreenHeader,
  Sheet,
  Spinner,
  SummaryRow,
  Text,
  useToast,
} from '@/components/ui';
import {
  liquidityApi,
  type LiquidityActivityItem,
  type LiquidityMe,
} from '@/services/liquidityApi';
import { runLiquidityTx, type LiquidityTxKind, type LiquidityTxResult } from '@/services/liquidityTx';
import { getSignableWallet, readErc20BalanceOf } from '@/services/wallet';
import { formatTokenAmount } from '@/services/p2p';
import type { ChainKey } from '@/constants/chains';
import { getChainByChainId } from '@/constants/chains';
import { promptKycRefusal } from '@/utils/kycRefusal';
import { shortAddress } from '@/utils/sendForm';
import {
  canClaimGasFree,
  claimRefusalMessage,
  formatLiquidityAmount,
  isZeroAmount,
  LIQUIDITY_OLD_WALLET_HAS_FUNDS,
  limitsKnown,
  liquidityAmountError,
  overBalanceError,
  liquidityMessage,
  oldWalletFundsMessage,
  oldWalletHasFunds,
  withdrawalMessage,
  type LiquidityAction,
} from '@/utils/liquidityView';

const CASH_OUT_ROUTE = '/wallet/paycrest/sell?asset=USDT';

const ACTIVITY_LABEL: Record<LiquidityActivityItem['kind'], string> = {
  deposit: 'Added',
  withdrawal: 'Withdrawn',
  claim: 'Claimed',
  earned: 'Earned',
  withdrawal_pending: 'Withdrawal pending',
  withdrawal_paid: 'Withdrawal paid',
};

/** Money leaving the LP's position is −; a paid pending withdrawal was already counted when it went pending. */
function activitySign(kind: LiquidityActivityItem['kind']): string {
  if (kind === 'withdrawal' || kind === 'claim' || kind === 'withdrawal_pending') return '−';
  if (kind === 'withdrawal_paid') return '';
  return '+';
}

export default function LiquidityScreen() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [me, setMe] = useState<LiquidityMe | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [activity, setActivity] = useState<LiquidityActivityItem[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [refreshing, setRefreshing] = useState(false);
  const [busy, setBusy] = useState<string | null>(null); // the stage label while acting
  const [sheet, setSheet] = useState<LiquidityAction | null>(null);
  const [amount, setAmount] = useState('');
  // The LP wallet's own token balance for the Add sheet: undefined while
  // loading, null when no RPC answered (unknown, not zero).
  const [walletBalance, setWalletBalance] = useState<string | null | undefined>();
  const [claimedOut, setClaimedOut] = useState(false); // offer the bank cash-out after a claim
  // The claimable amount when a gas-free claim was sent: Claim stays off until
  // a refresh shows it changed, so the operator's tx is never asked for twice.
  const [claimSentAt, setClaimSentAt] = useState<string | null>(null);
  const activityReq = useRef(0);
  // Set synchronously, so a double tap cannot start a second action before
  // the first one's busy state has rendered.
  const running = useRef(false);

  const depositToken = sheet === 'deposit' ? me?.token : undefined;
  const depositChain = me?.chain?.key;
  const depositWallet = me?.wallet;
  useEffect(() => {
    if (!depositToken || !depositChain || !depositWallet) return;
    let live = true;
    setWalletBalance(undefined);
    readErc20BalanceOf(depositToken.address, depositWallet, depositChain as ChainKey)
      .then((raw) => live && setWalletBalance(formatTokenAmount(raw.toString(), depositToken.decimals, depositToken.decimals)))
      .catch(() => live && setWalletBalance(null));
    return () => { live = false; };
  }, [depositToken, depositChain, depositWallet]);

  const applyMe = useCallback((next: LiquidityMe) => {
    const claimable = next.claimable ?? '';
    setMe(next);
    setClaimSentAt((sent) => (sent != null && sent !== claimable ? null : sent));
    setLoadFailed(false);
  }, []);
  const load = useCallback(async () => {
    const res = await liquidityApi.me();
    if (res.success && res.data) applyMe(res.data);
    else setLoadFailed(true);
  }, [applyMe]);
  const loadActivity = useCallback(async (cursor?: string) => {
    const req = ++activityReq.current;
    const res = await liquidityApi.activity(cursor);
    if (req !== activityReq.current) return;
    const items = res.success && res.data ? res.data.items : [];
    setActivity((prev) => (cursor && prev ? [...prev, ...items] : items));
    setNextCursor(res.success ? res.data?.nextCursor : undefined);
  }, []);
  useFocusEffect(
    useCallback(() => {
      load();
      loadActivity();
    }, [load, loadActivity]),
  );

  const onRefresh = async () => {
    setRefreshing(true);
    await Promise.all([load(), loadActivity()]);
    setRefreshing(false);
  };

  const header = <ScreenHeader title="Your liquidity" fallbackRoute="/p2p" />;
  if (!me) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
        {header}
        {loadFailed ? (
          <EmptyState icon="warning" title="Couldn't load your liquidity" subtitle="Check your connection and try again."
            actionLabel="Try again" onAction={load} />
        ) : (
          <Spinner />
        )}
      </SafeAreaView>
    );
  }

  const sym = me.token?.symbol ?? 'USDT';
  const amt = (v?: string) => `${formatLiquidityAmount(v)} ${sym}`;
  const gasSymbol = (me.chain && getChainByChainId(me.chain.id)?.nativeCurrency.symbol) || 'gas';
  const isLp = me.status === 'active' || me.status === 'paused';
  const acting = busy != null;
  const oldWalletFunded = oldWalletHasFunds(me);
  const claimLocked = claimSentAt != null && claimSentAt === (me.claimable ?? '');

  const begin = (label: string): boolean => {
    if (running.current) return false;
    running.current = true;
    setBusy(label);
    return true;
  };
  const end = () => {
    running.current = false;
    setBusy(null);
  };

  const activate = async () => {
    if (oldWalletFunded) {
      Alert.alert('Old wallet still holds liquidity', oldWalletFundsMessage(me));
      return;
    }
    if (!begin('Activating…')) return;
    try {
      const res = await liquidityApi.activate();
      if (res.success && res.data) {
        setMe(res.data);
        toast.success('Liquidity activated');
      } else if (promptKycRefusal(res.code, res.message)) {
        // The KYC prompt has been shown.
      } else if (res.code === LIQUIDITY_OLD_WALLET_HAS_FUNDS) {
        Alert.alert('Old wallet still holds liquidity', oldWalletFundsMessage(me));
      } else {
        Alert.alert("Couldn't activate", liquidityMessage(res.message, 'Try again in a moment.'));
      }
    } finally {
      end();
    }
  };

  // The LP's txs are signed by the LP wallet itself; this device must hold it.
  const lpWallet = async (): Promise<string | null> => {
    const wallet = await getSignableWallet();
    if (!wallet || wallet.toLowerCase() !== (me.wallet ?? '').toLowerCase()) {
      Alert.alert('Switch wallet', `Your liquidity is held by ${me.wallet}. Switch to that wallet on this device to continue.`);
      return null;
    }
    return wallet;
  };

  const report = (kind: LiquidityTxKind, r: LiquidityTxResult) => {
    const done: Record<LiquidityTxKind, string> = {
      deposit: 'Liquidity added',
      withdraw: 'Withdrawal sent to your wallet',
      claim: 'Earnings sent to your wallet',
    };
    switch (r.status) {
      case 'done':
        toast.success(done[kind]);
        if (kind === 'claim') setClaimedOut(true);
        return true;
      case 'pending':
        if (r.step === 'approve') {
          Alert.alert('Approval confirming', 'Approval is confirming. Tap Add again in a minute to finish adding liquidity.');
        } else {
          Alert.alert('Still confirming', 'Your transaction was sent and is still confirming. Pull down to refresh in a minute — do not send it again.');
        }
        return true;
      case 'reverted':
        Alert.alert('Not completed', 'The network rejected the transaction. Nothing changed; you can try again.');
        return false;
      case 'refused':
        Alert.alert("Couldn't continue", kind === 'claim'
          ? claimRefusalMessage(r.code, r.message)
          : liquidityMessage(r.message, 'Try again in a moment.'));
        return false;
      default:
        return false;
    }
  };

  const runTx = async (kind: LiquidityTxKind, value?: string) => {
    if (!begin('Preparing…')) return false;
    try {
      const wallet = await lpWallet();
      if (!wallet) return false;
      const r = await runLiquidityTx({
        me, kind, amount: value, wallet,
        onStage: setBusy,
        onGasAlert: (title, message) => Alert.alert(title, message),
      });
      if (kind === 'withdraw' && r.status === 'done') {
        // The split comes from the mined receipt's vault events, not a re-read that can lag.
        toast.success(withdrawalMessage(me, r.withdrawal));
        await Promise.all([load(), loadActivity()]);
        return true;
      }
      const moved = report(kind, r);
      if (moved) await Promise.all([load(), loadActivity()]);
      return moved;
    } catch (err) {
      Alert.alert('Not sent', err instanceof Error ? err.message : 'Try again.');
      return false;
    } finally {
      end();
    }
  };

  const submitSheet = async () => {
    if (!sheet) return;
    const err = liquidityAmountError(me, sheet, amount);
    if (err) {
      Alert.alert('Check the amount', err);
      return;
    }
    if (await runTx(sheet === 'deposit' ? 'deposit' : 'withdraw', amount.trim())) {
      setSheet(null);
      setAmount('');
    }
  };

  const claim = async () => {
    if (!canClaimGasFree(me)) {
      await runTx('claim');
      return;
    }
    if (!begin('Claiming…')) return;
    let rateLimited = false;
    try {
      const res = await liquidityApi.claimFor();
      if (res.success) {
        setClaimSentAt(me.claimable ?? '');
        toast.success('Earnings on the way to your wallet');
        setClaimedOut(true);
        await Promise.all([load(), loadActivity()]);
        return;
      }
      if (res.status === 429) rateLimited = true;
      else Alert.alert("Couldn't claim", claimRefusalMessage(res.code, res.message));
    } finally {
      end();
    }
    // A signed claim costs the LP gas, so it is never sent without asking.
    if (rateLimited) {
      Alert.alert('Claim with gas?', `The gas-free claim was used in the last 8 hours. Claim now with a little ${gasSymbol} for gas?`, [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Continue', onPress: () => { void runTx('claim'); } },
      ]);
    }
  };

  const sheetError = sheet && amount
    ? liquidityAmountError(me, sheet, amount) ?? (sheet === 'deposit' ? overBalanceError(amount, walletBalance, sym, me.token?.decimals) : null)
    : null;
  const pool = me.pool;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
      {header}
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}
      >
        {pool && (
          <AccentCard padding="md">
            <Text variant="caption" color="textSecondary">TSA Connect pool · {me.chain?.name ?? 'Pool network'}</Text>
            {pool.totalLiquidity != null && <SummaryRow label="Total liquidity" value={amt(pool.totalLiquidity)} />}
            {pool.apr30d != null && (
              <SummaryRow label="Earnings rate (last 30 days)" value={`${pool.apr30d}% a year`}
                hint="Past earnings, not a promise. LPs share every buyer's spread by their share of the pool." />
            )}
          </AccentCard>
        )}

        {!isLp && (
          <Card padding="md" style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700">Provide liquidity & earn</Text>
            <Text variant="bodySm" color="textSecondary">
              {limitsKnown(me) ? `Add ${me.limits.min}–${me.limits.max} ${sym}. ` : ''}Every time a buyer gets {sym} from the pool, the spread is shared
              between providers by their share. You can withdraw any time after a 24-hour lock on each deposit.
            </Text>
            <Text variant="caption" color="textMuted">Open to TSA Pay merchants and verified users.</Text>
            <Button title="Activate" variant="primary" loading={acting} disabled={acting} onPress={activate} />
          </Card>
        )}

        <Button title="Naira liquidity" variant="ghost" onPress={() => router.push('/liquidity/naira' as never)} />

        {isLp && (
          <>
            {me.paused && (
              <Text variant="bodySm" color="warning">
                Your liquidity is paused: you cannot add more. You can still withdraw and claim.
              </Text>
            )}
            {!me.poolOpen && !me.paused && (
              <Text variant="bodySm" color="textSecondary">
                The pool is closed to new sales right now. Your balance is safe; you can withdraw and claim.
              </Text>
            )}
            {me.walletChanged && (
              <Card padding="md" style={{ gap: spacing.sm }}>
                <Text variant="bodySm">
                  Your account now uses a different wallet. Withdraw and claim still go to {me.wallet}.{' '}
                  {oldWalletFunded ? oldWalletFundsMessage(me) : 'To add liquidity from your new wallet, activate it.'}
                </Text>
                {!oldWalletFunded && (
                  <Button title="Activate new wallet" variant="secondary" loading={acting} disabled={acting} onPress={activate} />
                )}
              </Card>
            )}

            <Card padding="md">
              {me.balanceReadable ? (
                <>
                  <SummaryRow label="Your liquidity" value={amt(me.principal)} total />
                  {me.lockedUntil && (
                    <SummaryRow label="Withdrawable now" value={amt(me.withdrawable)}
                      hint={`New deposits unlock 24 hours after you add them. The rest unlocks ${new Date(me.lockedUntil).toLocaleString()}.`} />
                  )}
                  {!isZeroAmount(me.pendingWithdrawal) && (
                    <SummaryRow label="Pending withdrawal" value={amt(me.pendingWithdrawal)} />
                  )}
                  <SummaryRow label="Share of the pool" value={`${me.sharePercent ?? '0'}%`} />
                  <SummaryRow label="Ready to claim" value={amt(me.claimable)} valueColor="success" />
                  <SummaryRow label="Earned all time" value={amt(me.earned)} />
                  <SummaryRow label="Limits" value={limitsKnown(me) ? `${me.limits.min}–${me.limits.max} ${sym}` : '—'} />
                </>
              ) : (
                <Text variant="bodySm" color="textSecondary">We can&apos;t read your balance right now. Pull to refresh.</Text>
              )}
            </Card>

            {acting && <Text variant="caption" color="textSecondary">{busy}</Text>}
            <View style={{ flexDirection: 'row', gap: spacing.md }}>
              <View style={{ flex: 1 }}>
                <Button title="Add" variant="primary"
                  disabled={acting || me.paused || !me.poolOpen || me.walletChanged || !me.allowed}
                  onPress={() => setSheet('deposit')} />
              </View>
              <View style={{ flex: 1 }}>
                <Button title="Withdraw" variant="secondary" disabled={acting || isZeroAmount(me.withdrawable ?? me.principal)}
                  onPress={() => setSheet('withdraw')} />
              </View>
            </View>
            {!me.allowed && !me.paused && !me.walletChanged && (
              <Text variant="caption" color="textMuted">Setting up your wallet in the pool — this takes a couple of minutes.</Text>
            )}
            <Button title="Claim earnings" variant="secondary"
              disabled={acting || claimLocked || isZeroAmount(me.claimable)} loading={busy === 'Claiming…'} onPress={claim} />
            <Text variant="caption" color="textMuted">
              {claimLocked
                ? 'Your claim is on its way. Pull down to refresh in a minute.'
                : `Gas-free from ${me.minClaim} ${sym}; smaller claims need a little ${gasSymbol}.`}
            </Text>
            {claimedOut && (
              <Button title="Cash out to your bank" variant="ghost" onPress={() => router.push(CASH_OUT_ROUTE as never)} />
            )}

            <Text variant="bodyMed" weight="700">Activity</Text>
            {activity == null ? (
              <Spinner />
            ) : activity.length ? (
              <Card padding="md">
                {activity.map((it) => (
                  <SummaryRow key={`${it.kind}:${it.txHash}:${it.at}`}
                    label={`${ACTIVITY_LABEL[it.kind]} · ${new Date(it.at).toLocaleString()}`}
                    value={`${activitySign(it.kind)}${amt(it.amount)}`}
                    valueColor={it.kind === 'earned' ? 'success' : undefined} />
                ))}
                {nextCursor && <Button title="Load more" variant="ghost" onPress={() => loadActivity(nextCursor)} />}
              </Card>
            ) : (
              <Text variant="bodySm" color="textSecondary">Nothing yet.</Text>
            )}
          </>
        )}
      </ScrollView>

      <Sheet
        visible={sheet != null}
        onClose={() => !acting && setSheet(null)}
        title={sheet === 'deposit' ? 'Add liquidity' : 'Withdraw liquidity'}
        footer={
          <Button title={busy ?? (sheet === 'deposit' ? 'Add' : 'Withdraw')} variant="primary"
            loading={acting} disabled={acting || !amount || !!sheetError} onPress={submitSheet} />
        }
      >
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.xs }}>
          <AmountInput value={amount} onChangeText={setAmount} symbol={sym} symbolPosition="suffix"
            onMax={sheet === 'withdraw'
              ? () => setAmount(me.withdrawable ?? me.principal ?? '')
              : walletBalance && !isZeroAmount(walletBalance) ? () => setAmount(walletBalance) : undefined} />
          {sheetError ? <Text variant="caption" color="danger">{sheetError}</Text> : null}
          <View style={{ marginTop: spacing.xs }}>
            {sheet === 'deposit' ? (
              <>
                <SummaryRow label="Your balance"
                  value={walletBalance === undefined ? '…' : walletBalance === null ? 'Unavailable' : amt(walletBalance)} />
                <SummaryRow label="From" value={me.wallet ? shortAddress(me.wallet) : '—'} />
                <SummaryRow label="Earns" value="From the start" />
                <SummaryRow label="Withdraw" value="After 24 hours" />
                <SummaryRow label="Network fee" value={`Paid in ${gasSymbol}`} />
              </>
            ) : (
              <>
                <SummaryRow label="Available now" value={amt(me.withdrawable ?? me.principal)} />
                <SummaryRow label="To" value={me.wallet ? shortAddress(me.wallet) : '—'} />
                <SummaryRow label="Earnings" value="Stay claimable" />
              </>
            )}
          </View>
        </View>
      </Sheet>
    </SafeAreaView>
  );
}
