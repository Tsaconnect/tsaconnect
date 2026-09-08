// utils/__tests__/payoutAccount.test.ts
//
// Per-currency account-number rules for the Paycrest buy/sell payout fields.
// The bug this guards against: NUBAN_LENGTH (10, NGN-only) used to be applied
// unconditionally, which would reject any real non-Nigerian account number
// that wasn't exactly 10 characters.

import {
  accountFieldLabel,
  accountFieldPlaceholder,
  accountNumberMaxLength,
  isDigitsOnlyAccountCurrency,
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
    expect(isDigitsOnlyAccountCurrency('UGX', 'mobile_money')).toBe(true);
    expect(isDigitsOnlyAccountCurrency('UGX')).toBe(false);
  });

  it('accepts local and country-code forms alike', () => {
    expect(isValidAccountNumber('UGX', '0772123456', 'mobile_money')).toBe(true);
    expect(isValidAccountNumber('UGX', '256772123456', 'mobile_money')).toBe(true);
    expect(isValidAccountNumber('UGX', '', 'mobile_money')).toBe(false);
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

  it('shows a phone-shaped placeholder', () => {
    expect(accountFieldPlaceholder('UGX', 'mobile_money')).toBe('0772123456');
    expect(accountFieldPlaceholder('NGN', 'bank')).toBe('0123456789');
    expect(accountFieldPlaceholder('KES', 'bank')).toBe('Account number');
  });
});
