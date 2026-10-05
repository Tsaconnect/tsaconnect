// The PM activation contract between the app and GET /p2p/pm/me + POST
// /p2p/pm/activate. Under central inbound the API mints no virtual account and
// says so with activationNeedsId=false; the app must then neither ask for nor
// send a BVN/NIN.

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../walletApi', () => ({
  getAuthHeaders: jest.fn().mockResolvedValue({ 'Content-Type': 'application/json' }),
}));

// eslint-disable-next-line import/first
import { activatePmAccount, getMyPmStatus } from '../p2p';

const fetchMock = jest.fn();
(global as any).fetch = fetchMock;

function respond(data: unknown) {
  fetchMock.mockResolvedValueOnce({
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify({ success: true, message: 'ok', data })),
    json: () => Promise.resolve({ success: true, message: 'ok', data }),
  });
}

beforeEach(() => fetchMock.mockReset());

describe('getMyPmStatus', () => {
  it('passes through activationNeedsId=false from central inbound', async () => {
    respond({ isPm: false, status: 'approved', activationNeedsId: false });
    await expect(getMyPmStatus()).resolves.toEqual({
      isPm: false,
      status: 'approved',
      activationNeedsId: false,
      payoutFees: null,
    });
  });

  it('passes through the payout fee schedule under central inbound', async () => {
    const payoutFees = {
      fundingFeeBps: 140,
      transferFeeTiers: [{ upToNgn: null, feeNgn: '53.75' }],
      minPayoutNgn: '100.00',
    };
    respond({ isPm: true, status: 'active', activationNeedsId: false, payoutFees });
    const s = await getMyPmStatus();
    expect(s.payoutFees).toEqual(payoutFees);
  });

  it('has no payout fees when the API sends none', async () => {
    respond({ isPm: true, status: 'active' });
    const s = await getMyPmStatus();
    expect(s.payoutFees).toBeNull();
  });

  it('assumes an id is needed when an older API omits the field', async () => {
    respond({ isPm: false, status: 'approved' });
    const s = await getMyPmStatus();
    expect(s.activationNeedsId).toBe(true);
  });

  it('assumes an id is needed when the request fails', async () => {
    fetchMock.mockRejectedValueOnce(new Error('offline'));
    const s = await getMyPmStatus();
    expect(s.activationNeedsId).toBe(true);
  });
});

describe('activatePmAccount', () => {
  it('sends no id when called without one', async () => {
    respond({ status: 'active' });
    await activatePmAccount();
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({});
  });

  it('still sends the id in virtual-account mode', async () => {
    respond({ status: 'active' });
    await activatePmAccount('BVN', ' 22233344455 ');
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({ idType: 'BVN', idNumber: '22233344455' });
  });
});
