// services/__tests__/signPreparedTransaction.test.ts
//
// A transfer the server prepared is signed as a legacy transaction with exactly its own
// fields, by the wallet named — the one shape the Paycrest deposit and the refund share.
const mockSign = jest.fn();
jest.mock('../wallet', () => ({ signTransaction: (...a: any[]) => mockSign(...a) }));

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { signPreparedTransaction } from '../signPreparedTransaction';

it('signs the prepared fields as a legacy transaction, with the named wallet', async () => {
  mockSign.mockResolvedValue('0xsigned');
  const tx = { to: '0xtoken', data: '0xa9059cbb', value: '0', gasLimit: '60000', gasPrice: '1000000000', nonce: 7, chainId: 56 };
  await expect(signPreparedTransaction({ ...tx, extra: 'ignored' } as typeof tx, '0xfrom')).resolves.toBe('0xsigned');
  expect(mockSign).toHaveBeenCalledWith({ type: 0, ...tx }, '0xfrom');
});
