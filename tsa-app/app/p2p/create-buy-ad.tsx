// app/p2p/create-buy-ad.tsx
//
// Merchant posts a buy-ad: they want to BUY crypto from users and will pay
// fiat. Different capital model from create-ad.tsx:
//
//   - No pool deposit required — the ad is a binding intent to buy.
//   - No EIP-712 voucher to sign — just create and it goes active.
//   - The `amount` field specifies the total crypto the merchant wants to buy.
//
// Flow:
//   1. Fill form (token, fiatCurrency, rate, limits, payment methods, amount)
//   2. POST /p2p/ads  { ...fields, side: 'buy', sellerAddress: merchantWallet }
//   3. Navigate back to marketplace (buy tab)
//
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
// Visual/UX only — the merchant-role gate, field validation, payment-method
// selection, wallet resolution, and the create-buy-ad submit flow are
// unchanged.

import { router } from 'expo-router';
import React, { useEffect, useMemo, useState } from 'react';
import { Alert, KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  paymentMethodsApi,
  parseTokenAmount,
  type P2PPaymentMethod,
} from '../../services/p2p';
import { getActiveWallet } from '../../services/wallet';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import { useCurrency } from '../../contexts/CurrencyContext';
import { useUserRole } from '../../hooks/useUserRole';
import CurrencySelector from '../../components/currency/CurrencySelector';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  Chip,
  EmptyState,
  Icon,
  Input,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

const SUPPORTED_TOKENS = [
  { symbol: 'USDT', decimals: 6 },
  { symbol: 'USDC', decimals: 6 },
  { symbol: 'MCGP', decimals: 18 },
] as const;

const CreateBuyAdScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { isMerchant, loading: roleLoading } = useUserRole();
  const { currency: marketCurrency } = useP2pCurrency();
  const { supportedCurrencies } = useCurrency();

  const [adCurrencyCode, setAdCurrencyCode] = useState<string | null>(null);
  const currencyCode = (adCurrencyCode ?? marketCurrency.code).toUpperCase();
  const currencyConfig =
    supportedCurrencies.find((c) => c.code === currencyCode) ?? marketCurrency;
  const symbol = currencyConfig.symbol;

  const [token, setToken] = useState<(typeof SUPPORTED_TOKENS)[number]>(SUPPORTED_TOKENS[0]);
  const [amountInput, setAmountInput] = useState(''); // human decimal — total to buy
  const [rateInput, setRateInput] = useState('');
  const [minLimitInput, setMinLimitInput] = useState('');
  const [maxLimitInput, setMaxLimitInput] = useState('');
  const [termsInput, setTermsInput] = useState('');
  const [allPaymentMethods, setAllPaymentMethods] = useState<P2PPaymentMethod[]>([]);
  const [selectedPmIds, setSelectedPmIds] = useState<string[]>([]);
  const [loadingPms, setLoadingPms] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [submitStage, setSubmitStage] = useState('');

  useEffect(() => {
    (async () => {
      try {
        const res = await paymentMethodsApi.list();
        if (res.success && res.data) setAllPaymentMethods(res.data);
      } catch (err) {
        console.warn('[create-buy-ad] paymentMethodsApi.list failed', err);
      } finally {
        setLoadingPms(false);
      }
    })();
  }, []);

  // Clear PM selection when currency changes.
  useEffect(() => {
    setSelectedPmIds([]);
  }, [currencyCode]);

  // Payment methods for the selected fiat currency.
  const paymentMethods = useMemo(
    () => allPaymentMethods.filter((m) => m.currency?.toUpperCase() === currencyCode),
    [allPaymentMethods, currencyCode],
  );

  const togglePm = (id: string) => {
    setSelectedPmIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  };

  const valid =
    parseFloat(amountInput || '0') > 0 &&
    parseFloat(rateInput || '0') > 0 &&
    selectedPmIds.length > 0;

  const handleSubmit = async () => {
    if (!valid || submitting) return;
    setSubmitting(true);
    try {
      setSubmitStage('Loading wallet…');
      const merchantWallet = await getActiveWallet();
      if (!merchantWallet) {
        throw new Error('No wallet found. Please set up a wallet first.');
      }

      let tokenAmount: string;
      try {
        tokenAmount = parseTokenAmount(amountInput.trim(), token.decimals);
      } catch (e: any) {
        throw new Error(`Invalid amount: ${e?.message}`);
      }

      const minLimit = minLimitInput.trim() || undefined;
      const maxLimit = maxLimitInput.trim() || undefined;
      const terms = termsInput.trim() || undefined;

      setSubmitStage('Publishing buy ad…');
      const createRes = await adsApi.create({
        token: token.symbol,
        tokenAmount,
        fiatCurrency: currencyCode,
        rate: rateInput.trim(),
        rateType: 'fixed',
        minLimit,
        maxLimit,
        paymentMethodIds: selectedPmIds,
        sellerAddress: merchantWallet,
        side: 'buy',
        terms,
      });

      if (!createRes.success || !createRes.data) {
        throw new Error(createRes.message || 'Server rejected the buy ad');
      }

      // Buy-ads don't require voucher signing — navigate directly to my-ads.
      Alert.alert('Buy ad published', 'Your buy ad is now live. Users can respond to it.', [
        {
          text: 'OK',
          onPress: () => router.replace('/p2p/my-ads'),
        },
      ]);
    } catch (err: any) {
      Alert.alert('Could not create buy ad', err?.message || String(err));
    } finally {
      setSubmitting(false);
      setSubmitStage('');
    }
  };

  // ─── Role gate ────────────────────────────────────────────────────────────

  if (roleLoading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ paddingTop: spacing['4xl'] }}>
          <Spinner label="Checking your account…" />
        </View>
      </SafeAreaView>
    );
  }

  if (!isMerchant) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="storefront"
            title="Merchants only"
            subtitle="Buy ads can only be posted by verified merchants. Apply for merchant status to unlock this feature."
            actionLabel="Become a merchant"
            onAction={() => router.push('/merchants/merchant-request')}
          />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="title1" weight="700">
            Post a buy ad
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            Tell users how much crypto you want to buy and at what rate. You pay fiat; the
            user&apos;s crypto is deposited into escrow at trade open.
          </Text>

          {/* ── Token ────────────────────────────────────────────────── */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
            Token to buy
          </Text>
          <View style={{ flexDirection: 'row', gap: spacing.sm }}>
            {SUPPORTED_TOKENS.map((t) => (
              <Chip
                key={t.symbol}
                label={t.symbol}
                selected={token.symbol === t.symbol}
                onPress={() => setToken(t)}
              />
            ))}
          </View>

          {/* ── Currency ─────────────────────────────────────────────── */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
            Fiat currency
          </Text>
          <CurrencySelector
            variant="card"
            selectedCode={currencyCode}
            onSelectCode={setAdCurrencyCode}
          />

          {/* ── Total amount to buy ──────────────────────────────────── */}
          <View style={{ marginTop: spacing.lg }}>
            <AmountInput
              label={`Total ${token.symbol} to buy`}
              placeholder="e.g. 100"
              symbol={token.symbol}
              symbolPosition="suffix"
              value={amountInput}
              onChangeText={setAmountInput}
            />
          </View>

          {/* ── Rate ─────────────────────────────────────────────────── */}
          <View style={{ marginTop: spacing.lg }}>
            <Input
              label={`Rate (${currencyCode} per ${token.symbol})`}
              placeholder="e.g. 1600"
              keyboardType="decimal-pad"
              value={rateInput}
              onChangeText={setRateInput}
              leftAdornment={
                <Text variant="bodyMed" weight="700" color="textSecondary">
                  {symbol}
                </Text>
              }
            />
          </View>

          {/* ── Limits (optional) ────────────────────────────────────── */}
          <View style={{ flexDirection: 'row', gap: spacing.md, marginTop: spacing.lg }}>
            <View style={{ flex: 1 }}>
              <Input
                label={`Min (${symbol})`}
                placeholder="0"
                keyboardType="decimal-pad"
                value={minLimitInput}
                onChangeText={setMinLimitInput}
              />
            </View>
            <View style={{ flex: 1 }}>
              <Input
                label={`Max (${symbol})`}
                placeholder="0"
                keyboardType="decimal-pad"
                value={maxLimitInput}
                onChangeText={setMaxLimitInput}
              />
            </View>
          </View>

          {/* ── Payment methods — where you'll send fiat FROM ────────── */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
            Your payment methods (where you&apos;ll pay from)
          </Text>
          {loadingPms ? (
            <View style={{ paddingVertical: spacing.lg }}>
              <Spinner />
            </View>
          ) : paymentMethods.length === 0 ? (
            <PressableScale onPress={() => router.push('/p2p/payment-methods')}>
              <AccentCard padding="md">
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Icon name="plus" size={20} color={colors.accent} />
                  <Text variant="bodyMed" weight="700" style={{ flex: 1 }}>
                    No {currencyCode} payment methods found. Add one first.
                  </Text>
                  <Icon name="chevronRight" size={18} color={colors.textMuted} />
                </View>
              </AccentCard>
            </PressableScale>
          ) : (
            <View style={{ gap: spacing.sm }}>
              {paymentMethods.map((m) => {
                const selected = selectedPmIds.includes(m.id);
                return (
                  <PressableScale key={m.id} onPress={() => togglePm(m.id)}>
                    <Card
                      padding="md"
                      style={{
                        borderWidth: 1,
                        borderColor: selected ? colors.accent : colors.border,
                        backgroundColor: selected ? colors.accentMuted : colors.surface,
                      }}
                    >
                      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                        <Icon
                          name={selected ? 'checkCircle' : 'closeCircle'}
                          size={22}
                          color={selected ? colors.accent : colors.textMuted}
                        />
                        <View style={{ flex: 1 }}>
                          <Text variant="bodyMed" weight="600">
                            {m.bankName}
                          </Text>
                          <Text variant="caption" color="textSecondary">
                            {m.accountName} · ••••{m.accountNumber.slice(-4)}
                          </Text>
                        </View>
                      </View>
                    </Card>
                  </PressableScale>
                );
              })}
            </View>
          )}

          {/* ── Terms (optional) ─────────────────────────────────────── */}
          <View style={{ marginTop: spacing.lg }}>
            <Input
              label="Terms (optional)"
              placeholder="Any special instructions for sellers…"
              value={termsInput}
              onChangeText={setTermsInput}
              multiline
              maxLength={500}
              counter={500}
              textAlignVertical="top"
              inputStyle={{ minHeight: 88 }}
            />
          </View>

          {/* ── Publish ──────────────────────────────────────────────── */}
          <Button
            title="Publish buy ad"
            leftIcon="check"
            onPress={handleSubmit}
            disabled={!valid || submitting}
            loading={submitting}
            style={{ marginTop: spacing.xl }}
          />
          {submitting && !!submitStage && (
            <View style={{ alignItems: 'center', marginTop: spacing.sm }}>
              <Text variant="caption" color="textSecondary">
                {submitStage}
              </Text>
            </View>
          )}

          <Text variant="caption" color="textMuted" style={{ marginTop: spacing.md, textAlign: 'center' }}>
            Users who respond will deposit {token.symbol} into escrow. You&apos;ll be notified to send
            fiat and mark as paid.
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default CreateBuyAdScreen;
