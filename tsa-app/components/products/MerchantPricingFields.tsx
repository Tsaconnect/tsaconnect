// Pricing-currency picker, price input and shipping-rate block shared by the
// merchant inventory add and edit screens. State lives in useMerchantPricing.
import React, { useState } from 'react';
import { View, ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { Input, PickerSheet, PressableScale, SelectRow, Text } from '@/components/ui';
import { PACKAGE_SIZE_PRESETS } from '@/constants/shipping';
import type { AmountField, MerchantPricing } from '@/hooks/useMerchantPricing';

const joinHelper = (...parts: string[]) => parts.filter(Boolean).join(' · ');

export const PricingCurrencySelect = ({ pricing, style }: { pricing: MerchantPricing; style?: ViewStyle }) => {
  const [open, setOpen] = useState(false);
  const { selectedCurrency: c } = pricing;
  return (
    <>
      <SelectRow
        label="Pricing Currency"
        value={`${c.flag ? c.flag + ' ' : ''}${c.code} (${c.symbol})`}
        onPress={() => setOpen(true)}
        disabled={!pricing.ready}
        style={style}
      />
      <PickerSheet
        title="Select Pricing Currency"
        items={pricing.currencyOptions}
        selectedId={pricing.selectedCurrencyCode}
        onSelect={(item) => {
          pricing.switchCurrency(item.id);
          setOpen(false);
        }}
        visible={open}
        onClose={() => setOpen(false)}
      />
    </>
  );
};

export const PriceInput = ({
  pricing,
  error,
  containerStyle,
}: {
  pricing: MerchantPricing;
  error?: string;
  containerStyle?: ViewStyle;
}) => (
  <Input
    containerStyle={containerStyle}
    label={`Price (${pricing.selectedCurrency.symbol}) *`}
    placeholder={pricing.ready ? (pricing.isUsd ? '0.00' : '0') : 'Loading rates…'}
    keyboardType="decimal-pad"
    editable={pricing.ready}
    value={pricing.amounts.price}
    onChangeText={(v) => pricing.setAmount('price', v)}
    error={error}
    helper={error ? undefined : joinHelper(pricing.formatUsd(pricing.amounts.price), '2% platform fee applies per sale')}
  />
);

const ZoneInput = ({ pricing, field, label }: { pricing: MerchantPricing; field: AmountField; label: string }) => (
  <Input
    containerStyle={{ flex: 1 }}
    label={`${label} (${pricing.selectedCurrency.symbol})`}
    placeholder={pricing.isUsd ? '0.00' : '0'}
    keyboardType="decimal-pad"
    editable={pricing.ready}
    value={pricing.amounts[field]}
    onChangeText={(v) => pricing.setAmount(field, v)}
  />
);

/** Package presets, base fee and per-zone rates. `onCard` picks the chip fill for card backgrounds. */
export const ShippingRatesFields = ({ pricing, onCard }: { pricing: MerchantPricing; onCard?: boolean }) => {
  const { colors, spacing, radius } = useTheme();
  const { selectedCurrency, isUsd } = pricing;

  return (
    <View style={{ gap: spacing.md }}>
      <Text variant="bodySm" weight="600" color="textSecondary">
        Package size (optional quick-select)
      </Text>
      <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
        {PACKAGE_SIZE_PRESETS.map((preset) => {
          const localPreset = pricing.toLocal(preset.baseFee);
          return (
            <PressableScale
              key={preset.key}
              onPress={() => pricing.applyPackagePreset(preset.baseFee)}
              disabled={!pricing.ready}
              style={{
                flex: 1,
                minWidth: 90,
                paddingVertical: spacing.sm,
                paddingHorizontal: spacing.sm,
                borderRadius: radius.md,
                borderWidth: 1,
                borderColor: colors.border,
                backgroundColor: onCard ? colors.surfaceAlt : colors.surface,
                alignItems: 'center',
                gap: 2,
              }}
            >
              <Text variant="caption" weight="700" align="center">
                {preset.label}
              </Text>
              <Text variant="caption" weight="600" color="accent">
                ${preset.baseFee}
                {!isUsd && localPreset ? ` · ${selectedCurrency.symbol}${localPreset}` : ''}
              </Text>
            </PressableScale>
          );
        })}
      </View>

      <Input
        label={`Base shipping fee (${selectedCurrency.symbol})`}
        placeholder={isUsd ? 'e.g. 2.00' : 'e.g. 1000'}
        keyboardType="decimal-pad"
        editable={pricing.ready}
        value={pricing.baseShippingFee}
        onChangeText={pricing.setBaseShippingFee}
        helper={joinHelper(
          pricing.formatUsd(pricing.baseShippingFee),
          'Auto-fills the zone rates below. Edit any zone to override.'
        )}
      />

      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        <Input
          containerStyle={{ flex: 1 }}
          label="Same Residential Area"
          value="Free"
          editable={false}
          inputStyle={{ color: colors.textMuted } as any}
        />
        <ZoneInput pricing={pricing} field="shippingSameCity" label="Same City" />
      </View>
      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        <ZoneInput pricing={pricing} field="shippingSameState" label="Same State" />
        <ZoneInput pricing={pricing} field="shippingSameCountry" label="Same Country" />
      </View>
      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        <ZoneInput pricing={pricing} field="shippingInternational" label="International" />
        <View style={{ flex: 1 }} />
      </View>

      {!isUsd && (
        <Text variant="caption" color="textMuted">
          Prices are saved in USD. Buyers paying in {selectedCurrency.code} see today&apos;s rate, so their local
          price moves with the exchange rate.
        </Text>
      )}
    </View>
  );
};

/** Appends the USD amounts from `pricing` to a product FormData. */
export function appendUsdAmounts(formData: FormData, pricing: MerchantPricing) {
  for (const [field, usd] of Object.entries(pricing.usdPayload())) {
    if (usd != null) formData.append(field, usd.toString());
  }
}
