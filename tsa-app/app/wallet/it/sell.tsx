// app/wallet/it/sell.tsx
//
// Sell MCGP for a payout destination. Debits spendable (credited) MCGP
// on-chain and opens the payout: NGN bank stays the default, no-quote path
// (NGN is IT's source currency); GHS bank and XOF mobile money (v1 catalog)
// route through a live FX quote before confirming. Settled by the company
// once paid out (or auto-payout, when wired).
import React, { useEffect, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { useCurrency } from '@/contexts/CurrencyContext';
import {
  AmountInput,
  Button,
  Card,
  Icon,
  Input,
  PickerSheet,
  SelectRow,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import type { IconName, PickerItem } from '@/components/ui';
import {
  getITBalance,
  getITRate,
  getWithdrawDestinations,
  itSell,
  quoteWithdrawal,
  ITSellOptions,
  WithdrawDestination,
  WithdrawQuote,
} from '@/services/itApi';
import {
  formatReceiveEstimate,
  isDestinationReadyToSubmit,
  isSellBlockedByQuote,
  mcgpAmountToNgn,
  needsQuote,
  pickDefaultDestination,
} from './sell-quote';
// Phase-1 display-currency helpers: show the NGN payout also in the user's
// selected display currency (secondary line) for the NGN destination.
import { ngnToUsd, shouldShowNgnSecondary } from './currency-display';

type Screen = 'form' | 'submitting' | 'done' | 'error';

const QUOTE_DEBOUNCE_MS = 450;

function destinationIcon(rail: string): IconName {
  return rail === 'mobile_money' ? 'mobile' : 'bank';
}

function railLabel(rail: string): string {
  return rail === 'mobile_money' ? 'Mobile money' : 'Bank transfer';
}

function destinationId(d: WithdrawDestination): string {
  return `${d.currency}:${d.rail}:${d.network ?? ''}`;
}

export default function ITSell() {
  const { colors, spacing } = useTheme();
  const { currency: displayCurrency, formatPrice, allRates } = useCurrency();
  const [screen, setScreen] = useState<Screen>('form');
  const [amount, setAmount] = useState('');
  const [error, setError] = useState('');
  const [credited, setCredited] = useState('0'); // wei
  const [sellRateKobo, setSellRateKobo] = useState('0');
  const [receiveLabel, setReceiveLabel] = useState('');
  // Raw NGN payout amount, set only for the NGN destination, so the display-
  // currency secondary line (Phase 1) can render. null for foreign-currency
  // payouts (those already show the destination currency via the quote).
  const [ngnOutValue, setNgnOutValue] = useState<number | null>(null);

  // Destination catalog + selection.
  const [destinations, setDestinations] = useState<WithdrawDestination[]>([]);
  const [destinationsLoading, setDestinationsLoading] = useState(true);
  const [destination, setDestination] = useState<WithdrawDestination | null>(null);
  const [showDestinationPicker, setShowDestinationPicker] = useState(false);

  // Bank-rail fields (optional client-side — server allows the manual-payout
  // fallback when blank, same as the pre-existing legacy NGN behavior).
  const [bankCode, setBankCode] = useState('');
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');

  // Mobile-money-rail fields (required — the backend rejects an incomplete
  // mobile_money destination outright).
  const [mobileCountry, setMobileCountry] = useState('');
  const [mobileMsisdn, setMobileMsisdn] = useState('');

  // Live FX quote for the selected non-NGN destination currency.
  const [quote, setQuote] = useState<WithdrawQuote | null>(null);
  const [quoteLoading, setQuoteLoading] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      const [b, r, d] = await Promise.all([getITBalance(), getITRate(), getWithdrawDestinations()]);
      if (b.success && b.data) setCredited(b.data.premiumCredited);
      if (r.success && r.data) setSellRateKobo(r.data.sellRateKobo);
      if (d.success && d.data) {
        setDestinations(d.data.destinations);
        setDestination(pickDefaultDestination(displayCurrency.code, d.data.destinations));
      }
      setDestinationsLoading(false);
    })();
    // Only run once on mount — the display currency default is a starting
    // point, not something that should yank the user's selection later.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Debounced live-quote fetch. Fires only when the selected destination
  // needs a quote (non-NGN) and there's a positive NGN amount to quote.
  useEffect(() => {
    if (!destination || !needsQuote(destination.currency)) {
      setQuote(null);
      setQuoteLoading(false);
      setQuoteError(null);
      return;
    }
    const ngnAmount = mcgpAmountToNgn(amount, sellRateKobo);
    if (ngnAmount <= 0) {
      setQuote(null);
      setQuoteLoading(false);
      setQuoteError(null);
      return;
    }
    let cancelled = false;
    setQuoteLoading(true);
    setQuoteError(null);
    const timer = setTimeout(async () => {
      const res = await quoteWithdrawal(ngnAmount, destination.currency);
      if (cancelled) return;
      setQuoteLoading(false);
      if (res.success && res.data) {
        setQuote(res.data);
        setQuoteError(null);
      } else {
        setQuote(null);
        setQuoteError(res.message || 'Live rate unavailable — try again shortly.');
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [amount, destination, sellRateKobo]);

  const selectDestination = (d: WithdrawDestination) => {
    setDestination(d);
    setShowDestinationPicker(false);
    // Clear rail-specific fields + any in-flight quote so we don't carry
    // stale details across an incompatible switch (e.g. bank code into a
    // mobile-money destination).
    setBankCode('');
    setAccountNumber('');
    setAccountName('');
    setMobileCountry('');
    setMobileMsisdn('');
    setQuote(null);
    setQuoteError(null);
  };

  const quoteState = destination
    ? { currency: destination.currency, quote, loading: quoteLoading, error: quoteError }
    : { currency: 'NGN', quote: null, loading: false, error: null };
  const showQuoteRow = !!destination && needsQuote(destination.currency);
  const quoteBlocksSubmit = showQuoteRow && isSellBlockedByQuote(quoteState);

  const submit = async () => {
    setError('');
    const amt = Number(amount);
    if (!amt || amt <= 0) {
      setError('Enter an amount');
      return;
    }
    let wei: bigint;
    try {
      wei = ethers.parseUnits(amount, 18);
    } catch {
      setError('Invalid amount');
      return;
    }
    if (wei > BigInt(credited || '0')) {
      setError('More than your spendable (credited) balance');
      return;
    }
    if (!destination) {
      setError('Choose a withdrawal destination');
      return;
    }
    if (!isDestinationReadyToSubmit(destination, { mobileCountry, mobileMsisdn })) {
      setError('Enter the mobile money country and number');
      return;
    }
    if (quoteBlocksSubmit) {
      setError('Waiting for a live rate — try again in a moment');
      return;
    }

    setScreen('submitting');
    const opts: ITSellOptions = {
      destinationCurrency: destination.currency,
      railType: destination.rail === 'mobile_money' ? 'mobile_money' : 'bank',
    };
    if (destination.rail === 'mobile_money') {
      opts.mobileNetwork = destination.network || '';
      opts.mobileCountry = mobileCountry.trim().toUpperCase();
      opts.mobileMsisdn = mobileMsisdn.trim();
    } else {
      if (bankCode.trim()) opts.bankCode = bankCode.trim();
      if (accountNumber.trim()) opts.accountNumber = accountNumber.trim();
      if (accountName.trim()) opts.accountName = accountName.trim();
    }

    const r = await itSell(wei.toString(), opts);
    if (!r.success || !r.data) {
      setError(r.message || 'Sell failed');
      setScreen('error');
      return;
    }
    if (needsQuote(destination.currency) && quote) {
      const amountStr = quote.destAmount.toLocaleString(undefined, { maximumFractionDigits: 2 });
      setReceiveLabel(`≈ ${amountStr} ${quote.destCurrency}`);
      setNgnOutValue(null); // foreign-currency payout: no NGN→display secondary
    } else {
      try {
        const naira = mcgpAmountToNgn(amount, sellRateKobo);
        setReceiveLabel(`₦${naira.toLocaleString(undefined, { maximumFractionDigits: 2 })}`);
        setNgnOutValue(naira);
      } catch {
        setReceiveLabel('');
        setNgnOutValue(null);
      }
    }
    setScreen('done');
  };

  // The payout itself is always real Naira (Nomba settles NGN only) — the
  // "→ ₦X" figure must stay in Naira. This is only a secondary, informational
  // conversion in the user's selected display currency, shown alongside
  // (never instead of) the Naira payout.
  const ngnRate = allRates.NGN?.midRate ?? null;
  const payoutUsd = ngnOutValue != null ? ngnToUsd(ngnOutValue, ngnRate) : null;
  const showPayoutSecondary = payoutUsd != null && shouldShowNgnSecondary(displayCurrency.code);

  if (screen === 'submitting') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Text variant="body" style={{ color: colors.textSecondary }}>Submitting sale…</Text>
      </SafeAreaView>
    );
  }

  if (screen === 'done') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="checkCircle" size={64} color={colors.success} />
        <Text variant="title2" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Sale submitted</Text>
        <Text variant="body" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
          {amount} MCGP{receiveLabel ? ` → ${receiveLabel}` : ''}. Your payout is processing.
        </Text>
        {showPayoutSecondary ? (
          <Text variant="bodySm" style={{ marginTop: spacing.xs, color: colors.textMuted, textAlign: 'center' }}>
            ≈ {formatPrice(payoutUsd as number)}
          </Text>
        ) : null}
        <View style={{ height: spacing.xl }} />
        <Button title="Done" onPress={() => router.replace('/wallet/it')} />
      </SafeAreaView>
    );
  }

  if (screen === 'error') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="closeCircle" size={64} color={colors.danger} />
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Sale failed</Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>{error}</Text>
        <View style={{ height: spacing.xl }} />
        <Button title="Back" variant="secondary" onPress={() => setScreen('form')} />
      </SafeAreaView>
    );
  }

  const destinationItems: PickerItem[] = destinations.map((d) => ({
    id: destinationId(d),
    label: d.label,
    sublabel: `${d.currency} • ${railLabel(d.rail)}`,
    icon: destinationIcon(d.rail),
  }));

  const submitDisabled =
    !amount || !destination || quoteBlocksSubmit || !isDestinationReadyToSubmit(destination, { mobileCountry, mobileMsisdn });

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }} keyboardShouldPersistTaps="handled">
        <Card>
          <SummaryCard>
            <SummaryRow
              label="Spendable (credited)"
              value={`${Number(ethers.formatUnits(credited || '0', 18)).toLocaleString(undefined, { maximumFractionDigits: 4 })} MCGP`}
            />
          </SummaryCard>
        </Card>
        <AmountInput
          label="Amount to sell"
          value={amount}
          onChangeText={setAmount}
          symbol="MCGP"
          symbolPosition="suffix"
          error={error || undefined}
        />

        <SelectRow
          label="Withdraw to"
          placeholder={destinationsLoading ? 'Loading options…' : 'Choose a destination'}
          value={destination?.label}
          leftIcon={destination ? destinationIcon(destination.rail) : 'wallet'}
          disabled={destinationsLoading}
          onPress={() => setShowDestinationPicker(true)}
        />

        {showQuoteRow && (
          <Card>
            <SummaryRow
              label="You'll receive"
              value={
                <Text
                  variant="bodyMed"
                  weight="600"
                  numeric
                  color={quoteError ? 'danger' : (quote?.stale ? 'textSecondary' : 'textPrimary')}
                >
                  {formatReceiveEstimate(quoteState)}
                </Text>
              }
              hint={quote?.stale ? 'This rate may be out of date — refresh before confirming.' : undefined}
            />
          </Card>
        )}

        {destination?.rail === 'bank' && (
          <View style={{ gap: spacing.md }}>
            <Input
              label="Bank code (optional)"
              placeholder="e.g. 058"
              autoCapitalize="none"
              value={bankCode}
              onChangeText={setBankCode}
            />
            <Input
              label="Account number (optional)"
              placeholder="Account number"
              keyboardType="number-pad"
              value={accountNumber}
              onChangeText={setAccountNumber}
            />
            <Input
              label="Account holder name (optional)"
              placeholder="As registered with the bank"
              autoCapitalize="words"
              value={accountName}
              onChangeText={setAccountName}
            />
          </View>
        )}

        {destination?.rail === 'mobile_money' && (
          <View style={{ gap: spacing.md }}>
            <Text variant="bodySm" color="textSecondary">
              Network: {destination.network ? destination.network[0].toUpperCase() + destination.network.slice(1) : '—'}
            </Text>
            <Input
              label="Country (ISO-2)"
              placeholder="e.g. SN"
              autoCapitalize="characters"
              maxLength={2}
              value={mobileCountry}
              onChangeText={(v) => setMobileCountry(v.toUpperCase())}
            />
            <Input
              label="Mobile money number"
              placeholder="Phone number"
              keyboardType="phone-pad"
              value={mobileMsisdn}
              onChangeText={setMobileMsisdn}
            />
          </View>
        )}

        <Button title="Sell" onPress={submit} disabled={submitDisabled} />
      </ScrollView>

      <PickerSheet
        visible={showDestinationPicker}
        onClose={() => setShowDestinationPicker(false)}
        title="Withdraw to"
        items={destinationItems}
        loading={destinationsLoading && destinations.length === 0}
        selectedId={destination ? destinationId(destination) : undefined}
        emptyText="No withdrawal destinations available right now."
        onSelect={(item) => {
          const d = destinations.find((x) => destinationId(x) === item.id);
          if (d) selectDestination(d);
        }}
      />
    </SafeAreaView>
  );
}
