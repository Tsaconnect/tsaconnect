// app/wallet/it/sell.tsx
//
// Sell MCGP for Naira — two steps on one route:
//
//   1. Amount.  Asset, then an amount in MCGP *or* NGN. An NGN amount is what
//      the user RECEIVES in their bank after fees (the owner's call, same as
//      the Paycrest sell). The counterpart and the fee breakdown come from
//      GET /api/it/sell/quote — the sell's own server pricing, in either
//      direction — so the app never inverts a fee itself.
//   2. Payout.  Currency / method / country / bank, account-name check, a
//      summary, then PIN → POST /api/it/sell with the agreed MCGP amount.
//
// The sell re-prices at submit. Before the PIN the quote is refreshed; if it
// moved the user is shown what changed and taps Sell again. After submit the
// server's own net is what the done screen shows.
//
// A stale or unavailable rate is a retryable state here ("Rates are refreshing"
// + Retry) that keeps every input — never the old dead-end "Sale failed".
//
// Step 2 → back (header chevron, Android back, the Back button) returns to
// step 1 with the amount intact; only step 1's back leaves the screen.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { usePreventRemove } from '@react-navigation/native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { useCurrency } from '@/contexts/CurrencyContext';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import {
  AmountInput,
  Button,
  Card,
  Divider,
  Icon,
  PickerSheet,
  SegmentedControl,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import {
  getITBalance,
  getITSellQuote,
  itSell,
  IT_AGREEMENT_REQUIRED,
  IT_PRICING_UNAVAILABLE,
  type ApiResult,
  type ITFeeQuote,
  type ITSellQuote,
  getWithdrawDestinations,
} from '@/services/itApi';
import { BankDestination, type BankDestinationValue } from '@/components/it/BankDestination';
import {
  exceedsSpendable,
  formatKobo,
  formatMcgp,
  sellQuoteMoved,
  sellQuoteRequest,
  type SellAmountMode,
  type SellQuoteRequest,
} from '@/utils/sellQuote';
import { ngnToUsd, shouldShowNgnSecondary } from '@/utils/currencyDisplay';
import { RAILS } from '@/constants/rails';

type Step = 'amount' | 'payout' | 'submitting' | 'done' | 'error';

// Payout destinations come from GET /it/withdraw/destinations, never a literal
// list. That endpoint is the same source Sell validates against, so anything
// rendered here is guaranteed to be accepted — a hardcoded option could offer a
// rail the API would reject, and the user would only find out after committing.
const CURRENCY_META: Record<string, { label: string; country: string }> = {
  NGN: { label: 'NGN · Nigerian Naira', country: 'NG' },
  GHS: { label: 'GHS · Ghanaian Cedi', country: 'GH' },
  USD: { label: 'USD · US Dollar', country: 'US' },
  EUR: { label: 'EUR · Euro', country: '' },
  GBP: { label: 'GBP · Pound Sterling', country: 'GB' },
};

function currencyLabelFor(code: string): string {
  return CURRENCY_META[code]?.label ?? code;
}
function countryFor(code: string): string {
  return CURRENCY_META[code]?.country ?? '';
}
const METHOD_OPTIONS: PickerItem[] = [
  { id: 'bank', label: 'Bank transfer', icon: 'bank' },
];

// Stablecoins navigate away to the Paycrest sell screen rather than being
// rendered here, so this form only ever needs to hold the MCGP asset.
const ASSET_OPTIONS: PickerItem[] = [
  { id: 'MCGP', label: 'MCGP', icon: 'wallet' },
  { id: 'USDC', label: 'USDC', icon: 'wallet' },
  { id: 'USDT', label: 'USDT', icon: 'wallet' },
];

const MODE_SEGMENTS: { key: SellAmountMode; label: string }[] = [
  { key: 'mcgp', label: 'MCGP' },
  { key: 'ngn', label: 'NGN' },
];

// Same cadence as the Paycrest screens: long enough not to quote every
// keystroke (an inverse quote can be several live fee lookups).
const QUOTE_DEBOUNCE_MS = 450;

const PRICING_REFRESHING = 'Rates are refreshing — try again in a moment.';

function feeDisplay(line?: { display?: string } | null): string {
  const d = line?.display?.trim();
  return d && d.length > 0 ? d : '—';
}

function quoteParams(req: SellQuoteRequest): { mcgpAmount: string } | { receiveKobo: string } | null {
  if (!req.ok) return null;
  return 'mcgpAmount' in req ? { mcgpAmount: req.mcgpAmount } : { receiveKobo: req.receiveKobo };
}

/** Transient failures — worth a Retry that keeps the inputs. A request that
 *  never reached the server carries no status. */
function isRetryable(r: ApiResult<unknown>): boolean {
  return r.code === IT_PRICING_UNAVAILABLE || r.status === undefined;
}

function retryMessage(r: ApiResult<unknown>): string {
  return r.code === IT_PRICING_UNAVAILABLE
    ? PRICING_REFRESHING
    : "Couldn't reach the server — check your connection and try again.";
}

/** Kobo as the plain decimal the NGN field takes ("50000.00"). */
function koboToInput(kobo: string): string {
  const v = BigInt(kobo);
  return `${(v / 100n).toString()}.${(v % 100n).toString().padStart(2, '0')}`;
}

/** Itemized fee lines from the server quote — the same lines the sell deducts. */
function FeeLines({ fq, grossKobo }: { fq: ITFeeQuote; grossKobo: string }) {
  return (
    <SummaryCard>
      <SummaryRow label="Sale value" value={formatKobo(grossKobo)} />
      <SummaryRow label="Platform fee" value={`₦${feeDisplay(fq.platformFee.inCurrency)}`} />
      <SummaryRow
        label="Provider fee"
        value={`₦${feeDisplay(fq.providerFee.fee)}`}
        hint={fq.providerFee.source === 'fallback' ? 'Estimated' : undefined}
      />
      <SummaryRow label="Tax" value={`₦${feeDisplay(fq.tax.total)}`} />
      <SummaryRow label="Swap fee" value={`₦${feeDisplay(fq.swapFee.fee)}`} />
      <Divider spacing="xs" />
      <SummaryRow label="Total fees" value={`−₦${feeDisplay(fq.totalFees)}`} />
    </SummaryCard>
  );
}

/** Inline "try again" state. Nothing has moved when this shows. */
function RetryNotice({ message, onRetry, busy }: { message: string; onRetry: () => void; busy?: boolean }) {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        backgroundColor: colors.warningBg,
        borderRadius: radius.sm,
        padding: spacing.md,
        gap: spacing.sm,
      }}
    >
      <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.sm }}>
        <Icon name="warning" size={16} color={colors.warning} />
        <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
          {message}
        </Text>
      </View>
      <Button
        title="Retry"
        variant="secondary"
        size="sm"
        leftIcon="refresh"
        fullWidth={false}
        loading={busy}
        onPress={onRetry}
      />
    </View>
  );
}

export default function ITSell() {
  const { colors, spacing, radius } = useTheme();
  const { currency: displayCurrency, formatPrice, allRates } = useCurrency();
  const [step, setStep] = useState<Step>('amount');
  // The picker's selection. MCGP is the only value this screen ever holds;
  // the stablecoins navigate away rather than being rendered here.
  const [asset, setAsset] = useState<'MCGP'>('MCGP');
  const [showAssetPicker, setShowAssetPicker] = useState(false);

  // ── step 1: amount ──
  const [mode, setMode] = useState<SellAmountMode>('mcgp');
  const [amount, setAmount] = useState('');
  const [spendable, setSpendable] = useState<string | null>(null); // wei, bought + credited
  const [balanceError, setBalanceError] = useState(false);
  const [quote, setQuote] = useState<ITSellQuote | null>(null);
  // Which request (amount + unit + payout currency) `quote` answers. Continue
  // requires it to match what is on screen right now, so a quote can never be
  // carried forward for an amount the user has since edited.
  const [quotedKey, setQuotedKey] = useState('');
  const [quoting, setQuoting] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);
  const [quoteRetry, setQuoteRetry] = useState<string | null>(null);
  const [quoteNonce, setQuoteNonce] = useState(0);
  const quoteSeq = useRef(0);

  // ── step 2: payout ──
  const [dest, setDest] = useState<BankDestinationValue>({
    bankCode: '',
    bankName: '',
    accountNumber: '',
    accountName: '',
    ready: false,
  });
  const [payoutCurrency, setPayoutCurrency] = useState('NGN');
  const [transferMethod, setTransferMethod] = useState<'bank' | 'mobile_money'>('bank');
  const [country, setCountry] = useState('NG');
  const [showCurrencyPicker, setShowCurrencyPicker] = useState(false);
  const [currencyOptions, setCurrencyOptions] = useState<PickerItem[]>([
    { id: 'NGN', label: currencyLabelFor('NGN'), icon: 'wallet' },
  ]);
  const [showMethodPicker, setShowMethodPicker] = useState(false);

  // The quote the user agreed to on Continue — frozen, so a late step-1
  // response can never change what step 2 submits. Refreshed (and the change
  // surfaced) before the PIN.
  const [agreed, setAgreed] = useState<ITSellQuote | null>(null);
  const [agreedRequest, setAgreedRequest] = useState<SellQuoteRequest | null>(null);
  const [movedNote, setMovedNote] = useState<string | null>(null);
  const [payoutError, setPayoutError] = useState('');
  const [payoutRetry, setPayoutRetry] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [showAuth, setShowAuth] = useState(false);
  // Bumped whenever step 2 is left or re-entered, so a pre-PIN re-quote that
  // lands afterwards is dropped instead of opening the PIN prompt later or
  // replacing the sale the user has since agreed to.
  const sellCheckSeq = useRef(0);

  // ── outcome ──
  const [error, setError] = useState('');
  const [receiveLabel, setReceiveLabel] = useState('');
  const [finalNote, setFinalNote] = useState<string | null>(null);
  const [ngnOutValue, setNgnOutValue] = useState<number | null>(null);

  // BankDestination's onChange must be referentially stable (see its own doc).
  const handleDestChange = useCallback((next: BankDestinationValue) => {
    setDest(next);
    setPayoutError('');
  }, []);

  // Back from step 2 (header chevron, Android back button, swipe) is a step
  // back, not a screen pop — the amount the user worked out stays put.
  const backToAmount = useCallback(() => {
    sellCheckSeq.current++;
    setChecking(false);
    setShowAuth(false);
    setStep('amount');
  }, []);
  usePreventRemove(step === 'payout', backToAmount);

  const loadBalance = useCallback(async () => {
    setBalanceError(false);
    const b = await getITBalance();
    // Spendable MCGP is spendable everywhere: IT sell draws on credited
    // first, then USDC-bought, exactly like stores and bills.
    if (b.success && b.data) {
      setSpendable((BigInt(b.data.premiumBought || '0') + BigInt(b.data.premiumCredited || '0')).toString());
    } else {
      setBalanceError(true);
    }
  }, []);

  useEffect(() => {
    loadBalance();
  }, [loadBalance]);

  // Payout destinations, straight from the catalog Sell validates against.
  // On failure we keep the NGN-only default rather than showing nothing: a
  // Nigerian user can still cash out, which is the overwhelmingly common case.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const res = await getWithdrawDestinations();
      if (cancelled || !res.success || !res.data?.destinations?.length) return;
      const banksOnly = res.data.destinations.filter((d) => d.rail === 'bank');
      if (banksOnly.length === 0) return;
      setCurrencyOptions(
        banksOnly.map((d) => ({
          id: d.currency,
          label: currencyLabelFor(d.currency),
          icon: 'wallet' as const,
        }))
      );
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const request = useMemo(() => sellQuoteRequest(mode, amount), [mode, amount]);
  const requestKey = `${JSON.stringify(quoteParams(request))}|${payoutCurrency}`;

  // Live quote while the user types. Only the latest request may land.
  useEffect(() => {
    const params = quoteParams(request);
    if (!params) {
      quoteSeq.current++;
      setQuote(null);
      setQuoting(false);
      setQuoteRetry(null);
      setQuoteError(request.ok ? null : request.error);
      return;
    }
    const seq = ++quoteSeq.current;
    setQuoting(true);
    const t = setTimeout(async () => {
      const r = await getITSellQuote(params, payoutCurrency);
      if (seq !== quoteSeq.current) return;
      setQuoting(false);
      if (r.success && r.data) {
        setQuote(r.data);
        setQuotedKey(`${JSON.stringify(params)}|${payoutCurrency}`);
        setQuoteError(null);
        setQuoteRetry(null);
        return;
      }
      setQuote(null);
      if (isRetryable(r)) {
        setQuoteRetry(retryMessage(r));
        setQuoteError(null);
      } else {
        setQuoteRetry(null);
        setQuoteError(r.message || 'Could not price this amount');
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [request, payoutCurrency, quoteNonce]);

  const overBalance = !!quote && (spendable == null || exceedsSpendable(quote.mcgpAmount, spendable));
  const quoteMatchesMode = !!quote && (quote.mode === 'receive') === (mode === 'ngn');
  const canContinue =
    !!quote && quoteMatchesMode && quotedKey === requestKey && !quoting && !overBalance && request.ok;

  const currencyLabel =
    currencyOptions.find((o) => o.id === payoutCurrency)?.label ?? currencyLabelFor(payoutCurrency);
  const methodLabel =
    METHOD_OPTIONS.find((o) => o.id === transferMethod)?.label ?? transferMethod;
  const countryLabel = country || '—';

  const onAssetChange = (next: string) => {
    if (next === 'USDC' || next === 'USDT') {
      // NGN stablecoin trades route through Paycrest, not Flutterwave: ~0.24-1.1%
      // against 2.15%, and ~73s against a next-morning settlement window. The
      // buy-stable / sell-stable screens are deliberately left in place but
      // unlinked, so reverting is a one-line change if Paycrest misbehaves.
      router.push(`/wallet/paycrest/sell?asset=${next}` as never);
      return;
    }
    setAsset('MCGP');
  };

  // Switching unit carries the priced counterpart across, so the user sees
  // the same sale expressed the other way rather than an empty field.
  const onModeChange = (next: SellAmountMode) => {
    if (next === mode) return;
    if (quote && quoteMatchesMode) {
      setAmount(next === 'ngn' ? koboToInput(quote.netKobo) : ethers.formatUnits(quote.mcgpAmount, 18));
    } else {
      setAmount('');
    }
    setMode(next);
  };

  const onContinue = () => {
    if (!canContinue || !quote) return;
    sellCheckSeq.current++;
    setShowAuth(false);
    setAgreed(quote);
    setAgreedRequest(request);
    setMovedNote(null);
    setPayoutError('');
    setPayoutRetry(null);
    setStep('payout');
  };

  // Refresh the agreed quote right before the PIN. The sell re-prices at
  // submit; a move is shown for a second look, never applied silently.
  const onSell = async () => {
    const params = agreedRequest ? quoteParams(agreedRequest) : null;
    if (!agreed || !params) return;
    if (!dest.ready) {
      setPayoutError('Select a bank and enter a valid account number');
      return;
    }
    setPayoutError('');
    setPayoutRetry(null);
    setChecking(true);
    const seq = ++sellCheckSeq.current;
    const r = await getITSellQuote(params, payoutCurrency);
    if (seq !== sellCheckSeq.current) return;
    setChecking(false);
    if (!r.success || !r.data) {
      if (isRetryable(r)) setPayoutRetry(retryMessage(r));
      else setPayoutError(r.message || 'Could not price this sale');
      return;
    }
    const fresh = r.data;
    if (spendable == null || exceedsSpendable(fresh.mcgpAmount, spendable)) {
      setAgreed(fresh);
      setPayoutError('More than your spendable balance at the current rate — go back and lower the amount.');
      return;
    }
    const moved = sellQuoteMoved(agreed, fresh);
    setAgreed(fresh);
    if (moved) {
      setMovedNote(`${moved} Check the summary and tap Sell again.`);
      return;
    }
    setMovedNote(null);
    setShowAuth(true);
  };

  const submitSell = async () => {
    setShowAuth(false);
    if (!agreed) return;
    const quoted = agreed;
    setError('');
    setStep('submitting');

    const r = await itSell(quoted.mcgpAmount, {
      destinationCurrency: payoutCurrency,
      railType: transferMethod,
      bankCode: dest.bankCode.trim(),
      accountNumber: dest.accountNumber.trim(),
      accountName: dest.accountName.trim(),
    });
    if (r.code === IT_AGREEMENT_REQUIRED) {
      // Reached here without accepting (deep link / stale state) — the hub owns
      // the agreement gate; send them there.
      router.replace('/wallet/it');
      return;
    }
    if (!r.success && r.code === IT_PRICING_UNAVAILABLE) {
      // Refused before any debit — nothing moved. Back to the payout step with
      // everything intact and a Retry.
      setPayoutRetry(PRICING_REFRESHING);
      setStep('payout');
      return;
    }
    if (!r.success || !r.data) {
      setError(r.message || 'Sell failed');
      setStep('error');
      return;
    }

    // The payout disburses the NET the backend computed at submit and echoed
    // on the intent — the true figure. Fall back to the agreed quote only for
    // an intent that carries none.
    const intentNet = r.data.intent?.netNgnKobo ?? '';
    const netKobo = /^\d+$/.test(intentNet) && intentNet !== '0' ? intentNet : quoted.netKobo;
    setReceiveLabel(formatKobo(netKobo));
    setNgnOutValue(Number(netKobo) / 100);
    setFinalNote(
      netKobo !== quoted.netKobo
        ? `The rate moved as your sale went through: you were quoted ${formatKobo(quoted.netKobo)}.`
        : null,
    );
    setStep('done');
  };

  const ngnRate = allRates.NGN?.midRate ?? null;
  const payoutUsd = ngnOutValue != null ? ngnToUsd(ngnOutValue, ngnRate) : null;
  const showPayoutSecondary = payoutUsd != null && shouldShowNgnSecondary(displayCurrency.code);

  if (step === 'submitting') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner size="large" />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary }}>Submitting sale…</Text>
      </SafeAreaView>
    );
  }

  if (step === 'done') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="checkCircle" size={64} color={colors.success} />
        <Text variant="title2" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Sale submitted</Text>
        <Text variant="body" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
          {agreed ? `${formatMcgp(agreed.mcgpAmount)} MCGP` : 'MCGP'}
          {receiveLabel ? ` → ${receiveLabel}` : ''}. Your payout is processing.
        </Text>
        {finalNote ? (
          <Text variant="bodySm" style={{ marginTop: spacing.xs, color: colors.textMuted, textAlign: 'center' }}>
            {finalNote}
          </Text>
        ) : null}
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

  if (step === 'error') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="closeCircle" size={64} color={colors.danger} />
        <Text variant="title3" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Sale failed</Text>
        <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>{error}</Text>
        <View style={{ height: spacing.xl }} />
        <Button title="Back" variant="secondary" onPress={() => setStep('payout')} />
      </SafeAreaView>
    );
  }

  if (step === 'payout' && agreed) {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }} keyboardShouldPersistTaps="handled">
          <Text variant="overline" color="textMuted">STEP 2 OF 2 · PAYOUT DETAILS</Text>

          <SelectRow
            label="Payout currency"
            value={currencyLabel}
            leftIcon="wallet"
            onPress={() => setShowCurrencyPicker(true)}
          />
          <SelectRow
            label="Method of transfer"
            value={methodLabel}
            leftIcon="bank"
            onPress={() => setShowMethodPicker(true)}
          />
          {/* Country is DERIVED from the payout currency rather than chosen.
              Two independent pickers can disagree, and a currency/country
              mismatch is only discovered by the provider at payout — after the
              on-chain debit. Read-only keeps them impossible to desync. */}
          <SummaryRow label="Country / region" value={countryLabel} />

          <BankDestination currency={payoutCurrency} value={dest} onChange={handleDestChange} />

          <Card>
            <SummaryCard>
              <SummaryRow label="You sell" value={`${formatMcgp(agreed.mcgpAmount)} MCGP`} />
              <SummaryRow label="Fees" value={`−${formatKobo(agreed.feeKobo)}`} />
              <SummaryRow label="You receive" value={formatKobo(agreed.netKobo)} total />
              <Divider spacing="xs" />
              <SummaryRow
                label="To account"
                value={
                  dest.ready
                    ? `${dest.bankName || dest.bankCode} · ${dest.accountNumber}\n${dest.accountName}`
                    : '—'
                }
              />
            </SummaryCard>
          </Card>

          {movedNote ? (
            <View style={{ backgroundColor: colors.warningBg, borderRadius: radius.sm, padding: spacing.md }}>
              <Text variant="bodySm" color="textSecondary">{movedNote}</Text>
            </View>
          ) : null}
          {payoutRetry ? <RetryNotice message={payoutRetry} onRetry={onSell} busy={checking} /> : null}
          {payoutError ? (
            <Text variant="bodySm" style={{ color: colors.danger }}>{payoutError}</Text>
          ) : null}

          <Button
            title="Sell"
            onPress={onSell}
            loading={checking}
            disabled={!dest.ready || checking}
          />
          <Button title="Back" variant="ghost" leftIcon="chevronLeft" onPress={backToAmount} />
        </ScrollView>

        <PickerSheet
          visible={showCurrencyPicker}
          onClose={() => setShowCurrencyPicker(false)}
          title="Payout currency"
          items={currencyOptions}
          selectedId={payoutCurrency}
          onSelect={(item) => {
            setPayoutCurrency(item.id);
            setCountry(countryFor(item.id));
            setShowCurrencyPicker(false);
          }}
        />
        <PickerSheet
          visible={showMethodPicker}
          onClose={() => setShowMethodPicker(false)}
          title="Method of transfer"
          items={METHOD_OPTIONS}
          selectedId={transferMethod}
          onSelect={(item) => {
            setTransferMethod(item.id as 'bank' | 'mobile_money');
            setShowMethodPicker(false);
          }}
        />
        <AuthorizationModal
          visible={showAuth}
          title="Authorize sell"
          description={`Sell ${formatMcgp(agreed.mcgpAmount)} MCGP for ${formatKobo(agreed.netKobo)} to ${dest.accountName} (${dest.accountNumber})`}
          onAuthorized={submitSell}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // step 1: amount
  const spendableLabel =
    spendable == null ? '—' : `${Number(ethers.formatUnits(spendable, 18)).toLocaleString(undefined, { maximumFractionDigits: 4 })} MCGP`;
  const grossNgn = quote ? Number(quote.grossKobo) / 100 : 0;
  const grossUsd = grossNgn > 0 ? ngnToUsd(grossNgn, allRates.NGN?.midRate ?? null) : null;

  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }} keyboardShouldPersistTaps="handled">
        <Text variant="overline" color="textMuted">STEP 1 OF 2 · AMOUNT</Text>

        <Card>
          <SummaryCard>
            <SummaryRow label="Spendable MCGP" value={spendableLabel} />
          </SummaryCard>
        </Card>
        {balanceError ? (
          <RetryNotice message="Couldn't load your balance." onRetry={loadBalance} />
        ) : null}

        <SelectRow
          label="Asset"
          value={asset}
          leftIcon="wallet"
          onPress={() => setShowAssetPicker(true)}
        />

        <View style={{ gap: spacing.xs }}>
          <Text variant="bodySm" color="textSecondary">Enter amount in</Text>
          <SegmentedControl segments={MODE_SEGMENTS} value={mode} onChange={onModeChange} />
        </View>

        <AmountInput
          label={mode === 'ngn' ? 'Amount to receive in your bank' : 'Amount of MCGP to sell'}
          value={amount}
          onChangeText={setAmount}
          symbol={mode === 'ngn' ? '₦' : 'MCGP'}
          symbolPosition={mode === 'ngn' ? 'prefix' : 'suffix'}
          onMax={
            mode === 'mcgp' && spendable != null && spendable !== '0'
              ? () => setAmount(ethers.formatUnits(spendable, 18))
              : undefined
          }
        />
        {mode === 'ngn' ? (
          <Text variant="caption" color="textMuted">
            The naira you want to land in your bank, after fees. We work out the MCGP.
          </Text>
        ) : null}

        {quoting && !quote ? <Spinner inline label="Pricing…" /> : null}
        {quoteRetry ? (
          <RetryNotice message={quoteRetry} onRetry={() => setQuoteNonce((n) => n + 1)} busy={quoting} />
        ) : null}
        {quoteError ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>{quoteError}</Text>
        ) : null}

        {quote && quoteMatchesMode ? (
          <Card>
            <SummaryCard>
              {mode === 'ngn' ? (
                <SummaryRow label="You sell" value={`${formatMcgp(quote.mcgpAmount)} MCGP`} total />
              ) : null}
              <SummaryRow label="You receive" value={formatKobo(quote.netKobo)} total />
            </SummaryCard>
            <Divider spacing="xs" />
            <FeeLines fq={quote.feeQuote} grossKobo={quote.grossKobo} />
            {shouldShowNgnSecondary(displayCurrency.code) && grossUsd != null ? (
              <Text variant="caption" color="textMuted">Sale value ≈ {formatPrice(grossUsd)}</Text>
            ) : null}
            {quoting ? <Spinner inline label="Updating…" /> : null}
          </Card>
        ) : null}

        {overBalance ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {spendable == null
              ? 'Your balance has not loaded yet.'
              : `More than your spendable balance (${spendableLabel}).`}
          </Text>
        ) : null}

        <Button title="Continue" onPress={onContinue} disabled={!canContinue} />

        <View
          style={{
            flexDirection: 'row',
            alignItems: 'flex-start',
            gap: spacing.sm,
            backgroundColor: colors.warningBg,
            borderRadius: radius.sm,
            padding: spacing.md,
          }}
        >
          <Icon name="warning" size={16} color={colors.warning} />
          <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
            Note: {RAILS.it.name} ({RAILS.it.acronym}) payouts are not always received instantly —
            payout limits from our partner or settlement differences can delay them.
          </Text>
        </View>
      </ScrollView>

      <PickerSheet
        visible={showAssetPicker}
        onClose={() => setShowAssetPicker(false)}
        title="Asset"
        items={ASSET_OPTIONS}
        selectedId={asset}
        onSelect={(item) => {
          setShowAssetPicker(false);
          onAssetChange(item.id);
        }}
      />
    </SafeAreaView>
  );
}
