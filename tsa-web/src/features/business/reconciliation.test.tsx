import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { fail, merchant, mountAt, ok, stubFetch } from '@/test/harness';
import { monthToDate } from './period';

const EXPORT = '/api/pay/merchant/payments/export';
const csv = (disposition?: string) =>
  new Response('reference\nPNR-1\n', { status: 200, headers: { 'Content-Type': 'text/csv', ...(disposition ? { 'Content-Disposition': disposition } : {}) } });
const createObjectURL = vi.fn(() => 'blob:csv');
const revokeObjectURL = vi.fn();
let saved: string[] = [];

const summary = (count = 2) => ok({
  succeeded: { count, customerTotal: 10_101_012, merchantNet: 10_000_000 },
  byStatus: { succeeded: 2, pending: 1, cancelled: 1 },
  refunds: { count: 1, amount: '12.50' },
});
const OCTOBER = '/business/live/reconciliation?from=2026-10-01&to=2026-10-31';
const summaryCalls = (f: ReturnType<typeof stubFetch>) =>
  f.mock.calls.map(([u]) => String(u)).filter((u) => u.startsWith('/api/pay/merchant/payments/summary'));

// The page's refunds list (Plan 3b): an empty period unless a test says otherwise.
const NO_REFUNDS = { '/api/pay/merchant/refunds': ok({ refunds: [], total: 0, page: 1, limit: 20 }) };

describe('refunds & reconciliation', () => {
  beforeEach(() => {
    saved = [];
    createObjectURL.mockClear();
    revokeObjectURL.mockClear();
    Object.assign(URL, { createObjectURL, revokeObjectURL });
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { saved.push(`${this.download}|${this.href}`); });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('totals the period: paid, received, refunded and how payments ended', async () => {
    const f = stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary() });
    mountAt(OCTOBER);
    expect(await screen.findByText('₦101,010.12')).toBeInTheDocument();
    expect(screen.getByText('Payments paid').nextElementSibling).toHaveTextContent('2');
    expect(screen.getByText('You received').nextElementSibling).toHaveTextContent('₦100,000.00');
    expect(screen.getByText('Refunded', { selector: 'p' }).nextElementSibling).toHaveTextContent('$12.50');
    expect(screen.getByText('1 refund')).toBeInTheDocument();
    expect(screen.getByText('Waiting', { selector: 'li > span' }).closest('li')).toHaveTextContent('1');
    expect(screen.getByText('Cancelled', { selector: 'li > span' }).closest('li')).toHaveTextContent('1');
    const url = new URL(summaryCalls(f)[0], 'http://x').searchParams;
    expect([url.get('mode'), url.get('from'), url.get('to')]).toEqual(['live', '2026-10-01', '2026-10-31']);
    // The refunds list under the totals reads the same period.
    await waitFor(() => expect(f.mock.calls.map(([u]) => String(u)).some((u) =>
      u.startsWith('/api/pay/merchant/refunds?') && u.includes('mode=live') && u.includes('from=2026-10-01') && u.includes('to=2026-10-31'))).toBe(true));
    expect(await screen.findByText('No refunds in this period')).toBeInTheDocument();
  });

  it('opens on this month so far', async () => {
    const f = stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary() });
    mountAt('/business/live/reconciliation');
    const { from, to } = monthToDate();
    expect(await screen.findByLabelText('From')).toHaveValue(from);
    expect(screen.getByLabelText('To')).toHaveValue(to);
    await waitFor(() => expect(summaryCalls(f).some((u) => u.includes(`from=${from}`) && u.includes(`to=${to}`))).toBe(true));
  });

  it('changing a date reads the new period', async () => {
    const f = stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary() });
    mountAt(OCTOBER);
    await screen.findByText('₦101,010.12');
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-10-15' } });
    await waitFor(() => expect(summaryCalls(f).some((u) => u.includes('to=2026-10-15'))).toBe(true));
  });

  it('an end before the start asks again and reads nothing', async () => {
    const f = stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary() });
    mountAt('/business/live/reconciliation?from=2026-10-31&to=2026-10-01');
    expect(await screen.findByRole('alert')).toHaveTextContent('The end date is before the start date.');
    expect(summaryCalls(f)).toHaveLength(0);
  });

  it('Export renews the session, then saves the period as a CSV named by the API', async () => {
    let merchantReads = 0;
    const f = stubFetch({
      ...NO_REFUNDS,
      '/api/pay/merchant': () => { merchantReads += 1; return ok(merchant); },
      '/api/pay/merchant/payments/summary': summary(),
      [EXPORT]: csv('attachment; filename="tsa-pay-payments-live-2026-10-01-2026-10-31.csv"'),
    });
    mountAt(OCTOBER);
    await screen.findByText('₦101,010.12');
    const before = merchantReads;
    await userEvent.click(screen.getByRole('button', { name: 'Export CSV' }));
    await waitFor(() => expect(saved).toEqual(['tsa-pay-payments-live-2026-10-01-2026-10-31.csv|blob:csv']));
    const call = (f.mock.calls as unknown as [string, RequestInit][]).find(([u]) => u.startsWith(EXPORT))!;
    expect(call[0]).toBe(`${EXPORT}?mode=live&from=2026-10-01&to=2026-10-31`);
    expect(call[1]).toMatchObject({ credentials: 'same-origin' });
    expect(merchantReads).toBeGreaterThan(before);
  });

  it('without a Content-Disposition the file is named from the mode and period', async () => {
    stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary(), [EXPORT]: csv() });
    mountAt(OCTOBER);
    await screen.findByText('₦101,010.12');
    await userEvent.click(screen.getByRole('button', { name: 'Export CSV' }));
    await waitFor(() => expect(saved).toEqual(['tsa-pay-live-2026-10-01-2026-10-31.csv|blob:csv']));
  });

  it.each([[422, 'Choose a shorter period.'], [429, 'Too many exports. Try again in a minute.']])(
    'a %i refusal shows the API message and saves nothing', async (status, message) => {
      stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary(), [EXPORT]: fail(status, message) });
      mountAt(OCTOBER);
      await screen.findByText('₦101,010.12');
      await userEvent.click(screen.getByRole('button', { name: 'Export CSV' }));
      expect(await screen.findByText(message)).toBeInTheDocument();
      expect(createObjectURL).not.toHaveBeenCalled();
      expect(saved).toEqual([]);
    });

  it('a failed session check says so and downloads nothing', async () => {
    let merchantReads = 0;
    const f = stubFetch({
      ...NO_REFUNDS,
      '/api/pay/merchant': () => (++merchantReads === 1 ? ok(merchant) : fail(503, 'TSA Connect is busy — try again')),
      '/api/pay/merchant/payments/summary': summary(),
      [EXPORT]: csv(),
    });
    mountAt(OCTOBER);
    await screen.findByText('₦101,010.12');
    await userEvent.click(screen.getByRole('button', { name: 'Export CSV' }));
    expect(await screen.findByText('TSA Connect is busy — try again')).toBeInTheDocument();
    expect(f.mock.calls.some(([u]) => String(u).startsWith(EXPORT))).toBe(false);
    expect(saved).toEqual([]);
  });

  it('Export is off when nothing was paid', async () => {
    stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary(0) });
    mountAt(OCTOBER);
    expect(await screen.findByText('Nothing was paid in this period.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Export CSV' })).toBeDisabled();
  });

  it('Export is off when more was paid than one export holds', async () => {
    stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary(10_001) });
    mountAt(OCTOBER);
    expect(await screen.findByText(/More than 10,000 payments were paid in this period/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Export CSV' })).toBeDisabled();
  });

  it('a period longer than 366 days is totalled but not exported', async () => {
    stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary() });
    mountAt('/business/live/reconciliation?from=2024-01-01&to=2025-01-01');
    expect(await screen.findByText('₦101,010.12')).toBeInTheDocument();
    expect(screen.getByText('Choose at most 366 days.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Export CSV' })).toBeDisabled();
  });

  it('test mode totals test payments', async () => {
    const f = stubFetch({ ...NO_REFUNDS, '/api/pay/merchant': ok(merchant), '/api/pay/merchant/payments/summary': summary() });
    mountAt('/business/test/reconciliation?from=2026-10-01&to=2026-10-31');
    await screen.findByText('₦101,010.12');
    expect(summaryCalls(f)[0]).toContain('mode=test');
  });
});
