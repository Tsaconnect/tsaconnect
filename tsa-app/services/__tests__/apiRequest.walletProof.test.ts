// createApiRequest (Paycrest buy/sell, TSA Pay checkout) on WALLET_PROOF_REQUIRED:
// the device's active wallet is not the account's linked one, so it is proved
// and linked, and the call is made once more — instead of a hard 403.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import * as walletApi from '../walletApi';
import { ApiError, createApiRequest } from '../apiRequest';
import { WALLET_KEY_UNAVAILABLE, WALLET_PROOF_REQUIRED } from '../walletProof';

const refusal = {
  ok: false,
  status: 403,
  json: async () => ({ success: false, code: WALLET_PROOF_REQUIRED, message: 'Confirm you own this wallet' }),
};
const ok = { ok: true, status: 200, json: async () => ({ success: true, data: { id: 'o1' } }) };

const request = () =>
  createApiRequest({ prefix: '/paycrest', unavailableMessage: 'unavailable', networkMessage: 'network' });

describe('createApiRequest and wallet proof', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
    jest.restoreAllMocks();
  });

  it('proves the active wallet and retries once', async () => {
    const link = jest.spyOn(walletApi, 'linkActiveWallet').mockResolvedValue({ success: true });
    (global.fetch as jest.Mock).mockResolvedValueOnce(refusal).mockResolvedValueOnce(ok);

    await expect(request()('/buy', { method: 'POST', body: '{}' })).resolves.toEqual({ id: 'o1' });
    expect(link).toHaveBeenCalledTimes(1);
    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  it('does not loop when the retry is refused too', async () => {
    jest.spyOn(walletApi, 'linkActiveWallet').mockResolvedValue({ success: true });
    (global.fetch as jest.Mock).mockResolvedValue(refusal);

    await expect(request()('/buy', { method: 'POST' })).rejects.toMatchObject({ code: WALLET_PROOF_REQUIRED });
    expect(global.fetch).toHaveBeenCalledTimes(2);
  });

  it("throws the linking failure's own message (e.g. no key on this device)", async () => {
    jest.spyOn(walletApi, 'linkActiveWallet').mockResolvedValue({
      success: false,
      code: WALLET_KEY_UNAVAILABLE,
      message: "This wallet's key isn't on this device",
    });
    (global.fetch as jest.Mock).mockResolvedValue(refusal);

    const err = (await request()('/buy', { method: 'POST' }).catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe(WALLET_KEY_UNAVAILABLE);
    expect(err.message).toMatch(/key isn't on this device/);
    expect(global.fetch).toHaveBeenCalledTimes(1);
  });
});
