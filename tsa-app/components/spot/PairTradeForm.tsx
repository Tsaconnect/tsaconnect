// components/spot/PairTradeForm.tsx
//
// The one buy/sell form every pair desk shows: the pair's balances on one line
// with Max, a single amount box in the token being paid, an optional live
// estimate, then Review. The Spot Trade desk (app/wallet/spot-trade) and the
// MCGP OTC desk (screens/swap) both render it, so a pair reads the same way
// whichever desk trades it — PM asked for exactly this after the OTC desk kept
// its swap-style two-card layout with token pickers and a flip arrow.
import React from 'react';
import { View, type ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { AmountInput, Button, PressableScale, Spinner, Text } from '@/components/ui';

export interface PairBalance {
  symbol: string;
  /** Pre-formatted figure; null while unknown (renders "—"). */
  amount: string | null;
  /** Network suffix for a cross-chain pair ("on BSC"). */
  network?: string;
}

export interface PairTradeFormProps {
  /** Left of the line; "Balance" unless the desk caps by something else. */
  balanceLabel?: string;
  balances: PairBalance[];
  onMax?: () => void;
  amountLabel: string;
  amount: string;
  onChangeAmount: (v: string) => void;
  /** Token the amount is entered in. */
  symbol: string;
  error?: string;
  /** "You receive ≈ …" — omitted until a price is known. */
  estimate?: string | null;
  estimateLoading?: boolean;
  hint?: string;
  /** Desk-specific notices (sell cap, price errors) between the form and Review. */
  children?: React.ReactNode;
  reviewTitle?: string;
  onReview: () => void;
  reviewLoading?: boolean;
  reviewDisabled?: boolean;
  reviewStyle?: ViewStyle;
}

export function PairTradeForm({
  balanceLabel = 'Balance',
  balances,
  onMax,
  amountLabel,
  amount,
  onChangeAmount,
  symbol,
  error,
  estimate,
  estimateLoading,
  hint,
  children,
  reviewTitle = 'Review',
  onReview,
  reviewLoading,
  reviewDisabled,
  reviewStyle,
}: PairTradeFormProps) {
  const { spacing } = useTheme();
  const line = balances
    .map((b) => `${b.amount ?? '—'} ${b.symbol}${b.network ? ` on ${b.network}` : ''}`)
    .join(' · ');
  return (
    <View style={{ gap: spacing.lg }}>
      <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', gap: spacing.md }}>
        <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
          {balanceLabel}: {line}
        </Text>
        {onMax ? (
          <PressableScale onPress={onMax} accessibilityLabel="Use max">
            <Text variant="bodySm" weight="700" color="accent">Max</Text>
          </PressableScale>
        ) : null}
      </View>
      <AmountInput
        label={amountLabel}
        value={amount}
        onChangeText={onChangeAmount}
        symbol={symbol}
        symbolPosition="suffix"
        error={error || undefined}
      />
      {estimateLoading ? (
        <View style={{ alignItems: 'flex-start' }}><Spinner size="small" /></View>
      ) : estimate ? (
        <Text variant="bodyMed" weight="600">{estimate}</Text>
      ) : null}
      {hint ? <Text variant="bodySm" color="textMuted">{hint}</Text> : null}
      {children}
      <Button
        title={reviewTitle}
        onPress={onReview}
        loading={reviewLoading}
        disabled={reviewDisabled}
        style={reviewStyle}
      />
    </View>
  );
}
