jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../walletApi', () => ({
  getAuthHeaders: jest.fn().mockResolvedValue({ 'Content-Type': 'application/json' }),
}));

// eslint-disable-next-line import/first
import { liquidityApi } from '../liquidityApi';

const fetchMock = jest.fn();
(global as any).fetch = fetchMock;
beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockResolvedValue({ ok: true, status: 200, text: () => Promise.resolve('{"success":true,"data":{"status":"none"}}') });
});

it('reads the caller\'s own liquidity, never by id', async () => {
  const res = await liquidityApi.me();
  expect(res.data?.status).toBe('none');
  expect(fetchMock.mock.calls[0][0]).toMatch(/\/liquidity\/me$/);
});

it('pages activity by type and cursor', async () => {
  await liquidityApi.activity('payouts', '2026-10-08T12:00:00Z');
  expect(fetchMock.mock.calls[0][0]).toMatch(/\/liquidity\/me\/activity\?type=payouts&cursor=2026-10-08T12%3A00%3A00Z$/);
});
