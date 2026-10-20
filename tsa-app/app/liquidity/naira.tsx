// app/liquidity/naira.tsx — naira liquidity (plan 2026-10-20). The LP sends USDT
// to the company desk and is owed a naira amount locked at that day's rate: the
// company carries the FX. The USDT still earns the pool share, shown in naira
// at today's rate. An exit is a request an admin pays in USDT at the day's rate.
import React, { useCallback, useRef, useState } from 'react';
import { Alert, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useFocusEffect } from 'expo-router';
import { useTheme } from '@/theme';
import { AmountInput, Button, Card, EmptyState, ScreenHeader, Sheet, Spinner, SummaryRow, Text, useToast } from '@/components/ui';
import { liquidityApi, type FiatPosition } from '@/services/liquidityApi';
import { sendPaycrestDeposit } from '@/services/paycrestDeposit';
import { getSignableWallet } from '@/services/wallet';
import { parseTokenAmount } from '@/services/p2p';
import { promptKycRefusal } from '@/utils/kycRefusal';
import { formatLiquidityAmount, formatNaira, isZeroAmount, liquidityMessage } from '@/utils/liquidityView';

type Sheet_ = 'deposit' | 'principal' | 'earnings';
const TITLE: Record<Sheet_, string> = { deposit: 'Add USDT as naira', principal: 'Withdraw naira', earnings: 'Withdraw earnings' };
const RECORD_RETRIES = 4;
const RECORD_DELAY_MS = 4_000;
const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export default function NairaLiquidityScreen() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [pos, setPos] = useState<FiatPosition | null>(null);
  const [failed, setFailed] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [sheet, setSheet] = useState<Sheet_ | null>(null);
  const [amount, setAmount] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const running = useRef(false);
  const [pendingHash, setPendingHash] = useState<string | null>(null); // sent, not yet recorded

  const load = useCallback(async () => {
    const res = await liquidityApi.fiat();
    if (res.success && res.data) { setPos(res.data); setFailed(false); } else setFailed(true);
  }, []);
  useFocusEffect(useCallback(() => { void load(); }, [load]));
  const onRefresh = async () => { setRefreshing(true); await load(); setRefreshing(false); };

  const header = <ScreenHeader title="Naira liquidity" fallbackRoute="/liquidity" />;
  if (!pos) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
        {header}
        {failed
          ? <EmptyState icon="warning" title="Couldn't load this" subtitle="Check your connection and try again." actionLabel="Try again" onAction={load} />
          : <Spinner />}
      </SafeAreaView>
    );
  }

  const sym = pos.token?.symbol ?? 'USDT';
  const decimals = pos.token?.decimals ?? 18;
  const open = pos.withdrawals.filter((w) => w.status === 'requested');
  const num = /^\d+(\.\d+)?$/.test(amount.trim());
  const sheetError = !sheet || !amount ? null : !num ? 'Enter a valid amount.'
    : sheet === 'deposit' && !pendingHash && Number(amount) < Number(pos.minDeposit) ? `Add at least ${pos.minDeposit} ${sym}.` : null;

  const begin = (label: string) => { if (running.current) return false; running.current = true; setBusy(label); return true; };
  const end = () => { running.current = false; setBusy(null); };

  // Records a sent transfer: the API proves it on-chain and locks the rate.
  // Slow confirmation, a rate blip or a network error are retried; the USDT is
  // never sent again — the hash is kept so the sheet can record it later.
  const record = async (hash: string): Promise<boolean> => {
    for (let i = 0; i < RECORD_RETRIES; i++) {
      const res = await liquidityApi.fiatDeposit(hash);
      if (res.success) {
        toast.success('Deposit recorded');
        setPendingHash(null); setSheet(null); setAmount('');
        await load();
        return true;
      }
      if (promptKycRefusal(res.code, res.message)) return true;
      const retryable = res.code === 'transfer_unproven' || res.code === 'fiat_unavailable' || !res.status || res.status >= 500;
      if (!retryable) {
        setPendingHash(null);
        Alert.alert("Couldn't record it", `${liquidityMessage(res.message, 'Try again in a moment.')}\n\nYour transfer: ${hash}`);
        return true;
      }
      await sleep(RECORD_DELAY_MS);
    }
    setPendingHash(hash);
    Alert.alert('Still confirming', `Your USDT was sent but we couldn't record it yet. Tap "Record transfer" in a minute — don't send again.\n\n${hash}`);
    return false;
  };

  // Every check that can refuse runs BEFORE the USDT leaves: once it is at the
  // desk, undoing it takes support.
  const deposit = async () => {
    if (pendingHash) { if (begin('Recording…')) { try { await record(pendingHash); } finally { end(); } } return; }
    if (!pos.eligible) { Alert.alert('Verify your identity', 'Finish identity verification to add USDT.'); return; }
    if (!pos.currentRate) { Alert.alert('Rates unavailable', 'We can\'t price your deposit right now. Try again shortly.'); return; }
    if (!begin('Sending…')) return;
    let hash = '';
    try {
      const wallet = await getSignableWallet();
      if (!wallet || !pos.chain || !pos.token) { Alert.alert('Wallet needed', 'Open your wallet on this device to continue.'); return; }
      if (!pos.wallet || wallet.toLowerCase() !== pos.wallet.toLowerCase()) {
        Alert.alert('Switch wallet', `Naira deposits must come from your account wallet (${pos.wallet ?? 'not set'}). Switch to it on this device.`);
        return;
      }
      hash = await sendPaycrestDeposit({
        chain: pos.chain.key, token: sym, tokenAmount: amount.trim(), depositAddress: pos.desk,
        tokenAddress: pos.token.address, amountBaseUnits: parseTokenAmount(amount.trim(), decimals), decimals,
      }, wallet, null);
      setBusy('Recording your deposit…');
      await record(hash);
    } catch (e) {
      if (hash) setPendingHash(hash);
      Alert.alert(hash ? 'Sent, but not recorded' : "Couldn't send", e instanceof Error ? e.message : 'Try again in a moment.');
    } finally {
      end();
    }
  };

  const withdraw = async (kind: 'principal' | 'earnings') => {
    if (!begin('Requesting…')) return;
    try {
      const res = await liquidityApi.fiatWithdraw(kind, amount.trim());
      if (res.success) {
        toast.success('Withdrawal requested');
        setSheet(null); setAmount('');
        await load();
      } else if (!promptKycRefusal(res.code, res.message)) {
        Alert.alert("Couldn't request it", liquidityMessage(res.message, 'Try again in a moment.'));
      }
    } finally {
      end();
    }
  };

  const submit = () => (sheet === 'deposit' ? deposit() : sheet ? withdraw(sheet) : undefined);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
      {header}
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}>
        <Card padding="md" style={{ gap: spacing.xs }}>
          <Text variant="caption" color="textSecondary">You are owed</Text>
          <Text variant="title1">{formatNaira(pos.principal)}</Text>
          <Text variant="bodySm" color="textSecondary">
            Fixed in naira. No matter how the dollar moves, you get back exactly this, paid in USDT at the day&apos;s rate.
          </Text>
        </Card>

        <Card padding="md">
          <SummaryRow label="USDT behind it" value={`${formatLiquidityAmount(pos.units)} ${sym}`} />
          {pos.avgRate && <SummaryRow label="Rate when added" value={`${formatNaira(pos.avgRate)} / $1`} />}
          {pos.currentRate && <SummaryRow label="Today's rate" value={`${formatNaira(pos.currentRate)} / $1`} />}
          {pos.todayUsdt && <SummaryRow label="Paid out today" value={`${formatLiquidityAmount(pos.todayUsdt)} ${sym}`} />}
          <SummaryRow label="Earned" value={`${formatLiquidityAmount(pos.earningsAvailable)} ${sym}`} valueColor="success"
            hint={pos.earningsNaira ? `About ${formatNaira(pos.earningsNaira)} at today's rate. Your USDT earns the same pool share as any provider's.` : undefined} />
        </Card>

        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          <View style={{ flex: 1 }}><Button title="Add USDT" variant="primary" disabled={!!busy} onPress={() => setSheet('deposit')} /></View>
          <View style={{ flex: 1 }}>
            <Button title="Withdraw" variant="secondary" disabled={!!busy || isZeroAmount(pos.principal)} onPress={() => setSheet('principal')} />
          </View>
        </View>
        <Button title="Withdraw earnings" variant="ghost" disabled={!!busy || isZeroAmount(pos.earningsAvailable)} onPress={() => setSheet('earnings')} />

        {open.length > 0 && (
          <Card padding="md">
            <Text variant="bodyMed">Waiting to be paid</Text>
            {open.map((w) => (
              <SummaryRow key={w.id} label={w.kind === 'principal' ? 'Naira withdrawal' : 'Earnings'}
                value={w.kind === 'principal' ? formatNaira(w.fiat) : `${formatLiquidityAmount(w.units)} ${sym}`} />
            ))}
            <Text variant="caption" color="textMuted">Paid to your wallet by the TSA team, usually within a day.</Text>
          </Card>
        )}
      </ScrollView>

      <Sheet visible={sheet != null} onClose={() => !busy && setSheet(null)} title={sheet ? TITLE[sheet] : ''}
        footer={<Button title={busy ?? (pendingHash ? 'Record transfer' : sheet === 'deposit' ? 'Send' : 'Request')} variant="primary"
          loading={!!busy} disabled={!!busy || (!pendingHash && !amount) || !!sheetError} onPress={submit} />}>
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.xs }}>
          <AmountInput value={amount} onChangeText={setAmount} symbol={sheet === 'principal' ? '₦' : sym}
            symbolPosition={sheet === 'principal' ? 'prefix' : 'suffix'}
            onMax={sheet === 'principal' ? () => setAmount(pos.principal) : sheet === 'earnings' ? () => setAmount(pos.earningsAvailable) : undefined} />
          {sheetError ? <Text variant="caption" color="danger">{sheetError}</Text> : null}
          <View style={{ marginTop: spacing.xs }}>
            {sheet === 'deposit' && (
              <>
                <SummaryRow label="Rate" value={pos.currentRate ? `${formatNaira(pos.currentRate)} / $1` : '—'} />
                <SummaryRow label="You will be owed" value={pos.currentRate && num ? formatNaira(String(Number(amount) * Number(pos.currentRate))) : '—'}
                  hint="Locked when we confirm your transfer." />
                <SummaryRow label="Minimum" value={`${pos.minDeposit} ${sym}`} />
              </>
            )}
            {sheet === 'principal' && <SummaryRow label="Available" value={formatNaira(pos.principal)} hint="Paid in USDT at the day's rate." />}
            {sheet === 'earnings' && <SummaryRow label="Available" value={`${formatLiquidityAmount(pos.earningsAvailable)} ${sym}`} hint={`Minimum ${pos.minEarnings} ${sym}.`} />}
          </View>
        </View>
      </Sheet>
    </SafeAreaView>
  );
}
