// components/checkout/PayWithToken.tsx
//
// Paying checkout with a swap: what it costs (under the unified "Pay with"
// selector on the review screen — see app/(detail)/checkout), the confirm
// sheet, and the progress of a run. Presentational — the money moves in
// services/checkoutFundingRun (via hooks/useCheckoutFunding); every figure shown
// is the server's funding quote.
import React from 'react';
import { Pressable, View } from 'react-native';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Icon, Sheet, Spinner, Stepper, SummaryRow, Text } from '@/components/ui';
import type { FundingQuote } from '@/services/checkoutFundingApi';
import { canForgetRun, etaLabel, type FundingRunState } from '@/services/checkoutFundingRun';
import type { FundingPick } from '@/hooks/useCheckoutFunding';
import { payAmountLabel } from '@/utils/payCheckout';
import { getChainByChainId } from '@/constants/chains';
import { fundingSteps } from './fundingSteps';

const usd = (n: number) => (n < 0.01 ? '< $0.01' : `$${n.toFixed(2)}`);
// Fees on a small order are fractions of a cent apart, so under $1 they keep a
// third decimal — rounded to the cent on its own, $0.024 + $0.014 read as
// $0.02 + $0.01 beside a $0.04 total.
const feeRound = (n: number) => (n >= 1 ? Math.round(n * 100) / 100 : Math.round(n * 1000) / 1000);
const feeUsd = (n: number) => {
  const r = feeRound(n);
  if (r === 0) return n > 0 ? '< $0.001' : '$0.00';
  const thousandths = r.toFixed(3);
  return r >= 1 || thousandths.endsWith('0') ? `$${r.toFixed(2)}` : `$${thousandths}`;
};
const stable = (base: string, symbol: string) => payAmountLabel(base, 6, symbol);

/**
 * What a swap-to-pay selection costs, under the unified "Pay with" row: a
 * spinner while pricing, an error, or "You'll send about X · arrives in Y".
 */
export function FundingPriceLine({
  pick,
  quote,
  pricing,
  error,
}: {
  pick: FundingPick | null;
  quote?: FundingQuote;
  pricing: boolean;
  error?: string;
}) {
  const { spacing } = useTheme();
  if (!pick) return null;
  return (
    <View style={{ gap: spacing.xs, marginTop: spacing.sm }}>
      {pricing ? <Spinner label="Checking the price…" /> : null}
      {error ? <Text variant="bodySm" color="danger">{error}</Text> : null}
      {quote ? (
        <Text variant="bodySm" color="textSecondary">
          {`You'll send about ${payAmountLabel(quote.fromAmount, pick.token.decimals, pick.token.symbol)} · arrives in ${etaLabel(quote.etaSeconds)}`}
        </Text>
      ) : null}
    </View>
  );
}

/**
 * What she agrees to before signing: what leaves her wallet and on which chain,
 * the fees in one line, and that the money is held in escrow. How it gets there
 * (her balance used first, the conversion, the per-fee split) is behind
 * "Details" — every figure still the server's.
 */
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
  const { colors, spacing } = useTheme();
  const [details, setDetails] = React.useState(false);
  const token = quote.token;
  const youPay = payAmountLabel(quote.fromAmount, pick.token.decimals, pick.token.symbol);
  const chain = getChainByChainId(pick.chainId)?.name ?? `chain ${pick.chainId}`;
  // Both are hers to pay — LiFi's cost and the gas we send and recover in the
  // route. The total is the sum of the parts as shown, so they always add up.
  const fees = feeRound(quote.lifiCostUsd) + feeRound(quote.gasUsd);
  return (
    <Sheet
      visible={visible}
      onClose={onClose}
      title={`Pay with ${pick.token.symbol}`}
      scrollable
      footer={<Button title={stale ? 'Refresh the price' : `Pay ${youPay}`} leftIcon="shield" onPress={onConfirm} />}
    >
      {/* Sheet leaves body padding to its caller; inset to match the header and footer. */}
      <View style={{ padding: spacing.lg, gap: spacing.md }}>
        <View style={{ gap: spacing.xs }}>
          <Text variant="bodySm" color="textSecondary">You pay</Text>
          <Text variant="title2" weight="700" numeric>{youPay}</Text>
          <Text variant="bodySm" color="textSecondary">{`≈ ${usd(quote.fromAmountUsd)} on ${chain}`}</Text>
        </View>

        <Pressable
          testID="funding-details-toggle"
          accessibilityRole="button"
          accessibilityState={{ expanded: details }}
          onPress={() => setDetails(d => !d)}
          style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: spacing.sm }}
        >
          <Text variant="bodySm" color="textSecondary" style={{ flexShrink: 1 }}>
            {`Includes ${feeUsd(fees)} in conversion and network fees`}
          </Text>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 2 }}>
            <Text variant="bodySm" weight="600" color="accent">Details</Text>
            <Icon name={details ? 'chevronDown' : 'chevronRight'} size={16} color={colors.accent} />
          </View>
        </Pressable>

        {details ? (
          <Card>
            <SummaryRow label="Order total" value={stable(quote.orderTotal, token)} />
            {quote.walletBalance !== '0' ? (
              <SummaryRow label={`From your ${token} balance`} value={stable(quote.walletBalance, token)} />
            ) : null}
            <SummaryRow label={`Converted to ${token}`} value={stable(quote.shortfall, token)} />
            <SummaryRow label="Conversion fee" value={feeUsd(quote.lifiCostUsd)} />
            <SummaryRow label="Network fees" value={feeUsd(quote.gasUsd)} />
            {quote.orders > 1 ? <SummaryRow label="Sellers paid" value={String(quote.orders)} /> : null}
          </Card>
        ) : null}

        <SummaryRow label="Arrives in" value={etaLabel(quote.etaSeconds)} />

        <AccentCard padding="md">
          <Text variant="bodySm">
            {`Your payment is held in escrow until the order is delivered. If the conversion doesn't complete, your ${pick.token.symbol} comes back and nothing is paid.`}
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
