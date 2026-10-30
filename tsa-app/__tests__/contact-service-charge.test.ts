import { contactServiceCharge } from '@/utils/contactServiceCharge';

describe('contactServiceCharge', () => {
  it('charges nothing extra when the provider set no fee, price or tiers', () => {
    expect(contactServiceCharge({ price: 0 }, { accessFeeUsd: 0, calloutFeeUsd: 0 })).toEqual({
      accessFeeUsd: 0,
      calloutFeeUsd: 0,
      payable: false,
    });
    expect(contactServiceCharge(null, null)).toEqual({ accessFeeUsd: 0, calloutFeeUsd: 0, payable: false });
  });

  it('reports the access fee with its call-out and makes it payable', () => {
    expect(contactServiceCharge({}, { accessFeeUsd: 5, calloutFeeUsd: 3 })).toEqual({
      accessFeeUsd: 5,
      calloutFeeUsd: 3,
      payable: true,
    });
  });

  it('ignores a flat call-out without an access fee, as checkout does', () => {
    expect(contactServiceCharge({}, { calloutFeeUsd: 2 })).toEqual({ accessFeeUsd: 0, calloutFeeUsd: 0, payable: false });
  });

  it('is payable through a price or a non-residential tier', () => {
    expect(contactServiceCharge({ price: 10 }, {}).payable).toBe(true);
    expect(contactServiceCharge({ shippingSameState: 3 }, {}).payable).toBe(true);
  });

  it('keeps the access-fee branch alone when a price is also set, as checkout does', () => {
    expect(contactServiceCharge({ price: 20, shippingSameState: 4 }, { accessFeeUsd: 5, calloutFeeUsd: 1 })).toEqual({
      accessFeeUsd: 5,
      calloutFeeUsd: 1,
      payable: true,
    });
  });
});
