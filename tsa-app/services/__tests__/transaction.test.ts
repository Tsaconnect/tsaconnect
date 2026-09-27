/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A tx that was broadcast but not confirmed in time may still be mined. The
 * caller must get its hash, so a retry reports that tx instead of sending a
 * second one (a second escrow would revert, or never be reported at all).
 */
jest.mock('../wallet', () => ({
  __esModule: true,
  signTransaction: jest.fn(async () => '0xsigned'),
  broadcastTransaction: jest.fn(async () => ({ hash: '0xsent', wait: () => new Promise(() => {}) })),
}));

import { signAndBroadcast, TxNotConfirmedError } from '../transaction';

test('a confirmation timeout reports the hash of the tx that was sent', async () => {
  jest.useFakeTimers();
  try {
    const sent = signAndBroadcast({ to: '0xescrow', data: '0x', value: '0' } as any, 'sonic');
    const outcome = sent.catch((e) => e);
    await jest.advanceTimersByTimeAsync(90_000);
    const err = await outcome;
    expect(err).toBeInstanceOf(TxNotConfirmedError);
    expect(err.txHash).toBe('0xsent');
    expect(err.message).toBe('Transaction confirmation timed out');
  } finally {
    jest.useRealTimers();
  }
});

test('an RPC failure while waiting keeps the hash; a revert does not', async () => {
  const { broadcastTransaction } = jest.requireMock('../wallet');
  broadcastTransaction.mockResolvedValueOnce({ hash: '0xsent', wait: async () => { throw new Error('network error'); } });
  const lost = await signAndBroadcast({ to: '0xescrow' } as any, 'sonic').catch((e) => e);
  expect(lost).toBeInstanceOf(TxNotConfirmedError);
  expect(lost.txHash).toBe('0xsent');

  const revert = Object.assign(new Error('execution reverted'), { code: 'CALL_EXCEPTION' });
  broadcastTransaction.mockResolvedValueOnce({ hash: '0xsent', wait: async () => { throw revert; } });
  await expect(signAndBroadcast({ to: '0xescrow' } as any, 'sonic')).rejects.toBe(revert);
});
