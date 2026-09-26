/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

// services/__tests__/payMerchantApi.links.test.ts
//
// The payment-link calls on the owner client: each goes to its route in the
// mode asked for, and a create carries the form's own Idempotency-Key (so a
// retry of the same form is the same request), not a fresh one per call.
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { API_BASE_URL } from '@/constants/api/config';
import {
  cancelPayLink, createOpenLink, createPayLink, getOpenLink, getPayLink, listOpenLinks, listPayLinks, payLinkModeOf,
  replaceOpenLink, updateOpenLink,
} from '../payMerchantApi';

const ok = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ success: true, data }) });
const call = (i = 0) => (global.fetch as jest.Mock).mock.calls[i] as [string, RequestInit & { headers: Record<string, string> }];

beforeEach(() => { (global as any).fetch = jest.fn(async () => ok({ id: 'p1' })); });

it('creates in the mode asked for, with the form\'s Idempotency-Key', async () => {
  const input = { amount: 500_000, description: 'Rice', expiresInDays: 7 as const };
  await createPayLink('test', input, 'form-key');
  const [url, init] = call();
  expect(url).toBe(`${API_BASE_URL}/pay/merchant/links?mode=test`);
  expect(init.method).toBe('POST');
  expect(JSON.parse(String(init.body))).toEqual(input);
  expect(init.headers['Idempotency-Key']).toBe('form-key');
});

it('lists, reads and cancels on the owner routes', async () => {
  await listPayLinks('live', 2, 50);
  expect(call(0)[0]).toBe(`${API_BASE_URL}/pay/merchant/links?mode=live&page=2&limit=50`);

  (global.fetch as jest.Mock).mockResolvedValueOnce(ok({ payment: { id: 'p1' }, payer: null, deliveries: [] }));
  await expect(getPayLink('live', 'p1')).resolves.toEqual({ id: 'p1' });
  expect(call(1)[0]).toBe(`${API_BASE_URL}/pay/merchant/payments/p1?mode=live`);

  await cancelPayLink('test', 'p1');
  expect(call(2)[0]).toBe(`${API_BASE_URL}/pay/merchant/links/p1/cancel?mode=test`);
  expect(call(2)[1].method).toBe('POST');
});

it('links are live only while live payments are allowed (TSA Pay live AND her switch)', () => {
  expect(payLinkModeOf({ liveAllowed: false })).toBe('test');
  expect(payLinkModeOf({ liveAllowed: true })).toBe('live');
});

it('reusable links: create with the form\'s key, list, read, change and replace on /open-links', async () => {
  const input = { label: 'Mama Ngozi Store', minAmount: 50_000, maxAmount: 0 };
  await createOpenLink('live', input, 'form-key');
  expect(call(0)[0]).toBe(`${API_BASE_URL}/pay/merchant/open-links?mode=live`);
  expect(call(0)[1].method).toBe('POST');
  expect(JSON.parse(String(call(0)[1].body))).toEqual(input);
  expect(call(0)[1].headers['Idempotency-Key']).toBe('form-key');

  await listOpenLinks('test');
  expect(call(1)[0]).toBe(`${API_BASE_URL}/pay/merchant/open-links?mode=test&page=1&limit=50`);

  await getOpenLink('test', 'o1');
  expect(call(2)[0]).toBe(`${API_BASE_URL}/pay/merchant/open-links/o1?mode=test`);

  await updateOpenLink('test', 'o1', { ...input, active: false }, 3);
  expect(call(3)[0]).toBe(`${API_BASE_URL}/pay/merchant/open-links/o1?mode=test`);
  expect(call(3)[1].method).toBe('PUT');
  expect(JSON.parse(String(call(3)[1].body))).toEqual({ ...input, active: false, expectedVersion: 3 });

  await replaceOpenLink('test', 'o1');
  expect(call(4)[0]).toBe(`${API_BASE_URL}/pay/merchant/open-links/o1/replace?mode=test`);
  expect(call(4)[1].method).toBe('POST');
});
