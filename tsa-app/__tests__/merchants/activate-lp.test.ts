jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

// eslint-disable-next-line import/first
import { activationOutcome } from '@/app/merchants/activate-pm';

describe('activationOutcome', () => {
  it('sends an activated LP to its dashboard', () => {
    expect(activationOutcome({ success: true }, 'lp')).toMatchObject({ kind: 'success', route: '/liquidity' });
    expect(activationOutcome({ success: true }, 'merchant')).toMatchObject({ kind: 'success', route: '/merchants/dashboard' });
  });
  it('explains a settlement-name mismatch', () => {
    const o = activationOutcome({ success: false, code: 'settlement_name_mismatch', message: 'x' }, 'lp');
    expect(o.kind).toBe('error');
    expect(o.kind === 'error' && o.message).toMatch(/verified name/);
  });
  it('routes KYC refusals to verification', () => {
    expect(activationOutcome({ success: false, code: 'kyc_verification_required' }, 'lp')).toEqual({ kind: 'kyc' });
  });
});
