// app/wallet/it/sell.tsx
//
// Sell MCGP for Naira (PM flow shell). Payout currency / method / country are
// locked to NGN bank / Nigeria until multi-currency withdrawal is re-enabled.
// Flow: form → Review (fee quote) → AuthorizationModal PIN → itSell.
// Bank list comes from GET /p2p/banks (Flutterwave-backed when FW is the
// directory provider) via getNigerianBanks — Nigeria only for now.
import React, { useEffect, useState } from 'react';
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
  Input,
  PickerSheet,
  PressableScale,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import {
  getITAgreement,
  getITBalance,
  getITFeeQuote,
  getITRate,
  itSell,
  IT_AGREEMENT_REQUIRED,
  type ITFeeQuote,
} from '@/services/itApi';
import { getNigerianBanks, resolveAccountName, type NgBank } from '@/services/ngBanks';
import { mcgpAmountToNgn } from './sell-quote';
import { ngnToUsd, shouldShowNgnSecondary } from './currency-display';
import { RAILS } from '@/constants/rails';

type Screen = 'form' | 'review' | 'submitting' | 'done' | 'error';

// Product-available options only (more currencies/rails/countries later).
const CURRENCY_OPTIONS: PickerItem[] = [
  { id: 'NGN', label: 'NGN · Nigerian Naira', icon: 'wallet' },
];
const METHOD_OPTIONS: PickerItem[] = [
  { id: 'bank', label: 'Bank transfer', icon: 'bank' },
];
const COUNTRY_OPTIONS: PickerItem[] = [
  { id: 'NG', label: 'Nigeria', icon: 'location' },
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
  const [credited, setCredited] = useState('0'); // wei
  const [sellRateKobo, setSellRateKobo] = useState('0');
  const [receiveLabel, setReceiveLabel] = useState('');
  const [ngnOutValue, setNgnOutValue] = useState<number | null>(null);

  const [banks, setBanks] = useState<NgBank[]>([]);
  const [banksLoading, setBanksLoading] = useState(true);
  const [showBankPicker, setShowBankPicker] = useState(false);
  const [bank, setBank] = useState<NgBank | null>(null);
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');
  // NUBAN name enquiry — validates the account number against the selected
  // bank (GET /p2p/banks/resolve → Flutterwave account-resolve when FW is live).
  const [resolving, setResolving] = useState(false);
  const [resolved, setResolved] = useState(false);
  const [resolveError, setResolveError] = useState<null | 'not_found' | 'unavailable'>(null);

  const [payoutCurrency, setPayoutCurrency] = useState('NGN');
  const [transferMethod, setTransferMethod] = useState('bank');
  const [country, setCountry] = useState('NG');
  const [showCurrencyPicker, setShowCurrencyPicker] = useState(false);
  const [showMethodPicker, setShowMethodPicker] = useState(false);
  const [showCountryPicker, setShowCountryPicker] = useState(false);

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
      if (b.success && b.data) setCredited(b.data.premiumCredited);
      if (r.success && r.data) setSellRateKobo(r.data.sellRateKobo);
    })();
  }, []);

  // The wallet's Withdraw sheet links straight here, so this screen can be the
  // user's first IT surface. The hub owns the trading-agreement gate — bounce
  // an un-agreed user there before they fill in bank details, rather than at
  // submit time. A failed check is not a block: the backend still gates itSell.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const ag = await getITAgreement();
      if (!cancelled && ag.success && ag.data && !ag.data.accepted) {
        router.replace('/wallet/it');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      // Nigeria-only for now (locked country). getNigerianBanks prefers
      // GET /p2p/banks — Flutterwave ListBanks(NG) when FW is the directory.
      const list = await getNigerianBanks();
      if (!cancelled) {
        setBanks(list);
        setBanksLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Debounced account-name lookup. Fires when a bank is selected and the
  // account number is a full 10-digit NUBAN — validates the number and
  // auto-fills the holder name. On provider outage the user can type the name.
  useEffect(() => {
    if (!bank || accountNumber.length !== 10) {
      setResolving(false);
      setResolved(false);
      setResolveError(null);
      return;
    }
    let cancelled = false;
    setResolving(true);
    setResolved(false);
    setResolveError(null);
    const timer = setTimeout(async () => {
      const res = await resolveAccountName(bank.code, accountNumber);
      if (cancelled) return;
      setResolving(false);
      if (res.ok) {
        setAccountName(res.accountName);
        setResolved(true);
        setError('');
      } else {
        setAccountName('');
        setResolveError(res.reason);
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [bank, accountNumber]);

  const bankItems: PickerItem[] = banks.map((b) => ({
    id: b.code,
    label: b.name,
    icon: 'bank' as const,
  }));

  const currencyLabel =
    CURRENCY_OPTIONS.find((o) => o.id === payoutCurrency)?.label ?? payoutCurrency;
  const methodLabel =
    METHOD_OPTIONS.find((o) => o.id === transferMethod)?.label ?? transferMethod;
  const countryLabel =
    COUNTRY_OPTIONS.find((o) => o.id === country)?.label ?? country;

  const ngnPayout = mcgpAmountToNgn(amount, sellRateKobo);
  const bankReady =
    !!bank?.code &&
    accountNumber.trim().length === 10 &&
    accountName.trim().length >= 2 &&
    !resolving &&
    resolveError !== 'not_found';

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
    if (wei > BigInt(credited || '0')) {
      setError('More than your spendable (credited) balance');
      return;
    }
    if (resolveError === 'not_found') {
      setError('Account number does not match the selected bank');
      return;
    }
    if (!bankReady || !bank) {
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
    const snapBankCode = bank.code.trim();
    const snapBankName = bank.name.trim();
    const snapAccountNumber = accountNumber.trim();
    const snapAccountName = accountName.trim();
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
      destinationCurrency: 'NGN',
      railType: 'bank',
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

    // Settlement pays the gross NgnKobo — /api/it/quote is display-only and is
    // not deducted from the payout (yet). Never promise feeQuote.netAmount here.
    setReceiveLabel(
      `₦${reviewNgnGross.toLocaleString(undefined, { maximumFractionDigits: 2 })}`,
    );
    setNgnOutValue(reviewNgnGross);
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
    // Fees are quote-only (not deducted from settlement). Wait for the quote
    // attempt to finish for a complete Review, but never block Transfer on
    // viable/net — payout is the gross NGN amount.
    const transferBlocked = feeLoading;
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Text variant="title3" weight="700" style={{ color: colors.textPrimary }}>
            Review transfer
          </Text>

          <Card>
            <SummaryCard>
              <SummaryRow label="You sell" value={`${reviewAmount} MCGP`} />
              <Divider spacing="xs" />
              <SummaryRow
                label="You receive (NGN)"
                value={`₦${reviewNgnGross.toLocaleString(undefined, { maximumFractionDigits: 2 })}`}
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
              Informational only — not deducted from this payout yet.
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
                <SummaryRow
                  label="Est. net if deducted"
                  value={`₦${feeDisplay(feeQuote.netAmount)}`}
                />
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
              label="Spendable (credited)"
              value={`${Number(ethers.formatUnits(credited || '0', 18)).toLocaleString(undefined, { maximumFractionDigits: 4 })} MCGP`}
            />
          </SummaryCard>
        </Card>

        <Text variant="bodySm" style={{ color: colors.textMuted }}>
          Sell credited MCGP for Naira. More payout currencies will open later.
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
        <SelectRow
          label="Country / region"
          value={countryLabel}
          leftIcon="location"
          onPress={() => setShowCountryPicker(true)}
        />

        <SelectRow
          label="Bank"
          placeholder={banksLoading ? 'Loading banks…' : 'Select bank'}
          value={bank?.name}
          leftIcon="bank"
          disabled={banksLoading && banks.length === 0}
          onPress={() => setShowBankPicker(true)}
        />
        <Input
          label="Account number"
          placeholder="10-digit NUBAN"
          keyboardType="number-pad"
          maxLength={10}
          value={accountNumber}
          onChangeText={(v) => {
            setAccountNumber(v.replace(/\D/g, '').slice(0, 10));
            setError('');
          }}
        />
        <Input
          label="Account holder name"
          placeholder={
            bank && accountNumber.length === 10
              ? 'Looking up account…'
              : 'Auto-fills when account number is valid'
          }
          autoCapitalize="words"
          value={accountName}
          onChangeText={(v) => {
            setAccountName(v);
            if (resolved) setResolved(false);
            setError('');
          }}
          editable={!resolving && !resolved}
          error={
            resolveError === 'not_found'
              ? 'We couldn’t find this account at the selected bank. Check the digits or bank.'
              : undefined
          }
          helper={
            resolveError === 'unavailable'
              ? 'Account verification is offline. You can type the name manually.'
              : resolved
                ? 'Account verified'
                : undefined
          }
          rightAdornment={
            resolving ? (
              <Spinner size="small" />
            ) : resolved ? (
              <PressableScale
                onPress={() => {
                  setResolved(false);
                  setAccountName('');
                }}
                hitSlop={10}
                accessibilityLabel="Clear and edit name"
              >
                <Icon name="checkCircle" size={20} color={colors.success} />
              </PressableScale>
            ) : undefined
          }
        />

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

        <Button title="Review" onPress={goReview} disabled={!amount || !bankReady} />

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
        items={CURRENCY_OPTIONS}
        selectedId={payoutCurrency}
        onSelect={(item) => {
          setPayoutCurrency(item.id);
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
          setTransferMethod(item.id);
          setShowMethodPicker(false);
        }}
      />
      <PickerSheet
        visible={showCountryPicker}
        onClose={() => setShowCountryPicker(false)}
        title="Country / region"
        items={COUNTRY_OPTIONS}
        selectedId={country}
        onSelect={(item) => {
          setCountry(item.id);
          setShowCountryPicker(false);
        }}
      />
      <PickerSheet
        visible={showBankPicker}
        onClose={() => setShowBankPicker(false)}
        title="Select bank"
        items={bankItems}
        loading={banksLoading && banks.length === 0}
        selectedId={bank?.code}
        emptyText="No banks available right now."
        onSelect={(item) => {
          const b = banks.find((x) => x.code === item.id) ?? null;
          setBank(b);
          setShowBankPicker(false);
          setAccountName('');
          setResolved(false);
          setResolveError(null);
          setError('');
        }}
      />
    </SafeAreaView>
  );
}
