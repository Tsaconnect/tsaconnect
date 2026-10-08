// services/__tests__/adPublish.test.ts — createAndSignAd, the create → sign →
// activate path shared by create-ad and the LP stock ad.
const mockCreate = jest.fn();
const mockSign = jest.fn();
jest.mock('../p2p', () => ({ adsApi: { create: (...a: any[]) => mockCreate(...a), sign: (...a: any[]) => mockSign(...a) } }));
const mockSignTypedData = jest.fn();
jest.mock('../wallet', () => ({ signTypedData: (...a: any[]) => mockSignTypedData(...a) }));

// eslint-disable-next-line import/first
import { createAndSignAd } from '../adPublish';

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
