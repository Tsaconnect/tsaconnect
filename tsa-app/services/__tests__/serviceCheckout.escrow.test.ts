/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A retried checkout must neither lose a tx it already sent nor keep a dead
 * one forever. A tx recorded on a confirmation timeout is settled against the
 * chain before it is trusted.
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

import { sendTx, settleSent } from '../checkoutTx';
import { signAndBroadcast, TxConfirmationTimeoutError } from '../transaction';

const unconfirmed = { hash: '0xsent', confirmed: false };

beforeEach(() => {
  mockChain.receipt = null;
  mockChain.tx = null;
});

test('a timed-out tx that has since mined is used, not sent again', async () => {
  mockChain.receipt = { status: 1 };
  await expect(settleSent(unconfirmed)).resolves.toEqual({ hash: '0xsent', confirmed: true });
});

test('a timed-out tx that reverted is forgotten, so a fresh one is sent', async () => {
  mockChain.receipt = { status: 0 };
  await expect(settleSent(unconfirmed)).resolves.toBeUndefined();
});

test('a timed-out tx the network dropped is forgotten', async () => {
  await expect(settleSent(unconfirmed)).resolves.toBeUndefined();
});

test('a timed-out tx still in flight makes the buyer wait, not re-send', async () => {
  mockChain.tx = { hash: '0xsent' };
  await expect(settleSent(unconfirmed)).rejects.toThrow('still confirming');
});

test('a confirmed tx is trusted without a chain read', async () => {
  mockChain.receipt = { status: 0 };
  const confirmed = { hash: '0xok', confirmed: true };
  await expect(settleSent(confirmed)).resolves.toBe(confirmed);
});

test('a confirmation timeout records the sent tx before failing', async () => {
  (signAndBroadcast as jest.Mock).mockRejectedValueOnce(new TxConfirmationTimeoutError('0xsent'));
  const onTimeout = jest.fn();
  await expect(sendTx({} as any, onTimeout)).rejects.toThrow('still confirming');
  expect(onTimeout).toHaveBeenCalledWith(unconfirmed);
});
