/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * MCGP orders are paid from Spendable MCGP — one backend call, no approve, no
 * escrow tx, nothing signed — while stablecoin orders keep the escrow flow.
 */
jest.mock('../wallet', () => ({ __esModule: true, getProvider: jest.fn() }));

jest.mock('../transaction', () => ({
  ...jest.requireActual('../transaction'),
  signAndBroadcast: jest.fn(),
}));

jest.mock('../orderApi', () => ({
  __esModule: true,
  createServiceOrder: jest.fn(),
  paySpendable: jest.fn(),
  prepareApprove: jest.fn(),
  prepareEscrow: jest.fn(),
  submitEscrow: jest.fn(),
}));

import { OrderNotPayableError, PaymentPendingError, payOrder } from '../serviceCheckout';
import { signAndBroadcast } from '../transaction';
import { paySpendable, prepareApprove, prepareEscrow, submitEscrow } from '../orderApi';

const mcgpOrder = { id: 'order-mcgp', token: 'MCGP' } as any;
const usdcOrder = { id: 'order-usdc', token: 'USDC' } as any;

beforeEach(() => {
  jest.clearAllMocks();
  (prepareApprove as jest.Mock).mockResolvedValue({ success: true, data: { approveTx: { to: '0xusdc' } } });
  (prepareEscrow as jest.Mock).mockResolvedValue({ success: true, data: { createOrderTx: { to: '0xescrow' } } });
  (submitEscrow as jest.Mock).mockResolvedValue({ success: true, data: usdcOrder });
  (signAndBroadcast as jest.Mock).mockResolvedValue('0xhash');
});

test('an MCGP order is held from Spendable MCGP with nothing signed', async () => {
  (paySpendable as jest.Mock).mockResolvedValue({
    success: true,
    data: { orderId: 'order-mcgp', status: 'escrowed', paymentSource: 'spendable_mcgp' },
  });
  const phases: string[] = [];
  const paid = await payOrder(mcgpOrder, (p) => phases.push(p));

  expect(paySpendable).toHaveBeenCalledWith('order-mcgp');
  expect(prepareApprove).not.toHaveBeenCalled();
  expect(prepareEscrow).not.toHaveBeenCalled();
  expect(signAndBroadcast).not.toHaveBeenCalled();
  expect(phases).toEqual(['paying_spendable', 'done']);
  expect(paid).toMatchObject({ status: 'escrowed', paymentSource: 'spendable_mcgp' });
});

test('a stablecoin order keeps the approve → escrow → submit flow', async () => {
  await payOrder(usdcOrder);
  expect(paySpendable).not.toHaveBeenCalled();
  expect(prepareApprove).toHaveBeenCalledWith('order-usdc');
  expect(signAndBroadcast).toHaveBeenCalledTimes(2);
  expect(submitEscrow).toHaveBeenCalledWith('order-usdc', '0xhash', '0xhash');
});

test('a shortfall surfaces the server message', async () => {
  (paySpendable as jest.Mock).mockResolvedValue({
    success: false,
    code: 'insufficient_balance',
    message: 'Not enough Spendable MCGP. This order needs 40 MCGP and your Spendable MCGP balance is 10 MCGP.',
  });
  await expect(payOrder(mcgpOrder)).rejects.toThrow('Not enough Spendable MCGP');
});

test('an order that can no longer be paid stops retries', async () => {
  (paySpendable as jest.Mock).mockResolvedValue({ success: false, code: 'order_not_payable', message: 'gone' });
  await expect(payOrder(mcgpOrder)).rejects.toBeInstanceOf(OrderNotPayableError);
});

test('a payment still confirming is reported as pending, not failed', async () => {
  (paySpendable as jest.Mock).mockResolvedValue({ success: false, code: 'payment_pending', message: 'confirming' });
  await expect(payOrder(mcgpOrder)).rejects.toBeInstanceOf(PaymentPendingError);
});
