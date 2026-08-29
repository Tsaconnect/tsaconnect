// services/__tests__/itStableApi.test.ts
//
// Transport-level tests for the stablecoin sell endpoints. Mirrors the
// approach in services/__tests__/lifiFlow.test.ts: stub global.fetch, assert
// on what we sent and how we interpreted the reply.
//
// The AsyncStorage mock is NOT optional. itApi imports getAuthHeaders from
// walletApi, which calls AsyncStorage.getItem; there is no `setupFiles` in
// this repo's jest config, so without the mock getAuthHeaders throws, req's
// catch swallows it, fetch is never called, and every assertion fails on
// mock.calls[0] being undefined — a TypeError masking whatever the test meant
// to prove. Same line as services/__tests__/sendRetry.test.ts:1-3.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import {
  quoteStableSell,
  createStableSell,
  reportStableDeposit,
} from '../itApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
  text: async () => JSON.stringify({ success: true, data }),
});

const errJson = (status: number, message: string, code?: string) => ({
  ok: false,
  status,
  json: async () => ({ success: false, message, code }),
  text: async () => JSON.stringify({ success: false, message, code }),
});

describe('stablecoin sell transport', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('sends an Idempotency-Key when creating a sell', async () => {
    // Every intent is a claim on a deposit, so a retried create must not
    // silently make a second one.
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({
        intent: { id: 'i1' },
        deposit: {
          chain: 'POLYGON',
          address: '0xabc',
          asset: 'USDC',
          // The contract the backend will verify the arrival against. The app
          // never resolves this itself — see StableDeposit.tokenAddress.
          tokenAddress: '0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359',
          amount: '100.000000',
        },
      })
    );

    await createStableSell({
      asset: 'USDC',
      amount: '100',
      currency: 'NGN',
      sourceChain: 'BSC',
      bank: { code: '044', number: '0690000031', name: 'Ada Okafor' },
    });

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(init.headers['Idempotency-Key']).toBeTruthy();
  });

  it('reuses one idempotency key across retries of the same create', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ intent: { id: 'i1' }, deposit: {} }));
    const body = {
      asset: 'USDC' as const,
      amount: '100',
      currency: 'NGN',
      sourceChain: 'BSC',
      bank: { code: '044', number: '0690000031', name: 'Ada Okafor' },
      idempotencyKey: 'fixed-key-1',
    };

    await createStableSell(body);
    await createStableSell(body);

    const first = (global.fetch as jest.Mock).mock.calls[0][1].headers['Idempotency-Key'];
    const second = (global.fetch as jest.Mock).mock.calls[1][1].headers['Idempotency-Key'];
    expect(first).toBe('fixed-key-1');
    expect(second).toBe('fixed-key-1');
  });

  it('surfaces the HTTP status so callers can tell 400 from 503', async () => {
    // A 400 is the user's to fix; a 503 is worth retrying. Without the status
    // the screen cannot tell them apart.
    (global.fetch as jest.Mock).mockResolvedValue(errJson(503, 'pricing unavailable'));
    const r = await quoteStableSell({ asset: 'USDC', amount: '100' });
    expect(r.success).toBe(false);
    expect(r.status).toBe(503);
  });

  it('surfaces the machine code for an already-claimed deposit', async () => {
    // This is the front-running case reaching the UI; the screen branches on
    // the code rather than matching message text.
    (global.fetch as jest.Mock).mockResolvedValue(
      errJson(400, 'this deposit has already been reported', 'stable_deposit_already_claimed')
    );
    const r = await reportStableDeposit('i1', '0xdeadbeef');
    expect(r.success).toBe(false);
    expect(r.code).toBe('stable_deposit_already_claimed');
  });

  it('posts the deposit hash to the intent-scoped path', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ recorded: true }));
    await reportStableDeposit('abc-123', '0xfeed');
    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(String(url)).toContain('/it/stable/sell/abc-123/deposit');
    expect(JSON.parse(init.body)).toEqual({ txHash: '0xfeed' });
  });
});
