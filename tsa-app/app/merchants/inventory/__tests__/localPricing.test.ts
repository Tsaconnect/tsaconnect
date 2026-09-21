jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

import { localToUsd, usdToLocal, convertCurrencyValue } from '@/hooks/useMerchantPricing';
import { calculateZoneRates, PACKAGE_SIZE_PRESETS } from '@/constants/shipping';

describe('Merchant Inventory Local Pricing Integration', () => {
  const ngnRate = 1500;
  const ugxRate = 3700;

  describe('Add Product pricing submission workflow', () => {
    it('converts entered local price to canonical USD for backend submission', () => {
      const merchantTypedLocalPrice = '75000'; // ₦75,000
      const submittedUsd = localToUsd(merchantTypedLocalPrice, ngnRate);
      expect(submittedUsd).toBe(50.0);
    });

    it('converts quick-select preset package sizes to local currency and back to USD', () => {
      // Preset "Medium" = $3
      const mediumPreset = PACKAGE_SIZE_PRESETS.find((p) => p.key === 'medium')!;
      expect(mediumPreset.baseFee).toBe(3);

      // In local currency (NGN)
      const localBase = usdToLocal(mediumPreset.baseFee, ngnRate);
      expect(localBase).toBe('4500'); // ₦4,500

      // Zone rates auto-filled from local base fee
      const localRates = calculateZoneRates(parseFloat(localBase));
      expect(localRates.sameCity).toBe(4500); // multiplier 1
      expect(localRates.sameState).toBe(6750); // multiplier 1.5
      expect(localRates.sameCountry).toBe(11250); // multiplier 2.5
      expect(localRates.international).toBe(22500); // multiplier 5

      // Converted to USD on submit
      expect(localToUsd(String(localRates.sameCity), ngnRate)).toBe(3.0);
      expect(localToUsd(String(localRates.sameState), ngnRate)).toBe(4.5);
      expect(localToUsd(String(localRates.sameCountry), ngnRate)).toBe(7.5);
      expect(localToUsd(String(localRates.sameInternational || localRates.international), ngnRate)).toBe(15.0);
    });

    it('converts custom manually typed shipping fees to USD', () => {
      const customLocalShipping = '3000'; // ₦3,000
      const submittedUsd = localToUsd(customLocalShipping, ngnRate);
      expect(submittedUsd).toBe(2.0);
    });
  });

  describe('Edit Product workflow', () => {
    it('converts stored backend USD price into merchant local currency on load', () => {
      // Backend product stored as $35.00 USD
      const backendProductPriceUsd = 35.0;

      // Loaded into input in NGN
      const ngnPriceInput = usdToLocal(backendProductPriceUsd, ngnRate);
      expect(ngnPriceInput).toBe('52500');

      // Loaded into input in UGX
      const ugxPriceInput = usdToLocal(backendProductPriceUsd, ugxRate);
      expect(ugxPriceInput).toBe('129500');
    });

    it('converts stored backend shipping zone fees into merchant local currency on load', () => {
      const backendShippingSameCityUsd = 2.0;
      const localSameCityInput = usdToLocal(backendShippingSameCityUsd, ngnRate);
      expect(localSameCityInput).toBe('3000');
    });

    it('maintains consistent valuation when merchant switches currency in the dropdown', () => {
      const rates = {
        NGN: { midRate: ngnRate },
        UGX: { midRate: ugxRate },
      };

      // Merchant starts in NGN with ₦52,500 ($35)
      const initialNgn = '52500';
      const switchedToUgx = convertCurrencyValue(initialNgn, 'NGN', 'UGX', rates);
      expect(switchedToUgx).toBe('129500');

      // Switching to USD produces $35
      const switchedToUsd = convertCurrencyValue(switchedToUgx, 'UGX', 'USD', rates);
      expect(switchedToUsd).toBe('35');
    });
  });
});
