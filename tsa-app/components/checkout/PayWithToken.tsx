// components/checkout/PayWithToken.tsx
//
// "Pay with another token" at checkout: the picker row and its price, the
// confirm sheet, and the progress of a run. Presentational — the money moves in
// services/checkoutFundingRun (via hooks/useCheckoutFunding); every figure shown
// is the server's funding quote.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Divider, Icon, PressableScale, Sheet, Spinner, Stepper, Text } from '@/components/ui';
import type { FundingQuote } from '@/services/checkoutFundingApi';
import { canForgetRun, etaLabel, type FundingRunState } from '@/services/checkoutFundingRun';
import type { FundingPick } from '@/hooks/useCheckoutFunding';
import { payAmountLabel } from '@/utils/payCheckout';
import { fundingSteps } from './fundingSteps';

const usd = (n: number) => (n < 0.01 ? '< $0.01' : `$${n.toFixed(2)}`);
const stable = (base: string, symbol: string) => payAmountLabel(base, 6, symbol);

/** The token she pays with and what it costs — under "Pay with" on the review screen. */
export function FundingForm({
  pick,
  onPick,
  quote,
  pricing,
  error,
  disabled,
}: {
  pick: FundingPick | null;
  onPick: () => void;
  quote?: FundingQuote;
  pricing: boolean;
  error?: string;
  disabled?: boolean;
}) {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ gap: spacing.sm, marginTop: spacing.md }}>
      <PressableScale onPress={onPick} disabled={disabled} accessibilityRole="button" accessibilityLabel="Choose the token to pay with">
        <Card style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
          <Icon name="swap" size={18} color={colors.accent} />
          <View style={{ flex: 1 }}>
            <Text variant="caption" color="textMuted">Pay with another token</Text>
            <Text variant="bodyMed" weight="700">{pick ? pick.token.symbol : 'Choose a token you hold'}</Text>
          </View>
          <Icon name="chevronRight" size={18} color={colors.textMuted} />
        </Card>
      </PressableScale>
      {pricing ? <Spinner label="Checking the price…" /> : null}
      {error ? <Text variant="bodySm" color="danger">{error}</Text> : null}
      {quote && pick ? (
        <Text variant="bodySm" color="textSecondary">
          {`You'll send about ${payAmountLabel(quote.fromAmount, pick.token.decimals, pick.token.symbol)} · arrives in ${etaLabel(quote.etaSeconds)}`}
        </Text>
      ) : null}
    </View>
  );
}

/** What she agrees to before signing: the source amount, LiFi's cost, the gas, the ETA, and where the money goes. */
export function FundingConfirmSheet({
  visible,
  onClose,
  onConfirm,
  quote,
  pick,
  stale,
}: {
  visible: boolean;
  onClose: () => void;
  onConfirm: () => void;
  quote: FundingQuote;
  pick: FundingPick;
  stale: boolean;
}) {
  const { spacing } = useTheme();
  const token = quote.token;
  const row = (label: string, value: string, bold?: boolean) => (
    <View style={{ flexDirection: 'row', justifyContent: 'space-between', gap: spacing.md, paddingVertical: spacing.xs }}>
      <Text variant="bodySm" color="textSecondary">{label}</Text>
      <Text variant="bodySm" weight={bold ? '700' : '600'} numeric>{value}</Text>
    </View>
  );
  return (
    <Sheet
      visible={visible}
      onClose={onClose}
      title="Pay with another token"
      scrollable
      footer={<Button title={stale ? 'Refresh the price' : `Swap and pay`} leftIcon="shield" onPress={onConfirm} />}
    >
      <View style={{ gap: spacing.sm }}>
        {row('You send', payAmountLabel(quote.fromAmount, pick.token.decimals, pick.token.symbol), true)}
        {row('Worth about', usd(quote.fromAmountUsd))}
        <Divider spacing="sm" />
        {row('Order total', stable(quote.orderTotal, token))}
        {quote.walletBalance !== '0' ? row(`Already in your wallet`, stable(quote.walletBalance, token)) : null}
        {row(`Swapped into ${token}`, stable(quote.shortfall, token))}
        <Divider spacing="sm" />
        {row('Swap cost (LiFi)', usd(quote.lifiCostUsd))}
        {row('Network fees we cover for you', usd(quote.gasUsd))}
        {row('TSA fee', 'None')}
        {row('Arrives in', etaLabel(quote.etaSeconds))}
        <AccentCard padding="md">
          <Text variant="bodySm">
            {`The ${token} arrives in your own wallet on Sonic, then pays the seller${quote.orders > 1 ? 's' : ''} into escrow — `
              + `${quote.orders} payment${quote.orders > 1 ? 's' : ''}, one after another. If the swap doesn't complete, your tokens come back to your wallet and nothing is paid.`}
          </Text>
        </AccentCard>
      </View>
    </Sheet>
  );
}

/** A run in progress, or how it ended, with the one safe way forward. */
export function FundingProgressView({
  state,
  sourceSymbol,
  orderToken,
  orderNames,
  onContinue,
  onPayAgain,
  onViewOrders,
  onDone,
}: {
  state: FundingRunState;
  sourceSymbol: string;
  orderToken: string;
  orderNames: string[];
  onContinue: () => void;
  onPayAgain: () => void;
  onViewOrders: () => void;
  onDone: () => void;
}) {
  const { colors, spacing } = useTheme();
  const steps = fundingSteps(state, { sourceSymbol, orderToken, orderNames });
  const failed = state.stage === 'failed';
  const rec = state.record;
  const canPayAgain = failed && state.outcome === 'retry' && !!rec && !rec.intentId && !rec.swapHash && !rec.delivered;
  // Leaving forgets the run only when nothing is in flight; otherwise checkout
  // follows it again next time (canForgetRun).
  const forgettable = canForgetRun(state);
  const canContinue = failed && ((state.outcome === 'retry' && !!rec?.delivered) || state.outcome === 'wait' || state.outcome === 'held');
  const moving = !failed && state.stage !== 'done';
  return (
    <View style={{ gap: spacing.md }}>
      <Card>
        <Stepper steps={steps} />
      </Card>
      {state.stage === 'bridging' ? (
        <Text variant="caption" color="textMuted">
          {`Your swap is on its way — usually ${etaLabel(rec?.etaSeconds)}. You can leave this screen; open checkout again to pick up where it stopped.`}
        </Text>
      ) : moving ? (
        <Text variant="caption" color="textMuted">Keep the app open while you sign.</Text>
      ) : null}
      {failed ? (
        <AccentCard railColor={state.outcome === 'refunded' || state.outcome === 'expired' ? colors.warning : colors.danger} padding="md">
          <Text variant="bodySm" color={state.outcome === 'refunded' || state.outcome === 'expired' ? 'textPrimary' : 'danger'}>
            {state.error ?? 'The payment could not be completed.'}
          </Text>
        </AccentCard>
      ) : null}
      {state.stage === 'done' ? (
        <AccentCard railColor={colors.success} padding="md">
          <Text variant="bodySm">{`Paid. Your ${orderToken} is held in escrow until you confirm delivery.`}</Text>
        </AccentCard>
      ) : null}
      {canContinue ? <Button title="Continue" onPress={onContinue} /> : null}
      {canPayAgain ? <Button title="Pay again" onPress={onPayAgain} /> : null}
      {state.stage === 'done' || failed || state.stage === 'bridging' ? (
        <Button title="View orders" variant={state.stage === 'done' ? 'primary' : 'secondary'} leftIcon="list" onPress={onViewOrders} />
      ) : null}
      {failed && forgettable && state.stage !== 'done' ? (
        <Button title="Back to checkout" variant="secondary" onPress={onDone} />
      ) : null}
    </View>
  );
}
