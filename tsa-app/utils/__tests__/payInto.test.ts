// utils/__tests__/payInto.test.ts
//
// The pay-in screens decide central vs virtual-account mode purely from what
// the card carries. A wrong call either hides the own-name warning in central
// mode (the mitigation silently gone) or shows it in VA mode (telling a payer
// their own account is wrong).

import { exactNairaOf, payerInstructionOf } from '@/utils/payInto';

const INSTRUCTION =
  'Pay from a bank account named JOHN DOE. Payments from other accounts can be delayed for review or ' +
  'credited to the wrong person. Send the exact amount shown. The account name shows as SOME COMPANY LTD. ' +
  "This is TSA Connect's registered company account.";

describe('payerInstructionOf', () => {
  it('returns the server wording verbatim in central mode', () => {
    expect(payerInstructionOf({ payerInstruction: INSTRUCTION })).toBe(INSTRUCTION);
  });

  it('is null for a virtual-account card, which carries no instruction', () => {
    expect(payerInstructionOf({})).toBeNull();
    expect(payerInstructionOf({ payerInstruction: undefined })).toBeNull();
  });

  it('is null for a missing card, a null field, or blank text', () => {
    expect(payerInstructionOf(null)).toBeNull();
    expect(payerInstructionOf(undefined)).toBeNull();
    expect(payerInstructionOf({ payerInstruction: null })).toBeNull();
    expect(payerInstructionOf({ payerInstruction: '   ' })).toBeNull();
  });
});

describe('exactNairaOf', () => {
  it('groups whole naira for display', () => {
    expect(exactNairaOf({ amountNgn: '5000' })).toBe('5,000');
    expect(exactNairaOf({ amountNgn: '1234567' })).toBe('1,234,567');
    expect(exactNairaOf({ amountNgn: '999' })).toBe('999');
  });

  it('is null in virtual-account mode, where the card has no amount', () => {
    expect(exactNairaOf({})).toBeNull();
    expect(exactNairaOf(null)).toBeNull();
  });

  it('refuses anything that is not a positive whole-naira figure rather than guess', () => {
    // The API answers "" for an order amount it could not parse.
    expect(exactNairaOf({ amountNgn: '' })).toBeNull();
    expect(exactNairaOf({ amountNgn: '0' })).toBeNull();
    expect(exactNairaOf({ amountNgn: '1000.50' })).toBeNull();
    expect(exactNairaOf({ amountNgn: '-500' })).toBeNull();
    expect(exactNairaOf({ amountNgn: 'abc' })).toBeNull();
  });
});
