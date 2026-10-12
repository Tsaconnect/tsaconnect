import { marketLoadError } from '../p2pMarketError';

describe('marketLoadError', () => {
  it('carries the KYC code from the server 403', () => {
    expect(marketLoadError({ status: 403, code: 'kyc_verification_required', message: 'x' }).kycCode).toBe(
      'kyc_verification_required',
    );
  });

  it('recognises an older API 403 without a code', () => {
    expect(marketLoadError({ status: 403, message: 'KYC verification required' }).kycCode).toBe(
      'kyc_verification_required',
    );
  });

  it('keeps the legal-name code so support copy shows', () => {
    expect(marketLoadError({ status: 403, code: 'kyc_legal_name_missing' }).kycCode).toBe('kyc_legal_name_missing');
  });

  it('is a plain connection error otherwise', () => {
    const e = marketLoadError({ message: 'Network error' });
    expect(e.kycCode).toBeUndefined();
    expect(e.message).toMatch(/Check your connection/);
  });

  it('does not treat other 403s as KYC', () => {
    expect(marketLoadError({ status: 403, message: 'forbidden' }).kycCode).toBeUndefined();
  });
});
