import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { accepted, fail, merchant, mountAt, ok, soon, stubFetch } from '@/test/harness';

const keysPending = accepted({ confirmation: { id: 'a1', kind: 'keys', expiresAt: soon, sentTo: 'a***@skyair.ng' } });

async function passwordThenCode(button: RegExp, code = '482913') {
  await userEvent.click(await screen.findByRole('button', { name: button }));
  await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
  await userEvent.click(screen.getByRole('button', { name: /send code/i }));
  await userEvent.type(await screen.findByLabelText('Code from your email'), code);
}

describe('API keys & webhooks', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('shows each key by its prefix only, and whether webhooks are set', async () => {
    stubFetch({ '/api/pay/merchant': ok({ ...merchant, hasTestKey: false, testKeyPrefix: undefined, webhookUrl: 'https://api.skyair.ng/tsa/webhook' }) });
    mountAt('/business/live/developers');
    expect(await screen.findByText(/^tsapk_live_7Hq2/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create test key' })).toBeInTheDocument();
    expect(screen.getByText('https://api.skyair.ng/tsa/webhook')).toBeInTheDocument();
    expect(screen.getByText('Not set')).toBeInTheDocument();
    expect(screen.getByText('X-TSA-Signature')).toBeInTheDocument();
  });

  it('says a live key is refused until live payments are switched on', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant) });
    mountAt('/business/live/developers');
    expect(await screen.findByText("Live payments are switched off — your live key is refused until TSA Connect switches them back on.")).toBeInTheDocument();
  });

  it('says nothing about live once it is on', async () => {
    stubFetch({ '/api/pay/merchant': ok({ ...merchant, liveEnabled: true }) });
    mountAt('/business/live/developers');
    expect(await screen.findByText(/^tsapk_live_7Hq2/)).toBeInTheDocument();
    expect(screen.queryByText(/Live payments aren't on yet/)).not.toBeInTheDocument();
  });

  it('rolling a key asks for the password, then the emailed code, then shows the key once', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/keys': keysPending,
      '/api/pay/merchant/actions/a1/confirm': ok({ mode: 'live', key: 'tsapk_live_NEWKEY' }),
    });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /create new live key/i }));
    expect(screen.getByText(/stops working immediately when you confirm/i)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    expect(await screen.findByText(/a\*\*\*@skyair\.ng/)).toBeInTheDocument();
    // The confirm step is the click that kills the old key, so it says so again.
    expect(screen.getByText(/stops working immediately when you confirm/i)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Code from your email'), '482913');
    await userEvent.click(screen.getByRole('button', { name: /create new live key/i }));
    expect(await screen.findByText('tsapk_live_NEWKEY')).toBeInTheDocument();
    expect(screen.getByText(/only time you'll see this key/i)).toBeInTheDocument();
    expect(f.mock.calls.filter(([u]) => u === '/api/pay/merchant/actions/a1/confirm')).toHaveLength(1);
    const request = f.mock.calls.find(([u]) => u === '/api/pay/merchant/keys');
    expect(JSON.parse(request![1].body)).toEqual({ mode: 'live', password: 'correct-horse' });
    const confirm = f.mock.calls.find(([u]) => u === '/api/pay/merchant/actions/a1/confirm');
    expect(JSON.parse(confirm![1].body)).toEqual({ code: '482913' });

    // Shown once, so nothing but Done closes it: not Escape, not a click outside, and there is no X.
    const result = screen.getByRole('dialog', { name: 'Your new live key' });
    expect(within(result).queryByRole('button', { name: 'Close' })).not.toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    await userEvent.click(document.querySelector('.fixed.inset-0')!); // the overlay around the dialog
    expect(screen.getByText('tsapk_live_NEWKEY')).toBeInTheDocument();

    // Closing the result drops the key.
    await userEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByText('tsapk_live_NEWKEY')).not.toBeInTheDocument();
  });

  it('creating a key when there is none has no warning and shows the new key once', async () => {
    let created = false;
    const f = stubFetch({
      '/api/pay/merchant': () => ok(created ? { ...merchant, hasTestKey: true, testKeyPrefix: 'tsapk_test_N3w0' } : { ...merchant, hasTestKey: false, testKeyPrefix: undefined }),
      '/api/pay/merchant/keys': keysPending,
      '/api/pay/merchant/actions/a1/confirm': () => {
        created = true;
        return ok({ mode: 'test', key: 'tsapk_test_N3w0FRESH' });
      },
    });
    mountAt('/business/test/developers');
    expect(await screen.findByText('No key yet')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Create test key' }));
    const dialog = screen.getByRole('dialog', { name: 'Create your test API key' });
    expect(within(dialog).queryByText(/stops working/i)).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    await userEvent.type(await screen.findByLabelText('Code from your email'), '482913');
    await userEvent.click(screen.getByRole('button', { name: 'Create test key' }));
    expect(await screen.findByText('tsapk_test_N3w0FRESH')).toBeInTheDocument();
    expect(JSON.parse(f.mock.calls.find(([u]) => u === '/api/pay/merchant/keys')![1].body)).toEqual({ mode: 'test', password: 'correct-horse' });
    await userEvent.click(screen.getByRole('button', { name: 'Done' }));
    // The confirm has no merchant in it, so the merchant is read again for the new prefix.
    expect(await screen.findByText('tsapk_test_N3w0••••••••••••')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create new test key' })).toBeInTheDocument();
  });

  // Review Focus 1.
  it('confirm is sent once even when clicked twice', async () => {
    let resolve!: (r: Response) => void;
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/keys': keysPending,
      '/api/pay/merchant/actions/a1/confirm': () => new Promise<Response>((r) => { resolve = r; }),
    });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /create new test key/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    await userEvent.type(await screen.findByLabelText('Code from your email'), '482913');
    const confirm = screen.getByRole('button', { name: /create new test key/i });
    await userEvent.click(confirm);
    await userEvent.click(confirm);
    resolve(ok({ mode: 'test', key: 'tsapk_test_X' }));
    expect(await screen.findByText('tsapk_test_X')).toBeInTheDocument();
    expect(f.mock.calls.filter(([u]) => u === '/api/pay/merchant/actions/a1/confirm')).toHaveLength(1);
  });

  // The button is disabled once React re-renders; two submits before that (a double
  // Enter) are stopped by the in-flight ref alone.
  it('two submits in the same tick still send one confirm', async () => {
    let resolve!: (r: Response) => void;
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/keys': keysPending,
      '/api/pay/merchant/actions/a1/confirm': () => new Promise<Response>((r) => { resolve = r; }),
    });
    mountAt('/business/live/developers');
    await passwordThenCode(/create new test key/i);
    const form = screen.getByLabelText('Code from your email').closest('form')!;
    act(() => {
      form.requestSubmit();
      form.requestSubmit();
    });
    resolve(ok({ mode: 'test', key: 'tsapk_test_X' }));
    expect(await screen.findByText('tsapk_test_X')).toBeInTheDocument();
    expect(f.mock.calls.filter(([u]) => u === '/api/pay/merchant/actions/a1/confirm')).toHaveLength(1);
  });

  it('a wrong password is said plainly and asks again', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/keys': fail(401, 'Incorrect password', 'password_incorrect') });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /create new test key/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'nope');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent("That password isn't right.");
    expect(screen.getByLabelText('Password')).toHaveValue('');
  });

  it('a wrong code is said plainly; an expired change starts again from the password', async () => {
    let answer = fail(422, "That code isn't right", 'code_invalid');
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/keys': keysPending,
      '/api/pay/merchant/actions/a1/confirm': () => answer,
    });
    mountAt('/business/live/developers');
    await passwordThenCode(/create new test key/i, '111111');
    await userEvent.click(screen.getByRole('button', { name: /create new test key/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent("That code isn't right.");
    expect(screen.getByLabelText('Code from your email')).toHaveValue('');

    answer = fail(410, 'This change has expired or had too many wrong codes — start again', 'no_pending_change');
    await userEvent.type(screen.getByLabelText('Code from your email'), '222222');
    await userEvent.click(screen.getByRole('button', { name: /create new test key/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent('This change expired or had too many wrong codes. Start again.');
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(screen.queryByLabelText('Code from your email')).not.toBeInTheDocument();
  });

  it('a bad webhook URL is refused with the server reason before any code', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/endpoints': fail(422, 'invalid merchant settings: webhook url: must be a public https address'),
    });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /edit webhooks/i }));
    await userEvent.type(screen.getByLabelText('Live webhook URL'), 'http://10.0.0.1/x');
    await userEvent.click(screen.getByRole('button', { name: /continue/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/public https address/);
  });

  it('saving webhooks sends all three settings against the version on screen and shows the result', async () => {
    const saved = { ...merchant, version: 8, webhookUrl: 'https://api.skyair.ng/tsa/webhook', returnDomains: ['skyair.ng', 'www.skyair.ng'] };
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/endpoints': accepted({ confirmation: { id: 'e1', kind: 'endpoints', expiresAt: soon, sentTo: 'a***@skyair.ng' } }),
      '/api/pay/merchant/actions/e1/confirm': ok({ merchant: saved }),
    });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /edit webhooks/i }));
    await userEvent.type(screen.getByLabelText('Live webhook URL'), 'https://api.skyair.ng/tsa/webhook');
    const domains = screen.getByLabelText('Return domains');
    await userEvent.clear(domains);
    await userEvent.type(domains, 'skyair.ng, www.skyair.ng,');
    await userEvent.click(screen.getByRole('button', { name: /continue/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    await userEvent.type(await screen.findByLabelText('Code from your email'), '482913');
    await userEvent.click(screen.getByRole('button', { name: 'Save webhooks' }));
    expect(await screen.findByText('https://api.skyair.ng/tsa/webhook')).toBeInTheDocument();
    const put = f.mock.calls.find(([u]) => u === '/api/pay/merchant/endpoints');
    expect(put![1].method).toBe('PUT');
    expect(JSON.parse(put![1].body)).toEqual({
      webhookUrl: 'https://api.skyair.ng/tsa/webhook', testWebhookUrl: '', returnDomains: ['skyair.ng', 'www.skyair.ng'], expectedVersion: 7, password: 'correct-horse',
    });
    const domainsCard = screen.getByRole('heading', { name: 'Return domains' }).closest('section')!;
    expect(within(domainsCard).getByText('www.skyair.ng')).toBeInTheDocument();
    // The confirm's merchant is used as-is: no second read of the merchant.
    await waitFor(() => expect(f.mock.calls.filter(([u]) => u === '/api/pay/merchant')).toHaveLength(1));
  });

  it('the same form opened from Return domains is titled for them, and still sends all three', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok({ ...merchant, webhookUrl: 'https://api.skyair.ng/tsa/webhook' }),
      '/api/pay/merchant/endpoints': fail(422, 'invalid merchant settings: return domain: bad'),
    });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: 'Edit return domains' }));
    expect(screen.getByRole('dialog', { name: 'Edit return domains' })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Return domains'), ', pay.skyair.ng');
    await userEvent.click(screen.getByRole('button', { name: /continue/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    await screen.findByRole('alert');
    const put = f.mock.calls.find(([u]) => u === '/api/pay/merchant/endpoints');
    expect(JSON.parse(put![1].body)).toEqual({
      webhookUrl: 'https://api.skyair.ng/tsa/webhook', testWebhookUrl: '', returnDomains: ['skyair.ng', 'pay.skyair.ng'], expectedVersion: 7, password: 'correct-horse',
    });
  });

  it('opened from the webhooks card it is titled Edit webhooks', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant) });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /edit webhooks/i }));
    expect(screen.getByRole('dialog', { name: 'Edit webhooks' })).toBeInTheDocument();
  });

  it('a stale screen is told to reload, and the form comes back with what was typed', async () => {
    let reads = 0;
    const f = stubFetch({
      // Someone else saved in between: the second read is version 8.
      '/api/pay/merchant': () => ok({ ...merchant, version: ++reads === 1 ? 7 : 8 }),
      '/api/pay/merchant/endpoints': (() => {
        let calls = 0;
        return () => (++calls === 1
          ? fail(409, 'Your payment settings changed since you opened this screen — reload and try again', 'merchant_stale')
          : fail(422, 'invalid merchant settings: return domain: bad'));
      })(),
    });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /edit webhooks/i }));
    await userEvent.type(screen.getByLabelText('Live webhook URL'), 'https://api.skyair.ng/tsa/webhook');
    await userEvent.type(screen.getByLabelText('Return domains'), ', pay.skyair.ng');
    await userEvent.click(screen.getByRole('button', { name: /continue/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Your settings changed since you opened this — reload and try again.');
    await userEvent.click(screen.getByRole('button', { name: 'Reload' }));

    const form = await screen.findByRole('dialog', { name: 'Edit webhooks' });
    expect(within(form).getByLabelText('Live webhook URL')).toHaveValue('https://api.skyair.ng/tsa/webhook');
    expect(within(form).getByLabelText('Return domains')).toHaveValue('skyair.ng, pay.skyair.ng');
    await waitFor(() => expect(reads).toBe(2));
    await userEvent.click(screen.getByRole('button', { name: /continue/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    await screen.findByRole('alert');
    const puts = f.mock.calls.filter(([u]) => u === '/api/pay/merchant/endpoints').map(([, init]) => JSON.parse(init.body));
    expect(puts.map((b) => b.expectedVersion)).toEqual([7, 8]);
    expect(puts[1]).toMatchObject({ webhookUrl: 'https://api.skyair.ng/tsa/webhook', returnDomains: ['skyair.ng', 'pay.skyair.ng'] });
  });

  it('a suspended merchant sees every change locked, and why', async () => {
    stubFetch({ '/api/pay/merchant': ok({ ...merchant, status: 'suspended' }) });
    mountAt('/business/live/developers');
    expect(await screen.findByText(/suspended — contact support to create a key, change your webhooks or see your signing secret/i)).toBeInTheDocument();
    for (const name of ['Create new live key', 'Create new test key', 'Edit webhooks', 'Edit return domains', 'Reveal', 'Create new secret']) {
      expect(screen.getByRole('button', { name })).toBeDisabled();
    }
  });

  it('revealing the signing secret shows the live and test secrets', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/webhook-secret': accepted({ confirmation: { id: 'w1', kind: 'webhook_secret', expiresAt: soon, sentTo: 'a***@skyair.ng' } }),
      '/api/pay/merchant/actions/w1/confirm': ok({ secretAction: 'reveal', webhookSecretLive: 'whsec_live_A', webhookSecretTest: 'whsec_test_B' }),
    });
    mountAt('/business/live/developers');
    await passwordThenCode(/^reveal$/i);
    await userEvent.click(screen.getByRole('button', { name: 'Reveal secret' }));
    const result = await screen.findByRole('dialog', { name: /signing secret/i });
    expect(within(result).getByText('whsec_live_A')).toBeInTheDocument();
    expect(within(result).getByText('whsec_test_B')).toBeInTheDocument();
    const request = f.mock.calls.find(([u]) => u === '/api/pay/merchant/webhook-secret');
    expect(JSON.parse(request![1].body)).toEqual({ action: 'reveal', password: 'correct-horse' });
  });

  it('rolling the signing secret warns first, then shows the new secrets once', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/webhook-secret': accepted({ confirmation: { id: 'w2', kind: 'webhook_secret', expiresAt: soon, sentTo: 'a***@skyair.ng' } }),
      '/api/pay/merchant/actions/w2/confirm': ok({ secretAction: 'roll', webhookSecretLive: 'whsec_live_NEW', webhookSecretTest: 'whsec_test_NEW' }),
    });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: 'Create new secret' }));
    expect(screen.getByRole('dialog', { name: 'Create a new signing secret' })).toHaveTextContent(/stops working immediately when you confirm, for live and test/);
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    await userEvent.type(await screen.findByLabelText('Code from your email'), '482913');
    await userEvent.click(screen.getByRole('button', { name: 'Create new secret' }));
    const result = await screen.findByRole('dialog', { name: /signing secret/i });
    expect(within(result).getByText('whsec_live_NEW')).toBeInTheDocument();
    expect(within(result).getByText('whsec_test_NEW')).toBeInTheDocument();
    expect(within(result).getByText(/only time you'll see these secrets/i)).toBeInTheDocument();
    expect(JSON.parse(f.mock.calls.find(([u]) => u === '/api/pay/merchant/webhook-secret')![1].body)).toEqual({ action: 'roll', password: 'correct-horse' });
  });

  it('a wrong password puts the cursor back in the emptied field; the code is tied to where it was sent', async () => {
    let answer = fail(401, 'Incorrect password', 'password_incorrect');
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/keys': () => answer });
    mountAt('/business/live/developers');
    await userEvent.click(await screen.findByRole('button', { name: /create new test key/i }));
    await userEvent.type(screen.getByLabelText('Password'), 'nope');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    await screen.findByRole('alert');
    expect(screen.getByLabelText('Password')).toHaveFocus();

    answer = keysPending;
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: /send code/i }));
    expect(await screen.findByLabelText('Code from your email')).toHaveAccessibleDescription(/We sent a 6-digit code to a\*\*\*@skyair\.ng/);
  });
});
