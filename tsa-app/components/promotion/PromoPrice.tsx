// Price block for a listing: plain price normally, or the promotion treatment
// (struck-through original, promo price, "(N% off)") when the listing is promoted.
// `format` is the caller's own currency formatter so every screen keeps its style.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Badge, Text } from '@/components/ui';
import { isPromoted, PromoFields } from '@/utils/promo';

export interface PromoPriceProps {
  product: Partial<PromoFields> & { price: number };
  format: (usd: number) => string;
  /** card = compact for lists; detail = larger for the detail header. */
  size?: 'card' | 'detail';
  /** Show the small "Promotion" tag next to the price. */
  showTag?: boolean;
  /** Force the full price (e.g. the buyer's window expired). */
  fullPrice?: boolean;
}

export const PromoTag: React.FC = () => {
  const { colors } = useTheme();
  return <Badge label="Promotion" color={colors.onAccent} background={colors.danger} size="sm" />;
};

export const PromoPrice: React.FC<PromoPriceProps> = ({
  product,
  format,
  size = 'card',
  showTag = true,
  fullPrice = false,
}) => {
  const { spacing } = useTheme();
  const priceVariant = size === 'detail' ? 'title2' : 'bodyMed';

  if (fullPrice || !isPromoted(product)) {
    return (
      <Text variant={priceVariant} weight="700" color="accent" numeric>
        {format(product.price)}
      </Text>
    );
  }

  const promoPrice = product.promoPrice as number;
  const pct = product.promoDiscountPercent as number;
  return (
    <View
      accessible
      accessibilityLabel={`Promotion. ${format(promoPrice)}, was ${format(product.price)}, ${pct} percent off`}
      style={{ gap: spacing.xs }}
    >
      {showTag && <PromoTag />}
      <View style={{ flexDirection: 'row', alignItems: 'baseline', flexWrap: 'wrap', gap: spacing.xs }}>
        <Text variant={priceVariant} weight="700" color="danger" numeric>
          {format(promoPrice)}
        </Text>
        <Text variant="caption" color="textMuted" numeric style={{ textDecorationLine: 'line-through' }}>
          {format(product.price)}
        </Text>
        <Text variant="caption" weight="600" color="danger">
          {`(${pct}% off)`}
        </Text>
      </View>
    </View>
  );
};

export default PromoPrice;
