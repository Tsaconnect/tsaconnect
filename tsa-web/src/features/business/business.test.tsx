import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { fail, link, merchant, mountAt, ok, payment, stubFetch } from '@/test/harness';

const noPayments = ok({ items: [], total: 0, page: 1, limit: 20 });

describe('business area', () => {
  afterEach(() => vi.unstubAllGlobals());

  // Review Focus 3.
  it('a user with no TSA Pay merchant is told how to apply — no crash, no spinner loop', async () => {
    stubFetch({ '/api/pay/merchant': fail(404, "You don't have a TSA Pay merchant account", 'no_merchant') });
    mountAt('/business/live/overview');
    expect(await screen.findByText(/apply for TSA Pay in the TSA Connect app/i)).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('TSA Pay switched off (a plain 404) reads the same as no merchant', async () => {
    stubFetch({ '/api/pay/merchant': fail(404, 'Not found') });
    mountAt('/business/live/payments');
    expect(await screen.findByText(/apply for TSA Pay in the TSA Connect app/i)).toBeInTheDocument();
  });

  it('a server error offers a retry, not the apply page', async () => {
    const f = stubFetch({ '/api/pay/merchant': fail(500, 'Request failed') });
    mountAt('/business/live/overview');
    expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't load your business/i);
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await waitFor(() => expect(f.mock.calls.filter(([u]) => u === '/api/pay/merchant')).toHaveLength(2));
  });

  // Review Focus 4.
  it('an unknown mode in the URL goes to live', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/foo/payments');
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/payments$/));
    expect(await screen.findByRole('heading', { name: 'Payments' })).toBeInTheDocument();
  });

  it('/business alone opens the live overview', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business');
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/overview$/));
  });

  it('the overview checklist marks what is done and links what is not', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/live/overview');
    const card = (await screen.findByRole('heading', { name: 'Finish setting up' })).closest('section')!;
    expect(within(card).getByText('3 of 6 done')).toBeInTheDocument();
    expect(within(card).getByRole('link', { name: /add a webhook/i })).toHaveAttribute('href', '/business/live/developers');
    expect(within(card).getByRole('link', { name: /make a test payment/i })).toHaveAttribute('href', '/docs');
    // Live is TSA Connect's step, not hers: it says so and links nowhere.
    expect(within(card).getByText("Switched off — contact TSA Connect to turn them back on")).toBeInTheDocument();
    expect(within(card).queryByRole('link', { name: /live payments/i })).not.toBeInTheDocument();
  });

  it('the checklist stays until live payments are switched on', async () => {
    stubFetch({
      '/api/pay/merchant': ok({ ...merchant, webhookUrl: 'https://api.skyair.ng/tsa/webhook' }),
      '/api/pay/merchant/payments': ok({ items: [payment], total: 1, page: 1, limit: 5 }),
    });
    mountAt('/business/live/overview');
    const card = (await screen.findByRole('heading', { name: 'Finish setting up' })).closest('section')!;
    expect(within(card).getByText('5 of 6 done')).toBeInTheDocument();
  });

  it('the checklist goes away once everything is done', async () => {
    stubFetch({
      '/api/pay/merchant': ok({ ...merchant, liveEnabled: true, webhookUrl: 'https://api.skyair.ng/tsa/webhook' }),
      '/api/pay/merchant/payments': ok({ items: [payment], total: 1, page: 1, limit: 5 }),
    });
    mountAt('/business/live/overview');
    expect(await screen.findByText('PNR-K8Q2ZL')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Finish setting up' })).not.toBeInTheDocument();
  });

  it('payments list shows what the merchant receives and opens the detail with webhook attempts', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: [payment], total: 1, page: 1, limit: 20 }),
      [`/api/pay/merchant/payments/${payment.id}`]: ok({
        payment,
        payer: { name: 'TOLUWANI ADEYEMI' },
        deliveries: [{ id: 'd1', event: 'payment.succeeded', attempts: 2, lastStatusCode: 200, createdAt: payment.createdAt, deliveredAt: payment.createdAt }],
      }),
    });
    mountAt('/business/live/payments');
    await userEvent.click(await screen.findByText('PNR-K8Q2ZL'));
    const drawer = await screen.findByRole('dialog');
    expect(within(drawer).getByText('₦84,500.00', { selector: 'b' })).toBeInTheDocument();
    // Who paid, their own reference and what it was for — each labelled.
    expect(within(drawer).getByText('Paid by').nextElementSibling).toHaveTextContent('TOLUWANI ADEYEMI');
    expect(within(drawer).queryByText('Verified')).not.toBeInTheDocument();
    expect(within(drawer).getByText('Your reference').nextElementSibling).toHaveTextContent('PNR-K8Q2ZL');
    expect(within(drawer).getByText('For').nextElementSibling).toHaveTextContent(payment.description);
    expect(within(drawer).getByText('payment.succeeded')).toBeInTheDocument();
    expect(within(drawer).getByText('200')).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent(`/business/live/payments/${payment.id}`);
  });

  it('closing the detail goes back to the list with its filters', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: [payment], total: 1, page: 1, limit: 20 }),
      [`/api/pay/merchant/payments/${payment.id}`]: ok({ payment, payer: null, deliveries: [] }),
    });
    mountAt('/business/live/payments?status=succeeded');
    await userEvent.click(await screen.findByText('PNR-K8Q2ZL'));
    const drawer = await screen.findByRole('dialog');
    expect(within(drawer).getByText(/no webhooks sent/i)).toBeInTheDocument();
    await userEvent.click(within(drawer).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/payments$/));
    expect(screen.getByRole('radio', { name: 'Succeeded' })).toBeChecked();
    // Closing replaces the detail entry, so Back doesn't reopen the drawer.
    expect(screen.getByTestId('navigation')).toHaveTextContent('REPLACE');
  });

  it('what the merchant receives is only shown once the money is on its way', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: [{ ...payment, id: 'p2', reference: 'PNR-33XH9C', status: 'expired' }], total: 1, page: 1, limit: 20 }),
    });
    mountAt('/business/live/payments');
    const row = (await screen.findByText('PNR-33XH9C')).closest('tr')!;
    expect(within(row).getByText('Expired')).toBeInTheDocument();
    expect(within(row).getByText('—')).toBeInTheDocument();
  });

  // The previous rows stay up while the next page loads, but never across modes.
  it('switching to test mode never shows live rows under the Test banner', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': (url) =>
        new URL(url, 'http://x').searchParams.get('mode') === 'test'
          ? new Promise<Response>(() => undefined) // still loading
          : ok({ items: [payment], total: 1, page: 1, limit: 20 }),
    });
    mountAt('/business/live/payments');
    expect(await screen.findByText('PNR-K8Q2ZL')).toBeInTheDocument();
    await userEvent.click(within(screen.getByRole('group', { name: 'Mode' })).getByRole('link', { name: 'Test' }));
    expect(await screen.findByText('Test mode — no real money moves')).toBeInTheDocument();
    expect(screen.queryByText('PNR-K8Q2ZL')).not.toBeInTheDocument();
  });

  it('the next page keeps the current rows on screen while it loads', async () => {
    let second = false;
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': (url) => {
        if (new URL(url, 'http://x').searchParams.get('page') === '2') {
          second = true;
          return new Promise<Response>(() => undefined);
        }
        return ok({ items: [payment], total: 45, page: 1, limit: 20 });
      },
    });
    mountAt('/business/live/payments');
    await userEvent.click(await screen.findByRole('button', { name: /next/i }));
    await waitFor(() => expect(second).toBe(true));
    expect(screen.getByText('PNR-K8Q2ZL')).toBeInTheDocument();
  });

  it('searching sends q and resets to page 1', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/live/payments?page=3');
    await userEvent.type(await screen.findByRole('searchbox', { name: /search payments/i }), 'abv{enter}');
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).includes('q=abv') && String(u).includes('page=1'))).toBe(true));
  });

  it('a status chip filters the list', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/test/payments');
    await userEvent.click(await screen.findByRole('radio', { name: 'Waiting' }));
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).includes('mode=test') && String(u).includes('status=pending'))).toBe(true));
  });

  it('Payments filters cancelled links apart from expired ones', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/live/payments');
    await userEvent.click(await screen.findByRole('radio', { name: 'Cancelled' }));
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).includes('status=cancelled'))).toBe(true));
  });

  it('a payment not yet paid says what the customer pays and what you\'ll receive', async () => {
    const waiting = { ...payment, id: 'p9', status: 'pending' as const, succeededAt: undefined, settlementRef: undefined };
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: [waiting], total: 1, page: 1, limit: 20 }),
      '/api/pay/merchant/payments/p9': ok({ payment: waiting, payer: null, deliveries: [] }),
    });
    mountAt('/business/live/payments/p9');
    const drawer = await screen.findByRole('dialog');
    expect(await within(drawer).findByText('Customer pays')).toBeInTheDocument();
    expect(within(drawer).queryByText('Customer paid')).not.toBeInTheDocument();
    expect(within(drawer).getByText("You'll receive").nextElementSibling).toHaveTextContent('₦84,500.00');
  });

  it('an empty test mode points to the docs', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/test/payments');
    expect(await screen.findByText('No payments yet')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /see the docs/i })).toHaveAttribute('href', '/docs');
  });

  it('pagination says where you are and moves a page', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: Array.from({ length: 20 }, (_, i) => ({ ...payment, id: `p${i}`, reference: `PNR-${i}` })), total: 45, page: 1, limit: 20 }),
    });
    mountAt('/business/live/payments');
    expect(await screen.findByText('Showing 1–20 of 45')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /prev/i })).toBeDisabled();
    await userEvent.click(screen.getByRole('button', { name: /next/i }));
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).includes('page=2'))).toBe(true));
  });

  it('a page past the end says so and offers the first page', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': ok({ items: [], total: 45, page: 9, limit: 20 }) });
    mountAt('/business/live/payments?page=9');
    expect(await screen.findByText('Showing 0 of 45')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /next/i })).toBeDisabled();
    await userEvent.click(screen.getByRole('button', { name: 'Go to first page' }));
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).includes('page=1'))).toBe(true));
  });

  it('a payment made as a link is tagged Link, with who it was for', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: [payment, link], total: 2, page: 1, limit: 20 }),
    });
    mountAt('/business/live/payments');
    const linkRow = (await screen.findByText(link.reference)).closest('tr')!;
    expect(within(linkRow).getByText('Link')).toBeInTheDocument();
    expect(within(linkRow).getByText(`Tolu Adeyemi · ${link.description}`)).toBeInTheDocument();
    const apiRow = screen.getByText(payment.reference).closest('tr')!;
    expect(within(apiRow).queryByText('Link')).not.toBeInTheDocument();
  });

  it('a date range narrows payments to the days they were made, from page 1', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/live/payments?page=2');
    fireEvent.change(await screen.findByLabelText('From'), { target: { value: '2026-10-01' } });
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-31' } });
    await waitFor(() => expect(f.mock.calls.some(([u]) => {
      const q = new URL(String(u), 'http://x').searchParams;
      return q.get('from') === '2026-10-01' && q.get('to') === '2026-10-31' && q.get('page') === '1';
    })).toBe(true));
  });

  it('half a date range is not sent, and says what is missing', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/live/payments?from=2026-10-01');
    expect(await screen.findByText('Choose both a start and an end date.')).toBeInTheDocument();
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).startsWith('/api/pay/merchant/payments?'))).toBe(true));
    expect(f.mock.calls.some(([u]) => String(u).includes('from='))).toBe(false);
  });

  it('an end date before the start is refused on the page', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/live/payments?from=2026-10-31&to=2026-10-01');
    expect(await screen.findByText('The end date is before the start date.')).toBeInTheDocument();
  });

  it('Clear dates takes the range off', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments': noPayments });
    mountAt('/business/live/payments?from=2026-10-01&to=2026-10-31');
    await userEvent.click(await screen.findByRole('button', { name: 'Clear dates' }));
    expect(screen.getByLabelText('From')).toHaveValue('');
    await waitFor(() => {
      const last = String(f.mock.calls.filter(([u]) => String(u).startsWith('/api/pay/merchant/payments?')).at(-1)![0]);
      expect(last).not.toContain('from=');
    });
  });

  it('payment links have no date filter', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/links': noPayments });
    mountAt('/business/live/links');
    await screen.findByRole('radio', { name: 'All' });
    expect(screen.queryByLabelText('From')).not.toBeInTheDocument();
  });

  it('Resend sends each webhook again and reads the attempts again', async () => {
    let reads = 0;
    const failed = { id: 'd1', event: 'payment.failed', attempts: 7, lastStatusCode: 500, lastError: 'http 500', createdAt: payment.createdAt, failedAt: payment.createdAt };
    const delivered = { id: 'd2', event: 'payment.succeeded', attempts: 1, lastStatusCode: 200, createdAt: payment.createdAt, deliveredAt: payment.createdAt };
    const noUrl = { id: 'd3', event: 'payment.expired', attempts: 1, lastStatusCode: 200, createdAt: payment.createdAt, deliveredAt: payment.createdAt };
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/payments': ok({ items: [payment], total: 1, page: 1, limit: 20 }),
      [`/api/pay/merchant/payments/${payment.id}`]: () => { reads += 1; return ok({ payment, payer: null, deliveries: [failed, delivered, noUrl] }); },
      '/api/pay/merchant/deliveries/d1/resend': ok({ delivery: { ...failed, attempts: 0, failedAt: undefined, nextAttemptAt: payment.createdAt } }),
      '/api/pay/merchant/deliveries/d2/resend': ok({ delivery: { ...delivered, id: 'd4', attempts: 0, deliveredAt: undefined } }),
      '/api/pay/merchant/deliveries/d3/resend': fail(422, 'Add a webhook URL for this mode under API keys & webhooks first', 'no_webhook_url'),
    });
    mountAt(`/business/live/payments/${payment.id}`);
    const drawer = await screen.findByRole('dialog');
    const row = (event: string) => within(drawer).getByText(event).closest('li')!;

    await userEvent.click(within(await waitFor(() => row('payment.failed'))).getByRole('button', { name: 'Resend' }));
    expect(await screen.findByText('Queued to send again — it goes out within a minute')).toBeInTheDocument();
    const call = f.mock.calls.find(([u]) => String(u).startsWith('/api/pay/merchant/deliveries/d1/resend'))!;
    expect(call[0]).toBe('/api/pay/merchant/deliveries/d1/resend?mode=live');
    expect(call[1].method).toBe('POST');
    await waitFor(() => expect(reads).toBe(2));

    await userEvent.click(within(row('payment.succeeded')).getByRole('button', { name: 'Resend' }));
    expect(await screen.findByText('Sent again as a new delivery — it goes out within a minute')).toBeInTheDocument();

    await userEvent.click(within(row('payment.expired')).getByRole('button', { name: 'Resend' }));
    expect(await screen.findByText('Add a webhook URL for this mode under API keys & webhooks first')).toBeInTheDocument();
  });
});
