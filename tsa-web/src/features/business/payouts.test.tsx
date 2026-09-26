import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { accepted, fail, merchant, mountAt, ok, soon, stubFetch } from '@/test/harness';

const banks = ok([{ code: '044', name: 'Access Bank' }, { code: '057', name: 'Zenith Bank' }]);
const usdcOnBase = ok([{ chainId: 8453, chain: 'base', symbol: 'USDC', tokenAddress: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913' }]);
const wallet = '0x52908400098527886E0F7030069857D2E4169EE7';
const settlementPending = (id: string) => accepted({ confirmation: { id, kind: 'settlement', expiresAt: soon, sentTo: 'a***@skyair.ng' } });
const cryptoMerchant = { ...merchant, version: 8, settlementMode: 'crypto', tokenSymbol: 'USDC', chainId: 8453, walletAddress: wallet };

async function chooseWallet(address = wallet) {
  await userEvent.click(await screen.findByRole('radio', { name: /crypto wallet/i }));
  await userEvent.selectOptions(await screen.findByLabelText('Token'), 'USDC:8453');
  await userEvent.type(screen.getByLabelText('Wallet address'), address);
  await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
  await userEvent.click(screen.getByRole('button', { name: 'Save' }));
}

describe('payouts', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('says where the merchant is paid now', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/banks': banks });
    mountAt('/business/live/payouts');
    expect(await screen.findByText('SKY AIR LIMITED')).toBeInTheDocument();
    expect(await screen.findByText(/Access Bank · \*\*\*\*\*\*3210/)).toBeInTheDocument();
  });

  it('a bank change shows the resolved name, then waits for the emailed code before it applies', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/banks/resolve': ok({ accountNumber: '1023456789', accountName: 'SKY AIR LTD' }),
      '/api/pay/merchant/settlement': settlementPending('b1'),
      '/api/pay/merchant/actions/b1/confirm': ok({ merchant: { ...merchant, version: 8, bankCode: '057', accountNumber: '******6789', accountName: 'SKY AIR LTD' } }),
    });
    mountAt('/business/live/payouts');
    await userEvent.selectOptions(await screen.findByLabelText('Bank'), '057');
    await userEvent.type(screen.getByLabelText('Account number'), '1023456789');
    expect(await screen.findByText(/SKY AIR LTD/)).toBeInTheDocument();
    const lookup = f.mock.calls.find(([u]) => String(u).startsWith('/api/pay/merchant/banks/resolve'));
    expect(String(lookup![0])).toContain('bankCode=057&accountNumber=1023456789');
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      const call = f.mock.calls.find(([u]) => u === '/api/pay/merchant/settlement');
      expect(JSON.parse(call![1].body)).toEqual({ settlementMode: 'fiat', bankCode: '057', accountNumber: '1023456789', password: 'correct-horse', expectedVersion: merchant.version });
    });
    // Nothing has changed yet: the code step is open and the old account still shows.
    const dialog = await screen.findByRole('dialog', { name: 'Confirm your payout bank account' });
    expect(within(dialog).getByText(/a\*\*\*@skyair\.ng/)).toBeInTheDocument();
    expect(screen.getByText(/Access Bank · \*\*\*\*\*\*3210/)).toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText('Code from your email'), '482913');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const confirm = f.mock.calls.find(([u]) => u === '/api/pay/merchant/actions/b1/confirm');
    expect(JSON.parse(confirm![1].body)).toEqual({ code: '482913' });
    // The confirmed merchant replaces the old one: the new account shows, the form clears.
    expect(await screen.findByText(/Zenith Bank · \*\*\*\*\*\*6789/)).toBeInTheDocument();
    expect(screen.getByLabelText('Password')).toHaveValue('');
  });

  it('saving where the merchant is already paid changes nothing and asks for no code', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/banks/resolve': ok({ accountNumber: '0123453210', accountName: 'SKY AIR LIMITED' }),
      '/api/pay/merchant/settlement': ok({ merchant }),
    });
    mountAt('/business/live/payouts');
    await userEvent.selectOptions(await screen.findByLabelText('Bank'), '044');
    await userEvent.type(screen.getByLabelText('Account number'), '0123453210');
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect((await screen.findAllByText('Payment settings saved'))[0]).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('two submits in the same tick send one save', async () => {
    let resolve!: (r: Response) => void;
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/banks/resolve': ok({ accountNumber: '1023456789', accountName: 'SKY AIR LTD' }),
      '/api/pay/merchant/settlement': () => new Promise<Response>((r) => { resolve = r; }),
    });
    mountAt('/business/live/payouts');
    await userEvent.selectOptions(await screen.findByLabelText('Bank'), '057');
    await userEvent.type(screen.getByLabelText('Account number'), '1023456789');
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    const form = screen.getByLabelText('Password').closest('form')!;
    act(() => {
      form.requestSubmit();
      form.requestSubmit();
    });
    resolve(settlementPending('b1'));
    expect(await screen.findByRole('dialog', { name: 'Confirm your payout bank account' })).toBeInTheDocument();
    expect(f.mock.calls.filter(([u]) => u === '/api/pay/merchant/settlement')).toHaveLength(1);
  });

  it("a name that is not theirs is explained with the bank's name", async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/banks/resolve': ok({ accountNumber: '1023456789', accountName: 'EMEKA NWOSU' }),
      '/api/pay/merchant/settlement': fail(422, "That account isn't in your business name or your verified name. Use an account in your own name.", 'name_mismatch'),
    });
    mountAt('/business/live/payouts');
    await userEvent.selectOptions(await screen.findByLabelText('Bank'), '057');
    await userEvent.type(screen.getByLabelText('Account number'), '1023456789');
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/belongs to EMEKA NWOSU/);
  });

  it('an account the bank cannot check can still be saved', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/banks': banks,
      '/api/pay/merchant/banks/resolve': fail(502, "We couldn't reach the bank directory — try again"),
    });
    mountAt('/business/live/payouts');
    await userEvent.selectOptions(await screen.findByLabelText('Bank'), '057');
    await userEvent.type(screen.getByLabelText('Account number'), '1023456789');
    expect(await screen.findByText(/couldn't check this account — you can still save/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
  });

  it('a crypto wallet only takes listed stablecoins and waits for the emailed code', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/settlement-options': ok([{ chainId: 8453, chain: 'base', symbol: 'USDC', tokenAddress: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913' }]),
      '/api/pay/merchant/settlement': accepted({ confirmation: { id: 's1', kind: 'settlement', expiresAt: soon, sentTo: 'a***@skyair.ng' } }),
      '/api/pay/merchant/actions/s1/confirm': ok({ merchant: { ...merchant, settlementMode: 'crypto', tokenSymbol: 'USDC', chainId: 8453, walletAddress: '0x52908400098527886E0F7030069857D2E4169EE7' } }),
    });
    mountAt('/business/live/payouts');
    await userEvent.click(await screen.findByRole('radio', { name: /crypto wallet/i }));
    await userEvent.selectOptions(await screen.findByLabelText('Token'), 'USDC:8453');
    await userEvent.type(screen.getByLabelText('Wallet address'), '0x52908400098527886E0F7030069857D2E4169EE7');
    await userEvent.type(screen.getByLabelText('Password'), 'correct-horse');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await userEvent.type(await screen.findByLabelText('Code from your email'), '482913');
    await userEvent.click(screen.getByRole('button', { name: /confirm/i }));
    await waitFor(() => expect(f.mock.calls.some(([u]) => u === '/api/pay/merchant/actions/s1/confirm')).toBe(true));
    const save = f.mock.calls.find(([u]) => u === '/api/pay/merchant/settlement');
    expect(JSON.parse(save![1].body)).toMatchObject({ settlementMode: 'crypto', chainId: 8453, tokenAddress: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', walletAddress: '0x52908400098527886E0F7030069857D2E4169EE7' });
    expect(await screen.findByText(/USDC on Base to 0x5290…9EE7/)).toBeInTheDocument();
  });

  it('a wallet address that is not one is caught before any request, with the cursor put back on it', async () => {
    const f = stubFetch({ '/api/pay/merchant': ok(merchant), '/api/pay/merchant/settlement-options': usdcOnBase });
    mountAt('/business/live/payouts');
    await chooseWallet('0x1234');
    expect(await screen.findByText(/0x followed by 40 letters and numbers/i)).toBeInTheDocument();
    expect(screen.getByLabelText('Wallet address')).toHaveFocus();
    expect(screen.getByLabelText('Wallet address')).toHaveAttribute('aria-invalid', 'true');
    expect(f.mock.calls.some(([u]) => u === '/api/pay/merchant/settlement')).toBe(false);
  });

  it('the emailed code for a wallet completes the change: the dialog closes and the form starts clean', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/settlement-options': usdcOnBase,
      '/api/pay/merchant/settlement': settlementPending('s1'),
      '/api/pay/merchant/actions/s1/confirm': ok({ merchant: cryptoMerchant }),
    });
    mountAt('/business/live/payouts');
    await chooseWallet();
    const dialog = await screen.findByRole('dialog', { name: 'Confirm your payout wallet' });
    expect(within(dialog).getByText(/a\*\*\*@skyair\.ng/)).toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText('Code from your email'), '482913');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect((await screen.findAllByText('Payment settings saved'))[0]).toBeInTheDocument();
    expect(screen.getByText(/USDC on Base to 0x5290…9EE7/)).toBeInTheDocument();
    expect(screen.getByLabelText('Wallet address')).toHaveValue('');
  });

  it('an expired wallet change asks for the password again and sends the same change', async () => {
    let confirms = 0;
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/pay/merchant/settlement-options': usdcOnBase,
      '/api/pay/merchant/settlement': (() => {
        let n = 0;
        return () => settlementPending(++n === 1 ? 's1' : 's2');
      })(),
      '/api/pay/merchant/actions/s1/confirm': () => {
        confirms++;
        return fail(410, 'This change has expired or had too many wrong codes — start again', 'no_pending_change');
      },
      '/api/pay/merchant/actions/s2/confirm': ok({ merchant: cryptoMerchant }),
    });
    mountAt('/business/live/payouts');
    await chooseWallet();
    const dialog = await screen.findByRole('dialog', { name: 'Confirm your payout wallet' });
    await userEvent.type(within(dialog).getByLabelText('Code from your email'), '482913');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('This change expired or had too many wrong codes. Start again.');
    expect(confirms).toBe(1);

    await userEvent.type(within(dialog).getByLabelText('Password'), 'correct-horse');
    await userEvent.click(within(dialog).getByRole('button', { name: /send code/i }));
    await userEvent.type(await within(dialog).findByLabelText('Code from your email'), '482913');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    const saves = f.mock.calls.filter(([u]) => u === '/api/pay/merchant/settlement').map(([, init]) => JSON.parse(init.body));
    expect(saves).toHaveLength(2);
    expect(saves[1]).toEqual(saves[0]);
    expect(saves[1]).toMatchObject({ settlementMode: 'crypto', walletAddress: wallet, password: 'correct-horse' });
  });

  it('a suspended merchant cannot change where they are paid', async () => {
    stubFetch({ '/api/pay/merchant': ok({ ...merchant, status: 'suspended' }), '/api/pay/merchant/banks': banks });
    mountAt('/business/live/payouts');
    expect(await screen.findByText(/suspended — contact support to change where you're paid/i)).toBeInTheDocument();
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument();
  });
});
