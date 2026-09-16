jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { createStableBuy, getStableBuyChains, quoteStableBuy } from '../itApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
});

describe('stablecoin buy API', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('quotes with minor units and reads the indicative flag', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({
        asset: 'USDC',
        currency: 'NGN',
        fiatMinor: '16502500',
        grossUnits: '100000000',
        assetUnits: '98450000',
        ratePerUnit: '167623',
        expiresAt: '2026-08-31T00:00:00Z',
        indicative: true,
      })
    );

    const res = await quoteStableBuy('USDC', '16502500', 'NGN');
    expect(res.success).toBe(true);
    expect(res.data?.assetUnits).toBe('98450000');
    // The screen keys off this rather than assuming the quote is soft.
    expect(res.data?.indicative).toBe(true);

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({
      asset: 'USDC',
      amount: '16502500',
      currency: 'NGN',
    });
  });

  it('reads bankName, not bank, from payInto', async () => {
    // ITPayTo (the MCGP type) calls this field `bank`. The stablecoin buy
    // endpoint returns `bankName`. Reusing the wrong type renders blank.
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({
        intent: { id: 'i1', direction: 'buy', status: 'pending_payment' },
        payInto: {
          accountNumber: '1234567890',
          bankName: 'Wema Bank',
          accountName: 'TSA / Ada Okafor',
          reference: 'ITVA-x',
        },
      })
    );

    const res = await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON');
    expect(res.data?.payInto.bankName).toBe('Wema Bank');
    expect(res.data?.payInto.accountNumber).toBe('1234567890');
  });

  it('sends an Idempotency-Key on create and reuses a caller-supplied one', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({ intent: { id: 'i1' }, payInto: {} })
    );

    await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON', 'fixed-key-1');
    await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON', 'fixed-key-1');

    const [, a] = (global.fetch as jest.Mock).mock.calls[0];
    const [, b] = (global.fetch as jest.Mock).mock.calls[1];
    // A retried create must not mint a second purchase.
    expect(a.headers['Idempotency-Key']).toBe('fixed-key-1');
    expect(b.headers['Idempotency-Key']).toBe('fixed-key-1');
  });

  it('asks for chains by asset', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ chains: ['POLYGON', 'SOLANA'] }));

    const res = await getStableBuyChains('USDT');
    expect(res.data?.chains).toEqual(['POLYGON', 'SOLANA']);
    const [url] = (global.fetch as jest.Mock).mock.calls[0];
    expect(String(url)).toContain('/it/stable/buy/chains?asset=USDT');
  });

  it('surfaces the backend machine code on failure', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 400,
      json: async () => ({
        success: false,
        message: 'this user already has a purchase awaiting payment',
        code: 'stable_buy_already_open',
      }),
    });

    const res = await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON');
    expect(res.success).toBe(false);
    expect(res.code).toBe('stable_buy_already_open');
    expect(res.status).toBe(400);
  });
});
