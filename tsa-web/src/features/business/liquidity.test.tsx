import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { fail, merchant, mountAt, ok, stubFetch } from '@/test/harness';
import { ApiError } from '@/lib/api';
import { activitySign, canClaim, claimErrorMessage, oldWalletHasFunds, oldWalletOnlyPending, tokenAmount, type LiquidityMe } from './liquidityApi';

const WALLET = '0x52908400098527886E0F7030069857D2E4169EE7';
const base: LiquidityMe = {
  status: 'none', poolOpen: true, balanceReadable: false, paused: false, walletChanged: false,
  chain: { id: 56, key: 'bsc', name: 'BNB Smart Chain' },
  token: { symbol: 'USDT', address: '0x55d398326f99059fF775485246999027B3197955', decimals: 18 },
  limits: { min: '5', max: '5000' }, minClaim: '0.5',
  pool: { totalLiquidity: '12000', apr30d: '9.4' },
};
const lp: LiquidityMe = {
  ...base, status: 'active', balanceReadable: true, wallet: WALLET,
  principal: '1000', claimable: '2.345678', earned: '7.5', sharePercent: '8.33',
};
const activity = ok({ items: [{ kind: 'earned', amount: '0.2', txHash: '0x1', at: '2026-10-14T09:00:00Z' }] });

describe('liquidity earnings', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('shows the position, the pool and the history, and claims gas-free with an idempotency key', async () => {
    const f = stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/liquidity/me': ok(lp),
      '/api/liquidity/me/activity': activity,
      '/api/liquidity/claim-for': ok({ txHash: '0xabc' }),
    });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText('Spread earnings')).toBeInTheDocument();
    expect(screen.getAllByText('2.3456 USDT').length).toBeGreaterThan(0);
    expect(screen.getByText('12,000 USDT')).toBeInTheDocument();
    expect(screen.queryByText(/collateral|backed/i)).not.toBeInTheDocument();
    expect(await screen.findByText('+0.2 USDT')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Claim 2.3456 USDT' }));
    await waitFor(() => expect(f.mock.calls.some(([u]) => String(u).endsWith('/api/liquidity/claim-for'))).toBe(true));
    const [, init] = f.mock.calls.find(([u]) => String(u).endsWith('/api/liquidity/claim-for'))!;
    expect(init.method).toBe('POST');
    expect(init.headers['Idempotency-Key']).toBeTruthy();
  });

  it('says what can be withdrawn now while a deposit is locked', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/liquidity/me': ok({ ...lp, withdrawable: '600', lockedUntil: '2026-10-15T09:00:00Z' }),
      '/api/liquidity/me/activity': activity,
    });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText(/600 USDT can be withdrawn now/)).toBeInTheDocument();
  });

  it('shows a pending withdrawal without a reason', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/liquidity/me': ok({ ...lp, pendingWithdrawal: '40' }),
      '/api/liquidity/me/activity': ok({ items: [
        { kind: 'withdrawal_pending', amount: '40', txHash: '0x2', at: '2026-10-14T10:00:00Z' },
        { kind: 'withdrawal_paid', amount: '10', txHash: '0x3', at: '2026-10-14T11:00:00Z' },
      ] }),
    });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText('Pending withdrawal: 40 USDT')).toBeInTheDocument();
    expect(screen.queryByText(/refill|short/i)).not.toBeInTheDocument();
    expect(await screen.findByText('Withdrawal pending')).toBeInTheDocument();
    expect(screen.getByText('−40 USDT')).toBeInTheDocument();
    expect(screen.getByText('Withdrawal paid')).toBeInTheDocument();
    expect(screen.getByText('10 USDT')).toBeInTheDocument();
  });

  it('hides the pending line when nothing is pending', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/liquidity/me': ok(lp), '/api/liquidity/me/activity': activity });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText('Spread earnings')).toBeInTheDocument();
    expect(screen.queryByText(/Pending withdrawal/)).not.toBeInTheDocument();
  });

  it('explains a claim refused for liquidity neutrally', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/liquidity/me': ok(lp),
      '/api/liquidity/me/activity': activity,
      '/api/liquidity/claim-for': fail(409, 'liquidity: insufficient liquidity', 'claim_unavailable'),
    });
    mountAt('/business/live/liquidity');
    await userEvent.click(await screen.findByRole('button', { name: 'Claim 2.3456 USDT' }));
    expect(await screen.findByText('Claims are not available right now. Try again later.')).toBeInTheDocument();
  });

  it('holds the claim below the minimum', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/liquidity/me': ok({ ...lp, claimable: '0.2' }),
      '/api/liquidity/me/activity': activity,
    });
    mountAt('/business/live/liquidity');
    expect(await screen.findByRole('button', { name: 'Claim 0.2 USDT' })).toBeDisabled();
    expect(screen.getByText('Claims start from 0.5 USDT here; smaller amounts can be claimed from the app.')).toBeInTheDocument();
  });

  it('tells an LP on a new wallet to empty the old one before activating', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/liquidity/me': ok({ ...lp, walletChanged: true }),
      '/api/liquidity/me/activity': activity,
    });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText(/Withdraw and claim from 0x5290.* first \(switch to it in the TSA Connect app\)/)).toBeInTheDocument();
  });

  it('asks nothing of the owner when only a pending withdrawal is left on the old wallet', async () => {
    stubFetch({
      '/api/pay/merchant': ok(merchant),
      '/api/liquidity/me': ok({ ...lp, walletChanged: true, principal: '0', claimable: '0', pendingWithdrawal: '20' }),
      '/api/liquidity/me/activity': activity,
    });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText(/A withdrawal from your old wallet is still pending\. You can switch wallets once it completes\./)).toBeInTheDocument();
    expect(screen.queryByText(/first \(switch to it/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Activate your new wallet/)).not.toBeInTheDocument();
  });

  it('sends a non-LP to the app to start', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/liquidity/me': ok(base) });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText('Provide liquidity and earn')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Claim/ })).not.toBeInTheDocument();
  });

  it('says it is not open yet when the pool is not wired', async () => {
    stubFetch({ '/api/pay/merchant': ok(merchant), '/api/liquidity/me': fail(404, 'Not found') });
    mountAt('/business/live/liquidity');
    expect(await screen.findByText('Not available yet')).toBeInTheDocument();
  });
});

describe('liquidity helpers', () => {
  it('formats exactly, never rounding up', () => {
    expect(tokenAmount('1234567.999999')).toBe('1,234,567.9999');
    expect(tokenAmount('5000')).toBe('5,000');
    expect(tokenAmount(undefined)).toBe('0');
  });
  it('claims from the minimum, on the LP wallet only', () => {
    expect(canClaim({ ...lp, claimable: '0.5' })).toBe(true);
    expect(canClaim({ ...lp, claimable: '0.499999999999999999' })).toBe(false);
    expect(canClaim({ ...lp, walletChanged: true })).toBe(false);
  });
  it('sees a position left on the old wallet', () => {
    expect(oldWalletHasFunds(lp)).toBe(false);
    expect(oldWalletHasFunds({ ...lp, walletChanged: true })).toBe(true);
    expect(oldWalletHasFunds({ ...lp, walletChanged: true, principal: '0', claimable: '0' })).toBe(false);
    expect(oldWalletHasFunds({ ...lp, walletChanged: true, principal: '0', claimable: '0', pendingWithdrawal: '5' })).toBe(true);
  });
  it('tells a pending-only old wallet apart from one with a position', () => {
    expect(oldWalletOnlyPending({ ...lp, walletChanged: true, principal: '0', claimable: '0', pendingWithdrawal: '5' })).toBe(true);
    expect(oldWalletOnlyPending({ ...lp, walletChanged: true, pendingWithdrawal: '5' })).toBe(false);
    expect(oldWalletOnlyPending({ ...lp, principal: '0', claimable: '0', pendingWithdrawal: '5' })).toBe(false);
  });
});

describe('liquidity copy', () => {
  it('maps claim refusals', () => {
    expect(claimErrorMessage(new ApiError('x', 409, 'claim_unavailable'))).toBe('Claims are not available right now. Try again later.');
    expect(claimErrorMessage(new ApiError('x', 429))).toBe('You can claim gas-free once every 8 hours. Claim from the app to do it now.');
    expect(claimErrorMessage(new Error('boom'))).toBe('boom');
  });
  it('signs each activity kind', () => {
    expect(['deposit', 'earned', 'withdrawal', 'claim', 'withdrawal_pending', 'withdrawal_paid'].map((k) => activitySign(k as never)))
      .toEqual(['+', '+', '−', '−', '−', '']);
  });
});
