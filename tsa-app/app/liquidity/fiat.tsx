// app/liquidity/fiat.tsx — fixed-value liquidity (plan 2026-10-20, multi-currency).
// The LP sends USDT to the company desk and is owed a fixed amount of a fiat
// currency, locked at that day's rate: the company carries the FX. The USDT
// still earns the pool share, shown in each currency at today's rate. An exit is
// a request the team pays in USDT at the day's rate.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useFocusEffect, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AmountInput, BottomActionBar, Button, Card, Chip, ErrorState, ScreenHeader, Sheet, SummaryRow, Text, useToast,
} from '@/components/ui';
import {
  FiatEarningsCard, FiatHero, FiatHistory, FiatIntro, FiatPositionCard, FiatSkeleton, FiatWaitingCard,
} from '@/components/liquidity/FiatParts';
import { liquidityApi, type FiatPosition } from '@/services/liquidityApi';
import { sendPaycrestDeposit } from '@/services/paycrestDeposit';
import { getSignableWallet } from '@/services/wallet';
import { parseTokenAmount } from '@/services/p2p';
import { promptKycRefusal } from '@/utils/kycRefusal';
import {
  exceedsAmount, formatCurrency, formatLiquidityAmount, heldPositions, isValidAmount, isZeroAmount, liquidityMessage,
  multiplyDecimal, currencySymbol,
} from '@/utils/liquidityView';

type Mode = 'deposit' | 'principal' | 'earnings';
const TITLE: Record<Mode, string> = { deposit: 'Add USDT', principal: 'Withdraw', earnings: 'Withdraw earnings' };
const RECORD_RETRIES = 10; // ~36 s: the API credits a transfer only once it is ~20 s old
const RECORD_DELAY_MS = 4_000;
const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export default function FiatLiquidityScreen() {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const [pos, setPos] = useState<FiatPosition | null>(null);
  const [failed, setFailed] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [mode, setMode] = useState<Mode | null>(null);
  const [currency, setCurrency] = useState('');
  const [amount, setAmount] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const running = useRef(false);
  // Arriving from Add / Withdraw on the liquidity screen opens that sheet once.
  const { action } = useLocalSearchParams<{ action?: string }>();
  const autoOpened = useRef(false);
  // A transfer that was sent but not yet recorded. Never sent again; only recorded.
  const [pending, setPending] = useState<{ hash: string; currency: string } | null>(null);

  const load = useCallback(async () => {
    const res = await liquidityApi.fiat();
    if (res.success && res.data) { setPos(res.data); setFailed(false); } else setFailed(true);
  }, []);
  useFocusEffect(useCallback(() => { void load(); }, [load]));
  const onRefresh = async () => { setRefreshing(true); await load(); setRefreshing(false); };

  useEffect(() => {
    if (!pos || autoOpened.current || (action !== 'add' && action !== 'withdraw')) return;
    autoOpened.current = true;
    const held = heldPositions(pos);
    if (action === 'add') {
      if (pos.currencies.length === 0) return; // nothing is priced right now: the screen explains
      setCurrency(pos.currencies[0].code);
      setAmount('');
      setMode('deposit');
    } else if (held.length > 0) {
      setCurrency(held[0].currency);
      setAmount('');
      setMode('principal');
    }
  }, [pos, action]);

  const header = <ScreenHeader title="Fixed-value liquidity" fallbackRoute="/liquidity" />;
  if (!pos) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top', 'bottom']}>
        {header}
        {failed
          ? <ErrorState title="Couldn't load this" subtitle="Check your connection and try again." onRetry={load} />
          : <FiatSkeleton />}
      </SafeAreaView>
    );
  }

  const sym = pos.token?.symbol ?? 'USDT';
  const decimals = pos.token?.decimals ?? 18;
  const held = heldPositions(pos);
  const hasEarnings = !isZeroAmount(pos.earningsAvailable);
  const rateOf = (code: string) => pos.currencies.find((c) => c.code === code)?.rate;
  const heldOf = (code: string) => held.find((h) => h.currency === code);

  const choices = mode === 'principal' ? held.map((h) => h.currency) : pos.currencies.map((c) => c.code);
  const rate = rateOf(currency);
  const valid = isValidAmount(amount, mode === 'principal' ? 2 : decimals);
  const owed = mode === 'deposit' && rate && valid ? multiplyDecimal(amount, rate) : null;

  let sheetError: string | null = null;
  if (mode && amount) {
    if (!valid) sheetError = 'Enter a valid amount.';
    else if (mode === 'deposit' && !pending && exceedsAmount(pos.minDeposit, amount)) sheetError = `Add at least ${pos.minDeposit} ${sym}.`;
    else if (mode === 'principal' && exceedsAmount(amount, heldOf(currency)?.principal, 2)) {
      sheetError = `You have ${formatCurrency(heldOf(currency)?.principal, currency, pos.currencies)} locked.`;
    } else if (mode === 'earnings' && exceedsAmount(amount, pos.earningsAvailable, decimals)) sheetError = `You have ${formatLiquidityAmount(pos.earningsAvailable)} ${sym} available.`;
    else if (mode === 'earnings' && exceedsAmount(pos.minEarnings, amount, decimals)) sheetError = `Withdraw at least ${pos.minEarnings} ${sym}.`;
  }

  const openSheet = (m: Mode) => {
    if (m === 'deposit' && pending) setCurrency(pending.currency);
    else if (m === 'principal') setCurrency(held[0]?.currency ?? '');
    else setCurrency(pos.currencies.find((c) => c.code === currency)?.code ?? pos.currencies[0]?.code ?? '');
    if (m !== 'deposit' || !pending) setAmount('');
    setMode(m);
  };

  const begin = (label: string) => { if (running.current) return false; running.current = true; setBusy(label); return true; };
  const end = () => { running.current = false; setBusy(null); };

  // Records a sent transfer: the API proves it on-chain and locks the rate.
  // Slow confirmation, a rate blip or a network error are retried; the USDT is
  // never sent again — the hash is kept so it can be recorded later.
  const record = async (hash: string, cur: string): Promise<boolean> => {
    for (let i = 0; i < RECORD_RETRIES; i++) {
      const res = await liquidityApi.fiatDeposit(hash, cur);
      if (res.success) {
        toast.success('Deposit recorded');
        setPending(null); setMode(null); setAmount('');
        await load();
        return true;
      }
      if (promptKycRefusal(res.code, res.message)) return true;
      const retryable = res.code === 'transfer_unproven' || res.code === 'fiat_unavailable' || !res.status || res.status >= 500;
      if (!retryable) {
        setPending(null);
        Alert.alert("Couldn't record it", `${liquidityMessage(res.message, 'Try again in a moment.')}\n\nYour transfer: ${hash}`);
        return true;
      }
      await sleep(RECORD_DELAY_MS);
    }
    setPending({ hash, currency: cur });
    Alert.alert('Still confirming', `Your USDT was sent but we couldn't record it yet. Tap "Record transfer" in a minute — don't send again.\n\n${hash}`);
    return false;
  };

  const recordPending = async () => {
    if (!pending || !begin('Recording…')) return;
    try { await record(pending.hash, pending.currency); } finally { end(); }
  };

  // Every check that can refuse runs BEFORE the USDT leaves: once it is at the
  // desk, undoing it takes support.
  const deposit = async () => {
    if (pending) { await recordPending(); return; }
    if (!pos.eligible) { Alert.alert('Verify your identity', 'Finish identity verification to add USDT.'); return; }
    if (!rate) { Alert.alert('Rates unavailable', "We can't price your deposit right now. Try again shortly."); return; }
    if (!begin('Sending…')) return;
    let hash = '';
    try {
      const wallet = await getSignableWallet();
      if (!wallet || !pos.chain || !pos.token) { Alert.alert('Wallet needed', 'Open your wallet on this device to continue.'); return; }
      if (!pos.wallet || wallet.toLowerCase() !== pos.wallet.toLowerCase()) {
        Alert.alert('Switch wallet', `Deposits must come from your account wallet (${pos.wallet ?? 'not set'}). Switch to it on this device.`);
        return;
      }
      hash = await sendPaycrestDeposit({
        chain: pos.chain.key, token: sym, tokenAmount: amount.trim(), depositAddress: pos.desk,
        tokenAddress: pos.token.address, amountBaseUnits: parseTokenAmount(amount.trim(), decimals), decimals,
      }, wallet, null);
      setBusy('Recording your deposit…');
      await record(hash, currency);
    } catch (e) {
      if (hash) setPending({ hash, currency });
      Alert.alert(hash ? 'Sent, but not recorded' : "Couldn't send", e instanceof Error ? e.message : 'Try again in a moment.');
    } finally {
      end();
    }
  };

  const withdraw = async (kind: 'principal' | 'earnings') => {
    if (!begin('Requesting…')) return;
    try {
      const res = await liquidityApi.fiatWithdraw(kind === 'principal'
        ? { kind, currency, amount: amount.trim() }
        : { kind, amount: amount.trim() });
      if (res.success) {
        toast.success('Withdrawal requested');
        setMode(null); setAmount('');
        await load();
      } else if (!promptKycRefusal(res.code, res.message)) {
        Alert.alert("Couldn't request it", liquidityMessage(res.message, 'Try again in a moment.'));
      }
    } finally {
      end();
    }
  };

  const submit = () => (mode === 'deposit' ? deposit() : mode ? withdraw(mode) : undefined);
  const noPositions = held.length === 0 && !pending;
  const actionsOff = !!busy || pos.currencies.length === 0;
  const earnCur = held[0]?.currency ?? pos.currencies[0]?.code;
  const earnValue = earnCur ? pos.earningsValue?.[earnCur] : undefined;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {header}
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} />}>
        {pending && (
          <Card padding="md" style={{ gap: spacing.sm }}>
            <Text variant="bodyMed" weight="700" color="warning">Your USDT was sent, but not recorded yet</Text>
            <Text variant="bodySm" color="textSecondary">Don&apos;t send it again. Record the transfer to lock in your amount.</Text>
            <Button title="Record transfer" variant="primary" loading={!!busy} disabled={!!busy} onPress={recordPending} />
          </Card>
        )}

        {noPositions ? (
          <FiatIntro onAdd={() => openSheet('deposit')} disabled={actionsOff} />
        ) : (
          <>
            {held.length > 0 && <FiatHero holdings={held} currencies={pos.currencies} />}
            <FiatWaitingCard pos={pos} sym={sym} decimals={decimals} />
            {held.map((h) => <FiatPositionCard key={h.currency} holding={h} currencies={pos.currencies} sym={sym} />)}
            <FiatEarningsCard pos={pos} sym={sym} busy={!!busy} onWithdraw={() => openSheet('earnings')} />
            <FiatHistory pos={pos} sym={sym} decimals={decimals} />
          </>
        )}
      </ScrollView>

      {!noPositions && (
        <BottomActionBar>
          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Button title="Withdraw" variant="secondary" disabled={actionsOff || held.length === 0} onPress={() => openSheet('principal')} />
            </View>
            <View style={{ flex: 1 }}>
              <Button title="Add USDT" variant="primary" leftIcon="plus" disabled={actionsOff} onPress={() => openSheet('deposit')} />
            </View>
          </View>
        </BottomActionBar>
      )}

      <Sheet visible={mode != null} onClose={() => !busy && setMode(null)} title={mode ? TITLE[mode] : ''} scrollable
        footer={<Button title={busy ?? (pending && mode === 'deposit' ? 'Record transfer' : mode === 'deposit' ? 'Send' : 'Request withdrawal')} variant="primary"
          loading={!!busy} disabled={!!busy || (!pending && !amount) || !!sheetError || (mode !== 'earnings' && !currency)} onPress={submit} />}>
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
          {mode !== 'earnings' && choices.length > 1 && (
            <View style={{ gap: spacing.xs }}>
              <Text variant="bodySm" weight="600" color="textSecondary">{mode === 'deposit' ? 'Lock in' : 'Withdraw from'}</Text>
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
                {choices.map((code) => (
                  <Chip key={code} label={code} selected={code === currency}
                    disabled={!!busy || (!!pending && mode === 'deposit')} onPress={() => { setCurrency(code); if (mode === 'principal') setAmount(''); }} />
                ))}
              </View>
            </View>
          )}
          {mode === 'deposit' && pending && (
            <Text variant="bodySm" color="warning">Your USDT is already sent. Record it below — don&apos;t send again.</Text>
          )}
          <View style={{ gap: spacing.xs }}>
            <AmountInput value={amount} onChangeText={setAmount}
              editable={!(pending && mode === 'deposit')}
              accessibilityLabel={mode === 'principal' ? `Amount in ${currency}` : `Amount in ${sym}`}
              symbol={mode === 'principal' ? currencySymbol(currency, pos.currencies) || currency : sym}
              symbolPosition={mode === 'principal' ? 'prefix' : 'suffix'}
              onMax={mode === 'principal' ? () => setAmount(heldOf(currency)?.principal ?? '')
                : mode === 'earnings' ? () => setAmount(pos.earningsAvailable) : undefined} />
            {sheetError ? <Text variant="caption" color="danger">{sheetError}</Text> : null}
          </View>
          <View>
            {mode === 'deposit' && (
              <>
                <SummaryRow label="Rate today" value={rate ? `${formatCurrency(rate, currency, pos.currencies)} per $1` : '—'} />
                <SummaryRow label="You will be owed" value={owed ? formatCurrency(owed, currency, pos.currencies) : '—'}
                  hint="Locked when we confirm your transfer. Sent USDT cannot be undone." />
                <SummaryRow label="Minimum" value={`${pos.minDeposit} ${sym}`} />
              </>
            )}
            {mode === 'principal' && (
              <>
                <SummaryRow label="Locked" value={formatCurrency(heldOf(currency)?.principal, currency, pos.currencies)} />
                <SummaryRow label="Rate today" value={heldOf(currency)?.currentRate ? `${formatCurrency(heldOf(currency)?.currentRate, currency, pos.currencies)} per $1` : '—'}
                  hint="You are paid in USDT at the day's rate, once the team approves." />
              </>
            )}
            {mode === 'earnings' && (
              <SummaryRow label="Available" value={`${formatLiquidityAmount(pos.earningsAvailable)} ${sym}`}
                hint={`${earnValue && earnCur ? `About ${formatCurrency(earnValue, earnCur, pos.currencies)}. ` : ''}Minimum ${pos.minEarnings} ${sym}. Paid in USDT.`} />
            )}
          </View>
          {!hasEarnings && mode === 'earnings' ? <Text variant="caption" color="textMuted">Nothing to withdraw yet.</Text> : null}
        </View>
      </Sheet>
    </SafeAreaView>
  );
}
