/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * How a name change reads: a refusal is the server's words except where the
 * phone must say it (a wallet another account holds; the limits, dated from the
 * refusal's data), and what comes back on a cancel names the proven wallet.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { ApiError } from '@/services/apiRequest';
import { NameChangeError } from '@/services/nameChangeApi';
import { fmtUsd, nameChangeErrorMessage, refundMessage } from '../nameChange';

describe('nameChangeErrorMessage', () => {
  it('a wallet another account holds reads as that, not a bare 409', () => {
    expect(nameChangeErrorMessage(new ApiError('Wallet address already in use by another account', 409), 'x'))
      .toMatch(/linked to another TSA Connect account/);
    expect(nameChangeErrorMessage(new ApiError('Open', 409, 'change_open'), 'x')).toBe('Open');
  });

  it('the cancel and yearly limits are dated from the refusal\'s data', () => {
    const cancel = new NameChangeError('you can cancel again from 2026-10-20', 409, 'cancel_limit', { data: { nextAllowedAt: '2026-10-20T09:00:00Z' } });
    expect(nameChangeErrorMessage(cancel, 'x')).toMatch(/cancelled for a refund only from .*2026.*You can still verify now/);
    const yearly = new NameChangeError('limit', 409, 'name_change_limit', { data: { nextAllowedAt: '2027-03-01T00:00:00Z' } });
    expect(nameChangeErrorMessage(yearly, 'x')).toMatch(/change your name again on .*2027/);
    // Without a date the server's words stand.
    expect(nameChangeErrorMessage(new NameChangeError('limit words', 409, 'cancel_limit'), 'x')).toBe('limit words');
  });

  it('every other refusal is the server\'s own words, or the fallback', () => {
    for (const code of ['wallet_proof_invalid', 'password_incorrect', 'account_locked', 'session_limit', 'session_pending', 'user_not_found', 'name_change_disabled']) {
      expect(nameChangeErrorMessage(new NameChangeError(`words for ${code}`, 409, code), 'x')).toBe(`words for ${code}`);
    }
    expect(nameChangeErrorMessage(null, 'fallback')).toBe('fallback');
  });
});

it('says what comes back on a cancel, and to which wallet', () => {
  const base = { payToken: 'USDC', payChain: 'base', payAmount: '1.00', payFrom: '0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A' };
  expect(refundMessage({ ...base, payMethod: 'stablecoin', status: 'paid' }))
    .toBe('We send 1 USDC back to the wallet you paid from (0x19E7…ff2A), on Base.');
  expect(refundMessage({ ...base, payMethod: 'stablecoin', status: 'paying' })).toBe('Nothing has been charged yet.');
  expect(refundMessage({ ...base, payMethod: 'mcgp_spendable', payToken: 'MCGP', payAmount: '12.50', status: 'paid' }))
    .toBe('Your 12.5 MCGP is returned to your MCGP balance.');
  expect(refundMessage({ ...base, payMethod: 'free', status: 'paid' })).toBe('');
});

it('a fee string reads as dollars only when it is one', () => {
  expect(fmtUsd('1.00')).toBe('$1.00');
  expect(fmtUsd('abc')).toBe('abc');
});
