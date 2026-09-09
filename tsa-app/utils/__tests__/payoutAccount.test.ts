// utils/__tests__/payoutAccount.test.ts
//
// Per-currency account-number rules for the Paycrest buy/sell payout fields.
// The bug this guards against: NUBAN_LENGTH (10, NGN-only) used to be applied
// unconditionally, which would reject any real non-Nigerian account number
// that wasn't exactly 10 characters.

import {
  accountFieldLabel,
  accountFieldPlaceholder,
  accountValueSurvivesKindChange,
  accountNumberMaxLength,
  isDigitsOnlyAccountField,
  isValidAccountNumber,
  sanitizeAccountNumberInput,
} from '@/utils/payoutAccount';

describe('isValidAccountNumber', () => {
  it('requires exactly 10 digits for NGN (NUBAN)', () => {
    expect(isValidAccountNumber('NGN', '0123456789')).toBe(true);
    expect(isValidAccountNumber('NGN', '012345678')).toBe(false); // 9 digits
    expect(isValidAccountNumber('NGN', '01234567890')).toBe(false); // 11 digits
  });

  it('is case-insensitive on the currency code', () => {
    expect(isValidAccountNumber('ngn', '0123456789')).toBe(true);
  });

  it('never blocks a valid non-Nigerian account for not being 10 digits', () => {
    expect(isValidAccountNumber('GHS', '12345')).toBe(true);
    expect(isValidAccountNumber('KES', 'GB33BUKB20201555555555')).toBe(true);
    expect(isValidAccountNumber('EUR', '12')).toBe(true);
  });

  it('rejects empty or whitespace-only input for any currency', () => {
    expect(isValidAccountNumber('GHS', '')).toBe(false);
    expect(isValidAccountNumber('GHS', '   ')).toBe(false);
    expect(isValidAccountNumber('NGN', '')).toBe(false);
  });
});

describe('accountNumberMaxLength', () => {
  it('is 10 for NGN', () => {
    expect(accountNumberMaxLength('NGN')).toBe(10);
  });

  it('is a generous generic cap for a currency with no known fixed rule', () => {
    expect(accountNumberMaxLength('GHS')).toBeGreaterThanOrEqual(20);
  });
});

describe('sanitizeAccountNumberInput', () => {
  it('strips non-digits and caps at 10 for NGN', () => {
    expect(sanitizeAccountNumberInput('NGN', '01-23 45(67)89xyz')).toBe('0123456789');
  });

  it('keeps letters for a currency with no digit-only rule (e.g. an IBAN)', () => {
    expect(sanitizeAccountNumberInput('KES', 'GB33 BUKB 2020 1555 5555 55')).toBe(
      'GB33BUKB20201555555555',
    );
  });
});

// Mobile money is a different kind of destination, not a different currency.
// Every UGX payout is MTN or Airtel, and KES and TZS carry a few among their
// banks — so the rule follows the institution the user picked, not the code.
describe('mobile-money destinations', () => {
  it('asks for a phone number, not an account number', () => {
    expect(accountFieldLabel('mobile_money')).toBe('Mobile money number');
    expect(accountFieldLabel('bank')).toBe('Account number');
    expect(accountFieldLabel()).toBe('Account number');
  });

  it('uses a numeric keypad whatever the currency', () => {
    // UGX has no fixed bank rule, so without the kind this would be false.
    expect(isDigitsOnlyAccountField('UGX', 'mobile_money')).toBe(true);
    expect(isDigitsOnlyAccountField('UGX')).toBe(false);
  });

  it('accepts local and country-code forms alike', () => {
    expect(isValidAccountNumber('UGX', '0772123456', 'mobile_money')).toBe(true);
    expect(isValidAccountNumber('UGX', '256772123456', 'mobile_money')).toBe(true);
    expect(isValidAccountNumber('UGX', '', 'mobile_money')).toBe(false);
  });

  // A ceiling alone let "0" through, and a truncated number that happens to
  // belong to someone else does not fail — it pays them.
  it('rejects a number too short to be anyone\'s', () => {
    expect(isValidAccountNumber('UGX', '0', 'mobile_money')).toBe(false);
    expect(isValidAccountNumber('UGX', '077', 'mobile_money')).toBe(false);
    expect(isValidAccountNumber('UGX', '07721234', 'mobile_money')).toBe(false); // 8
    expect(isValidAccountNumber('UGX', '077212345', 'mobile_money')).toBe(true); // 9
  });

  it('rejects anything that is not digits', () => {
    expect(isValidAccountNumber('KES', 'GB33BUKB20201555555555', 'mobile_money')).toBe(false);
    expect(isValidAccountNumber('KES', '0722 123 456', 'mobile_money')).toBe(false);
  });

  // The important one: NGN's 10-digit NUBAN rule is a BANK rule. Applied to a
  // Nigerian mobile-money number it would reject a real destination.
  it('does not apply the NUBAN length rule to a mobile-money number', () => {
    expect(isValidAccountNumber('NGN', '08031234567', 'mobile_money')).toBe(true);
    expect(isValidAccountNumber('NGN', '08031234567')).toBe(false);
  });

  it('strips non-digits and caps at E.164 length', () => {
    expect(sanitizeAccountNumberInput('UGX', '+256 772-123456', 'mobile_money')).toBe(
      '256772123456',
    );
    expect(sanitizeAccountNumberInput('UGX', '1234567890123456789', 'mobile_money')).toHaveLength(
      15,
    );
    expect(accountNumberMaxLength('UGX', 'mobile_money')).toBe(15);
  });

  it('shows a locally-shaped phone placeholder', () => {
    expect(accountFieldPlaceholder('UGX', 'mobile_money')).toBe('0772123456');
    expect(accountFieldPlaceholder('KES', 'mobile_money')).toBe('0722123456');
    // A corridor we have no example for still gets a phone-shaped hint rather
    // than another country's number.
    expect(accountFieldPlaceholder('ZAR', 'mobile_money')).toBe('Phone number');
    expect(accountFieldPlaceholder('NGN', 'bank')).toBe('0123456789');
    expect(accountFieldPlaceholder('KES', 'bank')).toBe('Account number');
  });
});

// A number typed for one kind of destination must not survive into the other.
// This is the guard on a concrete money-path sequence: in KES or TZS, whose
// lists mix banks and mobile money, a user types a bank account number, then
// switches to M-Pesa. Nothing else catches it — the value revalidates under
// the new kind's looser rule, and outside NGN there is no beneficiary-name
// check before the on-chain leg goes out.
describe('accountValueSurvivesKindChange', () => {
  it('discards the value when the kind changes', () => {
    expect(accountValueSurvivesKindChange('bank', 'mobile_money')).toBe(false);
    expect(accountValueSurvivesKindChange('mobile_money', 'bank')).toBe(false);
  });

  it('keeps it between two destinations of the same kind', () => {
    expect(accountValueSurvivesKindChange('bank', 'bank')).toBe(true);
    expect(accountValueSurvivesKindChange('mobile_money', 'mobile_money')).toBe(true);
  });

  // An API build that sends no type reads as bank everywhere, so switching
  // between two such destinations must not wipe a correct entry.
  it('treats a missing kind as bank on both sides', () => {
    expect(accountValueSurvivesKindChange(undefined, undefined)).toBe(true);
    expect(accountValueSurvivesKindChange('bank', undefined)).toBe(true);
    expect(accountValueSurvivesKindChange(undefined, 'mobile_money')).toBe(false);
  });

  // The specific sequence from the review, end to end through the validator:
  // the stale value must not merely be discarded by policy — it would have
  // been ACCEPTED had it survived, which is why the policy has to exist.
  it('would otherwise let a bank number pass as a mobile-money one', () => {
    const typedForABank = '01234567890123';
    expect(isValidAccountNumber('KES', typedForABank, 'bank')).toBe(true);
    expect(accountValueSurvivesKindChange('bank', 'mobile_money')).toBe(false);
  });
});
