import { isMerchantAccount } from '../isMerchantAccount';

const merchantRow = { isPm: true, kind: 'merchant' };
const none = { isPm: false };

describe('isMerchantAccount', () => {
  it('treats the merchant role as a merchant', () => {
    expect(isMerchantAccount('merchant', none)).toBe(true);
    expect(isMerchantAccount('Merchant', none)).toBe(true);
  });
  it('treats a staff role with a live merchant row as a merchant', () => {
    expect(isMerchantAccount('super_admin', merchantRow)).toBe(true);
    expect(isMerchantAccount('admin', merchantRow)).toBe(true);
  });
  it('does not treat staff without a merchant row, or a plain user, as one', () => {
    expect(isMerchantAccount('super_admin', none)).toBe(false);
    expect(isMerchantAccount('user', merchantRow)).toBe(false);
    expect(isMerchantAccount(null, merchantRow)).toBe(false);
  });
  it('ignores a liquidity-provider row', () => {
    expect(isMerchantAccount('super_admin', { isPm: true, kind: 'liquidity_provider' })).toBe(false);
  });
});
