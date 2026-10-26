import { isMerchantAccount } from '../isMerchantAccount';

describe('isMerchantAccount', () => {
  it('treats the merchant role as a merchant', () => {
    expect(isMerchantAccount('merchant', false)).toBe(true);
    expect(isMerchantAccount('Merchant', false)).toBe(true);
  });
  it('treats staff with an approved merchant application as a merchant', () => {
    expect(isMerchantAccount('super_admin', true)).toBe(true);
    expect(isMerchantAccount('admin', true)).toBe(true);
  });
  it('does not treat staff without an approval, or a plain user, as one', () => {
    expect(isMerchantAccount('super_admin', false)).toBe(false);
    expect(isMerchantAccount('user', true)).toBe(false);
    expect(isMerchantAccount(null, true)).toBe(false);
  });
});
