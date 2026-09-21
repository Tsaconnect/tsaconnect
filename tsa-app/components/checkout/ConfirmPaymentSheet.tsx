// Last stop before the wallet signs: the exact amount in the pay token, what it
// is made of, where it comes from and goes, and the signatures to expect.
// Everything shown comes from the backend quote the orders are created from.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { useCurrency } from '@/contexts/CurrencyContext';
import { Button, Card, Divider, Icon, Sheet, Text } from '@/components/ui';
import type { CartQuote } from '@/services/orderApi';
import { quoteUnits, useQuoteFormat } from '@/hooks/useCheckoutQuote';
import { QuoteBreakdown, TokenAmount } from './QuoteBreakdown';

export interface ConfirmPaymentSheetProps {
  visible: boolean;
  onClose: () => void;
  onConfirm: () => void;
  quote: CartQuote;
  shippingLabel: string;
  walletAddress: string | null;
  /** Pay-token balance in token units; null when unknown. */
  tokenBalance: number | null;
  /** S balance for network gas; null when unknown. */
  gasBalance: number | null;
}

const shortAddress = (a: string) => `${a.slice(0, 6)}…${a.slice(-4)}`;

export const ConfirmPaymentSheet = ({
  visible,
  onClose,
  onConfirm,
  quote,
  shippingLabel,
  walletAddress,
  tokenBalance,
  gasBalance,
}: ConfirmPaymentSheetProps) => {
  const { colors, spacing, radius } = useTheme();
  const { currency, formatPrice } = useCurrency();
  const fmt = useQuoteFormat(quote);
  const { totals, lines, token } = quote;

  const totalUnits = quoteUnits(totals.total, quote.decimals);
  const short = tokenBalance != null && tokenBalance < totalUnits;
  // Only a balance known to be zero blocks; an unreadable one lets the chain decide.
  const noGas = gasBalance === 0;
  const rateLine =
    token === 'MCGP'
      ? `1 MCGP = $${quote.usdPerToken.toFixed(4)} · OTC buy rate`
      : currency.code === 'USD'
        ? `1 ${token} = $1.00`
        : `$1 = ${formatPrice(1)} · for reference only`;

  const warning = short
    ? `Not enough ${token}. You need ${fmt.token(totals.total)}.`
    : noGas
      ? 'Your wallet has no S for network gas. Add a little S on Sonic, then try again.'
      : null;

  return (
    <Sheet
      visible={visible}
      onClose={onClose}
      title="Confirm payment"
      scrollable
      maxHeightRatio={0.92}
      footer={
        <View style={{ gap: spacing.sm }}>
          <Button
            title={`Confirm & pay ${fmt.token(totals.total)}`}
            leftIcon="shield"
            disabled={!!warning}
            onPress={onConfirm}
          />
          <Button title="Back to review" variant="ghost" onPress={onClose} />
        </View>
      }
    >
      <View style={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.lg, gap: spacing.md }}>
        <View style={{ alignItems: 'center', gap: 2, paddingVertical: spacing.sm }}>
          <Text variant="display" weight="700" numeric>
            {fmt.token(totals.total)}
          </Text>
          {fmt.approx(totals.total) ? (
            <Text variant="bodyMed" weight="600" color="accent" numeric>
              {fmt.approx(totals.total)}
            </Text>
          ) : null}
          <Text variant="caption" color="textMuted">
            {rateLine}
          </Text>
        </View>

        <Card padding="md">
          {lines.map((line, i) => (
            <View key={`${line.productId}-${i}`}>
              {i > 0 && <Divider spacing="sm" />}
              <View style={{ flexDirection: 'row', justifyContent: 'space-between', gap: spacing.md }}>
                <View style={{ flex: 1 }}>
                  <Text variant="bodySm" weight="600" numberOfLines={2}>
                    {line.name}
                    {line.quantity > 1 ? ` ×${line.quantity}` : ''}
                  </Text>
                  <Text variant="caption" color="textMuted">
                    {line.sellerName ? `Sold by ${line.sellerName}` : 'Seller'}
                  </Text>
                </View>
                <TokenAmount quote={quote} base={line.total} />
              </View>
            </View>
          ))}
        </Card>

        <Card padding="md">
          <QuoteBreakdown quote={quote} shippingLabel={shippingLabel} withTotal />
        </Card>

        <Card padding="md" style={{ gap: spacing.xs }}>
          {walletAddress ? (
            <Row label="From wallet" value={shortAddress(walletAddress)} />
          ) : null}
          {tokenBalance != null ? (
            <Row
              label={`${token} balance after`}
              value={short ? '—' : `${(tokenBalance - totalUnits).toLocaleString('en-US', { maximumFractionDigits: token === 'MCGP' ? 4 : 2 })} ${token}`}
            />
          ) : null}
          <Row label="Orders" value={String(lines.length)} />
        </Card>

        <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
          <Icon name="verified" size={18} color={colors.success} />
          <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
            Held in escrow. The seller is paid only after you confirm delivery. If it doesn&apos;t arrive, open a
            dispute and the escrow refunds you.
          </Text>
        </View>

        <View style={{ gap: spacing.xs }}>
          <Text variant="overline" color="textMuted">
            Your wallet will ask you to sign
          </Text>
          {lines.map((line, i) => (
            <View key={`sig-${line.productId}-${i}`} style={{ gap: spacing.xs }}>
              <SignStep n={i * 2 + 1} text={`Allow ${token} for ${line.name}`} radius={radius.pill} />
              <SignStep n={i * 2 + 2} text={`Lock ${fmt.token(line.total)} in escrow`} radius={radius.pill} />
            </View>
          ))}
        </View>

        {warning ? (
          <View style={{ backgroundColor: colors.dangerBg ?? colors.surfaceAlt, borderRadius: radius.sm, padding: spacing.sm }}>
            <Text variant="caption" color="danger">
              {warning}
            </Text>
          </View>
        ) : null}
      </View>
    </Sheet>
  );
};

const Row = ({ label, value }: { label: string; value: string }) => (
  <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
    <Text variant="caption" color="textSecondary">
      {label}
    </Text>
    <Text variant="caption" weight="600" numeric>
      {value}
    </Text>
  </View>
);

const SignStep = ({ n, text, radius }: { n: number; text: string; radius: number }) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
      <View
        style={{
          width: 20,
          height: 20,
          borderRadius: radius,
          backgroundColor: colors.surfaceAlt,
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Text variant="caption" weight="700" color="textSecondary">
          {n}
        </Text>
      </View>
      <Text variant="caption" style={{ flex: 1 }} numberOfLines={1}>
        {text}
      </Text>
    </View>
  );
};
