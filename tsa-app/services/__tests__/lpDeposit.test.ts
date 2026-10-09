// services/__tests__/lpDeposit.test.ts — the LP "Add liquidity" submit.
// Money rule: the amount deposited is exactly what the LP typed, measured
// against a FRESH balance read (review I4), never more, never rounded.
const mockBalance = jest.fn();
jest.mock('../p2p', () => ({
  ...jest.requireActual('../p2p'),
  poolApi: { balance: (...a: any[]) => mockBalance(...a) },
}));
const mockFund = jest.fn();
jest.mock('../escrowFunding', () => ({ ensureEscrowFunding: (...a: any[]) => mockFund(...a) }));
const mockCreateAndSign = jest.fn();
jest.mock('../adPublish', () => ({ createAndSignAd: (...a: any[]) => mockCreateAndSign(...a) }));
jest.mock('../walletApi', () => ({ getAuthHeaders: jest.fn().mockResolvedValue({}) }));
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn(), setItem: jest.fn() }));

// eslint-disable-next-line import/first
import { runLpDeposit } from '../lpDeposit';
// eslint-disable-next-line import/first
import type { LiquidityMe } from '../liquidityApi';

const E18 = 10n ** 18n;
const me: LiquidityMe = {
  status: 'active', poolOpen: true, balanceReadable: true, owedNgn: '0.00', paused: false,
  chain: { id: 97, key: 'bsc', name: 'BNB Smart Chain Testnet' },
  token: { symbol: 'USDT', address: '0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7', decimals: 18 },
  wallet: '0xabc', available: '40', locked: '0', stake: '40', aboveMax: '0', headroom: '4960',
  limits: { min: '5', max: '5000' }, voucher: { adId: 'a', status: 'active', remaining: '500000', renewDue: false },
};
const base = { me, wallet: '0xabc', statusError: () => 'err' };
const freshBalance = (available: bigint, locked = 0n) =>
  mockBalance.mockResolvedValue({ success: true, data: { available: available.toString(), locked: locked.toString() } });

beforeEach(() => jest.resetAllMocks());

// Review I4: the cached view says 40 available, the chain says 10 (the LP just
// withdrew 30). Typing 10 must deposit exactly 10.
it('funds exactly the typed amount over a FRESH balance', async () => {
  mockBalance.mockResolvedValue({ success: true, data: { available: (10n * E18).toString(), locked: '0' } });
  mockFund.mockResolvedValue({ status: 'funded', deposited: (10n * E18).toString() });
  const res = await runLpDeposit({ ...base, input: '10' });
  expect(mockFund).toHaveBeenCalledWith(expect.objectContaining({ amountBaseUnits: (20n * E18).toString(), chainId: 97 }));
  expect(res).toEqual({ status: 'done', depositedUnits: (10n * E18).toString(), stockAd: false });
});

it('pins the funding to the exact typed amount, so a balance move cannot widen the deposit', async () => {
  freshBalance(10n * E18);
  mockFund.mockResolvedValue({ status: 'funded', deposited: '10250000000000000001' });
  await runLpDeposit({ ...base, input: '10.250000000000000001' });
  expect(mockFund).toHaveBeenCalledWith(expect.objectContaining({
    amountBaseUnits: '20250000000000000001', exactDepositBaseUnits: '10250000000000000001', walletAddress: '0xabc', token: 'USDT',
  }));
});

it('never claims a deposit that did not happen', async () => {
  mockBalance.mockResolvedValue({ success: true, data: { available: (10n * E18).toString(), locked: '0' } });
  mockFund.mockResolvedValue({ status: 'already-funded' });
  const res = await runLpDeposit({ ...base, input: '10' });
  expect(res.status).toBe('invalid');
  expect(res.status === 'invalid' && res.error).toMatch(/Nothing was deposited/);
});

it('reports a balance change caught by funding as nothing deposited', async () => {
  freshBalance(10n * E18);
  mockFund.mockResolvedValue({ status: 'aborted', reason: 'balance-changed' });
  const res = await runLpDeposit({ ...base, input: '10' });
  expect(res.status === 'invalid' && res.error).toMatch(/Nothing was deposited/);
  expect(mockCreateAndSign).not.toHaveBeenCalled();
});

it('refuses when the fresh balance cannot be read', async () => {
  mockBalance.mockResolvedValue({ success: false, message: 'rpc down' });
  expect((await runLpDeposit({ ...base, input: '10' })).status).toBe('invalid');
  expect(mockFund).not.toHaveBeenCalled();
});

it('enforces the LP max on the fresh balance, not the cached headroom', async () => {
  // Cached headroom says 4,960; the chain says 4,995 is already held.
  freshBalance(4990n * E18, 5n * E18);
  const res = await runLpDeposit({ ...base, input: '10' });
  expect(res.status === 'invalid' && res.error).toMatch(/at most 5 USDT/);
  expect(mockFund).not.toHaveBeenCalled();
});

it('enforces the first-deposit minimum on the fresh balance', async () => {
  freshBalance(0n);
  const res = await runLpDeposit({ ...base, input: '4' });
  expect(res.status === 'invalid' && res.error).toMatch(/at least 5 USDT/);
  expect(mockFund).not.toHaveBeenCalled();
});

it('refuses more decimal places than the token has, and negative amounts, before any read', async () => {
  expect((await runLpDeposit({ ...base, input: '1.0000000000000000001' })).status).toBe('invalid');
  expect((await runLpDeposit({ ...base, input: '-5' })).status).toBe('invalid');
  expect(mockBalance).not.toHaveBeenCalled();
  expect(mockFund).not.toHaveBeenCalled();
});

it('stops on a gas abort and on a pending confirm, with no stock ad', async () => {
  freshBalance(0n);
  mockFund.mockResolvedValueOnce({ status: 'aborted', reason: 'no gas' });
  expect(await runLpDeposit({ ...base, me: { ...me, voucher: undefined }, input: '10' })).toEqual({ status: 'aborted' });
  mockFund.mockResolvedValueOnce({ status: 'pending-confirm', txHash: '0xt', deposited: (10n * E18).toString() });
  expect(await runLpDeposit({ ...base, me: { ...me, voucher: undefined }, input: '10' })).toEqual({ status: 'recover' });
  expect(mockCreateAndSign).not.toHaveBeenCalled();
});

it('re-signs the stock ad when it is due, with no deposit', async () => {
  mockCreateAndSign.mockResolvedValue({ id: 'ad2', status: 'active' });
  const res = await runLpDeposit({ ...base, me: { ...me, voucher: { ...me.voucher!, renewDue: true } }, input: '' });
  expect(mockFund).not.toHaveBeenCalled();
  expect(mockCreateAndSign).toHaveBeenCalledWith(expect.objectContaining({ sellerAddress: '0xabc' }));
  expect(res).toEqual({ status: 'done', depositedUnits: '0', stockAd: true });
});

it('creates the stock ad after the first deposit and says the deposit is safe if that fails', async () => {
  freshBalance(0n);
  mockFund.mockResolvedValue({ status: 'funded', deposited: (10n * E18).toString() });
  mockCreateAndSign.mockRejectedValue(new Error('pool closed'));
  await expect(runLpDeposit({ ...base, me: { ...me, voucher: undefined }, input: '10' }))
    .rejects.toThrow(/10 USDT is safe in your escrow balance.*pool closed/s);
  expect(mockCreateAndSign).toHaveBeenCalledWith(expect.objectContaining({
    payload: expect.objectContaining({ token: 'USDT', chainId: 97, sellerAddress: '0xabc', fiatCurrency: 'NGN' }),
  }));
});

// Review I4: "Leave the amount empty and tap Renew" works on the very next tap,
// even while /liquidity/me still shows the pre-deposit (empty) balance.
it('retries only the stock ad on Renew after a failed first publish, with no second deposit', async () => {
  mockCreateAndSign.mockResolvedValue({ id: 'ad1', status: 'active' });
  const stale = { ...me, available: '0', locked: '0', stake: '0', headroom: '5000', voucher: undefined };
  const res = await runLpDeposit({ ...base, me: stale, input: '' });
  expect(mockBalance).not.toHaveBeenCalled();
  expect(mockFund).not.toHaveBeenCalled();
  expect(res).toEqual({ status: 'done', depositedUnits: '0', stockAd: true });
});
