import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { fail, json, merchant, mountAt, ok, openLink, payment, stubFetch } from '@/test/harness';
import type { Payment } from './api';

// Live payments switched on: the form is offered in live mode.
const live = { ...merchant, liveEnabled: true, liveAllowed: true };
const noLinks = ok({ items: [], total: 0, page: 1, limit: 20 });
const paidThrough: Payment = {
  ...payment, id: 'op1', reference: 'open_abc123def456', source: 'open', openLinkId: openLink.id,
  amount: 750_000, description: '', linkLabel: openLink.label,
};
// A customer's note is theirs: shown as a note, next to the link's label.
const withNote: Payment = { ...paidThrough, id: 'op2', reference: 'open_note00000000', description: 'Two bags, flight TS204' };
const posts = (f: ReturnType<typeof stubFetch>) => f.mock.calls.filter(([, init]) => init?.method === 'POST');

describe('reusable payment links', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('the Reusable type lists each link with what it has taken, and whether it can be paid now', async () => {
    const paused = {
      ...openLink, id: 'ol2', label: 'Airport kiosk', active: false, takingPayments: false, notTakingReason: 'paused' as const,
      minAmount: 0, maxAmount: 0, paidCount: 0, paidTotal: 0, receivedTotal: 0,
    };
    const noPayout = { ...openLink, id: 'ol3', label: 'Lekki office', takingPayments: false, notTakingReason: 'no_payout' as const };
    const f = stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/links': noLinks,
      '/api/pay/merchant/open-links': ok({ items: [openLink, paused, noPayout], total: 3, page: 1, limit: 20 }),
    });
    mountAt('/business/live/links');
    await userEvent.click(await screen.findByRole('radio', { name: 'Reusable' }));
    const row = (await screen.findByRole('link', { name: openLink.label })).closest('tr')!;
    // A limit left at 0 is the business's own: always a range, never a bare "Any amount".
    expect(within(row).getByText('From ₦1,000.00 to ₦5,000,000.00')).toBeInTheDocument();
    expect(within(row).getByText('2')).toBeInTheDocument();
    // What the business received, not what customers paid.
    expect(within(row).getByText('₦12,250.00')).toBeInTheDocument();
    expect(within(row).getByText('Taking payments')).toBeInTheDocument();
    const pausedRow = screen.getByRole('link', { name: 'Airport kiosk' }).closest('tr')!;
    expect(within(pausedRow).getByText('Not taking payments')).toBeInTheDocument();
    expect(within(pausedRow).getByText('Paused')).toBeInTheDocument();
    expect(within(pausedRow).getByText('From ₦1,000.00 to ₦5,000,000.00')).toBeInTheDocument();
    const noPayoutRow = screen.getByRole('link', { name: 'Lekki office' }).closest('tr')!;
    expect(within(noPayoutRow).getByText('Not taking payments')).toBeInTheDocument();
    expect(within(noPayoutRow).getByText('Set your payout account')).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent('/business/live/links');
    expect(f.mock.calls.some(([u]) => u.startsWith('/api/pay/merchant/open-links?') && u.includes('mode=live'))).toBe(true);
  });

  it('request payment on the Reusable list makes a reusable link: label, limits, then share or print', async () => {
    const user = userEvent.setup();
    const made = { ...openLink, minAmount: 200_000, maxAmount: 5_000_000, paidCount: 0, paidTotal: 0 };
    const f = stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/open-links': (url) => (url.includes('page') ? noLinks : json(201, { success: true, message: 'Reusable link created', data: made })),
    });
    mountAt('/business/live/links?type=reusable');
    await user.click((await screen.findAllByRole('button', { name: /request payment/i }))[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Request payment' });
    // The list's type is the one chosen.
    expect(within(dialog).getByRole('radio', { name: /customer enters the amount/i })).toBeChecked();
    expect(within(dialog).queryByLabelText('Amount (₦)')).not.toBeInTheDocument();

    // The label is required; a maximum below the minimum is refused before sending.
    await user.type(within(dialog).getByLabelText('Minimum (₦, optional)'), '2000');
    await user.type(within(dialog).getByLabelText('Maximum (₦, optional)'), '1000');
    await user.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(within(dialog).getByLabelText('Label')).toHaveAccessibleDescription('Give the link a label — your customers see it.');
    expect(within(dialog).getByLabelText('Maximum (₦, optional)')).toHaveAccessibleDescription('The maximum must be at least the minimum.');
    expect(posts(f)).toHaveLength(0);

    await user.type(within(dialog).getByLabelText('Label'), openLink.label);
    await user.clear(within(dialog).getByLabelText('Maximum (₦, optional)'));
    await user.type(within(dialog).getByLabelText('Maximum (₦, optional)'), '50,000');
    await user.click(within(dialog).getByRole('button', { name: 'Create link' }));

    const ready = await screen.findByRole('dialog', { name: 'Your reusable link is ready' });
    const [url, init] = posts(f)[0];
    expect(url).toBe('/api/pay/merchant/open-links?mode=live');
    expect(JSON.parse(init.body)).toEqual({ label: openLink.label, minAmount: 200_000, maxAmount: 5_000_000 });
    expect(init.headers['Idempotency-Key']).toMatch(/^[0-9a-f-]{36}$/);
    expect(within(ready).getByText(/From ₦2,000.00 to ₦50,000.00/)).toBeInTheDocument();
    expect(within(ready).getByText(made.url)).toBeInTheDocument();
    expect(within(ready).getByRole('img', { name: 'QR code for the payment link' })).toBeInTheDocument();
    const text = new URL(within(ready).getByRole('link', { name: /share on whatsapp/i }).getAttribute('href')!).searchParams.get('text');
    expect(text).toBe(`Pay Sky Air Ltd — ${openLink.label}: ${made.url}`);
    expect(within(ready).getByRole('link', { name: /print poster/i })).toHaveAttribute('href', `/business/live/links/open/${made.id}/poster`);
  });

  // Review: one list's rows must never show through the other's columns while it loads.
  it('switching to Reusable never shows the one-time links while the reusable ones load', async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/links': ok({ items: [{ ...payment, id: 'l1', reference: 'link_abc', source: 'link', description: 'Balance for LOS–ABV' }], total: 1, page: 1, limit: 20 }),
      '/api/pay/merchant/open-links': () => new Promise<Response>(() => undefined), // still loading
    });
    mountAt('/business/live/links');
    expect(await screen.findByRole('link', { name: 'Balance for LOS–ABV' })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('radio', { name: 'Reusable' }));
    expect(await screen.findByRole('columnheader', { name: 'Label' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Balance for LOS–ABV' })).not.toBeInTheDocument();
    expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();
    expect(screen.queryByText('link_abc')).not.toBeInTheDocument();
  });

  it('choosing the type keeps focus on the choice; the first field is focused only when the form opens', async () => {
    stubFetch({ '/api/pay/merchant': ok(live), '/api/pay/merchant/links': noLinks });
    mountAt('/business/live/links');
    await userEvent.click((await screen.findAllByRole('button', { name: /request payment/i }))[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Request payment' });
    await waitFor(() => expect(within(dialog).getByLabelText('Amount (₦)')).toHaveFocus());
    const reusable = within(dialog).getByRole('radio', { name: /customer enters the amount/i });
    await userEvent.click(reusable);
    expect(within(dialog).getByLabelText('Label')).toBeInTheDocument();
    expect(reusable).toHaveFocus();
    // Switching back finds what was typed.
    await userEvent.type(within(dialog).getByLabelText('Label'), 'Shop 12');
    await userEvent.click(within(dialog).getByRole('radio', { name: /fixed amount/i }));
    await userEvent.click(within(dialog).getByRole('radio', { name: /customer enters the amount/i }));
    expect(within(dialog).getByLabelText('Label')).toHaveValue('Shop 12');
  });

  it('a live refusal while making a reusable link points to the reusable links in test mode', async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/open-links': (url) => (url.includes('page') ? noLinks
        : fail(403, "Live payments aren't switched on for your business yet. Try a test link first.", 'live_not_enabled')),
    });
    mountAt('/business/live/links?type=reusable');
    await userEvent.click((await screen.findAllByRole('button', { name: /request payment/i }))[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Request payment' });
    await userEvent.type(within(dialog).getByLabelText('Label'), 'Shop 12');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Create link' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent("Live payments aren't switched on");
    expect(within(dialog).getByRole('link', { name: 'Switch to test mode' })).toHaveAttribute('href', '/business/test/links?type=reusable');
  });

  it('with live off, the Reusable form says so up front and points to the reusable links in test mode', async () => {
    stubFetch({ '/api/pay/merchant': ok({ ...merchant, liveAllowed: false }), '/api/pay/merchant/open-links': noLinks });
    mountAt('/business/live/links?type=reusable');
    await userEvent.click((await screen.findAllByRole('button', { name: /request payment/i }))[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Request payment' });
    expect(within(dialog).getByRole('link', { name: 'Switch to test mode' })).toHaveAttribute('href', '/business/test/links?type=reusable');
  });

  it('the form starts with the choice of link, and switching it swaps the fields', async () => {
    stubFetch({ '/api/pay/merchant': ok(live), '/api/pay/merchant/links': noLinks });
    mountAt('/business/live/links');
    await userEvent.click((await screen.findAllByRole('button', { name: /request payment/i }))[0]);
    const dialog = await screen.findByRole('dialog', { name: 'Request payment' });
    expect(within(dialog).getByRole('radio', { name: /fixed amount.*for one customer/i })).toBeChecked();
    expect(within(dialog).getByLabelText('Amount (₦)')).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole('radio', { name: /customer enters the amount.*reusable, good for a shop QR/i }));
    expect(within(dialog).getByLabelText('Label')).toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Amount (₦)')).not.toBeInTheDocument();
  });

  it('a reusable link opens in a drawer: takings, recent payments, pause and replace', async () => {
    let current = openLink;
    const f = stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/open-links': ok({ items: [openLink], total: 1, page: 1, limit: 20 }),
      [`/api/pay/merchant/open-links/${openLink.id}`]: () => ok({ openLink: current, payments: [paidThrough, withNote] }),
      [`/api/pay/merchant/open-links/${openLink.id}/replace`]: () => {
        current = { ...current, slug: 'sky-air-ltd-9zzz', url: 'https://tsaconnectworld.com/l/sky-air-ltd-9zzz', version: current.version + 1 };
        return ok(current);
      },
    });
    mountAt('/business/live/links?type=reusable');
    await userEvent.click(await screen.findByRole('link', { name: openLink.label }));
    const drawer = await screen.findByRole('dialog', { name: openLink.label });
    expect(screen.getByTestId('location')).toHaveTextContent(`/business/live/links/open/${openLink.id}`);
    expect(within(drawer).getByText(/You received/)).toHaveTextContent('You received ₦12,250.00 from 2 payments');
    expect(within(drawer).getByRole('link', { name: openLink.label })).toHaveAttribute('href', `/business/live/payments/${paidThrough.id}`);
    expect(within(drawer).getByRole('link', { name: `${openLink.label} · Note: Two bags, flight TS204` }))
      .toHaveAttribute('href', `/business/live/payments/${withNote.id}`);
    expect(within(drawer).getByRole('link', { name: /print poster/i })).toHaveAttribute('href', `/business/live/links/open/${openLink.id}/poster`);

    // Pause: every setting goes back as it was, with active off, against the version on screen.
    f.mockImplementationOnce(async () => ok({ ...current, active: false, takingPayments: false, notTakingReason: 'paused', version: current.version + 1 }));
    await userEvent.click(within(drawer).getByRole('button', { name: 'Pause' }));
    expect(await within(drawer).findByText(/Paused\. Customers who open this link/)).toBeInTheDocument();
    const put = f.mock.calls.find(([, init]) => init?.method === 'PUT')!;
    expect(put[0]).toBe(`/api/pay/merchant/open-links/${openLink.id}?mode=live`);
    expect(JSON.parse(put[1].body)).toEqual({ label: openLink.label, minAmount: 100_000, maxAmount: 0, active: false, expectedVersion: 3 });
    expect(within(drawer).getByRole('button', { name: 'Resume' })).toBeInTheDocument();

    await userEvent.click(within(drawer).getByRole('button', { name: 'Replace link' }));
    const confirm = await screen.findByRole('dialog', { name: 'Replace this link?' });
    expect(confirm).toHaveAccessibleDescription('Your printed posters will stop working. Print the new one.');
    await userEvent.click(within(confirm).getByRole('button', { name: 'Replace link' }));
    expect(await within(drawer).findByText('https://tsaconnectworld.com/l/sky-air-ltd-9zzz')).toBeInTheDocument();
    expect(posts(f).map(([u]) => u)).toEqual([`/api/pay/merchant/open-links/${openLink.id}/replace?mode=live`]);

    // Closing goes back to the reusable links, not the one-time ones.
    await userEvent.click(within(drawer).getByRole('button', { name: 'Close' }));
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/links$/));
    expect(screen.getByRole('radio', { name: 'Reusable' })).toBeChecked();
  });

  it('a link that can\'t be paid says why, with the way to fix it', async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/open-links': noLinks,
      [`/api/pay/merchant/open-links/${openLink.id}`]: ok({ openLink: { ...openLink, takingPayments: false, notTakingReason: 'no_payout' }, payments: [] }),
    });
    mountAt(`/business/live/links/open/${openLink.id}?type=reusable`);
    const drawer = await screen.findByRole('dialog', { name: openLink.label });
    expect(within(drawer).getByText('Not taking payments')).toBeInTheDocument();
    expect(within(drawer).getByText(/Set your payout account\. Customers who open this link/)).toBeInTheDocument();
    expect(within(drawer).getByRole('link', { name: 'Set up payouts' })).toHaveAttribute('href', '/business/live/payouts');
  });

  it('a range the business\'s limits no longer allow says so, and Edit is right there', async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/open-links': noLinks,
      [`/api/pay/merchant/open-links/${openLink.id}`]: ok({ openLink: { ...openLink, takingPayments: false, notTakingReason: 'range_outside_limits' }, payments: [] }),
    });
    mountAt(`/business/live/links/open/${openLink.id}?type=reusable`);
    const drawer = await screen.findByRole('dialog', { name: openLink.label });
    expect(within(drawer).getByText(/Your amount range no longer fits your limits — edit the link\. Customers who open this link/)).toBeInTheDocument();
    await userEvent.click(within(drawer).getByRole('button', { name: 'Edit the link' }));
    expect(await screen.findByRole('dialog', { name: 'Edit link' })).toBeInTheDocument();
  });

  it('Edit changes the label and limits, checked as on create, against the version on screen', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/open-links': noLinks,
      [`/api/pay/merchant/open-links/${openLink.id}`]: ok({ openLink, payments: [] }),
    });
    mountAt(`/business/live/links/open/${openLink.id}?type=reusable`);
    const drawer = await screen.findByRole('dialog', { name: openLink.label });
    await userEvent.click(within(drawer).getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit link' });
    // Starts from what the link has: a limit of 0 is a blank box.
    expect(within(dialog).getByLabelText('Label')).toHaveValue(openLink.label);
    expect(within(dialog).getByLabelText('Minimum (₦, optional)')).toHaveValue('1000');
    expect(within(dialog).getByLabelText('Maximum (₦, optional)')).toHaveValue('');

    await userEvent.clear(within(dialog).getByLabelText('Label'));
    await userEvent.type(within(dialog).getByLabelText('Maximum (₦, optional)'), '500');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(within(dialog).getByLabelText('Label')).toHaveAccessibleDescription('Give the link a label — your customers see it.');
    expect(within(dialog).getByLabelText('Maximum (₦, optional)')).toHaveAccessibleDescription('The maximum must be at least the minimum.');
    expect(f.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);

    await userEvent.type(within(dialog).getByLabelText('Label'), 'Ikeja desk');
    await userEvent.clear(within(dialog).getByLabelText('Maximum (₦, optional)'));
    await userEvent.type(within(dialog).getByLabelText('Maximum (₦, optional)'), '20000');
    f.mockImplementationOnce(async () => ok({ ...openLink, label: 'Ikeja desk', maxAmount: 2_000_000, version: 4 }));
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit link' })).not.toBeInTheDocument());
    const put = f.mock.calls.find(([, init]) => init?.method === 'PUT')!;
    expect(put[0]).toBe(`/api/pay/merchant/open-links/${openLink.id}?mode=live`);
    expect(JSON.parse(put[1].body)).toEqual({ label: 'Ikeja desk', minAmount: 100_000, maxAmount: 2_000_000, active: true, expectedVersion: 3 });
    expect(await screen.findByRole('dialog', { name: 'Ikeja desk' })).toBeInTheDocument();
    expect(screen.getByText('From ₦1,000.00 to ₦20,000.00')).toBeInTheDocument();
  });

  it('an Edit that lost a race re-reads the link, keeps what was typed, and saves against the fresh version', async () => {
    let version = 3;
    const f = stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/open-links': noLinks,
      [`/api/pay/merchant/open-links/${openLink.id}`]: () => ok({ openLink: { ...openLink, version }, payments: [] }),
    });
    mountAt(`/business/live/links/open/${openLink.id}?type=reusable`);
    const drawer = await screen.findByRole('dialog', { name: openLink.label });
    await userEvent.click(within(drawer).getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit link' });
    await userEvent.type(within(dialog).getByLabelText('Label'), ' (front)');
    version = 5; // someone paused it in the app meanwhile
    f.mockImplementationOnce(async () => fail(409, 'This link changed since you opened it — reload and try again', 'link_stale'));
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('This link changed since you opened it');
    await waitFor(() => expect(f.mock.calls.filter(([u, init]) => u.startsWith(`/api/pay/merchant/open-links/${openLink.id}?`) && init.method === 'GET').length).toBeGreaterThanOrEqual(2));
    expect(within(dialog).getByLabelText('Label')).toHaveValue(`${openLink.label} (front)`);
    f.mockImplementationOnce(async () => ok({ ...openLink, label: `${openLink.label} (front)`, version: 6 }));
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit link' })).not.toBeInTheDocument());
    const puts = f.mock.calls.filter(([, init]) => init?.method === 'PUT').map(([, init]) => JSON.parse(init.body).expectedVersion);
    expect(puts).toEqual([3, 5]);
  });

  it('the poster: business, label, QR, "Scan to pay", the address written out, and Print', async () => {
    const print = vi.fn();
    vi.stubGlobal('print', print);
    stubFetch({
      '/api/pay/merchant': ok(live),
      [`/api/pay/merchant/open-links/${openLink.id}`]: ok({ openLink: { ...openLink, mode: 'test' }, payments: [] }),
    });
    mountAt(`/business/test/links/open/${openLink.id}/poster`);
    const poster = await screen.findByRole('article', { name: 'Poster' });
    expect(within(poster).getByRole('heading', { name: 'Sky Air Ltd' })).toBeInTheDocument();
    expect(within(poster).getByText(openLink.label)).toBeInTheDocument();
    expect(within(poster).getByRole('img', { name: 'QR code for the payment link' })).toBeInTheDocument();
    expect(within(poster).getByText('Scan to pay with TSA Connect')).toBeInTheDocument();
    expect(within(poster).getByText('tsaconnectworld.com/l/sky-air-ltd-7k3q')).toBeInTheDocument();
    expect(within(poster).getByText('From ₦1,000.00 to ₦5,000,000.00')).toBeInTheDocument();
    expect(within(poster).getByText('Test poster — no real money moves')).toBeInTheDocument();
    // No app frame on the page that prints.
    expect(screen.queryByRole('navigation', { name: 'Main' })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Print' }));
    expect(print).toHaveBeenCalledTimes(1);
    // Back lands on the link's drawer over the Reusable list.
    expect(screen.getByRole('link', { name: /back to the link/i })).toHaveAttribute('href', `/business/test/links/open/${openLink.id}?type=reusable`);
  });

  it('a live poster says nothing about test mode', async () => {
    stubFetch({ '/api/pay/merchant': ok(live), [`/api/pay/merchant/open-links/${openLink.id}`]: ok({ openLink, payments: [] }) });
    mountAt(`/business/live/links/open/${openLink.id}/poster`);
    expect(await screen.findByRole('article', { name: 'Poster' })).toBeInTheDocument();
    expect(screen.queryByText(/Test poster/)).not.toBeInTheDocument();
  });

  it('a payment made through a reusable link is tagged on Payments, is for the link, and points back to it', async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/payments': ok({ items: [paidThrough, withNote], total: 2, page: 1, limit: 20 }),
      [`/api/pay/merchant/payments/${paidThrough.id}`]: ok({ payment: paidThrough, payer: { name: 'ADA OBI' }, deliveries: [] }),
    });
    mountAt('/business/live/payments');
    const row = (await screen.findByRole('link', { name: paidThrough.reference })).closest('tr')!;
    expect(within(row).getByText('Reusable link')).toBeInTheDocument();
    // No note: what it was for is the link's label.
    expect(within(row).getByText(openLink.label)).toBeInTheDocument();
    const noted = screen.getByRole('link', { name: withNote.reference }).closest('tr')!;
    expect(within(noted).getByText(`${openLink.label} · Note: Two bags, flight TS204`)).toBeInTheDocument();

    await userEvent.click(within(row).getByRole('link', { name: paidThrough.reference }));
    const drawer = await screen.findByRole('dialog', { name: paidThrough.reference });
    // Paid through names the link, and opens it over the Reusable list.
    await waitFor(() => expect(within(drawer).getByText('Paid through').nextElementSibling).toHaveTextContent(openLink.label));
    expect(within(drawer).getByText('For').nextElementSibling).toHaveTextContent(openLink.label);
    expect(within(drawer).queryByText("Customer's note")).not.toBeInTheDocument();
    expect(within(within(drawer).getByText('Paid through').nextElementSibling as HTMLElement).getByRole('link'))
      .toHaveAttribute('href', `/business/live/links/open/${openLink.id}?type=reusable`);
  });

  it("a customer's note on a reusable-link payment is shown as their note, under what it was for", async () => {
    stubFetch({
      '/api/pay/merchant': ok(live),
      '/api/pay/merchant/payments': noLinks,
      [`/api/pay/merchant/payments/${withNote.id}`]: ok({ payment: withNote, payer: { name: 'ADA OBI' }, deliveries: [] }),
    });
    mountAt(`/business/live/payments/${withNote.id}`);
    const drawer = await screen.findByRole('dialog', { name: withNote.reference });
    await waitFor(() => expect(within(drawer).getByText("Customer's note").nextElementSibling).toHaveTextContent('Two bags, flight TS204'));
    expect(within(drawer).getByText('For').nextElementSibling).toHaveTextContent(openLink.label);
  });
});
