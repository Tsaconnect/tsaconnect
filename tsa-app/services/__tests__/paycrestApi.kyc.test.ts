// services/__tests__/paycrestApi.kyc.test.ts
//
// POST /paycrest/buy is money in, so the server refuses an unverified buyer
// with 403 and a machine code (kyc_verification_required, or
// kyc_legal_name_missing for a verified user with no stored legal name). The
// buy screen routes on that code through utils/kycRefusal.ts, so the transport
// must carry it on the thrown PaycrestError — the message alone is not
// something to branch on.
//
// The AsyncStorage mock is required — see paycrestApi.claim.test.ts.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { createBuy, PaycrestError } from '../paycrestApi';
import { kycRefusalFor } from '@/utils/kycRefusal';

const buyInput = {
  amount: '800',
  chain: 'base',
  token: 'USDC',
  walletAddress: '0x000000000000000000000000000000000000dEaD',
  refundBankCode: '044',
  refundAccount: '0123456789',
  currency: 'NGN',
};

const refusal = (status: number, body: unknown) => ({
  ok: false,
  status,
  json: async () => body,
  text: async () => JSON.stringify(body),
});

async function rejectionOf(p: Promise<unknown>): Promise<PaycrestError> {
  try {
    await p;
  } catch (e) {
    return e as PaycrestError;
  }
  throw new Error('expected the call to reject');
}

describe('createBuy KYC refusal', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it.each(['kyc_verification_required', 'kyc_legal_name_missing'])(
    'carries the %s code on the PaycrestError',
    async (code) => {
      (global.fetch as jest.Mock).mockResolvedValue(
        refusal(403, { success: false, message: 'server wording', code }),
      );

      const err = await rejectionOf(createBuy(buyInput));

      expect(err).toBeInstanceOf(PaycrestError);
      expect(err.status).toBe(403);
      expect(err.code).toBe(code);
      expect(err.message).toBe('server wording');
      expect(kycRefusalFor(err.code, err.message)).not.toBeNull();
    },
  );

  it('leaves code undefined for an ordinary failure, so no KYC prompt is shown', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      refusal(409, { success: false, message: 'You already have an order awaiting payment.' }),
    );

    const err = await rejectionOf(createBuy(buyInput));

    expect(err).toBeInstanceOf(PaycrestError);
    expect(err.code).toBeUndefined();
    expect(kycRefusalFor(err.code, err.message)).toBeNull();
  });
});
