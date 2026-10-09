/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

// services/__tests__/payMerchantApi.refunds.test.ts
//
// The owner's refund calls: listed in her mode, read, cancelled and submitted by refund id
// (no mode — a push names only the refund), and the signed transfer goes to the refund's
// own submit route, never /wallet/submit-tx.
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { API_BASE_URL } from '@/constants/api/config';
import { cancelPayRefund, getPayRefund, listPayRefunds, submitPayRefund } from '../payMerchantApi';

const ok = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ success: true, data }) });
const call = (i = 0) => (global.fetch as jest.Mock).mock.calls[i] as [string, RequestInit];

beforeEach(() => { (global as any).fetch = jest.fn(async () => ok({ refund: { id: 'r1', status: 'submitted' } })); });

it('lists her refunds in a mode, and reads one by id', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(ok({ refunds: [], total: 0, page: 1, limit: 50 }));
  await listPayRefunds('live');
  expect(call(0)[0]).toBe(`${API_BASE_URL}/pay/merchant/refunds?mode=live&page=1&limit=50`);

  (global.fetch as jest.Mock).mockResolvedValueOnce(ok({ refund: { id: 'r1' }, options: [] }));
  await expect(getPayRefund('r1')).resolves.toEqual({ refund: { id: 'r1' }, options: [] });
  expect(call(1)[0]).toBe(`${API_BASE_URL}/pay/merchant/refunds/r1`);
});

it('cancels and submits on the refund\'s own routes, answering the refund', async () => {
  await expect(cancelPayRefund('r1')).resolves.toEqual({ id: 'r1', status: 'submitted' });
  expect(call(0)[0]).toBe(`${API_BASE_URL}/pay/merchant/refunds/r1/cancel`);
  expect(call(0)[1].method).toBe('POST');

  await submitPayRefund('r1', { chainId: 56, tokenSymbol: 'USDT', signedTx: '0xsigned' });
  expect(call(1)[0]).toBe(`${API_BASE_URL}/pay/merchant/refunds/r1/submit`);
  expect(call(1)[1].method).toBe('POST');
  expect(JSON.parse(String(call(1)[1].body))).toEqual({ chainId: 56, tokenSymbol: 'USDT', signedTx: '0xsigned' });
  expect((global.fetch as jest.Mock).mock.calls.some(([u]) => String(u).includes('/wallet/submit-tx'))).toBe(false);
});
