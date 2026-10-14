// app/liquidity/index.tsx — TSA Liquidity, shared pool (spec 2026-10-14 §6).
// Everyone is in one pool: every buyer's spread is shared pro-rata to
// principal, and the company's collateral backs every unrefilled sale 100%, so
// principal is never at risk. Activate is instant; add, withdraw and claim are
// the LP's own txs (services/liquidityTx.ts). Withdraw and claim are never
// disabled for a pause or a closed pool: that is the custody promise.
import React, { useCallback, useRef, useState } from 'react';
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
import { getSignableWallet } from '@/services/wallet';
import { getChainByChainId } from '@/constants/chains';
import { promptKycRefusal } from '@/utils/kycRefusal';
import {
  canClaimGasFree,
  formatLiquidityAmount,
  isZeroAmount,
  LIQUIDITY_OLD_WALLET_HAS_FUNDS,
  liquidityAmountError,
  liquidityMessage,
  oldWalletFundsMessage,
  oldWalletHasFunds,
  type LiquidityAction,
} from '@/utils/liquidityView';

const CASH_OUT_ROUTE = '/wallet/paycrest/sell?asset=USDT';

const ACTIVITY_LABEL: Record<LiquidityActivityItem['kind'], string> = {
  deposit: 'Added',
  withdrawal: 'Withdrawn',
  claim: 'Claimed',
  earned: 'Earned',
};

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
  const [claimedOut, setClaimedOut] = useState(false); // offer the bank cash-out after a claim
  // The claimable amount when a gas-free claim was sent: Claim stays off until
  // a refresh shows it changed, so the operator's tx is never asked for twice.
  const [claimSentAt, setClaimSentAt] = useState<string | null>(null);
  const activityReq = useRef(0);
  // Set synchronously, so a double tap cannot start a second action before
  // the first one's busy state has rendered.
  const running = useRef(false);

  const load = useCallback(async () => {
    const res = await liquidityApi.me();
    if (res.success && res.data) {
      const claimable = res.data.claimable ?? '';
      setMe(res.data);
      setClaimSentAt((sent) => (sent != null && sent !== claimable ? null : sent));
      setLoadFailed(false);
    } else {
      setLoadFailed(true);
    }
  }, []);
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
      Alert.alert('Old wallet still holds liquidity', oldWalletFundsMessage(me.wallet));
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
        Alert.alert('Old wallet still holds liquidity', oldWalletFundsMessage(me.wallet));
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
        Alert.alert("Couldn't continue", liquidityMessage(r.message, 'Try again in a moment.'));
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
      else Alert.alert("Couldn't claim", liquidityMessage(res.message, 'Try again in a moment.'));
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

  const sheetError = sheet && amount ? liquidityAmountError(me, sheet, amount) : null;
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
            <SummaryRow label="Total liquidity" value={amt(pool.totalLiquidity)} />
            <SummaryRow label="Backed by company collateral" value={`${pool.coveragePercent}%`} valueColor="success" />
            <SummaryRow label="Earnings rate (last 30 days)" value={`${pool.apr30d}% a year`}
              hint="Past earnings, not a promise. LPs share every buyer's spread by their share of the pool." />
          </AccentCard>
        )}

        {!isLp && (
          <Card padding="md" style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700">Provide liquidity & earn</Text>
            <Text variant="bodySm" color="textSecondary">
              Add {me.limits.min}–{me.limits.max} {sym}. Every time a buyer gets {sym} from the pool, the spread is shared
              between providers by their share. Your {sym} is backed 100% by company collateral, and you can withdraw
              any time.
            </Text>
            <Text variant="caption" color="textMuted">Open to TSA Pay merchants and verified users.</Text>
            <Button title="Activate" variant="primary" loading={acting} disabled={acting} onPress={activate} />
          </Card>
        )}

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
                  {oldWalletFunded ? oldWalletFundsMessage(me.wallet) : 'To add liquidity from your new wallet, activate it.'}
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
                  <SummaryRow label="Share of the pool" value={`${me.sharePercent ?? '0'}%`} />
                  <SummaryRow label="Ready to claim" value={amt(me.claimable)} valueColor="success" />
                  <SummaryRow label="Earned all time" value={amt(me.earned)} />
                  <SummaryRow label="Limits" value={`${me.limits.min}–${me.limits.max} ${sym}`} />
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
                    value={`${it.kind === 'withdrawal' || it.kind === 'claim' ? '−' : '+'}${amt(it.amount)}`}
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
        <View style={{ gap: spacing.sm }}>
          <AmountInput value={amount} onChangeText={setAmount} symbol={sym} symbolPosition="suffix"
            onMax={sheet === 'withdraw' ? () => setAmount(me.withdrawable ?? me.principal ?? '') : undefined} />
          {sheetError ? (
            <Text variant="caption" color="danger">{sheetError}</Text>
          ) : (
            <Text variant="caption" color="textSecondary">
              {sheet === 'deposit'
                ? `From your wallet ${me.wallet}. It earns from the start and can be withdrawn 24 hours after you add it. You need a little ${gasSymbol} for the network fee.`
                : `Back to your wallet ${me.wallet}. Earnings stay claimable.`}
            </Text>
          )}
        </View>
      </Sheet>
    </SafeAreaView>
  );
}
