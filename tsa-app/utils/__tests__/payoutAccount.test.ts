// utils/__tests__/payoutAccount.test.ts
//
// Per-currency account-number rules for the Paycrest buy/sell payout fields.
// The bug this guards against: NUBAN_LENGTH (10, NGN-only) used to be applied
// unconditionally, which would reject any real non-Nigerian account number
// that wasn't exactly 10 characters.

import {
  accountNumberMaxLength,
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
