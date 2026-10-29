// services/__tests__/adPublish.test.ts — createAndSignAd, the create → sign →
// activate path shared by create-ad and the LP stock ad.
const mockCreate = jest.fn();
const mockSign = jest.fn();
jest.mock('../p2p', () => ({ adsApi: { create: (...a: any[]) => mockCreate(...a), sign: (...a: any[]) => mockSign(...a) } }));
const mockSignTypedData = jest.fn();
jest.mock('../wallet', () => ({ signTypedData: (...a: any[]) => mockSignTypedData(...a) }));
const mockEnsureReleaser = jest.fn();
jest.mock('../pmReleaser', () => ({
  PM_RELEASER_NOT_SET: 'pm_releaser_not_set',
  ensurePMReleaser: (...a: any[]) => mockEnsureReleaser(...a),
}));

// eslint-disable-next-line import/first
import { createAndSignAd } from '../adPublish';
// eslint-disable-next-line import/first
import { P2PCopyError } from '../p2pErrors';

const typedData = { domain: { chainId: 97 }, types: {}, message: { maxAmount: '500000000000000000000000' }, primaryType: 'AdVoucher' };
const payload = { token: 'USDT', tokenAmount: '1', fiatCurrency: 'NGN', paymentMethodIds: [], sellerAddress: '0xabc', chainId: 97 };
const statusError = (s?: number, m?: string) => `E${s}:${m}`;

beforeEach(() => jest.resetAllMocks());

it('creates, signs with the declared wallet, and requires an active ad', async () => {
  mockCreate.mockResolvedValue({ success: true, data: { ad: { id: 'ad1' }, typedData } });
  mockSignTypedData.mockResolvedValue('0xsig');
  mockSign.mockResolvedValue({ success: true, data: { id: 'ad1', status: 'active' } });
  const ad = await createAndSignAd({ payload, sellerAddress: '0xabc', statusError });
  expect(ad.status).toBe('active');
  expect(mockSignTypedData).toHaveBeenCalledWith(typedData.domain, typedData.types, typedData.message, '0xabc');
  expect(mockSign).toHaveBeenCalledWith('ad1', '0xsig', '0xabc');
});

it('surfaces the server status as the error', async () => {
  mockCreate.mockResolvedValue({ success: false, status: 409, message: 'stock ad exists' });
  await expect(createAndSignAd({ payload, sellerAddress: '0xabc', statusError })).rejects.toThrow('E409:stock ad exists');
  expect(mockSignTypedData).not.toHaveBeenCalled();
});

it('surfaces a sign refusal as the error', async () => {
  mockCreate.mockResolvedValue({ success: true, data: { ad: { id: 'ad1' }, typedData } });
  mockSignTypedData.mockResolvedValue('0xsig');
  mockSign.mockResolvedValue({ success: false, status: 503, message: 'pool closed' });
  await expect(createAndSignAd({ payload, sellerAddress: '0xabc', statusError })).rejects.toThrow('E503:pool closed');
});

it('refuses an ad the server left pending', async () => {
  mockCreate.mockResolvedValue({ success: true, data: { ad: { id: 'ad1' }, typedData } });
  mockSignTypedData.mockResolvedValue('0xsig');
  mockSign.mockResolvedValue({ success: true, data: { id: 'ad1', status: 'pending' } });
  await expect(createAndSignAd({ payload, sellerAddress: '0xabc', statusError })).rejects.toThrow(/pending/);
});

it('keeps the refusal data and hands the whole response to statusError (rate_out_of_band min/max)', async () => {
  const refusal = {
    success: false, status: 400, code: 'rate_out_of_band', message: 'p2p: rate out of band',
    data: { referenceRate: '1500', minRate: '1200', maxRate: '1800', bandBps: 2000 },
  };
  mockCreate.mockResolvedValue(refusal);
  const seen = jest.fn((s?: number, m?: string) => `E${s}:${m}`);
  await expect(createAndSignAd({ payload, sellerAddress: '0xabc', statusError: seen })).rejects.toMatchObject({
    code: 'rate_out_of_band', status: 400, data: refusal.data,
  });
  expect(seen).toHaveBeenCalledWith(400, 'p2p: rate out of band', refusal);
});

it('keeps the KYC refusal code on the error so the screen can prompt Verify', async () => {
  mockCreate.mockResolvedValue({ success: false, status: 403, code: 'kyc_verification_required', message: 'Identity verification required' });
  await expect(createAndSignAd({ payload, sellerAddress: '0xabc', statusError })).rejects.toMatchObject({ code: 'kyc_verification_required', status: 403 });
});

it('throws statusError copy as a P2PCopyError, so screens show it as-is', async () => {
  mockCreate.mockResolvedValue({ success: false, status: 400, message: 'signature recovered to 0x1, expected 0x2' });
  await expect(createAndSignAd({ payload, sellerAddress: '0xabc', statusError })).rejects.toBeInstanceOf(P2PCopyError);
});

it('lets a payment merchant name the release key, then activates the same voucher', async () => {
  mockCreate.mockResolvedValue({ success: true, data: { ad: { id: 'ad1' }, typedData } });
  mockSignTypedData.mockResolvedValue('0xsig');
  mockSign
    .mockResolvedValueOnce({ success: false, status: 409, code: 'pm_releaser_not_set', message: 'not set' })
    .mockResolvedValueOnce({ success: true, data: { id: 'ad1', status: 'active' } });
  mockEnsureReleaser.mockResolvedValue(undefined);
  const ad = await createAndSignAd({ payload, sellerAddress: '0xabc', statusError });
  expect(ad.status).toBe('active');
  expect(mockEnsureReleaser).toHaveBeenCalledWith(expect.objectContaining({ walletAddress: '0xabc', chainId: 97 }));
  expect(mockSignTypedData).toHaveBeenCalledTimes(1);
  expect(mockSign).toHaveBeenNthCalledWith(2, 'ad1', '0xsig', '0xabc');
});

it('never asks a non-merchant for the releaser', async () => {
  mockCreate.mockResolvedValue({ success: true, data: { ad: { id: 'ad1' }, typedData } });
  mockSignTypedData.mockResolvedValue('0xsig');
  mockSign.mockResolvedValue({ success: false, status: 409, code: 'p2p_direct_moved', message: 'moved' });
  await expect(createAndSignAd({ payload, sellerAddress: '0xabc', statusError })).rejects.toThrow('E409:moved');
  expect(mockEnsureReleaser).not.toHaveBeenCalled();
});
