// Merchant money inputs in the merchant's chosen currency: the currency
// picker, a generic amount input, the product price and the distance-fee block.
// Shared by the product and service listing forms; state lives in
// useMerchantPricing, which converts to USD on submit.
import React, { useState } from 'react';
import { View, ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { Input, PickerSheet, PressableScale, SelectRow, Text } from '@/components/ui';
import { PACKAGE_SIZE_PRESETS } from '@/constants/shipping';
import { BASE_FEE_KEY, type MerchantPricing } from '@/hooks/useMerchantPricing';

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

/** A money input typed in the merchant's currency, stored under `amountKey`. */
export const LocalAmountInput = ({
  pricing,
  amountKey,
  label,
  required,
  helper,
  error,
  placeholder,
  containerStyle,
}: {
  pricing: MerchantPricing;
  amountKey: string;
  label: string;
  required?: boolean;
  helper?: string;
  error?: string;
  placeholder?: string;
  containerStyle?: ViewStyle;
}) => {
  const value = pricing.amount(amountKey);
  return (
    <Input
      containerStyle={containerStyle}
      label={`${label} (${pricing.selectedCurrency.symbol})${required ? ' *' : ''}`}
      placeholder={pricing.ready ? placeholder ?? (pricing.isUsd ? '0.00' : '0') : 'Loading rates…'}
      keyboardType="decimal-pad"
      editable={pricing.ready}
      value={value}
      onChangeText={(v) => pricing.setAmount(amountKey, v)}
      error={error}
      helper={error ? undefined : joinHelper(pricing.formatUsd(value), helper ?? '') || undefined}
    />
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
  <LocalAmountInput
    pricing={pricing}
    amountKey="price"
    label="Price"
    required
    error={error}
    helper="2% platform fee applies per sale"
    containerStyle={containerStyle}
  />
);

const ZoneInput = ({ pricing, amountKey, label }: { pricing: MerchantPricing; amountKey: string; label: string }) => (
  <LocalAmountInput pricing={pricing} amountKey={amountKey} label={label} containerStyle={{ flex: 1 }} />
);

/**
 * Package presets, base fee and per-zone rates. `onCard` picks the chip fill
 * for card backgrounds; `residentialPriceable` makes the residential tier a
 * priced field instead of a fixed "Free".
 */
export const ShippingRatesFields = ({
  pricing,
  onCard,
  residentialPriceable,
  presetLabel = 'Package size (optional quick-select)',
}: {
  pricing: MerchantPricing;
  onCard?: boolean;
  residentialPriceable?: boolean;
  presetLabel?: string;
}) => {
  const { colors, spacing, radius } = useTheme();
  const { selectedCurrency, isUsd } = pricing;
  const baseFee = pricing.amount(BASE_FEE_KEY);

  return (
    <View style={{ gap: spacing.md }}>
      <Text variant="bodySm" weight="600" color="textSecondary">
        {presetLabel}
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
        label={`Base fee (${selectedCurrency.symbol})`}
        placeholder={isUsd ? 'e.g. 2.00' : 'e.g. 1000'}
        keyboardType="decimal-pad"
        editable={pricing.ready}
        value={baseFee}
        onChangeText={pricing.setBaseShippingFee}
        helper={joinHelper(pricing.formatUsd(baseFee), 'Auto-fills the zone rates below. Edit any zone to override.')}
      />

      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        {residentialPriceable ? (
          <ZoneInput pricing={pricing} amountKey="shippingResidential" label="Same Residential Area" />
        ) : (
          <Input
            containerStyle={{ flex: 1 }}
            label="Same Residential Area"
            value="Free"
            editable={false}
            inputStyle={{ color: colors.textMuted } as any}
          />
        )}
        <ZoneInput pricing={pricing} amountKey="shippingSameCity" label="Same City" />
      </View>
      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        <ZoneInput pricing={pricing} amountKey="shippingSameState" label="Same State" />
        <ZoneInput pricing={pricing} amountKey="shippingSameCountry" label="Same Country" />
      </View>
      <View style={{ flexDirection: 'row', gap: spacing.md }}>
        <ZoneInput pricing={pricing} amountKey="shippingInternational" label="International" />
        <View style={{ flex: 1 }} />
      </View>

      {!isUsd && <UsdStorageNote />}
    </View>
  );
};

/** Tells the merchant why a local price will not stay fixed for buyers. */
export const UsdStorageNote = () => (
  <Text variant="caption" color="textMuted">
    Prices are saved in USD. Buyers see them in their own currency at today&apos;s rate, so the local price moves
    with the exchange rate.
  </Text>
);

/** Appends the USD amounts from `pricing` to a product FormData. */
export function appendUsdAmounts(formData: FormData, pricing: MerchantPricing) {
  for (const [field, usd] of Object.entries(pricing.usdPayload())) {
    if (usd != null) formData.append(field, usd.toString());
  }
}
