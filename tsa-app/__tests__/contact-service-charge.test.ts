import { contactServiceCharge } from '@/utils/contactServiceCharge';

describe('contactServiceCharge', () => {
  it('charges nothing extra when the provider set no fee, price or tiers', () => {
    expect(contactServiceCharge({ price: 0 }, { accessFeeUsd: 0, calloutFeeUsd: 0 })).toEqual({
      accessFeeUsd: 0,
      payable: false,
    });
    expect(contactServiceCharge(null, null)).toEqual({ accessFeeUsd: 0, payable: false });
  });

  it('reports the access fee and makes it payable', () => {
    expect(contactServiceCharge({}, { accessFeeUsd: 5 })).toEqual({ accessFeeUsd: 5, payable: true });
  });

  it('is payable through a call-out, a price or a non-residential tier', () => {
    expect(contactServiceCharge({}, { calloutFeeUsd: 2 }).payable).toBe(true);
    expect(contactServiceCharge({ price: 10 }, {}).payable).toBe(true);
    expect(contactServiceCharge({ shippingSameState: 3 }, {}).payable).toBe(true);
  });
});
