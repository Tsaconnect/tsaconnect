jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../walletApi', () => ({
  getAuthHeaders: jest.fn().mockResolvedValue({ 'Content-Type': 'application/json' }),
}));

// eslint-disable-next-line import/first
import { chainsApi, p2pTokenDecimals } from '../p2p';

const fetchMock = jest.fn();
(global as any).fetch = fetchMock;
const respond = (data: unknown) =>
  fetchMock.mockResolvedValueOnce({ ok: true, status: 200, text: () => Promise.resolve(JSON.stringify({ success: true, data })) });

const CHAINS = [
  { chainId: 146, assets: ['USDT'], purpose: 'p2p', decimals: { USDT: 6 } },
  { chainId: 4242, assets: ['USDT'], purpose: 'pool', decimals: { USDT: 18 } },
];

beforeEach(() => fetchMock.mockReset());

describe('chain decimals from the API', () => {
  it('chainsApi.list drops pool chains but records their decimals', async () => {
    respond(CHAINS);
    const res = await chainsApi.list();
    expect(res.data?.map((c) => c.chainId)).toEqual([146]);
    expect(p2pTokenDecimals(4242, 'usdt')).toBe(18);
  });

  it('listAll keeps the pool chain (the reclaim screen)', async () => {
    respond(CHAINS);
    const res = await chainsApi.listAll();
    expect(res.data?.map((c) => c.chainId)).toEqual([146, 4242]);
  });

  it('p2pTokenDecimals prefers API decimals over the symbol table', async () => {
    respond([{ chainId: 97, assets: ['USDT'], purpose: 'pool', decimals: { USDT: 18 } }, { chainId: 999, assets: ['USDC'], decimals: { USDC: 18 } }]);
    await chainsApi.listAll();
    expect(p2pTokenDecimals(999, 'USDC')).toBe(18); // the table would say 6
    expect(p2pTokenDecimals(14601, 'USDT')).toBe(6); // never reported: the table
  });
});
