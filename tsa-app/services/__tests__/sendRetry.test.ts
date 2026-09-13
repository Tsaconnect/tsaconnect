jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'),
);

import { sendPreparedTransaction } from '../walletApi';

const baseTx = {
  to: '0xabc',
  data: '0x',
  value: '1',
  gasLimit: '21000',
  gasPrice: '1',
  nonce: 5,
  chainId: 14601,
};

const prepared = (nonce: number) => ({ success: true, data: { ...baseTx, nonce } });
const ok = (txHash: string) => ({ success: true, data: { txHash } });
const fail = (message: string) => ({ success: false, message });

const NONCE_ERR =
  'Failed to submit transaction to blockchain on sonic: failed to send transaction: nonce too low';

describe('sendPreparedTransaction', () => {
  it('returns the tx hash on first-try success without re-preparing', async () => {
    const prepare = jest.fn().mockResolvedValue(prepared(5));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(ok('0xhash'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(true);
    expect(res.data?.txHash).toBe('0xhash');
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it('re-prepares with a fresh nonce and retries once on "nonce too low"', async () => {
    const prepare = jest
      .fn()
      .mockResolvedValueOnce(prepared(5))
      .mockResolvedValueOnce(prepared(6));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest
      .fn()
      .mockResolvedValueOnce(fail(NONCE_ERR))
      .mockResolvedValueOnce(ok('0xhash2'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(true);
    expect(res.data?.txHash).toBe('0xhash2');
    expect(prepare).toHaveBeenCalledTimes(2);
    expect(submit).toHaveBeenCalledTimes(2);
    expect(sign).toHaveBeenLastCalledWith(expect.objectContaining({ nonce: 6 }));
  });

  it('does not retry on a non-nonce error', async () => {
    const prepare = jest.fn().mockResolvedValue(prepared(5));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail('insufficient funds for gas'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it('gives up after one retry when the nonce error persists', async () => {
    const prepare = jest.fn().mockResolvedValue(prepared(5));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail('nonce too low'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(prepare).toHaveBeenCalledTimes(2);
    expect(submit).toHaveBeenCalledTimes(2);
  });

  // Observed on Arbitrum: the gas price is priced at prepare time and the chain
  // reprices every block, so a user who pauses before confirming broadcasts a tx
  // capped below the current base fee. Re-preparing refetches the price.
  it('re-prepares with a fresh gas price and retries once on a stale base fee', async () => {
    const BASE_FEE_ERR =
      'Failed to submit transaction to blockchain on arbitrum: failed to send transaction: ' +
      'max fee per gas less than block base fee: address 0xabc, maxFeePerGas: 20006000 baseFee: 20098000';
    const prepare = jest
      .fn()
      .mockResolvedValueOnce({ success: true, data: { ...baseTx, gasPrice: '20006000' } })
      .mockResolvedValueOnce({ success: true, data: { ...baseTx, gasPrice: '25125000' } });
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest
      .fn()
      .mockResolvedValueOnce(fail(BASE_FEE_ERR))
      .mockResolvedValueOnce(ok('0xhash3'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(true);
    expect(res.data?.txHash).toBe('0xhash3');
    expect(prepare).toHaveBeenCalledTimes(2);
    expect(sign).toHaveBeenLastCalledWith(expect.objectContaining({ gasPrice: '25125000' }));
  });

  // "replacement transaction underpriced" means a tx of OURS is already pending
  // at this nonce and will mine. Re-preparing would return nonce+1 and
  // broadcast a SECOND transfer — the user's money sent twice.
  it('never retries an underpriced replacement, which would double-send', async () => {
    const prepare = jest.fn().mockResolvedValue(prepared(5));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail('replacement transaction underpriced'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  // The plain variant is safe in principle, but is not reliably distinguishable
  // from the replacement one by substring, so it is not retried either.
  it('does not retry a plain underpriced transaction', async () => {
    const prepare = jest.fn().mockResolvedValue(prepared(5));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail('transaction underpriced'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(submit).toHaveBeenCalledTimes(1);
  });

  // A gas-price rejection means the tx never entered the pool, so the nonce must
  // still be free. If re-preparing hands back a DIFFERENT nonce, a transaction
  // of ours reached the mempool after all and signing this one sends a second.
  it('abandons a gas-price retry when the nonce has moved', async () => {
    const prepare = jest
      .fn()
      .mockResolvedValueOnce(prepared(5))
      .mockResolvedValueOnce(prepared(6));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail('max fee per gas less than block base fee'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(prepare).toHaveBeenCalledTimes(2);
    // The second transaction is never signed and never broadcast.
    expect(sign).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  // A retry re-prices the same transfer. The second prepare is a fresh server
  // response that the user never approved and nothing else inspects, so a
  // destination that moved between attempts must not be signed.
  it.each([
    ['recipient', { to: '0xsomewhere-else' }],
    ['amount', { value: '999999999' }],
    ['calldata', { data: '0xdeadbeef' }],
  ])('abandons the retry when the re-prepared %s differs', async (_label, changed) => {
    const prepare = jest
      .fn()
      .mockResolvedValueOnce({ success: true, data: { ...baseTx } })
      .mockResolvedValueOnce({ success: true, data: { ...baseTx, ...changed } });
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail(NONCE_ERR));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(prepare).toHaveBeenCalledTimes(2);
    expect(sign).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it('reports the broadcast error, not the prepare error, when a retry cannot re-prepare', async () => {
    const prepare = jest
      .fn()
      .mockResolvedValueOnce(prepared(5))
      .mockResolvedValueOnce(fail('Failed to prepare transaction'));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail(NONCE_ERR));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(res.message).toBe(NONCE_ERR);
  });

  it('does not retry a revert, which re-preparing cannot cure', async () => {
    const prepare = jest.fn().mockResolvedValue(prepared(5));
    const sign = jest.fn().mockResolvedValue('0xsigned');
    const submit = jest.fn().mockResolvedValue(fail('execution reverted: 0xe450d38c'));

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it('surfaces a failed prepare without attempting to sign or submit', async () => {
    const prepare = jest.fn().mockResolvedValue(fail('Failed to prepare'));
    const sign = jest.fn();
    const submit = jest.fn();

    const res = await sendPreparedTransaction({ prepare, sign, submit, retryDelayMs: 0 });

    expect(res.success).toBe(false);
    expect(sign).not.toHaveBeenCalled();
    expect(submit).not.toHaveBeenCalled();
  });
});
