// Merchant form section: "Put this listing on promotion". Shared by every
// product/service listing form; state and FormData helpers live in promoForm.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Card, Chip, Input, SegmentedControl, Text, Toggle } from '@/components/ui';
import {
  REFERRAL_PRESETS,
  discountPercent,
  type ReferralChoice,
  type usePromotion,
} from './promoForm';
import { parseAmount } from '@/hooks/useMerchantPricing';

export * from './promoForm';

interface Props {
  promotion: ReturnType<typeof usePromotion>;
  /** The listing's original price as typed, in the same currency as the promo price. */
  originalPrice: string;
  currencySymbol: string;
  error?: string | null;
  /** Disables the discount price input (e.g. while exchange rates load). */
  priceDisabled?: boolean;
  helper?: string;
}

const COUNTDOWN_NOTE =
  'If a buyer has not reached the referral requirement when the countdown runs out, they pay the full price.';

export const PromotionFields = ({
  promotion,
  originalPrice,
  currencySymbol,
  error,
  priceDisabled,
  helper,
}: Props) => {
  const { spacing } = useTheme();
  const { state, patch: onChange, setPromoPrice: onPromoPriceChange } = promotion;
  const original = parseAmount(originalPrice);
  const promo = parseAmount(state.promoPrice);
  const percent = discountPercent(original, promo);
  const choices: { key: ReferralChoice; label: string }[] = [
    ...REFERRAL_PRESETS.map((n) => ({ key: n, label: n })),
    { key: 'manual', label: 'Enter manually' },
  ];

  return (
    <Card padding="lg" style={{ gap: spacing.lg }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
        <Text variant="body" weight="600" style={{ flex: 1 }}>
          Put this listing on promotion
        </Text>
        <Toggle
          value={state.enabled}
          onValueChange={(enabled) => onChange({ enabled })}
          accessibilityLabel="Put this listing on promotion"
        />
      </View>

      {state.enabled && (
        <>
          <View style={{ gap: spacing.sm }}>
            <Text variant="bodySm" weight="600" color="textSecondary">
              No of required referrals
            </Text>
            <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
              {choices.map((c) => (
                <Chip
                  key={c.key}
                  label={c.label}
                  selected={state.referralChoice === c.key}
                  onPress={() => onChange({ referralChoice: c.key })}
                />
              ))}
            </View>
            {state.referralChoice === 'manual' && (
              <Input
                label="Number of referrals"
                placeholder="e.g. 15"
                keyboardType="number-pad"
                value={state.manualReferrals}
                onChangeText={(manualReferrals) => onChange({ manualReferrals })}
              />
            )}
          </View>

          <View style={{ gap: spacing.sm }}>
            <Text variant="bodySm" weight="600" color="textSecondary">
              Enable count down when user clicks item
            </Text>
            <SegmentedControl
              segments={[
                { key: 'no', label: 'No' },
                { key: 'yes', label: 'Yes' },
              ]}
              value={state.countdownEnabled ? 'yes' : 'no'}
              onChange={(key) => onChange({ countdownEnabled: key === 'yes' })}
            />
            {state.countdownEnabled && (
              <Input
                label="Set count (minutes)"
                placeholder="e.g. 2 minutes"
                keyboardType="number-pad"
                value={state.countdownMinutes}
                onChangeText={(countdownMinutes) => onChange({ countdownMinutes })}
              />
            )}
            <Text variant="caption" color="textSecondary">
              {COUNTDOWN_NOTE}
            </Text>
          </View>

          <View style={{ gap: spacing.sm }}>
            <Input
              label={`Enter discount price (${currencySymbol})`}
              placeholder="0"
              keyboardType="decimal-pad"
              editable={!priceDisabled}
              value={state.promoPrice}
              onChangeText={onPromoPriceChange}
              helper={helper}
            />
            {percent != null && (
              <View
                style={{ flexDirection: 'row', alignItems: 'baseline', gap: spacing.sm, flexWrap: 'wrap' }}
                accessible
                accessibilityLabel={`${currencySymbol}${originalPrice} reduced to ${currencySymbol}${state.promoPrice}, ${percent} percent off`}
              >
                <Text variant="bodySm" color="textMuted" style={{ textDecorationLine: 'line-through' }}>
                  {currencySymbol}
                  {originalPrice}
                </Text>
                <Text variant="body" weight="700" color="accent">
                  {currencySymbol}
                  {state.promoPrice}
                </Text>
                <Text variant="bodySm" weight="600" color="success">
                  ({percent}% off)
                </Text>
              </View>
            )}
          </View>

          {error ? (
            <Text variant="caption" color="danger" accessibilityRole="alert">
              {error}
            </Text>
          ) : null}
        </>
      )}
    </Card>
  );
};

export default PromotionFields;
