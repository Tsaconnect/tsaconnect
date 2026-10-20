jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));
const mockWithProof = jest.fn((call: () => Promise<unknown>) => call());
jest.mock('../walletApi', () => ({
  getAuthHeaders: jest.fn().mockResolvedValue({ 'Content-Type': 'application/json' }),
  withWalletProof: (call: () => Promise<unknown>) => mockWithProof(call),
}));

// eslint-disable-next-line import/first
import { liquidityApi } from '../liquidityApi';

const fetchMock = jest.fn();
(global as any).fetch = fetchMock;
beforeEach(() => {
  fetchMock.mockReset();
  mockWithProof.mockClear();
  fetchMock.mockResolvedValue({ ok: true, status: 200, text: () => Promise.resolve('{"success":true,"data":{"status":"none"}}') });
});

const call = (i = 0) => ({ url: fetchMock.mock.calls[i][0] as string, init: fetchMock.mock.calls[i][1] as RequestInit });

it("reads the caller's own liquidity, never by id", async () => {
  const res = await liquidityApi.me();
  expect(res.data?.status).toBe('none');
  expect(call().url).toMatch(/\/liquidity\/me$/);
});

it('pages activity by cursor', async () => {
  await liquidityApi.activity('2026-10-08T12:00:00Z');
  expect(call().url).toMatch(/\/liquidity\/me\/activity\?cursor=2026-10-08T12%3A00%3A00Z$/);
});

it('activates through the wallet-proof retry', async () => {
  await liquidityApi.activate();
  expect(mockWithProof).toHaveBeenCalledTimes(1);
  expect(call().url).toMatch(/\/liquidity\/activate$/);
  expect(call().init.method).toBe('POST');
});

it('prepares the LP txs with the amount, and the gas-free claim with none', async () => {
  await liquidityApi.prepareDeposit('12.5');
  await liquidityApi.prepareWithdraw('3');
  await liquidityApi.prepareClaim();
  await liquidityApi.claimFor();
  expect(call(0).url).toMatch(/\/liquidity\/deposit\/prepare$/);
  expect(JSON.parse(call(0).init.body as string)).toEqual({ amount: '12.5' });
  expect(call(1).url).toMatch(/\/liquidity\/withdraw\/prepare$/);
  expect(JSON.parse(call(1).init.body as string)).toEqual({ amount: '3' });
  expect(call(2).url).toMatch(/\/liquidity\/claim\/prepare$/);
  expect(call(3).url).toMatch(/\/liquidity\/claim-for$/);
});

describe('fiat liquidity', () => {
  const body = (i = 0) => JSON.parse(call(i).init.body as string);

  it('reads the multi-currency position', async () => {
    await liquidityApi.fiat();
    expect(call().url).toMatch(/\/liquidity\/fiat$/);
  });

  it('records a deposit with its currency', async () => {
    await liquidityApi.fiatDeposit('0xabc', 'KES');
    expect(call().url).toMatch(/\/liquidity\/fiat\/deposits$/);
    expect(call().init.method).toBe('POST');
    expect(body()).toEqual({ txHash: '0xabc', currency: 'KES' });
  });

  it('requests a principal exit in a currency and an earnings exit without one', async () => {
    await liquidityApi.fiatWithdraw({ kind: 'principal', currency: 'GHS', amount: '250.50' });
    await liquidityApi.fiatWithdraw({ kind: 'earnings', amount: '1.25' });
    expect(call(0).url).toMatch(/\/liquidity\/fiat\/withdrawals$/);
    expect(body(0)).toEqual({ kind: 'principal', currency: 'GHS', amount: '250.50' });
    expect(body(1)).toEqual({ kind: 'earnings', amount: '1.25' });
  });
});
