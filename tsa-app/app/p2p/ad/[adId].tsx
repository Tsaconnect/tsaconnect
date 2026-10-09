// app/p2p/ad/[adId].tsx
//
// Ad detail — mode-aware for both sell-ads (user buys crypto, fiat-amount
// entry) and buy-ads (user sells crypto to a merchant, token-amount entry).
// Every on-chain step goes through services/p2pTx.runP2PTx (sponsored gas,
// prepare, sign, broadcast, wait mined, confirm); utils/p2pAdOpen maps the
// outcomes. A lost open confirm is stashed and lands on the trade (R4).
//
// Sell-ad (ad.side === 'sell'):
//   User enters fiat amount → receives token preview (net of the ad's fee)
//   Submit: confirm receive chain → runP2PTx('open_trade'):
//           tradesApi.open({fiatAmount, buyerAddress, paymentMethodId}) → confirmOpen
//
// Buy-ad (ad.side === 'buy'):
//   User enters crypto amount to sell → receives fiat preview
//   Submit: allowance check on the ad's chain → runP2PTx('approve')
//           if short → runP2PTx('open_trade'): tradesApi.open({tokenAmount,
//           sellerAddress, fiatAmount, paymentMethodId}) → confirmOpen
import { router, useLocalSearchParams } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  View,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import {
  adsApi,
  paymentMethodsApi,
  poolApi,
  tradesApi,
  formatFiat,
  formatTokenAmount,
  hasSufficientAllowance,
  maskedAccountLabel,
  p2pTokenDecimals,
  parseTokenAmount,
  previewFiatFromToken,
  exceedsCapacity,
  withinFiatLimits,
  TOKEN_DECIMALS as P2P_TOKEN_DECIMALS,
  type P2PAd,
  type P2PAdPaymentMethod,
  type P2PPaymentMethod,
  type P2PTrade,
} from '../../../services/p2p';
import {
  getNigerianBanks,
  resolveAccountName,
  type NgBank,
} from '../../../services/ngBanks';
import { getActiveWallet } from '../../../services/wallet';
import { runP2PTx, type RunP2PTxResult } from '../../../services/p2pTx';
import { p2pErrorMessage } from '../../../services/p2pErrors';
import { getChainByChainId } from '../../../constants/chains';
import { useCurrency } from '../../../contexts/CurrencyContext';
import SellerTrustBadges from '../../../components/p2p/SellerTrustBadges';
import { promptKycRefusal } from '../../../utils/kycRefusal';
import { confirmAsync } from '@/utils/confirmAsync';
import {
  allowanceReadFollowUp,
  approveFollowUp,
  buyAdFeeNote,
  openFollowUp,
  sellAdReceivePreview,
  waitForAllowance,
  type ApproveFollowUp,
  type OpenPrep,
} from '@/utils/p2pAdOpen';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountInput,
  AmountText,
  Avatar,
  Badge,
  BottomActionBar,
  Button,
  Card,
  Divider,
  Icon,
  Input,
  PickerSheet,
  PressableScale,
  RadioRow,
  SelectRow,
  Spinner,
  Text,
  useToast,
} from '@/components/ui';
import type { PickerItem } from '@/components/ui';
import { PayerInstructionNotice } from '@/components/common/PayerInstructionNotice';
import { payerInstructionOf } from '@/utils/payInto';

// TOKEN_DECIMALS is imported from services/p2p as P2P_TOKEN_DECIMALS — single source of truth.

const AdDetailScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const { adId } = useLocalSearchParams<{ adId: string }>();
  const [ad, setAd] = useState<P2PAd | null>(null);
  const [selectedPmId, setSelectedPmId] = useState<string>('');
  // For sell-ads: user enters fiat amount. For buy-ads: user enters token amount.
  const [amountInput, setAmountInput] = useState('');
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [submitStage, setSubmitStage] = useState('');
  const { supportedCurrencies } = useCurrency();
  const toast = useToast();
  // User's own payment methods — used on buy-ad side (merchant pays fiat HERE).
  const [userPaymentMethods, setUserPaymentMethods] = useState<P2PPaymentMethod[]>([]);
  const [loadingUserPms, setLoadingUserPms] = useState(false);

  // ─── PM payout bank state (buy-ad + isPm only) ───────────────────────────
  // When the user is selling to a PM buy-ad, collect their destination bank so
  // the backend knows where to send the Nomba fiat transfer.
  const [banks, setBanks] = useState<NgBank[]>([]);
  const [banksLoading, setBanksLoading] = useState(false);
  const [showBankPicker, setShowBankPicker] = useState(false);
  const [payoutBank, setPayoutBank] = useState<NgBank | null>(null);
  const [payoutAccountNumber, setPayoutAccountNumber] = useState('');
  const [payoutAccountName, setPayoutAccountName] = useState('');
  const [resolvingPayout, setResolvingPayout] = useState(false);
  const [payoutResolved, setPayoutResolved] = useState(false);
  const [payoutResolveError, setPayoutResolveError] = useState<null | 'not_found' | 'unavailable'>(null);

  // Sell-ad: show the seller's embedded payment-methods (buyer sends fiat here).
  // Buy-ad: show the user's own payment-methods (merchant sends fiat here).
  const isBuyAd = ad?.side === 'buy';

  // The public ad's methods are masked ("******7890", no holder name).
  const paymentMethods: (P2PPaymentMethod | P2PAdPaymentMethod)[] = isBuyAd
    ? userPaymentMethods
    : (ad?.paymentMethods ?? []);

  const load = useCallback(async () => {
    if (!adId) return;
    const adRes = await adsApi.get(String(adId));
    if (adRes.success && adRes.data) {
      setAd(adRes.data);
      const pms = adRes.data.paymentMethods ?? [];
      // Auto-select first if only one option (sell-ad mode).
      if (adRes.data.side !== 'buy' && pms.length === 1) setSelectedPmId(pms[0].id);
    }
    setLoading(false);
  }, [adId]);

  // Load user's own payment methods for buy-ad mode.
  const loadUserPms = useCallback(async () => {
    setLoadingUserPms(true);
    const res = await paymentMethodsApi.list();
    if (res.success && res.data) {
      setUserPaymentMethods(res.data);
      if (res.data.length === 1) setSelectedPmId(res.data[0].id);
    }
    setLoadingUserPms(false);
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Fetch user PMs once the ad loads and is a buy-ad.
  useEffect(() => {
    if (ad?.side === 'buy') {
      loadUserPms();
    }
  }, [ad?.side, loadUserPms]);

  // Load bank list when the ad is a PM buy-ad (deferred until needed).
  const isPmBuyAd = ad?.side === 'buy' && !!ad?.isPm;
  // PM SELL ad: the buyer pays the merchant's Nomba VA (pmPayTo), so there's no
  // manual payment method to pick — the picker is replaced by the VA card and
  // the method requirement is waived.
  const isPmSellAd = ad?.side === 'sell' && !!ad?.isPm;
  // Central mode: pmPayTo is the shared company account and carries the own-name notice.
  const adPayerInstruction = payerInstructionOf(ad?.pmPayTo);
  useEffect(() => {
    if (!isPmBuyAd) return;
    let cancelled = false;
    setBanksLoading(true);
    getNigerianBanks().then((list) => {
      if (!cancelled) {
        setBanks(list);
        setBanksLoading(false);
      }
    });
    return () => { cancelled = true; };
  }, [isPmBuyAd]);

  // Debounced payout account name resolution — triggers when bank is selected
  // AND accountNumber reaches 10 digits. Mirrors the payment-methods/new.tsx flow.
  useEffect(() => {
    if (!isPmBuyAd || !payoutBank || payoutAccountNumber.length < 10) {
      setResolvingPayout(false);
      setPayoutResolved(false);
      setPayoutResolveError(null);
      return;
    }
    let cancelled = false;
    setResolvingPayout(true);
    setPayoutResolved(false);
    setPayoutResolveError(null);
    setPayoutAccountName('');
    const timer = setTimeout(async () => {
      const res = await resolveAccountName(payoutBank.code, payoutAccountNumber);
      if (cancelled) return;
      setResolvingPayout(false);
      if (res.ok) {
        setPayoutAccountName(res.accountName);
        setPayoutResolved(true);
      } else {
        setPayoutResolveError(res.reason);
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [isPmBuyAd, payoutBank, payoutAccountNumber]);

  // FIX 1: Fail closed on unknown token decimals.
  // On the sell/deposit path a wrong decimal count makes parseTokenAmount produce
  // the wrong on-chain amount — approve/deposit mismatch or massive over-deposit.
  // tokenSupported gates the sell flow; buy-side (user receives crypto) is unaffected.
  const tokenSupported = ad ? P2P_TOKEN_DECIMALS[ad.token] !== undefined : false;
  // The sell/deposit path NEVER uses this fallback: it's gated by `tokenSupported`
  // (an unknown token blocks the sell flow entirely), so base-unit math always
  // uses a known decimals value. The 6 fallback is display-only for the sell-ad
  // (buy) path, where no base-unit deposit math runs.
  const tokenDecimals = ad ? p2pTokenDecimals(ad.chainId, ad.token) : 6;

  // For sell-ads: preview token amount the buyer receives.
  const tokenPreview = useMemo(() => {
    if (!ad || isBuyAd) return '0';
    const fiat = parseFloat(amountInput || '0');
    const rate = parseFloat(ad.rate);
    if (!isFinite(fiat) || !isFinite(rate) || rate <= 0) return '0';
    return (fiat / rate).toFixed(Math.min(tokenDecimals, 4));
  }, [ad, amountInput, tokenDecimals, isBuyAd]);

  // For buy-ads: preview fiat amount the seller (user) receives.
  const fiatPreview = useMemo(() => {
    if (!ad || !isBuyAd) return '0.00';
    try {
      const tokenBaseUnits = parseTokenAmount(amountInput || '0', tokenDecimals);
      return previewFiatFromToken(tokenBaseUnits, ad.rate, tokenDecimals);
    } catch {
      return '0.00';
    }
  }, [ad, amountInput, tokenDecimals, isBuyAd]);

  // Sell-ad "You receive": the buyer receives the crypto, so the ad's live
  // fee rate comes out of it (net + fee line; neutral note when unknown).
  const receivePreview = useMemo(
    () => sellAdReceivePreview(tokenPreview, tokenDecimals, ad?.feeBps, ad?.token ?? ''),
    [tokenPreview, tokenDecimals, ad?.feeBps, ad?.token],
  );

  // Sell-ad validation: check fiat amount range.
  const fiatAmountNum = isBuyAd ? 0 : parseFloat(amountInput || '0');
  const minOk = isBuyAd || !ad?.minLimit || fiatAmountNum >= parseFloat(ad.minLimit || '0');
  const maxOk = isBuyAd || !ad?.maxLimit || fiatAmountNum <= parseFloat(ad.maxLimit || '0');

  // PM payout gate: for PM buy-ads the seller must supply a verified bank account.
  const pmPayoutValid = !isPmBuyAd ||
    (!!payoutBank && payoutAccountNumber.length >= 10 && payoutResolved && !!payoutAccountName);

  const valid =
    !!ad &&
    !ad.rateStale &&
    parseFloat(amountInput || '0') > 0 &&
    (isPmSellAd || !!selectedPmId) &&
    // FIX 1: sell flow (buy-ad path) requires a known token.
    (isBuyAd ? tokenSupported : minOk && maxOk) &&
    pmPayoutValid;

  // ─── Sell-ad submit (buy crypto) ─────────────────────────────────────────

  // Returns a human-readable token-standard label for a chainId.
  // BSC (56/97) → "BEP-20"; Sonic (146/14601) → "native (Sonic)"; else chain shortName.
  const receiveStandard = (chainId: number): string => {
    if (chainId === 56 || chainId === 97) return 'BEP-20';
    if (chainId === 146 || chainId === 14601) return 'native (Sonic)';
    const chain = getChainByChainId(chainId);
    return chain?.shortName ?? String(chainId);
  };

  // One landing for both open paths. A lost confirm still lands on the trade:
  // runP2PTx stashed the hash before the confirm, so the trade screen's R4
  // banner can retry it; if it never shows, the trade appears once the server picks it up.
  const finishOpen = (r: RunP2PTxResult<OpenPrep, P2PTrade>) => {
    const next = openFollowUp(r);
    switch (next.kind) {
      case 'none':
        return;
      case 'kyc':
        promptKycRefusal(next.code, next.message, 'p2p');
        return;
      case 'alert':
        Alert.alert(next.title, next.message);
        if (next.reload) load();
        return;
      case 'trade':
        if (next.notice) toast.info(next.notice);
        router.replace(`/p2p/trade/${next.tradeId}`);
    }
  };

  const stopForApproval = (next: ApproveFollowUp) => {
    if (next.kyc) promptKycRefusal(next.kyc.code, next.kyc.message, 'p2p');
    else if (next.alert) Alert.alert(next.alert.title, next.alert.message);
  };

  const openStash = (prep: OpenPrep) => ({
    mode: 'open' as const,
    tradeId: prep.trade.id,
    token: prep.trade.token,
    amount: prep.trade.tokenAmount,
  });

  const handleOpenSellAd = async () => {
    if (!ad || submitting) return;
    setSubmitting(true);
    try {
      setSubmitStage('Loading wallet…');
      const buyerAddress = await getActiveWallet();
      if (!buyerAddress) {
        throw new Error('No wallet found. Please set up a wallet first.');
      }

      // On a sell-ad the user pays fiat and receives crypto — surface the
      // chain + token standard BEFORE signing so they know what arrives.
      const chainName = getChainByChainId(ad.chainId)?.name ?? `chain ${ad.chainId}`;
      const ok = await confirmAsync(
        'Confirm receive chain',
        `You'll receive ${ad.token} as ${receiveStandard(ad.chainId)} on ${chainName}. Make sure your destination wallet/exchange supports it.`,
        'Continue',
      );
      if (!ok) return;

      const r = await runP2PTx<OpenPrep, P2PTrade>({
        chainId: ad.chainId,
        walletAddress: buyerAddress,
        action: 'open_trade',
        adId: ad.id,
        onStage: setSubmitStage,
        prepare: () => tradesApi.open(ad.id, {
          fiatAmount: amountInput.trim(),
          paymentMethodId: selectedPmId,
          buyerAddress,
        }),
        confirm: (txHash, prep) => tradesApi.confirmOpen(prep.trade.id, txHash, buyerAddress),
        stash: openStash,
      });
      finishOpen(r);
    } catch (err: any) {
      // Wallet lookup, signing or broadcast: nothing reached the chain.
      Alert.alert('Could not open trade', p2pErrorMessage(err?.message));
    } finally {
      setSubmitting(false);
      setSubmitStage('');
    }
  };

  // ─── Buy-ad submit (sell crypto — approve → deposit + open) ─────────────

  const handleOpenBuyAd = async () => {
    if (!ad || submitting) return;

    // FIX 1: Fail closed — block immediately if the token isn't in the known
    // decimals map. A wrong decimal count would produce the wrong on-chain
    // base-unit amount (approve/deposit mismatch or massive over-deposit).
    if (!tokenSupported) {
      Alert.alert(
        'Unsupported token',
        `Cannot sell ${ad.token} here — this token is not supported by the app yet.`,
      );
      return;
    }

    setSubmitting(true);
    try {
      setSubmitStage('Loading wallet…');
      const sellerAddress = await getActiveWallet();
      if (!sellerAddress) {
        throw new Error('No wallet found. Please set up a wallet first.');
      }

      // Convert human-readable token amount to base units (chain decimals).
      let tokenBaseUnits: string;
      try {
        tokenBaseUnits = parseTokenAmount(amountInput.trim(), tokenDecimals);
      } catch (e: any) {
        throw new Error(`Invalid token amount: ${e?.message}`);
      }
      if (tokenBaseUnits === '0') {
        throw new Error('Token amount must be greater than zero.');
      }

      // FIX 2: Client-side capacity + fiat-limit validation — bail out
      // before any wallet/approve/broadcast calls on bad inputs.
      if (exceedsCapacity(tokenBaseUnits, ad.remaining)) {
        throw new Error("Amount exceeds the merchant's remaining capacity.");
      }
      if (!withinFiatLimits(fiatPreview, ad.minLimit, ad.maxLimit)) {
        const minNum = ad.minLimit ? parseFloat(ad.minLimit) : undefined;
        const fiatNum = parseFloat(fiatPreview);
        if (minNum !== undefined && isFinite(minNum) && fiatNum < minNum) {
          throw new Error("Below the merchant's minimum.");
        }
        throw new Error("Above the merchant's maximum.");
      }

      // Step 1: approve the escrow if the allowance on THIS chain is short.
      // Exact-amount approve (not unlimited) — intentional: leaves no lingering
      // allowance to the escrow after the trade. Its own gas drip ('approve')
      // comes right before it; the open's ('open_trade') right before that.
      setSubmitStage('Checking allowance…');
      // An unverified seller is refused here (403), before anything is signed.
      const read = allowanceReadFollowUp(await poolApi.allowance(ad.token, sellerAddress, ad.chainId));
      if (!read.proceed) {
        stopForApproval(read);
        return;
      }
      if (!hasSufficientAllowance(read.allowance ?? '0', tokenBaseUnits)) {
        const approval = await runP2PTx({
          chainId: ad.chainId,
          walletAddress: sellerAddress,
          action: 'approve',
          onStage: (s) => setSubmitStage(`Approving · ${s}`),
          prepare: () => poolApi.prepareApprove(ad.token, tokenBaseUnits, sellerAddress, ad.chainId),
          // The backend gas-estimates the open, which reverts until the API's
          // node sees the allowance.
          confirm: () => waitForAllowance(ad.token, sellerAddress, ad.chainId, tokenBaseUnits),
        });
        const next = approveFollowUp(approval);
        if (!next.proceed) {
          stopForApproval(next);
          return;
        }
      }

      // Step 2: Open the trade (openTradeWithDeposit).
      // FIX 3: fiatAmount is a display-only value sent here only to satisfy
      // the required field in OpenTradeInput. For buy-ads the backend treats
      // tokenAmount as authoritative and DERIVES fiat from tokenAmount × rate
      // at trade-open time — this value is ignored server-side for buy-ads.
      const r = await runP2PTx<OpenPrep, P2PTrade>({
        chainId: ad.chainId,
        walletAddress: sellerAddress,
        action: 'open_trade',
        adId: ad.id,
        onStage: setSubmitStage,
        prepare: () => tradesApi.open(ad.id, {
          sellerAddress,
          tokenAmount: tokenBaseUnits,
          fiatAmount: fiatPreview,
          paymentMethodId: selectedPmId,
          // Pass payout bank details for PM buy-ads so the backend knows where
          // to send the fiat transfer. Only set when isPm and resolved.
          ...(isPmBuyAd && payoutResolved
            ? {
                payoutAccountNumber,
                payoutBank: payoutBank!.code,
                payoutAccountName,
              }
            : {}),
        }),
        confirm: (txHash, prep) => tradesApi.confirmOpen(prep.trade.id, txHash, sellerAddress),
        stash: openStash,
      });
      finishOpen(r);
    } catch (err: any) {
      Alert.alert('Could not open trade', p2pErrorMessage(err?.message));
    } finally {
      setSubmitting(false);
      setSubmitStage('');
    }
  };

  const handleOpen = isBuyAd ? handleOpenBuyAd : handleOpenSellAd;

  // Bank picker items for PM payout — mapped to PickerSheet's { id, label } shape.
  // Must be declared before the early return so hooks are called unconditionally.
  const bankPickerItems: PickerItem[] = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name, icon: 'bank' as const })),
    [banks],
  );

  if (loading || !ad) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <Spinner label="Loading ad…" />
        </View>
      </SafeAreaView>
    );
  }

  const fiatSymbol =
    supportedCurrencies.find((c) => c.code === ad.fiatCurrency)?.symbol ?? ad.fiatCurrency;

  const sellerLabel = ad.sellerUsername || `seller-${ad.sellerId.slice(0, 4)}`;

  // For buy-ads the "seller" shown is the merchant posting the buy-ad.
  const counterpartySub = isBuyAd
    ? `Buying ${ad.token} with ${ad.fiatCurrency}`
    : `Selling ${ad.token} for ${ad.fiatCurrency}`;

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
          {/* Seller header */}
          <AccentCard padding="md">
            <PressableScale
              onPress={() => router.push(('/p2p/seller/' + ad.sellerId) as any)}
              style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}
            >
              <Avatar name={sellerLabel} size={42} />
              <View style={{ flex: 1 }}>
                <Text variant="bodyMed" weight="700" numberOfLines={1}>
                  @{sellerLabel}
                </Text>
                <Text variant="caption" color="textMuted">
                  {counterpartySub}
                </Text>
              </View>
              {ad.rateType === 'float' && (
                <Badge
                  label="Float"
                  icon="trendingUp"
                  color={colors.accent}
                  background={colors.accentMuted}
                  size="sm"
                />
              )}
            </PressableScale>
            <SellerTrustBadges stats={ad.sellerStats} />
          </AccentCard>

          {/* Price + meta summary */}
          <Card style={{ marginTop: spacing.md }}>
            <Text variant="overline" color="textMuted">
              PRICE
            </Text>
            <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 6, marginTop: spacing.xs }}>
              <AmountText value={ad.rate} currency={ad.fiatCurrency} variant="title1" />
              <Text variant="bodyMed" color="textMuted">
                / {ad.token}
              </Text>
            </View>

            {ad.rateType === 'float' && (
              <Text variant="caption" color="textSecondary" style={{ marginTop: spacing.sm }}>
                Float rate — recomputes from the live market when you open a trade.
              </Text>
            )}

            {ad.rateStale && (
              <AccentCard
                railColor={colors.danger}
                padding="md"
                style={{ marginTop: spacing.md, backgroundColor: colors.dangerBg }}
              >
                <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
                  <Icon name="warning" size={16} color={colors.danger} />
                  <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                    Live rate unavailable. Trades are temporarily disabled — the ad will
                    reactivate automatically once the feed recovers.
                  </Text>
                </View>
              </AccentCard>
            )}

            <Divider spacing="md" />

            <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
              <View>
                <Text variant="overline" color="textMuted">
                  AVAILABLE
                </Text>
                <Text variant="bodySm" weight="700" numeric style={{ marginTop: 2 }}>
                  {formatTokenAmount(ad.remaining, tokenDecimals, 4)} {ad.token}
                </Text>
              </View>
              {!isBuyAd && ad.minLimit && ad.maxLimit && (
                <View style={{ alignItems: 'flex-end' }}>
                  <Text variant="overline" color="textMuted">
                    ORDER RANGE
                  </Text>
                  <Text variant="bodySm" weight="700" numeric style={{ marginTop: 2 }}>
                    {fiatSymbol}{formatFiat(ad.minLimit, 0)} – {fiatSymbol}{formatFiat(ad.maxLimit, 0)}
                  </Text>
                </View>
              )}
            </View>
          </Card>

          {/* Terms */}
          {ad.terms && (
            <AccentCard padding="md" style={{ marginTop: spacing.md }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginBottom: spacing.xs }}>
                <Icon name="info" size={14} color={colors.accent} />
                <Text variant="overline" color="textSecondary">
                  {isBuyAd ? "MERCHANT'S TERMS" : "SELLER'S TERMS"}
                </Text>
              </View>
              <Text variant="bodySm" color="textSecondary">
                {ad.terms}
              </Text>
            </AccentCard>
          )}

          {/* Amount input — fiat for sell-ads; token for buy-ads */}
          {isBuyAd ? (
            <View style={{ marginTop: spacing.lg, gap: spacing.md }}>
              {/* FIX 1: show an error banner when the sell token isn't supported */}
              {!tokenSupported && (
                <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg }}>
                  <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
                    <Icon name="warning" size={16} color={colors.danger} />
                    <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                      Unsupported token — cannot sell {ad.token} here.
                    </Text>
                  </View>
                </AccentCard>
              )}
              <AmountInput
                label={`You send (${ad.token})`}
                placeholder="0.00"
                symbol={ad.token}
                value={amountInput}
                onChangeText={setAmountInput}
              />
              <View
                style={{
                  flexDirection: 'row',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  paddingHorizontal: spacing.md,
                  paddingVertical: spacing.md,
                  borderRadius: radius.md,
                  borderWidth: 1,
                  borderColor: colors.border,
                  backgroundColor: colors.surfaceAlt,
                }}
              >
                <Text variant="bodySm" color="textSecondary">
                  You receive
                </Text>
                <AmountText value={fiatPreview} currency={ad.fiatCurrency} variant="title3" color="textPrimary" />
              </View>
              <Text variant="caption" color="textMuted">
                {buyAdFeeNote(ad.feeBps)}
              </Text>
            </View>
          ) : (
            <View style={{ marginTop: spacing.lg, gap: spacing.md }}>
              <AmountInput
                label={`You pay (${ad.fiatCurrency})`}
                placeholder="0"
                symbol={fiatSymbol}
                value={amountInput}
                onChangeText={setAmountInput}
                error={
                  !minOk && amountInput
                    ? `Below seller's minimum (${fiatSymbol}${ad.minLimit})`
                    : !maxOk && amountInput
                      ? `Above seller's maximum (${fiatSymbol}${ad.maxLimit})`
                      : undefined
                }
              />
              <View
                style={{
                  flexDirection: 'row',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  paddingHorizontal: spacing.md,
                  paddingVertical: spacing.md,
                  borderRadius: radius.md,
                  borderWidth: 1,
                  borderColor: colors.border,
                  backgroundColor: colors.surfaceAlt,
                }}
              >
                <Text variant="bodySm" color="textSecondary">
                  You receive
                </Text>
                <View style={{ flexDirection: 'row', alignItems: 'baseline', gap: 4 }}>
                  <AmountText value={receivePreview.amount} variant="title3" color="textPrimary" />
                  <Text variant="bodySm" color="textMuted">
                    {ad.token}
                  </Text>
                </View>
              </View>
              <Text variant="caption" color="textMuted">
                {receivePreview.note}
              </Text>
            </View>
          )}

          {/* Payment method selection */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.lg, marginBottom: spacing.sm }}>
            {isBuyAd ? 'Receive fiat via (your account)' : 'Pay via'}
          </Text>
          {isPmSellAd ? (
            // PM sell ad: the buyer pays the merchant's Nomba VA and USDT
            // releases automatically. Show the pay-to instead of a method picker.
            <Card padding="md" style={{ borderWidth: 1, borderColor: colors.accent, backgroundColor: colors.accentMuted, gap: spacing.xs }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="checkCircle" size={20} color={colors.accent} />
                <Text variant="bodyMed" weight="700" style={{ flex: 1 }}>
                  {adPayerInstruction
                    ? 'Auto-settled · pay our collection account'
                    : 'Auto-settled · pay the merchant\u2019s account'}
                </Text>
              </View>
              {ad?.pmPayTo ? (
                <View style={{ marginLeft: spacing.lg + spacing.sm }}>
                  <Text variant="bodyMed" weight="700" numeric>{ad.pmPayTo.accountNumber}</Text>
                  <Text variant="caption" color="textSecondary">
                    {[ad.pmPayTo.bank, ad.pmPayTo.accountName].filter(Boolean).join(' · ')}
                  </Text>
                </View>
              ) : null}
              {adPayerInstruction ? (
                <PayerInstructionNotice instruction={adPayerInstruction} />
              ) : null}
              <Text variant="caption" color="textMuted">
                After you open the trade, send the exact amount to this account. Your {ad?.token ?? 'crypto'} releases automatically once your transfer is confirmed.
              </Text>
            </Card>
          ) : loadingUserPms ? (
            <View style={{ paddingVertical: spacing.md }}>
              <Spinner inline label="Loading payment methods…" />
            </View>
          ) : paymentMethods.length === 0 ? (
            <Text variant="bodySm" color="textMuted" style={{ paddingVertical: spacing.sm }}>
              {isBuyAd
                ? 'You have no payment methods. Add one so the merchant knows where to send fiat.'
                : 'Seller has no payment methods configured on this ad.'}
            </Text>
          ) : (
            <View style={{ gap: spacing.sm }}>
              {paymentMethods.map((m) => (
                <RadioRow
                  key={m.id}
                  label={m.bankName}
                  sublabel={maskedAccountLabel(m)}
                  selected={selectedPmId === m.id}
                  onPress={() => setSelectedPmId(m.id)}
                />
              ))}
            </View>
          )}

          {/* PM payout bank entry — only when the buy-ad is from a PM */}
          {isPmBuyAd && (
            <View style={{ marginTop: spacing.lg, gap: spacing.md }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="bank" size={16} color={colors.accent} />
                <Text variant="bodySm" weight="700" color="textPrimary">
                  Your payout bank account
                </Text>
              </View>

              {banksLoading && banks.length === 0 ? (
                <View style={{ paddingVertical: spacing.md }}>
                  <Spinner inline label="Loading banks…" />
                </View>
              ) : (
                <>
                  <SelectRow
                    label="Bank"
                    placeholder="Choose your bank"
                    value={payoutBank?.name}
                    leftIcon="bank"
                    onPress={() => setShowBankPicker(true)}
                  />

                  <Input
                    label="Account number"
                    keyboardType="number-pad"
                    maxLength={20}
                    placeholder="0123456789"
                    value={payoutAccountNumber}
                    onChangeText={(v) => {
                      const digits = v.replace(/\D/g, '');
                      setPayoutAccountNumber(digits);
                      // Clear resolved state when number changes.
                      if (payoutResolved) {
                        setPayoutResolved(false);
                        setPayoutAccountName('');
                      }
                    }}
                  />

                  <Input
                    label="Account name"
                    placeholder="Verified automatically"
                    value={payoutAccountName}
                    editable={false}
                    error={
                      payoutResolveError === 'not_found'
                        ? "Couldn't verify this account — check the number/bank."
                        : payoutResolveError === 'unavailable'
                          ? "Couldn't verify this account — check the number/bank."
                          : undefined
                    }
                    rightAdornment={
                      resolvingPayout ? (
                        <Spinner size="small" />
                      ) : payoutResolved ? (
                        <Icon name="checkCircle" size={20} color={colors.success} />
                      ) : undefined
                    }
                  />
                </>
              )}

              <AccentCard padding="md">
                <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
                  <Icon name="info" size={14} color={colors.accent} />
                  <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                    {'You\'ll be paid here. The account name must match your own name (KYC) or your trade goes to manual review.'}
                  </Text>
                </View>
              </AccentCard>
            </View>
          )}

          <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.lg }}>
            {isBuyAd
              ? `After opening, the merchant has 30 minutes to send ${ad.fiatCurrency} to your account. Release your ${ad.token} only after confirming receipt.`
              : `After opening, you have 30 minutes to send fiat. The seller releases your ${ad.token} after confirming receipt.`}
          </Text>
        </ScrollView>

        <BottomActionBar>
          {submitting && submitStage ? (
            // The Button's loading state shows only a spinner, so surface the
            // live multi-stage progress text (Signing…, Broadcasting…, etc.)
            // here — same staged feedback the pre-redesign screen gave.
            <Text variant="caption" color="textMuted" style={{ textAlign: 'center', marginBottom: spacing.sm }}>
              {submitStage}
            </Text>
          ) : null}
          <Button
            title="Open trade"
            onPress={handleOpen}
            disabled={!valid || submitting}
            loading={submitting}
            size="lg"
          />
        </BottomActionBar>
      </KeyboardAvoidingView>

      {/* PM payout bank picker — rendered outside KeyboardAvoidingView so the
          Sheet floats above keyboard correctly. Only mounted when isPmBuyAd. */}
      {isPmBuyAd && (
        <PickerSheet
          visible={showBankPicker}
          onClose={() => setShowBankPicker(false)}
          title="Choose your bank"
          items={bankPickerItems}
          loading={banksLoading && banks.length === 0}
          selectedId={payoutBank?.code}
          emptyText="No banks found."
          onSelect={(item) => {
            const b = banks.find((x) => x.code === item.id);
            if (!b) return;
            setPayoutBank(b);
            setShowBankPicker(false);
            // Clear account name when bank changes — must re-resolve.
            setPayoutResolved(false);
            setPayoutAccountName('');
            setPayoutResolveError(null);
          }}
        />
      )}
    </SafeAreaView>
  );
};

export default AdDetailScreen;
