/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Checkout relies on two things from the order client: the confirmed total
 * reaches the backend with the order request, and a "price_changed" refusal
 * reaches the screen as a code it can branch on (not just a message string).
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => 'jwt'), setItem: jest.fn(async () => null) },
}));

import { createOrders } from '../orderApi';

const fetchMock = jest.fn();
beforeAll(() => {
  (global as any).fetch = fetchMock;
});
afterEach(() => fetchMock.mockReset());

const respond = (status: number, body: unknown) =>
  fetchMock.mockResolvedValueOnce({ ok: status < 400, status, statusText: '', json: async () => body });

it('sends the confirmed total with the order request', async () => {
  respond(201, { success: true, data: { orders: [] } });
  await createOrders('USDC', 'Ikeja', 'Lagos', 'Nigeria', undefined, undefined, undefined, '30100000');

  const body = JSON.parse(fetchMock.mock.calls[0][1].body);
  expect(body).toMatchObject({ token: 'USDC', expectedTotal: '30100000' });
});

it('surfaces a price_changed refusal as a code', async () => {
  respond(409, { success: false, message: 'The total changed since you confirmed it.', code: 'price_changed' });
  const res = await createOrders('USDC', 'Ikeja', 'Lagos', 'Nigeria', undefined, undefined, undefined, '1');

  expect(res.success).toBe(false);
  expect(res.code).toBe('price_changed');
});

it('omits the total for callers that do not confirm one', async () => {
  respond(201, { success: true, data: { orders: [] } });
  await createOrders('USDT');

  const body = JSON.parse(fetchMock.mock.calls[0][1].body);
  expect(body.expectedTotal).toBeUndefined();
});

it('sends the picked saved address with the order request', async () => {
  respond(201, { success: true, data: { orders: [] } });
  await createOrders('USDC', 'Ikeja', 'Lagos', 'Nigeria', undefined, undefined, undefined, '1', 'addr-1');

  const body = JSON.parse(fetchMock.mock.calls[0][1].body);
  expect(body.deliveryAddressId).toBe('addr-1');
});
