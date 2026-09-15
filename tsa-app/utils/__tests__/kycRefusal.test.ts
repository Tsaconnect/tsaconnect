// utils/__tests__/kycRefusal.test.ts
//
// The codes are asserted literally: they must match the Go API
// (internal/handlers/it_handler.go kycRequiredCode / kycLegalNameMissingCode),
// and a drifted spelling fails silently — the user just sees a generic error on
// a payment screen with nowhere to go.

// jest.mock calls below are hoisted above this import by babel-jest.
import {
  KYC_LEGAL_NAME_MISSING,
  KYC_ROUTE,
  KYC_VERIFICATION_REQUIRED,
  kycRefusalFor,
  promptKycRefusal,
} from '../kycRefusal';

const mockAlert = jest.fn();
const mockPush = jest.fn();

jest.mock('react-native', () => ({
  Alert: { alert: (...args: unknown[]) => mockAlert(...args) },
}));
jest.mock('expo-router', () => ({
  router: { push: (href: unknown) => mockPush(href) },
}));

beforeEach(() => {
  mockAlert.mockReset();
  mockPush.mockReset();
});

describe('kyc refusal codes', () => {
  it('match the server spelling', () => {
    expect(KYC_VERIFICATION_REQUIRED).toBe('kyc_verification_required');
    expect(KYC_LEGAL_NAME_MISSING).toBe('kyc_legal_name_missing');
  });

  it('route verification to the Didit screen', () => {
    expect(KYC_ROUTE).toBe('/profile/kyc');
  });
});

describe('kycRefusalFor', () => {
  it('sends an unverified user to verify', () => {
    expect(kycRefusalFor(KYC_VERIFICATION_REQUIRED)?.kind).toBe('verify');
  });

  it('sends a verified user with no legal name to support, not to verify', () => {
    const r = kycRefusalFor(KYC_LEGAL_NAME_MISSING, 'server says contact support');
    expect(r?.kind).toBe('support');
    expect(r?.message).toBe('server says contact support');
  });

  it('ignores every other code', () => {
    expect(kycRefusalFor(undefined)).toBeNull();
    expect(kycRefusalFor('it_agreement_required')).toBeNull();
  });
});

describe('promptKycRefusal', () => {
  it('offers a Verify now action that opens the KYC screen', () => {
    expect(promptKycRefusal(KYC_VERIFICATION_REQUIRED)).toBe(true);
    const buttons = mockAlert.mock.calls[0][2] as { text: string; onPress?: () => void }[];
    const verify = buttons.find((b) => b.text === 'Verify now');
    verify?.onPress?.();
    expect(mockPush).toHaveBeenCalledWith('/profile/kyc');
  });

  it('shows the support message without routing anywhere', () => {
    expect(promptKycRefusal(KYC_LEGAL_NAME_MISSING, 'contact support')).toBe(true);
    expect(mockAlert).toHaveBeenCalledWith('Contact support', 'contact support');
    expect(mockPush).not.toHaveBeenCalled();
  });

  it('does nothing for a response that is not a KYC refusal', () => {
    expect(promptKycRefusal('stable_buy_already_open', 'x')).toBe(false);
    expect(mockAlert).not.toHaveBeenCalled();
  });
});
