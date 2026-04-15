// Shipping zone multipliers — applied to the merchant's base shipping fee
// to auto-fill per-zone rates. Merchants can override any field manually.
export const SHIPPING_MULTIPLIERS = {
  sameCity: 1,
  sameState: 1.5,
  sameCountry: 2.5,
  international: 5,
} as const;

// Quick-select package size presets — pre-fill the base shipping fee for common cases.
export const PACKAGE_SIZE_PRESETS: {
  key: string;
  label: string;
  description: string;
  baseFee: number;
}[] = [
  { key: 'small', label: 'Small / Light', description: 'Accessories, books, small electronics', baseFee: 1 },
  { key: 'medium', label: 'Medium', description: 'Clothing, shoes, mid-size items', baseFee: 3 },
  { key: 'heavy', label: 'Heavy / Bulky', description: 'Appliances, furniture, large items', baseFee: 8 },
];

export function calculateZoneRates(baseFee: number) {
  return {
    sameCity: +(baseFee * SHIPPING_MULTIPLIERS.sameCity).toFixed(2),
    sameState: +(baseFee * SHIPPING_MULTIPLIERS.sameState).toFixed(2),
    sameCountry: +(baseFee * SHIPPING_MULTIPLIERS.sameCountry).toFixed(2),
    international: +(baseFee * SHIPPING_MULTIPLIERS.international).toFixed(2),
  };
}
