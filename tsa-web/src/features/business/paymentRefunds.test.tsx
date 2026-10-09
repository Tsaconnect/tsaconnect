import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/react-query';
import { fail, json, link, merchant, mountAt, ok, payment, stubFetch } from '@/test/harness';
import type { Payment, PaymentRefunds, Refund } from './api';

const HASH = `0x${'ab'.repeat(32)}`;
const PAYER = '0x2222222222222222222222222222222222222222';

const refund = (over: Partial<Refund> = {}): Refund => ({
  id: 'r1', paymentId: payment.id, mode: 'live', status: 'confirmed', amount: '12.50', cap: '15.00', credited: '12.50',
  reason: 'Flight cancelled', toAddress: PAYER, fromAddress: '0x1111111111111111111111111111111111111111',
  chainId: 56, tokenSymbol: 'USDT', txHash: HASH, requestedVia: 'web',
  createdAt: '2026-09-25T10:00:00Z', confirmedAt: '2026-09-25T10:12:00Z', ...over,
});
const refunds = (over: Partial<PaymentRefunds> = {}): PaymentRefunds => ({
  refunds: [], cap: '15.00', remaining: '15.00', refundable: true, notRefundableReason: '', ...over,
});

function stub(p: Payment, r: PaymentRefunds, created?: Refund) {
  return stubFetch({
    '/api/pay/merchant': ok(merchant),
    '/api/pay/merchant/payments': ok({ items: [p], total: 1, page: 1, limit: 20 }),
    '/api/pay/merchant/links': ok({ items: [link], total: 1, page: 1, limit: 20 }),
    [`/api/pay/merchant/payments/${p.id}`]: ok({ payment: p, payer: { name: 'Ada Obi' }, deliveries: [] }),
    [`/api/pay/merchant/payments/${link.id}`]: ok({ payment: link, payer: null, deliveries: [] }),
    [`/api/pay/merchant/payments/${p.id}/refunds`]: (_url, init) =>
      init?.method === 'POST' ? json(201, { success: true, message: 'Refund requested', data: { refund: created } }) : ok(r),
  });
}

const posts = (f: ReturnType<typeof stubFetch>) => f.mock.calls.filter(([, init]) => init?.method === 'POST');

describe('refunds in the payment drawer', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('asks for a live refund: prefilled with what is left, priced in naira, sent with an idempotency key', async () => {
    const user = userEvent.setup();
    const f = stub(payment, refunds(), refund({ status: 'requested', chainId: undefined, txHash: undefined, tokenSymbol: undefined, confirmedAt: null }));
    mountAt(`/business/live/payments/${payment.id}`);
    const drawer = await screen.findByRole('dialog');
    await user.click(await within(drawer).findByRole('button', { name: 'Refund' }));

    const dialog = await screen.findByRole('dialog', { name: 'Refund this payment' });
    // The same rule the docs state: the merchant pays, up to what the customer paid (fee included), to their TSA wallet.
    expect(dialog).toHaveAccessibleDescription(
      "You, the merchant, pay it from your own TSA wallet in the TSA Connect app, in USDT or USDC, to your customer's TSA Connect wallet. " +
        'You can refund in part or in full, up to what your customer actually paid (which includes any fee they paid); TSA does not return its own fee.');
    const amount = within(dialog).getByLabelText('Amount (USD)');
    expect(amount).toHaveValue('15.00');
    expect(within(dialog).getByText(/about ₦85,353\.54 at this payment's rate/)).toBeInTheDocument();
    await user.clear(amount);
    await user.type(amount, '12.5');
    expect(within(dialog).getByText(/about ₦71,127\.95/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Reason (optional)'), 'Flight cancelled');
    await user.click(within(dialog).getByRole('button', { name: 'Request refund' }));

    await waitFor(() => expect(posts(f)).toHaveLength(1));
    const [url, init] = posts(f)[0];
    expect(url).toBe(`/api/pay/merchant/payments/${payment.id}/refunds?mode=live`);
    expect(JSON.parse(init.body)).toEqual({ amount: '12.50', reason: 'Flight cancelled' });
    expect(init.headers['Idempotency-Key']).toBeTruthy();
    expect(await screen.findByText('Refund requested — approve and send it in the TSA Connect app')).toBeInTheDocument();
  });

  // Review Focus 3.
  it('refuses more than is left, or a malformed amount, without sending anything', async () => {
    const user = userEvent.setup();
    const f = stub(payment, refunds());
    mountAt(`/business/live/payments/${payment.id}`);
    await user.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'Refund' }));
    const dialog = await screen.findByRole('dialog', { name: 'Refund this payment' });
    const amount = within(dialog).getByLabelText('Amount (USD)');
    await user.clear(amount);
    await user.type(amount, '20');
    await user.click(within(dialog).getByRole('button', { name: 'Request refund' }));
    expect(within(dialog).getByText('You can refund up to 15.00 USD.')).toBeInTheDocument();
    await user.clear(amount);
    await user.type(amount, '1,000');
    expect(within(dialog).getByText('Enter an amount in US dollars, like 12.50 (up to 6 decimal places).')).toBeInTheDocument();
    expect(posts(f)).toHaveLength(0);
  });

  it('test mode simulates the refund and says it is done', async () => {
    const user = userEvent.setup();
    const testPayment: Payment = { ...payment, mode: 'test' };
    const f = stub(testPayment, refunds({ cap: '56.90', remaining: '56.90' }),
      refund({ mode: 'test', amount: '56.90', status: 'confirmed', chainId: undefined, txHash: undefined, tokenSymbol: undefined }));
    mountAt(`/business/test/payments/${payment.id}`);
    await user.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'Simulate refund' }));
    const dialog = await screen.findByRole('dialog', { name: 'Refund this payment' });
    expect(within(dialog).getByText(/Test mode: nothing is sent/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Simulate refund' }));
    await waitFor(() => expect(posts(f)).toHaveLength(1));
    expect(posts(f)[0][0]).toBe(`/api/pay/merchant/payments/${payment.id}/refunds?mode=test`);
    expect(await screen.findByText('Test refund done')).toBeInTheDocument();
  });

  it('reads the payments list again after a refund, so its refund status refreshes', async () => {
    const user = userEvent.setup();
    const testPayment: Payment = { ...payment, mode: 'test' };
    const f = stub(testPayment, refunds({ cap: '56.90', remaining: '56.90' }), refund({ mode: 'test', status: 'confirmed', chainId: undefined, txHash: undefined, tokenSymbol: undefined }));
    const listCalls = () => f.mock.calls.filter(([u]) => new URL(u, 'http://x').pathname === '/api/pay/merchant/payments').length;
    mountAt(`/business/test/payments/${payment.id}`);
    await user.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'Simulate refund' }));
    const before = listCalls();
    expect(before).toBeGreaterThan(0);
    await user.click(within(await screen.findByRole('dialog', { name: 'Refund this payment' })).getByRole('button', { name: 'Simulate refund' }));
    await waitFor(() => expect(listCalls()).toBeGreaterThan(before));
  });

  // A test refund confirms at once and changes the period's refund totals.
  it('marks the reconciliation summary stale after a refund', async () => {
    const user = userEvent.setup();
    const invalidate = vi.spyOn(QueryClient.prototype, 'invalidateQueries');
    const testPayment: Payment = { ...payment, mode: 'test' };
    stub(testPayment, refunds({ cap: '56.90', remaining: '56.90' }), refund({ mode: 'test', status: 'confirmed', chainId: undefined, txHash: undefined, tokenSymbol: undefined }));
    mountAt(`/business/test/payments/${payment.id}`);
    await user.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'Simulate refund' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Refund this payment' })).getByRole('button', { name: 'Simulate refund' }));
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ['payment-summary', 'test'] }));
    invalidate.mockRestore();
  });

  it('a refunds read that failed offers to try again', async () => {
    let failing = true;
    const p = payment;
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: [p], total: 1, page: 1, limit: 20 }),
      [`/api/pay/merchant/payments/${p.id}`]: ok({ payment: p, payer: { name: 'Ada Obi' }, deliveries: [] }),
      [`/api/pay/merchant/payments/${p.id}/refunds`]: () => (failing ? fail(404, 'Not found') : ok(refunds())),
    });
    const user = userEvent.setup();
    mountAt(`/business/live/payments/${p.id}`);
    const drawer = await screen.findByRole('dialog');
    expect(await within(drawer).findByText("Couldn't load refunds")).toBeInTheDocument();
    failing = false;
    await user.click(within(drawer).getByRole('button', { name: 'Try again' }));
    expect(await within(drawer).findByRole('button', { name: 'Refund' })).toBeInTheDocument();
  });

  it('a payment that can\'t be refunded says why and offers no button', async () => {
    stub(payment, refunds({ refundable: false, notRefundableReason: "Refunds aren't available for this payment" }));
    mountAt(`/business/live/payments/${payment.id}`);
    const drawer = await screen.findByRole('dialog');
    expect(await within(drawer).findByText("Refunds aren't available for this payment")).toBeInTheDocument();
    expect(within(drawer).queryByRole('button', { name: 'Refund' })).toBeNull();
  });

  // As Plan 3a answers it: an open refund makes the payment not refundable, with a reason.
  it('a refund still waiting blocks another and says what it waits for', async () => {
    stub(payment, refunds({
      refundable: false,
      notRefundableReason: 'A refund is already in progress',
      remaining: '2.50',
      refunds: [refund({ status: 'requested', chainId: undefined, txHash: undefined, tokenSymbol: undefined, confirmedAt: null })],
    }));
    mountAt(`/business/live/payments/${payment.id}`);
    const drawer = await screen.findByRole('dialog');
    expect(await within(drawer).findByText('A refund of 12.50 USD is waiting for you to approve and send it in the TSA Connect app.')).toBeInTheDocument();
    expect(within(drawer).queryByText('A refund is already in progress')).toBeNull();
    expect(within(drawer).queryByRole('button', { name: 'Refund' })).toBeNull();
    expect(within(drawer).getByText('Awaiting approval')).toBeInTheDocument();
    expect(within(drawer).getByText(/Network chosen when it is sent/)).toBeInTheDocument();
  });

  // Plans review I6: a send the network refused shows why in the drawer.
  it('a refund the network refused says why and that it is retried', async () => {
    stub(payment, refunds({
      refundable: false,
      notRefundableReason: 'A refund is already in progress',
      remaining: '2.50',
      refunds: [refund({ status: 'submitted', confirmedAt: null, lastBroadcastError: 'Not enough gas in your wallet' })],
    }));
    mountAt(`/business/live/payments/${payment.id}`);
    const drawer = await screen.findByRole('dialog');
    expect(await within(drawer).findByText(/Couldn't broadcast: Not enough gas in your wallet — top up gas; we retry automatically\./)).toBeInTheDocument();
    expect(within(drawer).getByText('Sending')).toBeInTheDocument();
  });

  it('lists each refund with its network and a link to the transaction', async () => {
    stub(payment, refunds({ remaining: '2.50', refunds: [refund()] }));
    mountAt(`/business/live/payments/${payment.id}`);
    const drawer = await screen.findByRole('dialog');
    expect(await within(drawer).findByText('Refunded')).toBeInTheDocument();
    expect(within(drawer).getByText(/USDT on BNB Chain/)).toBeInTheDocument();
    expect(within(drawer).getByRole('link', { name: 'View transaction 0xabab…abab' })).toHaveAttribute('href', `https://bscscan.com/tx/${HASH}`);
  });

  it('a simulated test refund shows no network and no link', async () => {
    stub({ ...payment, mode: 'test' }, refunds({ remaining: '2.50', refunds: [refund({ mode: 'test', chainId: undefined, txHash: undefined, tokenSymbol: undefined })] }));
    mountAt(`/business/test/payments/${payment.id}`);
    const drawer = await screen.findByRole('dialog');
    expect(await within(drawer).findByText(/Simulated — nothing was sent/)).toBeInTheDocument();
    expect(within(drawer).queryByRole('link', { name: /view transaction/i })).toBeNull();
  });

  it('a payment that has not succeeded has no refunds section and asks for none', async () => {
    const f = stub(link, refunds());
    mountAt(`/business/live/links/${link.id}`);
    const drawer = await screen.findByRole('dialog');
    await within(drawer).findByText('Payment link');
    expect(within(drawer).queryByText('Refunds')).toBeNull();
    expect(f.mock.calls.some(([u]) => u.includes('/refunds'))).toBe(false);
  });
});
