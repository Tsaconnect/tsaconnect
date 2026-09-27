/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * escrowOrder is the one approve → escrow → submit sequence (service flows and
 * the cart checkout). A retried payment must neither lose a tx it already sent
 * nor keep a dead one forever, and must stop when the order cannot be paid.
 */
const mockChain = { receipt: null as any, tx: null as any };

jest.mock('../wallet', () => ({
  __esModule: true,
  getProvider: () => ({
    getTransactionReceipt: jest.fn(async () => mockChain.receipt),
    getTransaction: jest.fn(async () => mockChain.tx),
  }),
}));

jest.mock('../transaction', () => ({
  ...jest.requireActual('../transaction'),
  signAndBroadcast: jest.fn(),
}));

jest.mock('../orderApi', () => ({
  __esModule: true,
  createServiceOrder: jest.fn(),
  prepareApprove: jest.fn(),
  prepareEscrow: jest.fn(),
  submitEscrow: jest.fn(),
}));

import { escrowOrder, OrderNotPayableError, sendTx, settleSent, type EscrowProgress } from '../serviceCheckout';
import { signAndBroadcast, TxNotConfirmedError } from '../transaction';
import { prepareApprove, prepareEscrow, submitEscrow } from '../orderApi';

const order = { id: 'order-1' } as any;
const unconfirmed = { hash: '0xsent', confirmed: false };
const ok = (data: any) => ({ success: true, data });

beforeEach(() => {
  jest.clearAllMocks();
  mockChain.receipt = null;
  mockChain.tx = null;
  (prepareApprove as jest.Mock).mockResolvedValue(ok({ approveTx: { to: '0xusdc' } }));
  (prepareEscrow as jest.Mock).mockResolvedValue(ok({ createOrderTx: { to: '0xescrow' } }));
  (submitEscrow as jest.Mock).mockResolvedValue(ok(order));
});

describe('settleSent', () => {
  test('a tx that has since mined is used, not sent again', async () => {
    mockChain.receipt = { status: 1 };
    await expect(settleSent(unconfirmed)).resolves.toEqual({ hash: '0xsent', confirmed: true });
  });

  test('a tx that reverted is forgotten, so a fresh one is sent', async () => {
    mockChain.receipt = { status: 0 };
    await expect(settleSent(unconfirmed)).resolves.toBeUndefined();
  });

  test('a tx the network dropped is forgotten', async () => {
    await expect(settleSent(unconfirmed)).resolves.toBeUndefined();
  });

  test('a tx still in flight makes the buyer wait, not re-send', async () => {
    mockChain.tx = { hash: '0xsent' };
    await expect(settleSent(unconfirmed)).rejects.toThrow('still confirming');
  });

  test('a confirmed tx is trusted without a chain read', async () => {
    mockChain.receipt = { status: 0 };
    const confirmed = { hash: '0xok', confirmed: true };
    await expect(settleSent(confirmed)).resolves.toBe(confirmed);
  });
});

test('an unconfirmed tx is recorded before the send fails', async () => {
  (signAndBroadcast as jest.Mock).mockRejectedValueOnce(new TxNotConfirmedError('0xsent'));
  const onUnconfirmed = jest.fn();
  await expect(sendTx({} as any, onUnconfirmed)).rejects.toThrow('still confirming');
  expect(onUnconfirmed).toHaveBeenCalledWith(unconfirmed);
});

test('a retry reports an escrow already sent instead of signing again', async () => {
  (signAndBroadcast as jest.Mock)
    .mockResolvedValueOnce('0xapprove')
    .mockRejectedValueOnce(new TxNotConfirmedError('0xescrow-tx'));
  const progress: EscrowProgress = {};

  await expect(escrowOrder(order, undefined, progress)).rejects.toThrow('still confirming');
  expect(progress).toEqual({
    approve: { hash: '0xapprove', confirmed: true },
    escrow: { hash: '0xescrow-tx', confirmed: false },
  });

  mockChain.receipt = { status: 1 }; // the escrow has since mined
  await escrowOrder(order, undefined, progress);
  expect(signAndBroadcast).toHaveBeenCalledTimes(2); // nothing signed on the retry
  expect(submitEscrow).toHaveBeenCalledWith('order-1', '0xapprove', '0xescrow-tx');
});

test('an order that can no longer be paid stops the retry', async () => {
  (prepareApprove as jest.Mock).mockResolvedValueOnce({
    success: false,
    code: 'order_not_payable',
    message: 'Cannot prepare escrow: order is in cancelled status',
  });
  await expect(escrowOrder(order)).rejects.toBeInstanceOf(OrderNotPayableError);
});

test('a reused approval is dropped when the escrow step then fails', async () => {
  const progress: EscrowProgress = { approve: { hash: '0xold-approve', confirmed: true } };
  (prepareEscrow as jest.Mock).mockResolvedValueOnce({ success: false, message: 'Failed to prepare escrow transaction' });

  await expect(escrowOrder(order, undefined, progress)).rejects.toThrow('Failed to prepare escrow');
  expect(progress.approve).toBeUndefined(); // the next try signs a fresh approve
});

test('a submit the server refuses for good (order cancelled) ends the retry too', async () => {
  (signAndBroadcast as jest.Mock).mockResolvedValueOnce('0xapprove').mockResolvedValueOnce('0xescrow-tx');
  (submitEscrow as jest.Mock).mockResolvedValueOnce({
    success: false,
    code: 'order_not_payable',
    message: 'Your payment reached escrow after this order was cancelled. Contact support to have it refunded.',
  });
  await expect(escrowOrder(order)).rejects.toBeInstanceOf(OrderNotPayableError);
});
