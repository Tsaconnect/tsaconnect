// utils/__tests__/paycrestDepositView.test.ts
import { depositCardState, type DepositViewInput } from '@/utils/paycrestDepositView';

const base: DepositViewInput = {
  direction: 'sell',
  status: 'awaiting_deposit',
  expired: false,
};

describe('depositCardState', () => {
  it('asks for the transfer when nothing has been sent yet', () => {
    expect(depositCardState(base)).toEqual({ kind: 'address' });
  });

  // The bug this exists to kill: the app broadcast the transfer itself, then
  // the order screen rendered "send to this address" as if it had not, which
  // reads as an instruction to send a second time.
  it('shows progress, not an address, once the app sent the deposit', () => {
    expect(depositCardState({ ...base, sentTxHash: '0xabc' })).toEqual({
      kind: 'progress',
      txHash: '0xabc',
    });
  });

  it('shows progress while the send is still in flight, before there is a hash', () => {
    expect(depositCardState({ ...base, sending: true })).toEqual({ kind: 'progress' });
  });

  // App killed and reopened from activity: the hash is gone from route state,
  // but the server remembers the user claimed the payment.
  it('falls back to the claim when the hash did not survive a restart', () => {
    expect(depositCardState({ ...base, paymentClaimedAt: '2026-09-17T00:00:00Z' })).toEqual({
      kind: 'claimed',
    });
  });

  // A lost answer, or a kill after the broadcast: this phone may have sent it
  // and has no hash. Never an address — that would invite the second send.
  it('shows a deposit this phone may have sent as unconfirmed, ahead of the claim', () => {
    expect(depositCardState({ ...base, mayBeOut: true })).toEqual({ kind: 'unconfirmed' });
    expect(depositCardState({ ...base, mayBeOut: true, paymentClaimedAt: '2026-09-17T00:00:00Z' })).toEqual({
      kind: 'unconfirmed',
    });
    expect(depositCardState({ ...base, mayBeOut: true, sentTxHash: '0xabc' })).toEqual({
      kind: 'progress',
      txHash: '0xabc',
    });
    expect(depositCardState({ ...base, mayBeOut: true, expired: true })).toEqual({ kind: 'hidden' });
  });

  it('prefers the hash over the claim when it has both', () => {
    expect(
      depositCardState({ ...base, sentTxHash: '0xabc', paymentClaimedAt: '2026-09-17T00:00:00Z' }),
    ).toEqual({ kind: 'progress', txHash: '0xabc' });
  });

  it('hides the card once the window has closed', () => {
    expect(depositCardState({ ...base, expired: true })).toEqual({ kind: 'hidden' });
    expect(depositCardState({ ...base, expired: true, sentTxHash: '0xabc' })).toEqual({
      kind: 'hidden',
    });
  });

  it('hides the card once the order is no longer waiting on the user', () => {
    for (const status of ['pending', 'settling', 'settled', 'refunded', 'failed'] as const) {
      expect(depositCardState({ ...base, status })).toEqual({ kind: 'hidden' });
    }
  });

  // The buy leg pays a bank, not an address — this helper must never claim it.
  it('never speaks for the buy leg', () => {
    expect(depositCardState({ ...base, direction: 'buy' })).toEqual({ kind: 'hidden' });
    expect(depositCardState({ ...base, direction: 'buy', sentTxHash: '0xabc' })).toEqual({
      kind: 'hidden',
    });
  });
});
