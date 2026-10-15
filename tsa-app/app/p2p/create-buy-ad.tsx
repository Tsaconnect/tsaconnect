// app/p2p/create-buy-ad.tsx
//
// A verified user posts a buy-ad: they want to BUY crypto from users and will pay
// fiat. Different capital model from create-ad.tsx:
//
//   - No pool deposit required — the ad is a binding intent to buy.
//   - No EIP-712 voucher to sign — just create and it goes active.
//   - The `amount` field specifies the total crypto the poster wants to buy.
//
// Flow:
//   1. Fill form (token, fiatCurrency, rate, limits, payment methods, amount)
//   2. POST /p2p/ads  { ...fields, side: 'buy', sellerAddress: merchantWallet }
//   3. Navigate back to marketplace (buy tab)
//
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
// Visual/UX only — field validation, payment-method
// selection, wallet resolution, and the create-buy-ad submit flow are
// unchanged.

import { router } from 'expo-router';
import React, { useEffect, useMemo, useState } from 'react';
import { Alert, KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  chainsApi,
  paymentMethodsApi,
  parseTokenAmount,
  p2pTokenDecimals,
  type P2PChainInfo,
  type P2PPaymentMethod,
} from '../../services/p2p';
import { getActiveWallet } from '../../services/wallet';
import {
  adChainsFor,
  adChainsView,
  adPublishChain,
  adTokenSymbols,
  NO_OPEN_NETWORK_TITLE,
  type ChainsLoad,
} from '../../utils/p2pChains';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import { useCurrency } from '../../contexts/CurrencyContext';
import { feeBpsFor, formatFeePct } from '../../services/p2pFee';
import { p2pErrorMessage, p2pKycCode } from '../../services/p2pErrors';
import { promptKycRefusal } from '../../utils/kycRefusal';
import { useIsPaymentMerchant } from '../../hooks/useIsPaymentMerchant';
import CurrencySelector from '../../components/currency/CurrencySelector';
import { getNetworkDisplayName } from '../../constants/chains';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  Chip,
  ErrorState,
  Icon,
  Input,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

// Token chips shown until /p2p/chains lands (publish waits for it). Symbols
// only: decimals depend on the chain (p2pTokenDecimals). MCGP is deliberately
// absent: wallet MCGP may not be used in P2P (owner decision 2026-09-28), and
// the API refuses new MCGP ads.
const FALLBACK_TOKENS = ['USDT', 'USDC'];

const CreateBuyAdScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const kbHeight = useKeyboardHeight();
  const { currency: marketCurrency } = useP2pCurrency();
  const { supportedCurrencies } = useCurrency();
  // Active PMs pay sellers automatically from their Nomba account, so no manual
  // payment method is selected — the picker is replaced by an info card and the
  // publish requirement is waived (the backend waives it for PM ads too).
  const { isPm } = useIsPaymentMerchant();

  const [adCurrencyCode, setAdCurrencyCode] = useState<string | null>(null);
  const currencyCode = (adCurrencyCode ?? marketCurrency.code).toUpperCase();
  const currencyConfig =
    supportedCurrencies.find((c) => c.code === currencyCode) ?? marketCurrency;
  const symbol = currencyConfig.symbol;

  // ── Chain / token data from the backend ─────────────────────────────────
  // Publish waits for these (adPublishChain): a failed fetch shows Retry,
  // never a chainId-less create that the API would default to Sonic.
  const [enabledChains, setEnabledChains] = useState<P2PChainInfo[]>([]);
  const [chainsLoad, setChainsLoad] = useState<ChainsLoad>('loading');

  // Tokens on the chains open to this caller for new ads.
  const availableTokens: string[] = useMemo(
    () => (chainsLoad === 'ready' ? adTokenSymbols(enabledChains) : FALLBACK_TOKENS),
    [enabledChains, chainsLoad],
  );
  const chainsView = adChainsView(chainsLoad, enabledChains);

  const [tokenSymbol, setTokenSymbol] = useState<string>(FALLBACK_TOKENS[0]);
  const token = useMemo(() => ({ symbol: tokenSymbol }), [tokenSymbol]);
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

  // ── Chain selector state ──────────────────────────────────────────────
  // The open chains offering the token, BSC first.
  const chainsForToken: P2PChainInfo[] = useMemo(
    () => adChainsFor(enabledChains, token.symbol),
    [enabledChains, token.symbol],
  );

  const [selectedChainId, setSelectedChainId] = useState<number | undefined>(undefined);

  useEffect(() => {
    if (chainsForToken.length > 0) {
      setSelectedChainId(chainsForToken[0].chainId);
    } else {
      setSelectedChainId(undefined);
    }
  }, [chainsForToken]);

  const showChainChooser = chainsLoad === 'ready' && chainsForToken.length > 1;

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

  // Fetch enabled chains for asset-first, chain-follows selection (and on Retry).
  const loadChains = React.useCallback(async () => {
    setChainsLoad('loading');
    try {
      const res = await chainsApi.list();
      if (res.success && res.data && res.data.length > 0) {
        setEnabledChains(res.data);
        setChainsLoad('ready');
        return;
      }
    } catch (err) {
      console.warn('[create-buy-ad] chainsApi.list failed', err);
    }
    setChainsLoad('failed');
  }, []);
  useEffect(() => {
    loadChains();
  }, [loadChains]);

  // A token the open chains no longer offer moves to the first one they do.
  useEffect(() => {
    if (availableTokens.length > 0 && !availableTokens.includes(tokenSymbol)) setTokenSymbol(availableTokens[0]);
  }, [availableTokens, tokenSymbol]);

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

  // Chain-aware decimals (BSC stables are 18dp); token.decimals is the
  // chain-blind fallback and would post 1e12 too little on BSC.
  const chainForFee = selectedChainId ?? chainsForToken[0]?.chainId;
  const adDecimals = p2pTokenDecimals(chainForFee, token.symbol);
  const feePct = formatFeePct(feeBpsFor(enabledChains, chainForFee, token.symbol));

  const valid =
    parseFloat(amountInput || '0') > 0 &&
    parseFloat(rateInput || '0') > 0 &&
    (isPm || selectedPmIds.length > 0);

  const handleSubmit = async () => {
    if (!valid || submitting) return;
    setSubmitting(true);
    try {
      // Never create without chainId: the API would default it to Sonic, with
      // this amount scaled by the selected chain's decimals.
      const adChain = adPublishChain(chainsLoad, selectedChainId);
      if (adChain.kind === 'block') throw new Error(adChain.message);

      setSubmitStage('Loading wallet…');
      const merchantWallet = await getActiveWallet();
      if (!merchantWallet) {
        throw new Error('No wallet found. Please set up a wallet first.');
      }

      let tokenAmount: string;
      try {
        tokenAmount = parseTokenAmount(amountInput.trim(), adDecimals);
      } catch (e: any) {
        throw new Error(`Invalid amount: ${e?.message}`);
      }

      if (BigInt(tokenAmount) === 0n) {
        throw new Error('Amount is too small for this token.');
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
        chainId: adChain.chainId,
      });

      if (!createRes.success || !createRes.data) {
        // The server's KYC 403 drives the Verify prompt (a cached status goes stale).
        if (promptKycRefusal(p2pKycCode(createRes), createRes.message, 'p2p')) return;
        // rate_out_of_band carries min/max in data → "Rate must be between X and Y".
        throw new Error(p2pErrorMessage(createRes, 'Server rejected the buy ad'));
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

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
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
          {chainsView === 'none_open' ? (
            <ErrorState
              title={NO_OPEN_NETWORK_TITLE}
              subtitle="New ads are paused on every network for now. Check again in a little while."
              onRetry={loadChains}
              retryLabel="Retry"
            />
          ) : (
            <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
              {availableTokens.map((sym) => (
                <Chip
                  key={sym}
                  label={sym}
                  selected={token.symbol === sym}
                  onPress={() => setTokenSymbol(sym)}
                />
              ))}
            </View>
          )}

          {/* ── Chain ────────────────────────────────────────────────── */}
          {chainsLoad === 'failed' && (
            <ErrorState
              title="Couldn't load networks"
              subtitle="Publishing needs the ad's network. Check your connection and retry."
              onRetry={loadChains}
              retryLabel="Retry"
            />
          )}
          {showChainChooser && (
            <>
              <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
                Network
              </Text>
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
                {chainsForToken.map((c) => (
                  <Chip
                    key={c.chainId}
                    label={getNetworkDisplayName(c.chainId)}
                    selected={selectedChainId === c.chainId}
                    onPress={() => setSelectedChainId(c.chainId)}
                  />
                ))}
              </View>
            </>
          )}

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
            {isPm ? 'Payouts' : "Your payment methods (where you'll pay from)"}
          </Text>
          {isPm ? (
            // PM buy ad: sellers are paid automatically from the merchant's Nomba
            // account when they respond and deposit — no manual method to pick.
            <Card padding="md" style={{ borderWidth: 1, borderColor: colors.accent, backgroundColor: colors.accentMuted }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="checkCircle" size={20} color={colors.accent} />
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="700">Auto-paid from your payout account</Text>
                  <Text variant="caption" color="textSecondary">
                    Sellers are paid out automatically once they respond and deposit — no payment method needed.
                  </Text>
                </View>
              </View>
            </Card>
          ) : loadingPms ? (
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
                        {selected ? (
                          <Icon name="checkCircle" size={22} color={colors.accent} />
                        ) : (
                          // Unselected = empty outline circle (a checkbox affordance),
                          // not a ✕ — an X reads as "remove/error", not "tap to select".
                          <View
                            style={{
                              width: 22,
                              height: 22,
                              borderRadius: 11,
                              borderWidth: 2,
                              borderColor: colors.border,
                            }}
                          />
                        )}
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

          {/* Fee disclosure — the rate comes from the API; the poster is the crypto receiver on a buy ad. */}
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'flex-start',
              gap: spacing.sm,
              marginTop: spacing.lg,
              backgroundColor: colors.infoBg,
              borderRadius: radius.sm,
              padding: spacing.md,
            }}
          >
            <Icon name="info" size={16} color={colors.info} />
            <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
              {feePct
                ? `A ${feePct} fee comes out of the ${token.symbol} you receive when a trade completes.`
                : `A fee comes out of the ${token.symbol} you receive when a trade completes. The exact rate shows on each trade.`}
            </Text>
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
