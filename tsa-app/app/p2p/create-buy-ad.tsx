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
  chainsApi,
  paymentMethodsApi,
  parseTokenAmount,
  type P2PChainInfo,
  type P2PPaymentMethod,
} from '../../services/p2p';
import { getActiveWallet } from '../../services/wallet';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import { useCurrency } from '../../contexts/CurrencyContext';
import { useUserRole } from '../../hooks/useUserRole';
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
  EmptyState,
  Icon,
  Input,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

// Static fallback token list — used when the backend's /p2p/chains endpoint
// is unreachable so the screen never crashes with an empty token set. MCGP is
// deliberately absent: wallet MCGP may not be used in P2P (owner decision
// 2026-09-28), and the API refuses new MCGP ads.
const FALLBACK_TOKENS = [
  { symbol: 'USDT', decimals: 6 },
  { symbol: 'USDC', decimals: 6 },
];

type TokenEntry = { symbol: string; decimals: number };

// Sonic chain IDs (mainnet + testnet).
const SONIC_CHAIN_IDS = new Set([146, 14601]);

function isSonicChain(chainId: number): boolean {
  return SONIC_CHAIN_IDS.has(chainId);
}

function sortChains(chains: P2PChainInfo[]): P2PChainInfo[] {
  return [...chains].sort((a, b) => {
    const aS = isSonicChain(a.chainId) ? 0 : 1;
    const bS = isSonicChain(b.chainId) ? 0 : 1;
    if (aS !== bS) return aS - bS;
    return a.chainId - b.chainId;
  });
}

const CreateBuyAdScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const kbHeight = useKeyboardHeight();
  const { isMerchant, loading: roleLoading } = useUserRole();
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
  const [enabledChains, setEnabledChains] = useState<P2PChainInfo[]>([]);
  const [chainsFetched, setChainsFetched] = useState(false);

  const availableTokens: TokenEntry[] = useMemo(() => {
    if (!chainsFetched || enabledChains.length === 0) return FALLBACK_TOKENS;
    const symbols = new Set(enabledChains.flatMap((c) => c.assets));
    return Array.from(symbols).map((sym) => {
      const fallback = FALLBACK_TOKENS.find((t) => t.symbol === sym);
      return fallback ?? { symbol: sym, decimals: 18 };
    });
  }, [enabledChains, chainsFetched]);

  const [token, setToken] = useState<TokenEntry>(FALLBACK_TOKENS[0]);
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
  const chainsForToken: P2PChainInfo[] = useMemo(() => {
    if (enabledChains.length === 0) return [];
    return sortChains(enabledChains.filter((c) => c.assets.includes(token.symbol)));
  }, [enabledChains, token.symbol]);

  const [selectedChainId, setSelectedChainId] = useState<number | undefined>(undefined);

  useEffect(() => {
    if (chainsForToken.length > 0) {
      setSelectedChainId(chainsForToken[0].chainId);
    } else {
      setSelectedChainId(undefined);
    }
  }, [chainsForToken]);

  const showChainChooser = chainsFetched && chainsForToken.length > 1;

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

  // Fetch enabled chains for asset-first, chain-follows selection.
  useEffect(() => {
    (async () => {
      try {
        const res = await chainsApi.list();
        if (res.success && res.data && res.data.length > 0) {
          setEnabledChains(res.data);
        }
      } catch (err) {
        console.warn('[create-buy-ad] chainsApi.list failed, using fallback tokens', err);
      } finally {
        setChainsFetched(true);
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
    (isPm || selectedPmIds.length > 0);

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
        ...(selectedChainId != null ? { chainId: selectedChainId } : {}),
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
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
            {availableTokens.map((t) => (
              <Chip
                key={t.symbol}
                label={t.symbol}
                selected={token.symbol === t.symbol}
                onPress={() => setToken(t)}
              />
            ))}
          </View>

          {/* ── Chain ────────────────────────────────────────────────── */}
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

          {/* Merchant fee disclosure — as the ad poster (merchant) you are
              charged 1% only on a successfully completed trade. Mirrors the
              on-chain FeatureP2PPlatform rate (1%). */}
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
              A 1% platform fee is charged to you (the merchant) only when a trade completes — on
              USDT and USDC alike. It is split instantly into the system fee, cashback to the
              buyer and their direct upline, and Trade Points.
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
