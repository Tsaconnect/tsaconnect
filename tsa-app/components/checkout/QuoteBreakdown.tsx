// The fee breakdown of a cart quote: each row in the pay token with the
// app-currency equivalent beneath it. Shared by checkout review, the confirm
// sheet and the receipt so the three can never disagree.
import React from 'react';
import { View } from 'react-native';
import { SummaryRow, Text } from '@/components/ui';
import type { CartQuote } from '@/services/orderApi';
import { listingAmount, useQuoteFormat } from '@/hooks/useCheckoutQuote';

export const TokenAmount = ({
  quote,
  base,
  strong,
}: {
  quote: CartQuote;
  base: string;
  strong?: boolean;
}) => {
  const fmt = useQuoteFormat(quote);
  const approx = fmt.approx(base);
  return (
    <View style={{ alignItems: 'flex-end' }}>
      <Text variant="bodySm" weight={strong ? '700' : '600'} numeric>
        {fmt.token(base)}
      </Text>
      {approx ? (
        <Text variant="caption" color="textMuted" numeric>
          {approx}
        </Text>
      ) : null}
    </View>
  );
};

const isZero = (base: string) => {
  try {
    return BigInt(base) === 0n;
  } catch {
    return true;
  }
};

export const QuoteBreakdown = ({
  quote,
  shippingLabel = 'Shipping',
  withTotal,
}: {
  quote: CartQuote;
  shippingLabel?: string;
  withTotal?: boolean;
}) => {
  const { totals, lines } = quote;
  const orders = lines.length;
  return (
    <View>
      <SummaryRow label="Items" value={<TokenAmount quote={quote} base={listingAmount(totals)} />} />
      <SummaryRow
        label={shippingLabel}
        value={isZero(totals.shippingAmount) ? 'Free' : <TokenAmount quote={quote} base={totals.shippingAmount} />}
      />
      {!isZero(totals.platformFee) && (
        // Informational: it is inside the item price, not added to the total.
        <SummaryRow label="Platform fee" value="Paid by seller" valueColor="textMuted" />
      )}
      <SummaryRow
        label="Network fee"
        hint={
          isZero(totals.gasFee)
            ? `Waived for ${quote.token}`
            : orders > 1
              ? `Per order × ${orders}`
              : 'Covers the escrow transaction'
        }
        value={isZero(totals.gasFee) ? 'Free' : <TokenAmount quote={quote} base={totals.gasFee} />}
      />
      {withTotal && (
        <SummaryRow total label="Total" value={<TokenAmount quote={quote} base={totals.total} strong />} />
      )}
    </View>
  );
};
