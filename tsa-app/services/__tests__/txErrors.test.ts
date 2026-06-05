import { decodeRevertError } from '../txErrors';

// Build a 32-byte ABI word (right-aligned hex, no 0x) from a value.
const word = (v: bigint | string) =>
  (typeof v === 'bigint' ? v.toString(16) : v).padStart(64, '0');

const SENDER = '87c2422be9a11b71a5c530ecd156134d3b04e3ce';
const NEEDED = 25_000_000_000_000_000_000n; // 25 USDT @ 18 decimals (the BSC case)

describe('decodeRevertError', () => {
  it('decodes ERC20InsufficientBalance into balance + needed (base units)', () => {
    // Mirrors the real failed "Create Sell Ad": balance 0, needed 25e18.
    const msg =
      'estimate gas (would revert): failed to estimate gas: execution reverted: 0xe450d38c' +
      word(SENDER) +
      word(0n) +
      word(NEEDED);
    expect(decodeRevertError(msg)).toEqual({
      kind: 'insufficient-balance',
      balance: '0',
      needed: '25000000000000000000',
    });
  });

  it('decodes ERC20InsufficientAllowance', () => {
    const msg = 'execution reverted: 0xfb8f41b2' + word(SENDER) + word(0n) + word(NEEDED);
    expect(decodeRevertError(msg)).toEqual({
      kind: 'insufficient-allowance',
      allowance: '0',
      needed: '25000000000000000000',
    });
  });

  it('returns unknown-revert for an unrecognised selector', () => {
    expect(decodeRevertError('execution reverted: 0xdeadbeef' + word(1n))).toEqual({
      kind: 'unknown-revert',
    });
  });

  it('returns unknown-revert for a revert with no hex payload', () => {
    expect(decodeRevertError('execution reverted')).toEqual({ kind: 'unknown-revert' });
  });

  it('returns null for a non-revert message (caller keeps the raw text)', () => {
    expect(decodeRevertError('Network request failed')).toBeNull();
    expect(decodeRevertError('')).toBeNull();
    expect(decodeRevertError(undefined)).toBeNull();
  });

  it('does not throw on a malformed/truncated balance payload', () => {
    expect(() => decodeRevertError('execution reverted: 0xe450d38c1234')).not.toThrow();
    expect(decodeRevertError('execution reverted: 0xe450d38c1234')).toEqual({ kind: 'unknown-revert' });
  });
});
