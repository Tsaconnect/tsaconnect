import { hasPayoutFee, shippingWithoutPayoutFee } from '../payoutFee';

describe('payoutFee', () => {
  it('takes the payout fee back out of the shipping amount', () => {
    expect(shippingWithoutPayoutFee('4906138', '906138')).toBe('4000000');
    expect(shippingWithoutPayoutFee('906138', '906138')).toBe('0');
  });

  it('leaves the shipping alone when there is no payout fee', () => {
    expect(shippingWithoutPayoutFee('4000000')).toBe('4000000');
    expect(shippingWithoutPayoutFee('4000000', '0')).toBe('4000000');
    expect(shippingWithoutPayoutFee('4000000', '')).toBe('4000000');
  });

  it('never goes below zero and never throws on junk', () => {
    expect(shippingWithoutPayoutFee('100', '500')).toBe('0');
    expect(shippingWithoutPayoutFee('abc', 'x')).toBe('0');
  });

  it('knows when there is a payout fee', () => {
    expect(hasPayoutFee('906138')).toBe(true);
    expect(hasPayoutFee('0')).toBe(false);
    expect(hasPayoutFee(undefined)).toBe(false);
  });
});
