// components/pay/PayLivePanel.tsx
//
// The live-payment body of app/pay.tsx: choose what to pay with, see what it
// will cost her, tap Pay. Presentational — the money moves in the hook it is
// given (usePayNaira for a naira merchant, usePayCrypto for a crypto one), and
// authorization is the hook's AuthorizationModal, rendered by the screen.
import React, { useEffect, useRef, useState } from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Icon, PressableScale, Spinner, Text } from '@/components/ui';
import AssetPicker from '@/components/assets/AssetPicker';
import { SWAPPED_STAGE_LABELS } from '@/components/paycrest/SwappedSellForm';
import type { SellStage } from '@/hooks/useSwappedSell';
import type { LiFiToken } from '@/services/lifi';
import type { PaycrestOrder } from '@/services/paycrestApi';
import type { PayCheckout } from '@/services/payApi';
import { KYC_ROUTE, KYC_VERIFICATION_REQUIRED } from '@/utils/kycRefusal';
import { shortfallCopy } from '@/utils/payCheckout';
import { formatKobo } from '@/utils/stableSellQuote';

/** What the panel needs of either live-payment hook. */
export type PayHook = {
  stage: 'idle' | 'paying' | 'shortfall' | 'waiting' | 'failed';
  error?: string;
  errorCode?: string;
  swapStage?: SellStage;
  order?: PaycrestOrder;
  missingUsdc?: string;
  payWith(token: LiFiToken, chainId: number): void;
  estimate(token: LiFiToken, chainId: number): Promise<string>;
  completeWithWallet?(): Promise<void>;
};

type Props = {
  checkout: PayCheckout;
  pay: PayHook;
  /** Limits the picker, e.g. to networks she can sign on. */
  allow?: (chainId: number) => boolean;
  /** What the merchant receives, when it is not naira: "The store receives 33.32 USDT on BSC". */
  receives?: string;
  note?: string;
};

type Pick = { token: LiFiToken; chainId: number };
type Estimate = { pick: Pick; label?: string; error?: string };

export function PayLivePanel({ checkout, pay, allow, receives, note }: Props) {
  const { colors, spacing } = useTheme();
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pick, setPick] = useState<Pick | null>(null);
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const seq = useRef(0);
  const estimateFor = pay.estimate;

  useEffect(() => {
    if (!pick) return;
    const mine = ++seq.current;
    estimateFor(pick.token, pick.chainId)
      .then((label) => { if (mine === seq.current) setEstimate({ pick, label }); })
      .catch((e) => {
        if (mine === seq.current) setEstimate({ pick, error: e instanceof Error ? e.message : 'Could not price this.' });
      });
    // estimate closes over the checkout and wallet; only a new pick re-prices.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pick]);

  const priced = estimate && estimate.pick === pick ? estimate : null;
  const pricing = !!pick && !priced;
  const running = pay.stage === 'paying';
  const busy = running || pay.stage === 'waiting';

  const progress = pay.swapStage ? SWAPPED_STAGE_LABELS[pay.swapStage] : undefined;

  return (
    <>
      {receives && (
        <Text variant="bodySm" style={{ color: colors.textSecondary }}>{receives}</Text>
      )}

      <PressableScale onPress={() => setPickerOpen(true)} disabled={busy}>
        <Card style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
          <View style={{ flex: 1 }}>
            <Text variant="caption" style={{ color: colors.textMuted }}>Pay with</Text>
            <Text variant="body" weight="700">{pick ? pick.token.symbol : 'Choose a token'}</Text>
          </View>
          <Icon name="chevronRight" size={20} color={colors.textMuted} />
        </Card>
      </PressableScale>
      {note && <Text variant="caption" style={{ color: colors.textMuted }}>{note}</Text>}

      {pricing && (
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
          <Spinner size="small" />
          <Text variant="caption" style={{ color: colors.textMuted }}>Checking the price…</Text>
        </View>
      )}
      {priced?.label && (
        <Text variant="body">{`You'll pay about ${priced.label}`}</Text>
      )}
      {priced?.error && (
        <Text variant="bodySm" style={{ color: colors.warning }}>{priced.error}</Text>
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

      {pay.stage === 'shortfall' && pay.completeWithWallet && (
        <AccentCard railColor={colors.warning} padding="md" style={{ backgroundColor: colors.warningBg, gap: spacing.md }}>
          <Text variant="bodySm">{shortfallCopy(pay.missingUsdc)}</Text>
          <Button title="Use my USDC" onPress={() => { void pay.completeWithWallet?.(); }} />
        </AccentCard>
      )}

      {pay.stage === 'failed' && (
        <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg, gap: spacing.md }}>
          <Text variant="bodySm" color="danger">{pay.error ?? 'The payment could not be completed.'}</Text>
          {pay.errorCode === KYC_VERIFICATION_REQUIRED && (
            <Button title="Verify identity" onPress={() => router.push(KYC_ROUTE as never)} />
          )}
          {pay.order && (
            <Button
              title="Open the payment order"
              variant="ghost"
              onPress={() => router.push(`/wallet/paycrest/order/${pay.order!.id}` as never)}
            />
          )}
        </AccentCard>
      )}

      {pay.stage === 'waiting' && (
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
          <Spinner inline />
          <Text variant="bodySm" color="textSecondary">Waiting for confirmation…</Text>
        </View>
      )}

      {/* A direct payment that failed after its order opened cannot be started again. */}
      {pay.stage !== 'shortfall' && !pay.order && pay.stage !== 'waiting' && (
        <Button
          title={`Pay ${formatKobo(String(checkout.customerTotal))}`}
          size="lg"
          onPress={() => pick && pay.payWith(pick.token, pick.chainId)}
          loading={running}
          disabled={!pick || !priced?.label || running}
        />
      )}

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
    </>
  );
}
