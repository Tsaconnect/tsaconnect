// A TSA Pay swap that landed short comes back from the payout order as a 409
// with the machine code `pay_shortfall` and how much USDC is missing. The app
// keys on the code and shows the figure — it never parses the sentence.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { getOrder, PaycrestError } from '../paycrestApi';

const fail = (body: Record<string, unknown>, status = 409) => ({
  ok: false,
  status,
  json: async () => ({ success: false, ...body }),
});

beforeEach(() => {
  (global as any).fetch = jest.fn();
});

it('carries missingUsdc from the response body', async () => {
  (global.fetch as jest.Mock).mockResolvedValue(
    fail({ message: 'The swap landed short', code: 'pay_shortfall', missingUsdc: '0.42' }),
  );
  await expect(getOrder('o1')).rejects.toMatchObject({ status: 409, code: 'pay_shortfall', missingUsdc: '0.42' });
});

it('reads it from data too, when the server nests it', async () => {
  (global.fetch as jest.Mock).mockResolvedValue(
    fail({ message: 'short', code: 'pay_shortfall', data: { missingUsdc: '1.5' } }),
  );
  await expect(getOrder('o1')).rejects.toMatchObject({ missingUsdc: '1.5' });
});

it('is absent on any other error, and can be built directly', async () => {
  (global.fetch as jest.Mock).mockResolvedValue(fail({ message: 'no', code: 'rate_moved' }));
  const e = await getOrder('o1').catch((x) => x);
  expect(e).toBeInstanceOf(PaycrestError);
  expect(e.missingUsdc).toBeUndefined();
  expect(new PaycrestError('m', 409, 'pay_shortfall', '2').missingUsdc).toBe('2');
});
