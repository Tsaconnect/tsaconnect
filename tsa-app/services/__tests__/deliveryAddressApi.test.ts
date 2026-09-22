/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => 'jwt'), setItem: jest.fn(async () => null) },
}));

import {
  createDeliveryAddress,
  deleteDeliveryAddress,
  formatDeliveryAddress,
  listDeliveryAddresses,
  updateDeliveryAddress,
  type DeliveryAddressInput,
} from '../deliveryAddressApi';

const fetchMock = jest.fn();
beforeAll(() => {
  (global as any).fetch = fetchMock;
});
afterEach(() => fetchMock.mockReset());

const respond = (status: number, body: unknown) =>
  fetchMock.mockResolvedValueOnce({ ok: status < 400, status, statusText: '', json: async () => body });

const input: DeliveryAddressInput = {
  label: 'Home',
  recipientName: 'Ada Obi',
  phone: '+2348012345678',
  street: '12 Allen Avenue',
  city: 'Ikeja',
  state: 'Lagos',
  country: 'Nigeria',
  isDefault: true,
};

it('lists the saved addresses', async () => {
  respond(200, { success: true, data: [{ id: 'a1', ...input }] });
  const res = await listDeliveryAddresses();

  expect(fetchMock.mock.calls[0][0]).toMatch(/\/delivery-addresses$/);
  expect(res.data?.[0].id).toBe('a1');
});

it('creates, updates and deletes by id', async () => {
  respond(201, { success: true, data: { id: 'a1', ...input } });
  await createDeliveryAddress(input);
  expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'POST' });
  expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual(input);

  respond(200, { success: true, data: { id: 'a1', ...input } });
  await updateDeliveryAddress('a1', input);
  expect(fetchMock.mock.calls[1][0]).toMatch(/\/delivery-addresses\/a1$/);
  expect(fetchMock.mock.calls[1][1]).toMatchObject({ method: 'PUT' });

  respond(200, { success: true, data: null });
  await deleteDeliveryAddress('a1');
  expect(fetchMock.mock.calls[2][0]).toMatch(/\/delivery-addresses\/a1$/);
  expect(fetchMock.mock.calls[2][1]).toMatchObject({ method: 'DELETE' });
});

it('surfaces the server message on a refusal', async () => {
  respond(400, { success: false, message: 'Missing phone' });
  const res = await createDeliveryAddress({ ...input, phone: '' });

  expect(res).toMatchObject({ success: false, message: 'Missing phone' });
});

it('formats an address on one line, skipping blanks', () => {
  expect(formatDeliveryAddress({ street: '12 Allen Avenue', city: 'Ikeja', state: '', country: 'Nigeria' })).toBe(
    '12 Allen Avenue, Ikeja, Nigeria',
  );
});
