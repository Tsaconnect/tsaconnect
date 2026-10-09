// Wire shapes the landed API fixes (docs/plans/p2p-launch/sdd/app-constraints.md).
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));
jest.mock('../walletApi', () => ({ getAuthHeaders: jest.fn(async () => ({ 'Content-Type': 'application/json' })) }));
jest.mock('../../constants/api/config', () => ({ API_BASE_URL: 'https://api.test/api' }));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { adsApi, gasApi, maskedAccountLabel, SONIC_CHAIN_IDS, tradesApi } from '../p2p';

const fetchMock = jest.fn();
beforeEach(() => {
  fetchMock.mockReset();
  fetchMock.mockResolvedValue({ status: 200, ok: true, text: async () => JSON.stringify({ success: true, data: {} }) });
  (global as any).fetch = fetchMock;
});

function lastCall() {
  const [url, init] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1];
  return { url, method: init.method, headers: init.headers, body: init.body ? JSON.parse(init.body) : undefined };
}

describe('mark-paid on chain (R1)', () => {
  it('prepares at /mark-paid/prepare with the proof and the paying wallet', async () => {
    await tradesApi.prepareMarkPaid('t-1', 'https://img.test/p.jpg', '0xBUYER');
    const c = lastCall();
    expect(c).toMatchObject({
      url: 'https://api.test/api/p2p/trades/t-1/mark-paid/prepare',
      method: 'POST',
      body: { proofImageUrl: 'https://img.test/p.jpg', callerAddress: '0xBUYER' },
    });
    expect(c.headers['Idempotency-Key']).toBeTruthy();
  });

  it('confirms with the broadcast hash', async () => {
    await tradesApi.confirmMarkPaid('t-1', '0xabc');
    expect(lastCall()).toMatchObject({
      url: 'https://api.test/api/p2p/trades/t-1/mark-paid/confirm',
      method: 'POST',
      body: { txHash: '0xabc' },
    });
  });

  it('no longer exposes the off-chain markPaid', () => {
    expect((tradesApi as any).markPaid).toBeUndefined();
  });
});

describe('gas top-up (R3)', () => {
  it('posts the action with the trade it is for', async () => {
    await gasApi.topUp({ action: 'release', tradeId: 't-1' });
    expect(lastCall()).toMatchObject({
      url: 'https://api.test/api/p2p/gas/top-up',
      method: 'POST',
      body: { action: 'release', tradeId: 't-1' },
    });
  });
  it('posts the ad id for open_trade and omits absent ids', async () => {
    await gasApi.topUp({ action: 'open_trade', adId: 'a-1' });
    expect(lastCall().body).toEqual({ action: 'open_trade', adId: 'a-1' });
    await gasApi.topUp({ action: 'approve' });
    expect(lastCall().body).toEqual({ action: 'approve' });
  });
  it('serves only Sonic mainnet-or-testnet ids', () => {
    expect(SONIC_CHAIN_IDS.has(146)).toBe(true);
    expect(SONIC_CHAIN_IDS.has(56)).toBe(false);
  });
});

describe('ad cancel (buy-ad has no tx)', () => {
  it('passes a null unsignedTx through untouched', async () => {
    fetchMock.mockResolvedValueOnce({ status: 200, ok: true, text: async () => JSON.stringify({ success: true, data: { ad: { id: 'a' }, unsignedTx: null } }) });
    const res = await adsApi.prepareCancel('a', '0xS');
    expect(res.data?.unsignedTx).toBeNull();
  });
});

describe('maskedAccountLabel (R2)', () => {
  it('uses last4 from the masked public method (no accountName any more)', () => {
    expect(maskedAccountLabel({ accountNumber: '******7890', accountNumberLast4: '7890' })).toBe('••••7890');
  });
  it('derives last4 from a masked number when last4 is missing', () => {
    expect(maskedAccountLabel({ accountNumber: '******7890' })).toBe('••••7890');
  });
  it('keeps the holder name for the user’s own methods', () => {
    expect(maskedAccountLabel({ accountName: 'Ada Obi', accountNumber: '0123456789' })).toBe('Ada Obi · ••••6789');
  });
  it('renders nothing rather than "undefined"', () => {
    expect(maskedAccountLabel({})).toBe('');
  });
});
