// app/wallet/it/sell.tsx
//
// Sell MCGP for Naira (PM flow shell). Payout currency / method / country are
// locked to NGN bank / Nigeria until multi-currency withdrawal is re-enabled.
// Flow: form → Review (fee quote) → AuthorizationModal PIN → itSell.
// Bank list comes from GET /p2p/banks (Flutterwave-backed when FW is the
// directory provider) via getNigerianBanks — Nigeria only for now.
import React, { useCallback, useEffect, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
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
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import {
  getITBalance,
  getITFeeQuote,
  getITRate,
  itSell,
  IT_AGREEMENT_REQUIRED,
  type ITFeeQuote,
  getWithdrawDestinations,
} from '@/services/itApi';
import { BankDestination, type BankDestinationValue } from '@/components/it/BankDestination';
import { mcgpAmountToNgn } from '@/utils/sellQuote';
import { ngnToUsd, shouldShowNgnSecondary } from '@/utils/currencyDisplay';
import { RAILS } from '@/constants/rails';

type Screen = 'form' | 'review' | 'submitting' | 'done' | 'error';

// Product-available options only (more currencies/rails/countries later).
// Payout destinations come from GET /it/withdraw/destinations, never a literal
// list. That endpoint is the same source Sell validates against, so anything
// rendered here is guaranteed to be accepted — a hardcoded option could offer a
// rail the API would reject, and the user would only find out after committing.
const CURRENCY_META: Record<string, { label: string; country: string; digits: number }> = {
  NGN: { label: 'NGN · Nigerian Naira', country: 'NG', digits: 10 },
  GHS: { label: 'GHS · Ghanaian Cedi', country: 'GH', digits: 13 },
  USD: { label: 'USD · US Dollar', country: 'US', digits: 12 },
  EUR: { label: 'EUR · Euro', country: '', digits: 34 },
  GBP: { label: 'GBP · Pound Sterling', country: 'GB', digits: 8 },
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

function feeDisplay(line?: { display?: string } | null): string {
  const d = line?.display?.trim();
  return d && d.length > 0 ? d : '—';
}

export default function ITSell() {
  const { colors, spacing, radius } = useTheme();
  const { currency: displayCurrency, formatPrice, allRates } = useCurrency();
  const [screen, setScreen] = useState<Screen>('form');
  const [amount, setAmount] = useState('');
  const [error, setError] = useState('');
  const [spendable, setSpendable] = useState('0'); // wei, bought + credited
  const [sellRateKobo, setSellRateKobo] = useState('0');
  const [receiveLabel, setReceiveLabel] = useState('');
  const [ngnOutValue, setNgnOutValue] = useState<number | null>(null);

  const [dest, setDest] = useState<BankDestinationValue>({
    bankCode: '',
    bankName: '',
    accountNumber: '',
    accountName: '',
    ready: false,
  });
  // BankDestination's onChange must be referentially stable (see its own
  // doc) — wrapped instead of passing setDest directly so we can also clear
  // the stale form error on any bank/account edit, matching every other
  // field's onChangeText below.
  const handleDestChange = useCallback((next: BankDestinationValue) => {
    setDest(next);
    setError('');
  }, []);

  const [payoutCurrency, setPayoutCurrency] = useState('NGN');
  const [transferMethod, setTransferMethod] = useState<'bank' | 'mobile_money'>('bank');
  const [country, setCountry] = useState('NG');
  const [showCurrencyPicker, setShowCurrencyPicker] = useState(false);
  const [currencyOptions, setCurrencyOptions] = useState<PickerItem[]>([
    { id: 'NGN', label: currencyLabelFor('NGN'), icon: 'wallet' },
  ]);
  const [showMethodPicker, setShowMethodPicker] = useState(false);

  // Frozen snapshot for the Review screen — set when leaving the form so a
  // late fee response can't pair with a different amount the user edited.
  const [reviewAmount, setReviewAmount] = useState('');
  const [reviewNgnGross, setReviewNgnGross] = useState(0);
  const [reviewBankCode, setReviewBankCode] = useState('');
  const [reviewBankName, setReviewBankName] = useState('');
  const [reviewAccountNumber, setReviewAccountNumber] = useState('');
  const [reviewAccountName, setReviewAccountName] = useState('');

  const [feeQuote, setFeeQuote] = useState<ITFeeQuote | null>(null);
  const [feeLoading, setFeeLoading] = useState(false);
  const [feeError, setFeeError] = useState<string | null>(null);
  const [showAuth, setShowAuth] = useState(false);

  useEffect(() => {
    (async () => {
      const [b, r] = await Promise.all([getITBalance(), getITRate()]);
      // Spendable MCGP is spendable everywhere: IT sell draws on credited
      // first, then USDC-bought, exactly like stores and bills.
      if (b.success && b.data) {
        setSpendable((BigInt(b.data.premiumBought || '0') + BigInt(b.data.premiumCredited || '0')).toString());
      }
      if (r.success && r.data) setSellRateKobo(r.data.sellRateKobo);
    })();
  }, []);

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

  const currencyLabel =
    currencyOptions.find((o) => o.id === payoutCurrency)?.label ?? currencyLabelFor(payoutCurrency);
  const methodLabel =
    METHOD_OPTIONS.find((o) => o.id === transferMethod)?.label ?? transferMethod;
  const countryLabel = country || '—';

  const ngnPayout = mcgpAmountToNgn(amount, sellRateKobo);

  const goReview = async () => {
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
    if (wei > BigInt(spendable || '0')) {
      setError('More than your spendable balance');
      return;
    }
    if (!dest.ready) {
      setError('Select a bank and enter a valid account number');
      return;
    }
    if (ngnPayout <= 0) {
      setError('Could not price this amount — try again shortly');
      return;
    }

    // Freeze form values and move to Review immediately so inputs can't change
    // under a mid-flight fee quote (and double-tap can't start a second fetch
    // with a different amount).
    const snapAmount = amount;
    const snapNgn = ngnPayout;
    const snapBankCode = dest.bankCode.trim();
    const snapBankName = dest.bankName.trim();
    const snapAccountNumber = dest.accountNumber.trim();
    const snapAccountName = dest.accountName.trim();
    setReviewAmount(snapAmount);
    setReviewNgnGross(snapNgn);
    setReviewBankCode(snapBankCode);
    setReviewBankName(snapBankName);
    setReviewAccountNumber(snapAccountNumber);
    setReviewAccountName(snapAccountName);
    setFeeQuote(null);
    setFeeError(null);
    setFeeLoading(true);
    setScreen('review');

    const res = await getITFeeQuote({
      flow: 'withdraw',
      asset: 'MCGP',
      amount: snapNgn.toFixed(2),
      currency: 'NGN',
    });
    setFeeLoading(false);
    if (!res.success || !res.data?.quote) {
      setFeeQuote(null);
      setFeeError(res.message || 'Fee quote unavailable — try again shortly.');
      return;
    }
    setFeeError(null);
    setFeeQuote(res.data.quote);
  };

  const submitSell = async () => {
    setShowAuth(false);
    setError('');
    setScreen('submitting');

    let wei: bigint;
    try {
      wei = ethers.parseUnits(reviewAmount, 18);
    } catch {
      setError('Invalid amount');
      setScreen('error');
      return;
    }

    const r = await itSell(wei.toString(), {
      destinationCurrency: payoutCurrency,
      railType: transferMethod,
      bankCode: reviewBankCode,
      accountNumber: reviewAccountNumber,
      accountName: reviewAccountName,
    });
    if (r.code === IT_AGREEMENT_REQUIRED) {
      // Reached here without accepting (deep link / stale state) — the hub owns
      // the agreement gate; send them there.
      router.replace('/wallet/it');
      return;
    }
    if (!r.success || !r.data) {
      setError(r.message || 'Sell failed');
      setScreen('error');
      return;
    }

    // The payout disburses the NET the backend computed and echoed on the
    // intent; fall back to the local quote, then to gross, so the confirmation
    // never overstates what will land.
    const netKobo = Number(r.data.intent?.netNgnKobo ?? '');
    const net = Number.isFinite(netKobo) && netKobo > 0
      ? netKobo / 100
      : Number(feeQuote?.netAmount?.minor ?? 0) / 100 || reviewNgnGross;
    setReceiveLabel(`₦${net.toLocaleString(undefined, { maximumFractionDigits: 2 })}`);
    setNgnOutValue(net);
    setScreen('done');
  };

  const ngnRate = allRates.NGN?.midRate ?? null;
  const payoutUsd = ngnOutValue != null ? ngnToUsd(ngnOutValue, ngnRate) : null;
  const showPayoutSecondary = payoutUsd != null && shouldShowNgnSecondary(displayCurrency.code);

  if (screen === 'submitting') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner size="large" />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary }}>Submitting sale…</Text>
      </SafeAreaView>
    );
  }

  if (screen === 'done') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        <Icon name="checkCircle" size={64} color={colors.success} />
        <Text variant="title2" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>Sale submitted</Text>
        <Text variant="body" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
          {reviewAmount} MCGP{receiveLabel ? ` → ${receiveLabel}` : ''}. Your payout is processing.
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

  if (screen === 'review') {
    // Fees ARE deducted at settlement (backend quotes the same engine), so the
    // Review must wait for a quote and refuse to continue without one.
    // Fees are deducted at settlement, so a transfer can't be authorized on an
    // unknown quote: the backend refuses to price it and the user would have
    // confirmed an amount nobody could compute.
    const transferBlocked = feeLoading || !feeQuote;
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
            Review transfer
          </Text>

          <Card>
            <SummaryCard>
              <SummaryRow label="You sell" value={`${reviewAmount} MCGP`} />
              <SummaryRow
                label="Sale value"
                value={`₦${reviewNgnGross.toLocaleString(undefined, { maximumFractionDigits: 2 })}`}
              />
              <SummaryRow label="Fees" value={feeQuote ? `−₦${feeDisplay(feeQuote.totalFees)}` : '—'} />
              <Divider spacing="xs" />
              <SummaryRow
                label="You receive (NGN)"
                value={feeQuote ? `₦${feeDisplay(feeQuote.netAmount)}` : '—'}
              />
              <Divider spacing="xs" />
              <SummaryRow label="Payout currency" value={currencyLabel} />
              <SummaryRow label="Method" value={methodLabel} />
              <SummaryRow label="Country / region" value={countryLabel} />
              <Divider spacing="xs" />
              <SummaryRow label="Bank" value={reviewBankName || reviewBankCode} />
              <SummaryRow label="Account number" value={reviewAccountNumber} />
              <SummaryRow label="Account name" value={reviewAccountName} />
            </SummaryCard>
          </Card>

          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary, marginBottom: spacing.xs }}>
              Estimated fees
            </Text>
            <Text variant="bodySm" style={{ color: colors.textMuted, marginBottom: spacing.sm }}>
              Deducted from this payout — your bank receives the amount below.
            </Text>
            {feeLoading ? (
              <Spinner inline label="Loading fee quote…" />
            ) : feeError ? (
              <Text variant="bodySm" style={{ color: colors.textMuted }}>{feeError}</Text>
            ) : feeQuote ? (
              <SummaryCard>
                <SummaryRow label="Platform fee" value={`₦${feeDisplay(feeQuote.platformFee.inCurrency)}`} />
                <SummaryRow
                  label="Provider fee"
                  value={`₦${feeDisplay(feeQuote.providerFee.fee)}`}
                  hint={feeQuote.providerFee.source === 'fallback' ? 'Estimated' : undefined}
                />
                <SummaryRow label="Tax" value={`₦${feeDisplay(feeQuote.tax.total)}`} />
                <SummaryRow label="Swap fee" value={`₦${feeDisplay(feeQuote.swapFee.fee)}`} />
                <Divider spacing="xs" />
                <SummaryRow label="Total fees" value={`₦${feeDisplay(feeQuote.totalFees)}`} />
                <SummaryRow label="You receive" value={`₦${feeDisplay(feeQuote.netAmount)}`} />
              </SummaryCard>
            ) : null}
          </Card>

          <Button
            title="Transfer"
            onPress={() => setShowAuth(true)}
            disabled={transferBlocked}
          />
          <Button
            title="Back"
            variant="ghost"
            onPress={() => {
              setShowAuth(false);
              setFeeLoading(false);
              setFeeError(null);
              setFeeQuote(null);
              setScreen('form');
            }}
          />
        </ScrollView>

        <AuthorizationModal
          visible={showAuth}
          title="Authorize sell"
          description={`Sell ${reviewAmount} MCGP to ${reviewAccountName} (${reviewAccountNumber})`}
          onAuthorized={submitSell}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // form
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }} keyboardShouldPersistTaps="handled">
        <Card>
          <SummaryCard>
            <SummaryRow
              label="Spendable MCGP"
              value={`${Number(ethers.formatUnits(spendable || '0', 18)).toLocaleString(undefined, { maximumFractionDigits: 4 })} MCGP`}
            />
          </SummaryCard>
        </Card>

        <Text variant="bodySm" style={{ color: colors.textMuted }}>
          Sell spendable MCGP for Naira. More payout currencies will open later.
        </Text>

        <AmountInput
          label="Amount of MCGP to sell"
          value={amount}
          onChangeText={(v) => {
            setAmount(v);
            setError('');
          }}
          symbol="MCGP"
          symbolPosition="suffix"
        />

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

        {error ? (
          <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text>
        ) : null}

        {ngnPayout > 0 ? (
          <Text variant="bodySm" style={{ color: colors.textMuted }}>
            Estimated gross ≈ ₦{ngnPayout.toLocaleString(undefined, { maximumFractionDigits: 2 })}
            {shouldShowNgnSecondary(displayCurrency.code) && ngnToUsd(ngnPayout, allRates.NGN?.midRate ?? null) != null
              ? ` · ≈ ${formatPrice(ngnToUsd(ngnPayout, allRates.NGN?.midRate ?? null) as number)}`
              : ''}
          </Text>
        ) : null}

        <Button title="Review" onPress={goReview} disabled={!amount || !dest.ready} />

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
    </SafeAreaView>
  );
}
