// app/wallet/it/sell.tsx
//
// Sell MCGP for Naira (PM flow shell). Payout currency / method / country are
// locked to NGN bank / Nigeria until multi-currency withdrawal is re-enabled.
// Flow: form → Review (fee quote) → AuthorizationModal PIN → itSell.
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
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import {
  getITBalance,
  getITFeeQuote,
  getITRate,
  itSell,
  type ITFeeQuote,
} from '@/services/itApi';
import { mcgpAmountToNgn } from './sell-quote';
import { ngnToUsd, shouldShowNgnSecondary } from './currency-display';

type Screen = 'form' | 'review' | 'submitting' | 'done' | 'error';

function feeDisplay(line?: { display?: string } | null): string {
  const d = line?.display?.trim();
  return d && d.length > 0 ? d : '—';
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
  const [ngnOutValue, setNgnOutValue] = useState<number | null>(null);

  const [bankCode, setBankCode] = useState('');
  const [accountNumber, setAccountNumber] = useState('');
  const [accountName, setAccountName] = useState('');

  // Frozen snapshot for the Review screen — set when leaving the form so a
  // late fee response can't pair with a different amount the user edited.
  const [reviewAmount, setReviewAmount] = useState('');
  const [reviewNgnGross, setReviewNgnGross] = useState(0);
  const [reviewBankCode, setReviewBankCode] = useState('');
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

  const ngnPayout = mcgpAmountToNgn(amount, sellRateKobo);
  const bankReady =
    !!bankCode.trim() && accountNumber.trim().length === 10 && !!accountName.trim();

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
    if (!bankReady) {
      setError('Bank code, 10-digit account number, and account name are required');
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
    const snapBankCode = bankCode.trim();
    const snapAccountNumber = accountNumber.trim();
    const snapAccountName = accountName.trim();
    setReviewAmount(snapAmount);
    setReviewNgnGross(snapNgn);
    setReviewBankCode(snapBankCode);
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
              <SummaryRow label="Payout currency" value="NGN" />
              <SummaryRow label="Method" value="Bank transfer" />
              <SummaryRow label="Country / region" value="Nigeria" />
              <Divider spacing="xs" />
              <SummaryRow label="Bank code" value={reviewBankCode} />
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
          value="NGN · Nigerian Naira"
          leftIcon="wallet"
          disabled
          onPress={() => {}}
        />
        <SelectRow
          label="Method of transfer"
          value="Bank transfer"
          leftIcon="bank"
          disabled
          onPress={() => {}}
        />
        <SelectRow
          label="Country / region"
          value="Nigeria"
          leftIcon="location"
          disabled
          onPress={() => {}}
        />

        <Input
          label="Bank code"
          placeholder="e.g. 058"
          autoCapitalize="none"
          value={bankCode}
          onChangeText={(v) => {
            setBankCode(v);
            setError('');
          }}
        />
        <Input
          label="Account number"
          placeholder="10-digit NUBAN"
          keyboardType="number-pad"
          maxLength={10}
          value={accountNumber}
          onChangeText={(v) => {
            setAccountNumber(v);
            setError('');
          }}
        />
        <Input
          label="Account holder name"
          placeholder="As registered with the bank"
          autoCapitalize="words"
          value={accountName}
          onChangeText={(v) => {
            setAccountName(v);
            setError('');
          }}
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
      </ScrollView>
    </SafeAreaView>
  );
}
