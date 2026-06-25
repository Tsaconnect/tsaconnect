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
