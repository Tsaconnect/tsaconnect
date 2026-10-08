jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

// eslint-disable-next-line import/first
import { cachesMerchantRole, initBusinessType, isPickerLocked, lpSettlementPayload, resolveMerchantGate } from '@/app/merchants/merchant-request';

describe('liquidity_provider applications', () => {
  it('opens a locked LP form from ?type=liquidity_provider', () => {
    expect(initBusinessType('liquidity_provider')).toBe('liquidity_provider');
    expect(isPickerLocked('liquidity_provider')).toBe(true);
  });

  it('sends an unverified applicant to verification', () => {
    expect(resolveMerchantGate({ existingRequest: null, formType: 'liquidity_provider', pmActive: false, kycVerified: false })).toEqual({ screen: 'verify' });
  });

  it('an LP request gates only its own form, and marks lp on its status screens', () => {
    const lpPending = { businessType: 'liquidity_provider', status: 'pending' };
    expect(resolveMerchantGate({ existingRequest: lpPending, formType: 'liquidity_provider', pmActive: false })).toEqual({ screen: 'pending', pm: false, lp: true });
    expect(resolveMerchantGate({ existingRequest: { ...lpPending, status: 'approved' }, formType: 'liquidity_provider', pmActive: false }))
      .toEqual({ screen: 'approved', pm: false, payFlow: false, lp: true });
    // A marketplace merchant's old request does not block an LP application.
    expect(resolveMerchantGate({ existingRequest: { businessType: 'general_products', status: 'approved' }, formType: 'liquidity_provider', pmActive: false }))
      .toEqual({ screen: 'form' });
  });

  // Review I5: an approved LP never caches role=merchant (O-B3, plan B P2).
  it('caches the merchant role only for marketplace types', () => {
    expect(cachesMerchantRole('liquidity_provider')).toBe(false);
    expect(cachesMerchantRole('payment_acceptance')).toBe(false);
    expect(cachesMerchantRole('general_products')).toBe(true);
    expect(cachesMerchantRole('p2p_merchant')).toBe(true);
  });

  it('sends settlement fields and the app wallet, never caps', () => {
    const body = lpSettlementPayload({ settlementBankCode: '058', settlementAccountNumber: '0123456789', settlementAccountName: 'ADA OBI', makerWalletAddress: ' 0x52908400098527886E0F7030069857D2E4169EE7 ' });
    expect(body).toEqual({ settlementBankCode: '058', settlementAccountNumber: '0123456789', settlementAccountName: 'ADA OBI', makerWalletAddress: '0x52908400098527886E0F7030069857D2E4169EE7' });
    expect(body).not.toHaveProperty('requestedPerTradeCapUsd');
  });
});
