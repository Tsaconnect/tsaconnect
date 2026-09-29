import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { accepted, fail, merchant as base, mountAt, ok, soon, stubFetch } from '@/test/harness';

// Renames switched on, as the API says in the owner's merchant view.
const merchant = { ...base, businessNameChangeEnabled: true };

const banks = ok([{ code: '044', name: 'Access Bank' }, { code: '057', name: 'Zenith Bank' }]);
const namePending = (id: string) => accepted({ confirmation: { id, kind: 'business_name', expiresAt: soon, sentTo: 'a***@skyair.ng' } });
const waiting = { ...merchant, pendingBusinessName: 'Sky Air Nigeria', pendingSince: new Date(Date.now() - 3 * 60 * 60_000).toISOString() };
const waitingNote = "Your new name 'Sky Air Nigeria' is waiting for approval. Customers and payouts keep 'Sky Air Ltd' until then.";

const nameCard = () => screen.findByRole('region', { name: 'Business name' });

async function typeNewName(name: string) {
  await userEvent.click(await screen.findByRole('button', { name: 'Change business name' }));
  const edit = await screen.findByRole('dialog', { name: 'Change your business name' });
  const field = within(edit).getByLabelText('New business name');
  await userEvent.clear(field);
  if (name) await userEvent.type(field, name);
  await userEvent.click(within(edit).getByRole('button', { name: 'Continue' }));
  return edit;
}

async function sendPassword(title: string, password = 'correct-horse') {
  const guard = await screen.findByRole('dialog', { name: title });
  await userEvent.type(within(guard).getByLabelText('Password'), password);
  await userEvent.click(within(guard).getByRole('button', { name: /send code/i }));
  return guard;
}

describe('business name', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('a rename needs the password and an emailed code, and waits for approval with the current name still in use', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/business-name': namePending('n1'),
      '/api/pay/merchant/actions/n1/confirm': ok({ merchant: waiting }),
    });
    mountAt('/business/live/payouts');
    const card = await nameCard();
    expect(within(card).getByText('Sky Air Ltd')).toBeInTheDocument();

    // Spacing is tidied as the API stores it.
    await typeNewName('  Sky   Air Nigeria ');
    const guard = await sendPassword('Confirm your new business name');
    expect(within(guard).getByText(/renamed "Sky Air Nigeria"\. It changes only when we approve it/)).toBeInTheDocument();
    await userEvent.type(await within(guard).findByLabelText('Code from your email'), '482913');
    await userEvent.click(within(guard).getByRole('button', { name: 'Send for approval' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const save = f.mock.calls.find(([u]) => u === '/api/pay/merchant/business-name');
    expect(save![1].method).toBe('PUT');
    expect(JSON.parse(save![1].body)).toEqual({ name: 'Sky Air Nigeria', password: 'correct-horse', expectedVersion: merchant.version });
    expect(JSON.parse(f.mock.calls.find(([u]) => u === '/api/pay/merchant/actions/n1/confirm')![1].body)).toEqual({ code: '482913' });

    // The name is unchanged until approval; the card says which name waits.
    expect(await within(card).findByText(/is waiting for approval/)).toHaveTextContent(waitingNote);
    expect(within(card).getByText('Sky Air Ltd')).toBeInTheDocument();
    expect(within(card).getByText('Asked 3 h ago.')).toBeInTheDocument();
    expect(screen.queryAllByText('Sky Air Nigeria')).toHaveLength(0);
    expect((await screen.findAllByText('Sent for approval'))[0]).toBeInTheDocument();
  });

  it('a blank, too short, unchanged or already-waiting name is caught before any request', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(waiting), '/api/pay/merchant/banks': banks });
    mountAt('/business/live/payouts');
    const edit = await typeNewName('');
    const field = within(edit).getByLabelText('New business name');
    expect(within(edit).getByText('Enter a name of 2 to 100 characters.')).toBeInTheDocument();
    expect(field).toHaveAttribute('aria-invalid', 'true');
    await userEvent.type(field, 'S');
    await userEvent.click(within(edit).getByRole('button', { name: 'Continue' }));
    expect(within(edit).getByText('Enter a name of 2 to 100 characters.')).toBeInTheDocument();
    await userEvent.clear(field);
    await userEvent.type(field, 'Sky  Air Nigeria');
    await userEvent.click(within(edit).getByRole('button', { name: 'Continue' }));
    expect(within(edit).getByText('That name is already waiting for approval.')).toBeInTheDocument();
    expect(f.mock.calls.some(([u]) => u === '/api/pay/merchant/business-name')).toBe(false);
  });

  it('with nothing waiting, the current name is not a change', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/banks': banks });
    mountAt('/business/live/payouts');
    const edit = await typeNewName(' Sky Air  Ltd');
    expect(within(edit).getByText("That's already your business name.")).toBeInTheDocument();
    expect(f.mock.calls.some(([u]) => u === '/api/pay/merchant/business-name')).toBe(false);
  });

  it('asking for the current name back withdraws the waiting one without a code', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(waiting),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/business-name': ok({ merchant }),
    });
    mountAt('/business/live/payouts');
    const card = await nameCard();
    expect(within(card).getByText(/is waiting for approval/)).toHaveTextContent(waitingNote);
    await typeNewName('Sky Air Ltd');
    await sendPassword('Keep your business name');
    // The 200 answer finishes the dialog: no code step left open.
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(JSON.parse(f.mock.calls.find(([u]) => u === '/api/pay/merchant/business-name')![1].body)).toMatchObject({ name: 'Sky Air Ltd' });
    expect((await screen.findAllByText('Rename request withdrawn'))[0]).toBeInTheDocument();
    expect(within(card).queryByText(/is waiting for approval/)).not.toBeInTheDocument();
  });

  it("a name the API won't take is shown on the name field", async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/business-name': fail(422, 'invalid merchant settings: a business name can\'t contain "TSA Pay"'),
    });
    mountAt('/business/live/payouts');
    await typeNewName('Sky Air TSA Pay');
    await sendPassword('Confirm your new business name');
    const edit = await screen.findByRole('dialog', { name: 'Change your business name' });
    const field = within(edit).getByLabelText('New business name');
    expect(field).toHaveValue('Sky Air TSA Pay');
    expect(field).toHaveAttribute('aria-invalid', 'true');
    expect(within(edit).getByText('A business name can\'t contain "TSA Pay"')).toBeInTheDocument();
  });

  it('cancelling the code step goes back to the form with the name typed', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/banks': banks });
    mountAt('/business/live/payouts');
    await typeNewName('Sky Air Nigeria');
    const guard = await screen.findByRole('dialog', { name: 'Confirm your new business name' });
    await userEvent.click(within(guard).getByRole('button', { name: 'Cancel' }));
    const edit = await screen.findByRole('dialog', { name: 'Change your business name' });
    expect(within(edit).getByLabelText('New business name')).toHaveValue('Sky Air Nigeria');
  });

  it('a wrong password is refused under the password', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/business-name': fail(401, 'Incorrect password', 'password_incorrect'),
    });
    mountAt('/business/live/payouts');
    await typeNewName('Sky Air Nigeria');
    const guard = await sendPassword('Confirm your new business name', 'wrong');
    expect(await within(guard).findByRole('alert')).toHaveTextContent("That password isn't right.");
  });

  it('with renames switched off (or an API that does not say), the name and a waiting rename show, and there is no way to ask', async () => {
    for (const off of [{ businessNameChangeEnabled: false }, { businessNameChangeEnabled: undefined }]) {
      stubFetch({ '/api/pay/merchant': ok({ ...waiting, ...off }), '/api/pay/merchant/banks': banks });
      const { unmount } = mountAt('/business/live/payouts');
      const card = await nameCard();
      expect(within(card).getByText('Sky Air Ltd')).toBeInTheDocument();
      expect(within(card).getByText(/is waiting for approval/)).toBeInTheDocument();
      expect(within(card).queryByRole('button', { name: 'Change business name' })).not.toBeInTheDocument();
      unmount();
      vi.unstubAllGlobals();
    }
  });

  it('a suspended merchant cannot ask for a new name', async () => {
    stubFetch({ '/api/pay/merchant': ok({ ...merchant, status: 'suspended' }), '/api/pay/merchant/banks': banks });
    mountAt('/business/live/payouts');
    await nameCard();
    expect(screen.queryByRole('button', { name: 'Change business name' })).not.toBeInTheDocument();
  });
});
