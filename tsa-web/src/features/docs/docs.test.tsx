import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { Docs } from './Docs';
import { createResponse, webhookBody } from './samples';

function mount() {
  return render(
    <MemoryRouter initialEntries={['/docs']}>
      <Docs />
    </MemoryRouter>,
  );
}

describe('developer docs', () => {
  let f: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    f = vi.fn();
    vi.stubGlobal('fetch', f);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('is a public page that needs no session and makes no requests', () => {
    mount();
    for (const name of ['Create a payment', 'The payment object', 'Webhooks', 'Verify the signature', 'Test mode', 'Errors']) {
      expect(screen.getByRole('heading', { level: 2, name })).toBeInTheDocument();
    }
    expect(f).not.toHaveBeenCalled();
  });

  it('every section is linked from the table of contents', () => {
    mount();
    const toc = screen.getByRole('navigation', { name: 'On this page' });
    for (const link of within(toc).getAllByRole('link')) {
      const id = link.getAttribute('href')!.replace(/^#/, '');
      expect(document.getElementById(id)).toBeInTheDocument();
    }
    expect(within(toc).getByRole('link', { name: 'Verify the signature' })).toHaveAttribute('href', '#verify-the-signature');
  });

  it('the Node verification sample compares in constant time over "<t>.<body>"', () => {
    mount();
    const sample = screen.getByRole('figure', { name: 'Verify a webhook in Node' });
    expect(sample.textContent).toContain('crypto.timingSafeEqual');
    expect(sample.textContent).toContain('`${t}.${rawBody}`');
  });

  it('names the real header, signature format and events', () => {
    mount();
    const verify = screen.getByRole('heading', { level: 2, name: 'Verify the signature' }).closest('section')!;
    expect(within(verify).getAllByText('X-TSA-Signature').length).toBeGreaterThan(0);
    expect(within(verify).getByText('t=<timestamp>,v1=<signature>')).toBeInTheDocument();
    const hooks = screen.getByRole('heading', { level: 2, name: 'Webhooks' }).closest('section')!;
    for (const event of ['payment.succeeded', 'payment.failed', 'payment.expired', 'payment.refunded']) {
      expect(within(hooks).getAllByText(event).length).toBeGreaterThan(0);
    }
    expect(within(hooks).getByText(/1 minute, 5 minutes, 30 minutes, 2 hours, 6 hours and 24 hours/)).toBeInTheDocument();
  });

  it('lists both live refusals and the allowlist refusal in the error table', () => {
    mount();
    const errors = screen.getByRole('heading', { level: 2, name: 'Errors' }).closest('section')!;
    for (const code of ['live_disabled', 'live_not_enabled', 'ip_not_allowed', 'https_required']) {
      const row = within(errors).getByText(code).closest('tr')!;
      expect(within(row).getByText('403')).toBeInTheDocument();
    }
  });

  it('Copy puts the whole sample on the clipboard', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    mount();
    const sample = screen.getByRole('figure', { name: 'Verify a webhook in Node' });
    await user.click(within(sample).getByRole('button', { name: 'Copy Verify a webhook in Node' }));
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining('crypto.timingSafeEqual'));
    expect(writeText.mock.calls[0][0]).toBe(sample.querySelector('pre')!.textContent);
  });

  it('test mode says payouts come first, and what a create answers until then', () => {
    mount();
    const test = screen.getByRole('heading', { level: 2, name: 'Test mode' }).closest('section')!;
    expect(within(test).getByRole('link', { name: 'Payouts' })).toHaveAttribute('href', '/business/live/payouts');
    expect(within(test).getByText(/every\s+create answers/)).toBeInTheDocument();
  });

  it('gives the reference and description limits in bytes', () => {
    mount();
    const create = screen.getByRole('heading', { level: 2, name: 'Create a payment' }).closest('section')!;
    expect(within(create).getByText(/up to 128 bytes/)).toBeInTheDocument();
    expect(within(create).getByText(/up to 255 bytes/)).toBeInTheDocument();
  });

  it('the Node create sample checks the answer is JSON before parsing it', () => {
    mount();
    const sample = screen.getByRole('figure', { name: 'Create a payment in Node' }).textContent!;
    expect(sample.indexOf("res.headers.get('content-type')")).toBeGreaterThan(-1);
    expect(sample.indexOf("res.headers.get('content-type')")).toBeLessThan(sample.indexOf('res.json()'));
    expect(sample).toContain('!res.ok || !body.success');
  });

  it('documents the lookup by your own reference: exact, in the key\'s mode, as data, like GET /payments/:id', () => {
    mount();
    const create = screen.getByRole('heading', { level: 2, name: 'Create a payment' }).closest('section')!;
    expect(within(create).getByRole('heading', { level: 3, name: 'Find a payment by your reference' })).toBeInTheDocument();
    expect(within(create).getByText('GET /payments?reference=<your reference>')).toBeInTheDocument();
    expect(within(create).getAllByText('GET /payments/:id').length).toBeGreaterThan(0);
    expect(within(create).getByText(/exactly like/).textContent).toContain('GET /payments/:id');
    const sample = screen.getByRole('figure', { name: 'Find by reference with curl' });
    expect(sample.textContent).toContain('--data-urlencode "reference=PNR-K8Q2ZL"');
  });

  it('documents the payment code on the payment object and in the samples', () => {
    mount();
    const object = screen.getByRole('heading', { level: 2, name: 'The payment object' }).closest('section')!;
    expect(within(object).getByText('payCode')).toBeInTheDocument();
    expect(within(object).getByText(/tap Pay on the home screen/)).toBeInTheDocument();
    // There is no API lookup by code: the dashboard search is where it is matched.
    const payCodeRow = within(object).getByText(/tap Pay on the home screen/).textContent!;
    expect(payCodeRow).toContain('search for it in your dashboard');
    expect(payCodeRow).toContain('no API lookup by code');
    expect(createResponse).toContain('"payCode": "TSA-7K2QF-M9XWD"');
    expect(webhookBody).toContain('"payCode": "TSA-7K2QF-M9XWD"');
  });

  it('documents refunds: the event, its body, the payment fields, and that status stays succeeded', () => {
    mount();
    const hooks = screen.getByRole('heading', { level: 2, name: 'Webhooks' }).closest('section')!;
    expect(within(hooks).getByText(/and again each time you refund one/)).toBeInTheDocument();
    expect(within(hooks).queryByText("When a payment reaches a final status, we POST an event to that mode's webhook URL.")).toBeNull();
    const sample = screen.getByRole('figure', { name: 'payment.refunded event body' }).textContent!;
    expect(sample).toContain('"type": "payment.refunded"');
    expect(sample).toContain('"refund": {');
    expect(sample).toContain('"token": "USDT"');
    expect(sample).toContain('"refundStatus": "partial"');
    for (const key of ['chainId', 'confirmedAt', 'txHash']) expect(sample).toContain(`"${key}":`);
    expect(hooks.textContent).toContain("to your customer's TSA Connect wallet");
    expect(hooks.textContent).toContain('up to what your customer actually paid (which includes any fee they paid); TSA does not return its own fee');
    expect(hooks.textContent).not.toContain('the fee is not returned');
    const object = screen.getByRole('heading', { level: 2, name: 'The payment object' }).closest('section')!;
    for (const field of ['refundedAmount', 'refundStatus']) expect(within(object).getAllByText(field).length).toBeGreaterThan(0);
    expect(object.textContent).toContain("A refund doesn't change status: a refunded payment stays succeeded.");
    const test = screen.getByRole('heading', { level: 2, name: 'Test mode' }).closest('section')!;
    expect(test.textContent).toContain('a fixed ₦1,500 per US dollar');
  });
});
