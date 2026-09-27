import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { fail, json, link, merchant, mountAt, ok, stubFetch } from '@/test/harness';
import type { Payment } from './api';

const noLinks = ok({ items: [], total: 0, page: 1, limit: 20 });
// Live payments switched on: the form is offered in live mode.
const live = { ...merchant, liveEnabled: true, liveAllowed: true };
const posts = (f: ReturnType<typeof stubFetch>) => f.mock.calls.filter(([, init]) => init?.method === 'POST');
const created = (payment: Payment = link) => json(201, { success: true, message: 'Payment link created', data: payment });

async function fillForm(fields: { amount?: string; description?: string; customer?: string; reference?: string } = {}) {
  const dialog = await screen.findByRole('dialog', { name: 'Request payment' });
  const { amount = '5,000', description = link.description, customer, reference } = fields;
  if (amount) await userEvent.type(within(dialog).getByLabelText('Amount (₦)'), amount);
  if (description) await userEvent.type(within(dialog).getByLabelText("What it's for"), description);
  if (customer) await userEvent.type(within(dialog).getByLabelText('Customer name (optional)'), customer);
  if (reference) await userEvent.type(within(dialog).getByLabelText('Your reference (optional)'), reference);
  return dialog;
}

const openRequest = async () => userEvent.click((await screen.findAllByRole('button', { name: /request payment/i }))[0]);

describe('payment links', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('lists links by who they are for and what for, with Cancelled apart from Expired', async () => {
    const cancelled: Payment = { ...link, id: 'l2', reference: 'link_2', status: 'expired', cancelled: true, customerName: undefined, description: 'Excess baggage' };
    const expired: Payment = { ...link, id: 'l3', reference: 'link_3', status: 'expired', description: 'Seat upgrade' };
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/links': ok({ items: [link, cancelled, expired], total: 3, page: 1, limit: 20 }) });
    mountAt('/business/live/links');
    const first = (await screen.findByRole('link', { name: link.description })).closest('tr')!;
    expect(within(first).getByText('Tolu Adeyemi')).toBeInTheDocument();
    expect(within(first).getByText('₦5,000.00')).toBeInTheDocument();
    // The pills use the filters' words.
    expect(within(first).getByText('Awaiting payment')).toBeInTheDocument();
    expect(within(first).getByText('1 Oct, 16:01')).toBeInTheDocument();
    expect(within(screen.getByText('Excess baggage').closest('tr')!).getByText('Cancelled')).toBeInTheDocument();
    expect(within(screen.getByText('Seat upgrade').closest('tr')!).getByText('Expired')).toBeInTheDocument();
    for (const header of ['Customer', 'What for', 'Amount', 'Status', 'Created', 'Expires']) {
      expect(screen.getByRole('columnheader', { name: header })).toBeInTheDocument();
    }
  });

  it('the Cancelled filter asks the API for the links the owner cancelled', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/links': noLinks });
    mountAt('/business/test/links');
    for (const label of ['All', 'Awaiting payment', 'Paid', 'Expired']) expect(await screen.findByRole('radio', { name: label })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('radio', { name: 'Cancelled' }));
    await waitFor(() => expect(f.mock.calls.some(([u]) => u.startsWith('/api/pay/merchant/links?') && u.includes('mode=test') && u.includes('status=cancelled'))).toBe(true));
  });

  it('with no links yet, offers to request a payment', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/links': noLinks });
    mountAt('/business/live/links');
    expect(await screen.findByText('No payment links yet')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /request payment/i })).toHaveLength(2);
  });

  it('request payment makes a link and hands it over: amount, what she receives, copy, WhatsApp and a QR code', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const f = stubFetch({ '/api/pay/merchant': ok(live), '/api/pay/merchant/links': (url) => (url.includes('?mode=live') && !url.includes('page') ? created({ ...link, merchantNet: 490_000 }) : noLinks) });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await fillForm({ customer: 'Tolu Adeyemi' });
    // The name is shown on the link, so the form says who can see it.
    expect(within(dialog).getByLabelText('Customer name (optional)')).toHaveAccessibleDescription('Anyone with the link can see this name.');
    // 7 days unless she picks otherwise.
    expect(within(dialog).getByRole('radio', { name: '7 days' })).toBeChecked();
    await user.click(within(dialog).getByRole('radio', { name: '3 days' }));
    await user.click(within(dialog).getByRole('button', { name: 'Create link' }));

    const ready = await screen.findByRole('dialog', { name: 'Your payment link is ready' });
    const post = f.mock.calls.find(([u, init]) => u === '/api/pay/merchant/links?mode=live' && init.method === 'POST')!;
    expect(JSON.parse(post[1].body)).toEqual({
      amount: 500_000, description: link.description, customerName: 'Tolu Adeyemi', reference: '', expiresInDays: 3,
    });
    expect(post[1].headers['Idempotency-Key']).toMatch(/^[0-9a-f-]{36}$/);

    expect(within(ready).getByText('₦5,000.00', { selector: 'p' })).toBeInTheDocument();
    // What she asked, what the customer is charged (fee on top) and what she gets.
    expect(within(ready).getByText('Your customer pays').nextElementSibling).toHaveTextContent('₦5,050.51');
    expect(within(ready).getByText("You'll receive").nextElementSibling).toHaveTextContent('₦4,900.00');
    expect(within(ready).getByText(link.checkoutUrl)).toBeInTheDocument();
    await user.click(within(ready).getByRole('button', { name: 'Copy payment link' }));
    expect(writeText).toHaveBeenCalledWith(link.checkoutUrl);
    const whatsApp = within(ready).getByRole('link', { name: /share on whatsapp/i });
    const text = new URL(whatsApp.getAttribute('href')!).searchParams.get('text');
    expect(whatsApp.getAttribute('href')).toMatch(/^https:\/\/wa\.me\/\?text=/);
    // The message quotes what the customer will actually be charged.
    expect(text).toBe(`Sky Air Ltd is requesting ₦5,050.51 for ${link.description}. Pay here: ${link.checkoutUrl}`);
    expect(within(ready).getByRole('img', { name: 'QR code for the payment link' })).toBeInTheDocument();

    await user.click(within(ready).getByRole('button', { name: 'Done' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('says what is missing before sending anything', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(live), '/api/pay/merchant/links': noLinks });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await fillForm({ amount: '', description: '' });
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(within(dialog).getByLabelText('Amount (₦)')).toHaveAccessibleDescription('Enter an amount in naira, like 5000 or 5000.50.');
    expect(within(dialog).getByLabelText("What it's for")).toHaveAccessibleDescription('Say what the payment is for — your customer sees this.');
    expect(f.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
  });

  it('a reference already used is refused on the reference field', async () => {
    let calls = 0;
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/links': (url) => {
        if (url.includes('page')) return noLinks;
        calls += 1;
        return calls === 1 ? fail(409, "You've already used this reference — use another or leave it blank", 'reference_taken') : created();
      },
    });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await fillForm({ reference: 'INV-1' });
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    const reference = within(dialog).getByLabelText('Your reference (optional)');
    await waitFor(() => expect(reference).toHaveAccessibleDescription("You've already used this reference — use another or leave it blank"));
    expect(within(dialog).queryByRole('alert')).not.toBeInTheDocument();
    await userEvent.clear(reference);
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(await screen.findByRole('dialog', { name: 'Your payment link is ready' })).toBeInTheDocument();
  });

  // A reply lost on the way is retried with the same key (the API replays the link);
  // an edit is a new request, so it never meets "key reused with a different payload".
  it('the Idempotency-Key follows the form: the same on a plain retry, new after an edit', async () => {
    let calls = 0;
    const f = stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/links': (url) => {
        if (url.includes('page')) return noLinks;
        calls += 1;
        return calls < 3 ? fail(503, 'Service unavailable') : created();
      },
    });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await fillForm();
    const send = within(dialog).getByRole('button', { name: 'Create link' });
    await userEvent.click(send);
    await within(dialog).findByRole('alert');
    await userEvent.click(send);
    await waitFor(() => expect(posts(f)).toHaveLength(2));
    await userEvent.type(within(dialog).getByLabelText('Customer name (optional)'), 'Tolu');
    await userEvent.click(send);
    expect(await screen.findByRole('dialog', { name: 'Your payment link is ready' })).toBeInTheDocument();
    const [first, retry, edited] = posts(f).map(([, init]) => init.headers['Idempotency-Key']);
    expect(retry).toBe(first);
    expect(edited).not.toBe(first);
  });

  it('a key the API has seen with another body says the link may exist, and the next try uses a new key', async () => {
    let calls = 0;
    const f = stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/links': (url) => {
        if (url.includes('page')) return noLinks;
        calls += 1;
        return calls === 1 ? fail(409, 'Idempotency-Key reused with a different request payload') : created();
      },
    });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await fillForm();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('This link may already have been made — check your payment links.');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(await screen.findByRole('dialog', { name: 'Your payment link is ready' })).toBeInTheDocument();
    const [first, second] = posts(f).map(([, init]) => init.headers['Idempotency-Key']);
    expect(second).not.toBe(first);
  });

  it('each open of the form is a new Idempotency-Key', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(live), '/api/pay/merchant/links': (url) => (url.includes('page') ? noLinks : created()) });
    mountAt('/business/live/links');
    for (let i = 0; i < 2; i++) {
      await openRequest();
      const dialog = await fillForm();
      await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
      await userEvent.click(within(await screen.findByRole('dialog', { name: 'Your payment link is ready' })).getByRole('button', { name: 'Done' }));
    }
    const keys = posts(f).map(([, init]) => init.headers['Idempotency-Key']);
    expect(new Set(keys).size).toBe(2);
  });

  it('a refusal without a code is shown as the API words it', async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/links': (url) => (url.includes('page') ? noLinks : fail(422, 'Enter an amount between ₦1,000.00 and ₦5,000,000.00')),
    });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await fillForm({ amount: '5' });
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Enter an amount between ₦1,000.00 and ₦5,000,000.00');
  });

  it('live not switched on says so before the form, and points to test mode', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok({ ...merchant, liveAllowed: false }), '/api/pay/merchant/links': noLinks });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await screen.findByRole('dialog', { name: 'Request payment' });
    expect(within(dialog).getByText(/Live payments aren't on for your business yet — make a test link/)).toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Amount (₦)')).not.toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Create link' })).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole('link', { name: 'Switch to test mode' }));
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/test\/links$/));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(posts(f)).toHaveLength(0);
  });

  it('a live refusal from the API (switched off meanwhile) also points to test mode', async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/links': (url) => (url.includes('page') ? noLinks
        : fail(403, "Live payments aren't switched on for your business yet. Try a test link first.", 'live_not_enabled')),
    });
    mountAt('/business/live/links');
    await openRequest();
    const dialog = await fillForm();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent("Live payments aren't switched on for your business yet. Try a test link first.");
    expect(within(dialog).getByRole('link', { name: 'Switch to test mode' })).toHaveAttribute('href', '/business/test/links');
  });

  it('no payout destination points to Payouts', async () => {
    stubFetch({
      '/api/pay/merchant': ok({ ...merchant, settlementMode: '' }),
      '/api/pay/merchant/links': (url) => (url.includes('page') ? noLinks
        : fail(409, 'Set where you want to be paid before you request a payment', 'settlement_not_set')),
    });
    mountAt('/business/test/links');
    await openRequest();
    const dialog = await fillForm();
    // Test mode says so in the form too.
    expect(within(dialog).getByText('This is a test link — no real money moves.')).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Set where you want to be paid before you request a payment');
    expect(within(dialog).getByRole('link', { name: 'Set up payouts' })).toHaveAttribute('href', '/business/test/payouts');
  });

  it('a suspended merchant cannot request a payment', async () => {
    stubFetch({ '/api/pay/merchant': ok({ ...merchant, status: 'suspended' }), '/api/pay/merchant/links': noLinks });
    mountAt('/business/live/links');
    expect(await screen.findByText(/contact support to request a payment/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /request payment/i })).toBeDisabled();
  });

  it('a link opens in the drawer to share it again, and cancelling asks first', async () => {
    const closed: Payment = { ...link, status: 'expired', cancelled: true, failureReason: 'cancelled by the merchant' };
    let reread = 0;
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/links': ok({ items: [link], total: 1, page: 1, limit: 20 }),
      [`/api/pay/merchant/payments/${link.id}`]: () => {
        reread += 1;
        // The read after the cancel is held back: the drawer must already show the answer.
        return reread === 1 ? ok({ payment: link, payer: null, deliveries: [] }) : new Promise<Response>(() => undefined);
      },
      [`/api/pay/merchant/links/${link.id}/cancel`]: ok(closed),
    });
    mountAt('/business/live/links?status=pending');
    await userEvent.click(await screen.findByRole('link', { name: link.description }));
    const drawer = await screen.findByRole('dialog', { name: link.reference });
    expect(screen.getByTestId('location')).toHaveTextContent(`/business/live/links/${link.id}`);
    expect(within(drawer).getByText('Requested for').nextElementSibling).toHaveTextContent('Tolu Adeyemi');
    expect(within(drawer).getByRole('button', { name: 'Copy payment link' })).toBeInTheDocument();
    expect(within(drawer).getByRole('link', { name: /share on whatsapp/i })).toBeInTheDocument();
    expect(within(drawer).getByRole('img', { name: 'QR code for the payment link' })).toBeInTheDocument();
    expect(within(drawer).getByText('Awaiting payment')).toBeInTheDocument();
    // No webhook was ever sent for this link, so there is no webhooks section.
    expect(within(drawer).queryByText('Webhooks sent')).not.toBeInTheDocument();

    await userEvent.click(within(drawer).getByRole('button', { name: 'Cancel link' }));
    const confirm = await screen.findByRole('dialog', { name: 'Cancel this link?' });
    expect(confirm).toHaveAccessibleDescription("The link will stop working. This can't be undone.");
    // Keep link changes nothing.
    await userEvent.click(within(confirm).getByRole('button', { name: 'Keep link' }));
    expect(f.mock.calls.some(([u]) => u.includes('/cancel'))).toBe(false);

    await userEvent.click(within(drawer).getByRole('button', { name: 'Cancel link' }));
    await userEvent.click(within(await screen.findByRole('dialog', { name: 'Cancel this link?' })).getByRole('button', { name: 'Cancel link' }));
    expect(await screen.findByText('Link cancelled')).toBeInTheDocument();
    expect(within(drawer).getByText('Cancelled')).toBeInTheDocument();
    expect(within(drawer).queryByRole('button', { name: 'Cancel link' })).not.toBeInTheDocument();
    expect(within(drawer).queryByText('Why it failed')).not.toBeInTheDocument();
    const cancel = f.mock.calls.find(([u]) => u.includes('/cancel'))!;
    expect(cancel[0]).toBe(`/api/pay/merchant/links/${link.id}/cancel?mode=live`);
    expect(cancel[1].method).toBe('POST');

    // Closing goes back to the links list with its filter.
    await userEvent.click(within(screen.getByRole('dialog', { name: link.reference })).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/links$/));
    expect(screen.getByRole('radio', { name: 'Awaiting payment' })).toBeChecked();
  });

  it('a link someone started paying cannot be cancelled, and says so', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/links': noLinks,
      [`/api/pay/merchant/payments/${link.id}`]: ok({ payment: link, payer: null, deliveries: [] }),
      [`/api/pay/merchant/links/${link.id}/cancel`]: fail(409, "This link can't be cancelled — it's already being paid, paid, or closed", 'not_cancellable'),
    });
    mountAt(`/business/live/links/${link.id}`);
    const drawer = await screen.findByRole('dialog', { name: link.reference });
    await userEvent.click(within(drawer).getByRole('button', { name: 'Cancel link' }));
    await userEvent.click(within(await screen.findByRole('dialog', { name: 'Cancel this link?' })).getByRole('button', { name: 'Cancel link' }));
    expect(await screen.findByText("This link can't be cancelled — it's already being paid, paid, or closed")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Cancel this link?' })).not.toBeInTheDocument());
  });

  it('a paid link has nothing to share or cancel', async () => {
    const paid: Payment = { ...link, status: 'succeeded', succeededAt: '2026-09-25T10:00:00Z' };
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/links': noLinks,
      [`/api/pay/merchant/payments/${link.id}`]: ok({ payment: paid, payer: { name: 'TOLU ADEYEMI' }, deliveries: [] }),
    });
    mountAt(`/business/live/links/${link.id}`);
    const drawer = await screen.findByRole('dialog', { name: link.reference });
    expect(within(drawer).getByText(link.checkoutUrl)).toBeInTheDocument();
    expect(within(drawer).queryByRole('button', { name: 'Cancel link' })).not.toBeInTheDocument();
    expect(within(drawer).queryByRole('link', { name: /share on whatsapp/i })).not.toBeInTheDocument();
  });
});
