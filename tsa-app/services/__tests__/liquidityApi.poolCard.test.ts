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

it('reads the pinned pool card for naira', async () => {
  fetchMock.mockResolvedValue({ ok: true, status: 200, text: () => Promise.resolve('{"success":true,"data":{"enabled":true,"available":false}}') });
  const res = await liquidityApi.poolCard();
  expect(res.data?.enabled).toBe(true);
  expect(res.data?.available).toBe(false);
  expect(fetchMock.mock.calls[0][0]).toMatch(/\/p2p\/pool-card\?currency=NGN$/);
});
