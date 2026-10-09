import { Route } from 'react-router-dom';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { fail, ok, payment, renderWith, stubFetch } from '@/test/harness';
import { RefundsList } from './RefundsList';
import type { RefundRow } from './api';

const row: RefundRow = {
  id: 'r1', paymentId: payment.id, mode: 'live', status: 'confirmed', amount: '12.50', cap: '15.00', credited: '12.50',
  toAddress: '0x2222222222222222222222222222222222222222', chainId: 56, tokenSymbol: 'USDT', requestedVia: 'web',
  createdAt: '2026-10-02T09:00:00Z', confirmedAt: '2026-10-02T09:10:00Z',
  paymentCode: 'TSA-7K3QX-M2P9D', customerName: 'Ada Obi', description: 'LOS–ABV 12 Oct',
};
const HASH = `0x${'ab'.repeat(32)}`;

const mount = () => renderWith(
  <Route path="/business/:mode/reconciliation" element={<RefundsList mode="live" from="2026-10-01" to="2026-10-08" />} />,
  '/business/live/reconciliation',
);

describe('refunds list (reconciliation)', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('lists the period\'s refunds, each opening its payment', async () => {
    const f = stubFetch({ '/api/pay/merchant/refunds': ok({ refunds: [row], total: 1, page: 1, limit: 20 }) });
    mount();
    expect(await screen.findByText('12.50 USDT')).toBeInTheDocument();
    expect(screen.getByText('Ada Obi')).toBeInTheDocument();
    expect(screen.getByText('USDT on BNB Chain')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'TSA-7K3QX-M2P9D' })).toHaveAttribute('href', `/business/live/payments/${payment.id}`);
    const q = new URL(f.mock.calls[0][0], 'http://x').searchParams;
    expect(Object.fromEntries(q)).toEqual({ mode: 'live', from: '2026-10-01', to: '2026-10-08', page: '1' });
  });

  it('filters by status, and says when nothing matches', async () => {
    const user = userEvent.setup();
    const f = stubFetch({
      '/api/pay/merchant/refunds': (url) => ok(url.includes('status=failed')
        ? { refunds: [], total: 0, page: 1, limit: 20 }
        : { refunds: [row], total: 1, page: 1, limit: 20 }),
    });
    mount();
    await screen.findByText('12.50 USDT');
    await user.click(screen.getByRole('radio', { name: 'Failed' }));
    expect(await screen.findByText('No refunds in this period')).toBeInTheDocument();
    expect(screen.getByText('Try another status.')).toBeInTheDocument();
    await waitFor(() => expect(f.mock.calls.at(-1)![0]).toContain('status=failed'));
  });

  it('offers the under-review status as a filter', async () => {
    const user = userEvent.setup();
    const f = stubFetch({ '/api/pay/merchant/refunds': ok({ refunds: [row], total: 1, page: 1, limit: 20 }) });
    mount();
    await screen.findByText('12.50 USDT');
    await user.click(screen.getByRole('radio', { name: 'Under review' }));
    await waitFor(() => expect(f.mock.calls.at(-1)![0]).toContain('status=needs_review'));
  });

  it('shows why a send was refused, and links a real transaction only', async () => {
    stubFetch({
      '/api/pay/merchant/refunds': ok({
        refunds: [
          { ...row, id: 'r2', status: 'submitted', txHash: HASH, lastBroadcastError: 'Not enough BNB for gas' },
          { ...row, id: 'r3', txHash: '../address/0x1' },
        ],
        total: 2, page: 1, limit: 20,
      }),
    });
    mount();
    expect(await screen.findByText('Not enough BNB for gas')).toBeInTheDocument();
    // Each link names its own transaction, so a table of them reads apart.
    const links = screen.getAllByRole('link', { name: /^View transaction/ });
    expect(links[0]).toHaveAccessibleName('View transaction 0xabab…abab');
    expect(links).toHaveLength(1);
    expect(links[0]).toHaveAttribute('href', `https://bscscan.com/tx/${HASH}`);
  });

  it('shows plain USD until a token is known', async () => {
    stubFetch({
      '/api/pay/merchant/refunds': ok({ refunds: [{ ...row, status: 'requested', tokenSymbol: undefined, chainId: undefined }], total: 1, page: 1, limit: 20 }),
    });
    mount();
    expect(await screen.findByText('12.50 USD')).toBeInTheDocument();
  });

  it('goes back to the first page when a period change leaves the page past the end', async () => {
    const f = stubFetch({
      '/api/pay/merchant/refunds': (url) => ok(url.includes('page=3')
        ? { refunds: [], total: 5, page: 3, limit: 20 }
        : { refunds: [row], total: 5, page: 1, limit: 20 }),
    });
    renderWith(
      <Route path="/business/:mode/reconciliation" element={<RefundsList mode="live" from="2026-10-01" to="2026-10-08" />} />,
      '/business/live/reconciliation?refundPage=3',
    );
    expect(await screen.findByText('12.50 USDT')).toBeInTheDocument();
    expect(screen.queryByText(/Showing 0/)).not.toBeInTheDocument();
    expect(new URL(f.mock.calls.at(-1)![0], 'http://x').searchParams.get('page')).toBe('1');
  });

  it('a failed read offers a retry', async () => {
    let reads = 0;
    const f = stubFetch({ '/api/pay/merchant/refunds': () => (++reads === 1 ? fail(500, 'boom') : ok({ refunds: [row], total: 1, page: 1, limit: 20 })) });
    mount();
    expect(await screen.findByRole('alert')).toHaveTextContent("We couldn't load your refunds");
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('12.50 USDT')).toBeInTheDocument();
    expect(f.mock.calls.filter(([u]) => String(u).startsWith('/api/pay/merchant/refunds'))).toHaveLength(2);
  });
});
