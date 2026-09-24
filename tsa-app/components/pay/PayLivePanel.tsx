// components/pay/PayLivePanel.tsx
//
// The live-payment body of app/pay.tsx: choose what to pay with, see what it
// will cost her, tap Pay — and, for a payment already under way, whatever it is
// asking of her (Continue payment, Use my USDC) or telling her (a failure).
// Presentational — the money moves in the hook it is given (usePayNaira for a
// naira merchant, usePayCrypto for a crypto one), and authorization is the
// hook's AuthorizationModal, rendered by the wrapper.
//
// `showForm` is whether the checkout is payable right now. The form (picker,
// price, Pay) needs it; the prompts about an attempt do not — a payment reopened
// mid-flight has no form, but its shortfall must still be answerable.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Icon, PressableScale, Spinner, Text } from '@/components/ui';
import AssetPicker from '@/components/assets/AssetPicker';
import { SWAPPED_STAGE_LABELS } from '@/components/paycrest/SwappedSellForm';
import type { PayHook } from '@/hooks/usePayLive';
import type { LiFiToken } from '@/services/lifi';
import type { PayCheckout } from '@/services/payApi';
import { KYC_ROUTE, KYC_VERIFICATION_REQUIRED } from '@/utils/kycRefusal';
import { PRICE_STALE_MS, shortfallCopy } from '@/utils/payCheckout';
import { formatKobo } from '@/utils/stableSellQuote';

type Props = {
  checkout: PayCheckout;
  pay: PayHook;
  /** The checkout is payable right now: show the picker, the price and Pay. */
  showForm: boolean;
  /** Limits the picker: networks she can sign on, and the merchant's own token if it can't be paid yet. */
  allow?: (chainId: number, token?: LiFiToken) => boolean;
  /** What the merchant receives, when it is not naira — given the exact amount when the server knew it. */
  receives?: (amountLabel?: string) => string | undefined;
  note?: string;
};

type Pick = { token: LiFiToken; chainId: number };
/** A price lookup for one pick: what she would pay, in words and base units, and when it was looked up. */
type Priced = { pick: Pick; label?: string; fromAmount?: string; receives?: string; error?: string; at: number };

export function PayLivePanel({ checkout, pay, showForm, allow, receives, note }: Props) {
  const { colors, spacing } = useTheme();
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pick, setPick] = useState<Pick | null>(null);
  const [priced, setPriced] = useState<Priced | null>(null);
  const seq = useRef(0);
  const estimateFor = pay.estimate;

  /** Look the price up again. Only the latest lookup counts, so a slow old one cannot overwrite a newer figure. */
  const reprice = useCallback(async (p: Pick) => {
    const mine = ++seq.current;
    try {
      const e = await estimateFor(p.token, p.chainId);
      if (mine === seq.current) setPriced({ pick: p, label: e.pay, fromAmount: e.fromAmount, receives: e.receives, at: Date.now() });
    } catch (e) {
      if (mine === seq.current) setPriced({ pick: p, error: e instanceof Error ? e.message : 'Could not price this.', at: Date.now() });
    }
  }, [estimateFor]);

  // A new pick — or her wallet arriving, which a price needs — is priced.
  useEffect(() => {
    if (pick) void reprice(pick);
    // estimate closes over the checkout and wallet; only the pick and the wallet re-price.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pick, pay.walletAddress]);

  // What she was shown is what she pays: a figure is looked up again when it is
  // 30 s old, for as long as she may still tap Pay.
  const mayPay = showForm && (pay.stage === 'idle' || (pay.stage === 'failed' && pay.retryable !== false));
  useEffect(() => {
    if (!pick || !mayPay) return;
    const t = setInterval(() => { void reprice(pick); }, PRICE_STALE_MS);
    return () => clearInterval(t);
  }, [pick, mayPay, reprice]);

  // A refusal (the price moved past what she agreed to) means the figure on screen is stale: look it up again now.
  useEffect(() => {
    if (pick && showForm && pay.stage === 'failed' && pay.retryable !== false) void reprice(pick);
    // only the flip to failed re-prices.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pay.stage]);

  const onPay = () => {
    if (!pick || !price?.label || !price.fromAmount) return;
    // A stale figure is not paid: look it up, show what it is now, and let her confirm that.
    if (Date.now() - price.at > PRICE_STALE_MS) { void reprice(pick); return; }
    pay.payWith(pick.token, pick.chainId, { pay: price.label, fromAmount: price.fromAmount });
  };

  const price = priced && priced.pick === pick ? priced : null;
  // Payable only with BOTH the words and the base-unit amount the signature will be held to.
  const priceOk = !!price?.label && !!price.fromAmount;
  const pricing = !!pick && !price;
  const running = pay.stage === 'paying';
  const orderId = pay.order?.id ?? checkout.orderId;
  const retryable = pay.retryable !== false;
  const progress = pay.swapStage ? SWAPPED_STAGE_LABELS[pay.swapStage] : undefined;
  const receivesLine = receives?.(price?.receives);

  // Pay is offered only when starting over is safe: not while an attempt is
  // asking for her, waiting on the server, or may be under way ("don't pay again").
  const showPay = showForm && retryable && !orderId && !['shortfall', 'waiting', 'continue'].includes(pay.stage);
  const showContinue = !!pay.continuePayment && (pay.stage === 'continue' || (pay.stage === 'failed' && !!pay.canContinue));

  return (
    <>
      {showForm && receivesLine && (
        <Text variant="bodySm" style={{ color: colors.textSecondary }}>{receivesLine}</Text>
      )}

      {showForm && (
        <PressableScale onPress={() => setPickerOpen(true)} disabled={running || pay.stage === 'waiting'}>
          <Card style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Text variant="caption" style={{ color: colors.textMuted }}>Pay with</Text>
              <Text variant="body" weight="700">{pick ? pick.token.symbol : 'Choose a token'}</Text>
            </View>
            <Icon name="chevronRight" size={20} color={colors.textMuted} />
          </Card>
        </PressableScale>
      )}
      {showForm && note && <Text variant="caption" style={{ color: colors.textMuted }}>{note}</Text>}

      {showForm && pricing && (
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
          <Spinner size="small" />
          <Text variant="caption" style={{ color: colors.textMuted }}>Checking the price…</Text>
        </View>
      )}
      {showForm && priceOk ? <Text variant="body">{`You'll pay about ${price?.label}`}</Text> : null}
      {showForm && price && !priceOk && !price.error && (
        <Text variant="bodySm" style={{ color: colors.warning }}>
          {"We couldn't work out the price for this token. Pick another."}
        </Text>
      )}
      {showForm && price?.error && (
        <Text variant="bodySm" style={{ color: colors.warning }}>{price.error}</Text>
      )}

      {running && (
        <Card style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
          <Spinner size="small" />
          <Text variant="bodySm" weight="600" style={{ flex: 1 }}>{progress ?? 'Starting your payment…'}</Text>
        </Card>
      )}
      {running && (
        <Text variant="caption" style={{ color: colors.textMuted }}>
          Keep the app open. If it closes, come back to this payment and it will pick up where it stopped.
        </Text>
      )}

      {pay.stage === 'continue' && (
        <AccentCard railColor={colors.warning} padding="md" style={{ backgroundColor: colors.warningBg }}>
          <Text variant="bodySm">
            Your payment is under way but needs you to finish it. Nothing more is taken until you confirm.
          </Text>
        </AccentCard>
      )}

      {pay.stage === 'shortfall' && pay.completeWithWallet && (
        <AccentCard railColor={colors.warning} padding="md" style={{ backgroundColor: colors.warningBg, gap: spacing.md }}>
          <Text variant="bodySm">{shortfallCopy(pay.missingUsdc, pay.walletShort)}</Text>
          <Button title="Use my USDC" onPress={() => pay.completeWithWallet?.()} />
        </AccentCard>
      )}

      {pay.stage === 'failed' && (
        <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg, gap: spacing.md }}>
          <Text variant="bodySm" color="danger">{pay.error ?? 'The payment could not be completed.'}</Text>
          {pay.errorCode === KYC_VERIFICATION_REQUIRED && (
            <Button title="Verify identity" onPress={() => router.push(KYC_ROUTE as never)} />
          )}
        </AccentCard>
      )}

      {showContinue && <Button title="Continue payment" size="lg" onPress={() => pay.continuePayment?.()} />}

      {orderId && ['failed', 'waiting', 'continue'].includes(pay.stage) && (
        <Button
          title="Open the payment order"
          variant="ghost"
          onPress={() => router.push(`/wallet/paycrest/order/${orderId}` as never)}
        />
      )}

      {showForm && pay.stage === 'waiting' && (
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
          <Spinner inline />
          <Text variant="bodySm" color="textSecondary">Waiting for confirmation…</Text>
        </View>
      )}

      {showPay && (
        <Button
          title={`Pay ${formatKobo(String(checkout.customerTotal))}`}
          size="lg"
          onPress={onPay}
          loading={running}
          disabled={!pick || !priceOk || running}
        />
      )}

      {showForm && (
        <AssetPicker
          visible={pickerOpen}
          side="from"
          holdingsOnly
          title="Pay with"
          allow={allow}
          onSelect={(token, chainId) => {
            setPick({ token: { ...token, chainId }, chainId });
            setPickerOpen(false);
          }}
          onClose={() => setPickerOpen(false)}
        />
      )}
    </>
  );
}
