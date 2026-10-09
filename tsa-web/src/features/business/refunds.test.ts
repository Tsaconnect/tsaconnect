import { formatUsd, isOpenRefund, microOf, nairaForRefund, parseUsd, refundAmountError, waitingNote } from './refunds';
import type { Refund } from './api';

describe('refund money', () => {
  it('reads US dollars as micro-USD, up to six decimals', () => {
    expect(parseUsd('12.5')).toBe(12_500_000n);
    expect(parseUsd(' 15 ')).toBe(15_000_000n);
    expect(parseUsd('0.000001')).toBe(1n);
  });

  // Review Focus 3.
  it('refuses what is not a positive amount with at most six decimals', () => {
    for (const bad of ['', '0', '0.00', '12.5555555', '1,000', '$12', '-1', '1e3', '12.']) expect(parseUsd(bad)).toBeNull();
  });

  it('writes micro-USD like the API: two decimals at least, up to six', () => {
    expect(formatUsd(12_500_000n)).toBe('12.50');
    expect(formatUsd(15_000_000n)).toBe('15.00');
    expect(formatUsd(1_234_567n)).toBe('1.234567');
    expect(formatUsd(0n)).toBe('0.00');
    expect(microOf('2.5')).toBe(2_500_000n);
    expect(microOf('nonsense')).toBe(0n);
  });

  it('prices a refund in naira at the payment\'s own rate (customer total over the cap)', () => {
    expect(nairaForRefund(15_000_000n, 8_535_354, 15_000_000n)).toBe(8_535_354);
    expect(nairaForRefund(12_500_000n, 8_535_354, 15_000_000n)).toBe(7_112_795);
    expect(nairaForRefund(1n, 8_535_354, 0n)).toBeNull();
  });

  it('says what is wrong with a typed amount', () => {
    expect(refundAmountError('', 15_000_000n)).toBe('Enter an amount.');
    expect(refundAmountError('abc', 15_000_000n)).toBe('Enter an amount in US dollars, like 12.50 (up to 6 decimal places).');
    expect(refundAmountError('20', 15_000_000n)).toBe('You can refund up to 15.00 USD.');
    expect(refundAmountError('15', 15_000_000n)).toBeNull();
  });
});

describe('refund wording', () => {
  const r = (status: Refund['status']) => ({ amount: '12.50', status, tokenSymbol: status === 'requested' ? undefined : 'USDT' } as Refund);
  it('a refund still moving is open', () => {
    expect(['requested', 'submitted', 'needs_review'].every((s) => isOpenRefund(s as Refund['status']))).toBe(true);
    expect(['confirmed', 'failed', 'cancelled'].some((s) => isOpenRefund(s as Refund['status']))).toBe(false);
  });
  it('tells the merchant what an open refund waits for', () => {
    expect(waitingNote(r('requested'))).toBe('A refund of 12.50 USD is waiting for you to approve and send it in the TSA Connect app.');
    expect(waitingNote(r('submitted'))).toBe('A refund of 12.50 USDT is on its way — waiting for the network to confirm it.');
    expect(waitingNote(r('needs_review'))).toBe('A refund of 12.50 USDT is being checked by TSA Connect.');
  });
  // Plans review I6: a send the network refused says why, and that it is retried.
  it('a refund the network refused says why', () => {
    expect(waitingNote({ ...r('submitted'), lastBroadcastError: 'Not enough gas in your wallet' })).toBe(
      "A refund of 12.50 USDT hasn't reached the network yet. Couldn't broadcast: Not enough gas in your wallet — top up gas; we retry automatically.");
    expect(waitingNote({ ...r('submitted'), lastBroadcastError: '' })).toBe('A refund of 12.50 USDT is on its way — waiting for the network to confirm it.');
  });
});
