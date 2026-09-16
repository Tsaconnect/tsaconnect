// The prepare-tx boundary: what the backend returns becomes what the wallet
// signs, and — because a send can retry — what the double-send guard compares
// against. A field that quietly defaults is a guard that quietly stops working,
// so this is about failing closed rather than parsing leniently.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('test-token'),
}));

jest.mock('../wallet', () => ({ getActiveWallet: async () => '0xfrom' }));

import { prepareSendTransaction } from '../walletApi';

const mockFetch = jest.fn();
global.fetch = mockFetch as any;

const respond = (data: unknown) => {
  mockFetch.mockResolvedValueOnce({
    ok: true,
    status: 200,
    headers: { get: () => 'application/json' },
    text: async () => JSON.stringify({ success: true, data }),
    json: async () => ({ success: true, data }),
  });
};

const wholeTx = {
  to: '0xusdc',
  data: '0xa9059cbb',
  value: '0',
  gasLimit: '65000',
  gasPrice: '1100000000',
  nonce: 7,
  chainId: 8453,
};

beforeEach(() => mockFetch.mockReset());

describe('prepareSendTransaction', () => {
  it('returns the prepared transaction when every field is present', async () => {
    respond(wholeTx);

    const res = await prepareSendTransaction('USDC', '0xto', '1.5', 8453);

    expect(res.success).toBe(true);
    expect(res.data?.nonce).toBe(7);
    expect(res.data?.chainId).toBe(8453);
  });

  // Nonce 0 is a legitimate value — a wallet's first ever transaction — so it
  // must survive, which is exactly why "missing" cannot be spelled the same way.
  it('keeps a genuine zero nonce', async () => {
    respond({ ...wholeTx, nonce: 0 });

    const res = await prepareSendTransaction('USDC', '0xto', '1.5', 8453);

    expect(res.success).toBe(true);
    expect(res.data?.nonce).toBe(0);
  });

  it.each([
    ['nonce is missing', (({ nonce, ...rest }) => rest)(wholeTx)],
    ['nonce is null', { ...wholeTx, nonce: null }],
    ['nonce is not a number', { ...wholeTx, nonce: 'later' }],
    ['chainId is missing', (({ chainId, ...rest }) => rest)(wholeTx)],
    ['chainId is null', { ...wholeTx, chainId: null }],
  ])('fails rather than defaulting to zero when %s', async (_label, payload) => {
    respond(payload);

    const res = await prepareSendTransaction('USDC', '0xto', '1.5', 8453);

    // The danger is not the error — it is a success carrying a zero nonce,
    // which reads as "unchanged" to the retry's double-send guard.
    expect(res.success).toBe(false);
    expect(res.data?.nonce).toBeUndefined();
  });
});
