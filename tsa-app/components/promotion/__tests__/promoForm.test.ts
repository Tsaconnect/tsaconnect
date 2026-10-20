jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

import {
  DEFAULT_PROMO_STATE,
  appendPromoFields,
  discountPercent,
  promoStateFromProduct,
  validatePromo,
  type PromoState,
} from '../promoForm';

const on = (patch: Partial<PromoState> = {}): PromoState => ({
  ...DEFAULT_PROMO_STATE,
  enabled: true,
  promoPrice: '200',
  ...patch,
});

// RN's FormData is not iterable under jest; record appends instead.
const recorder = () => {
  const data: Record<string, string> = {};
  return { fd: { append: (k: string, v: string) => { data[k] = v; } } as unknown as FormData, data };
};

describe('validatePromo', () => {
  it('passes when off', () => expect(validatePromo(DEFAULT_PROMO_STATE, 0)).toBeNull());
  it('accepts a valid promotion', () => expect(validatePromo(on(), 2000)).toBeNull());
  it('rejects promo price >= original or <= 0', () => {
    expect(validatePromo(on({ promoPrice: '2000' }), 2000)).toMatch(/lower/);
    expect(validatePromo(on({ promoPrice: '0' }), 2000)).toMatch(/greater than 0/);
    expect(validatePromo(on({ promoPrice: '' }), 2000)).toMatch(/greater than 0/);
  });
  it('rejects a missing original price', () => expect(validatePromo(on(), NaN)).toMatch(/listing price/));
  it('validates manual referrals as integer >= 1 with no upper bound', () => {
    expect(validatePromo(on({ referralChoice: 'manual', manualReferrals: '500' }), 2000)).toBeNull();
    expect(validatePromo(on({ referralChoice: 'manual', manualReferrals: '0' }), 2000)).toMatch(/referrals/);
    expect(validatePromo(on({ referralChoice: 'manual', manualReferrals: '2.5' }), 2000)).toMatch(/referrals/);
    expect(validatePromo(on({ referralChoice: 'manual', manualReferrals: '' }), 2000)).toMatch(/referrals/);
  });
  it('validates the countdown only when enabled', () => {
    expect(validatePromo(on({ countdownMinutes: 'x' }), 2000)).toBeNull();
    expect(validatePromo(on({ countdownEnabled: true, countdownMinutes: '0' }), 2000)).toMatch(/minutes/);
    expect(validatePromo(on({ countdownEnabled: true, countdownMinutes: '2' }), 2000)).toBeNull();
  });
});

describe('discountPercent', () => {
  it('computes whole-number percent', () => expect(discountPercent(2000, 200)).toBe(90));
  it('is null when there is no discount', () => {
    expect(discountPercent(2000, 2000)).toBeNull();
    expect(discountPercent(0, 10)).toBeNull();
  });
});

describe('appendPromoFields', () => {
  it('sends only promoEnabled=false when off', () => {
    const { fd, data } = recorder();
    appendPromoFields(fd, DEFAULT_PROMO_STATE);
    expect(data).toEqual({ promoEnabled: 'false' });
  });
  it('sends referrals, price and countdown seconds', () => {
    const { fd, data } = recorder();
    appendPromoFields(fd, on({ countdownEnabled: true, countdownMinutes: '2' }));
    expect(data).toEqual({
      promoEnabled: 'true',
      promoRequiredReferrals: '5',
      promoPrice: '200',
      promoCountdownSeconds: '120',
    });
  });
  it('sends an empty countdown when off and prefers the USD price', () => {
    const { fd, data } = recorder();
    appendPromoFields(fd, on({ referralChoice: 'manual', manualReferrals: '12' }), 0.13);
    expect(data).toMatchObject({
      promoRequiredReferrals: '12',
      promoPrice: '0.13',
      promoCountdownSeconds: '',
    });
  });
});

describe('promoStateFromProduct', () => {
  it('is default when the product has no promotion', () =>
    expect(promoStateFromProduct({})).toEqual(DEFAULT_PROMO_STATE));
  it('maps preset and manual referral counts and countdown', () => {
    expect(
      promoStateFromProduct({ promoEnabled: true, promoRequiredReferrals: 10, promoPrice: 4, promoCountdownSeconds: 120 })
    ).toMatchObject({ referralChoice: '10', countdownEnabled: true, countdownMinutes: '2', promoPrice: '4' });
    expect(promoStateFromProduct({ promoEnabled: true, promoRequiredReferrals: 7, promoPrice: 4 })).toMatchObject({
      referralChoice: 'manual',
      manualReferrals: '7',
      countdownEnabled: false,
    });
  });
});
