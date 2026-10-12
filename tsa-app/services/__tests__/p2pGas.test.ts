const mockTopUp = jest.fn();
jest.mock('../p2p', () => ({
  gasApi: { topUp: (...a: any[]) => mockTopUp(...a) },
  p2pChainKey: (id: number) => `chain-${id}`,
  SONIC_GAS_SPONSOR_CHAIN_IDS: new Set([146]),
}));
const mockEnsureGas = jest.fn();
jest.mock('../wallet', () => ({ ensureGas: (...a: any[]) => mockEnsureGas(...a) }));
const mockWaitMined = jest.fn();
jest.mock('../txWait', () => ({ waitMined: (...a: any[]) => mockWaitMined(...a) }));
const mockRnAlert = jest.fn();
jest.mock('react-native', () => ({ Alert: { alert: (...a: any[]) => mockRnAlert(...a) } }));
jest.mock('expo-router', () => ({ router: { push: jest.fn() } }));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { ensureP2PGas, type EnsureP2PGasParams } from '../p2pGas';

const W = '0xSIGNER';
beforeEach(() => {
  jest.clearAllMocks();
  mockEnsureGas.mockResolvedValue(true);
});

it('never asks the sponsor off Sonic mainnet (incl. testnet 14601)', async () => {
  await expect(ensureP2PGas({ chainId: 56, walletAddress: W, action: 'release', tradeId: 't' })).resolves.toBe(true);
  await ensureP2PGas({ chainId: 14601, walletAddress: W, action: 'release', tradeId: 't' });
  expect(mockTopUp).not.toHaveBeenCalled();
  expect(mockEnsureGas).toHaveBeenCalledWith('chain-56', W, expect.any(Function));
});

it('sends the action with its tradeId / adId', async () => {
  mockTopUp.mockResolvedValue({ success: true, data: { skipped: true } });
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'mark_paid', tradeId: 't1' });
  expect(mockTopUp).toHaveBeenCalledWith({ action: 'mark_paid', tradeId: 't1' });
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a1' });
  expect(mockTopUp).toHaveBeenLastCalledWith({ action: 'open_trade', adId: 'a1' });
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'approve' });
  expect(mockTopUp).toHaveBeenLastCalledWith({ action: 'approve' });
  expect(mockWaitMined).not.toHaveBeenCalled();
});

it('waits for the sponsored tx to mine BEFORE the balance check', async () => {
  const order: string[] = [];
  mockTopUp.mockResolvedValue({ success: true, data: { skipped: false, txHash: '0xtop' } });
  mockWaitMined.mockImplementation(async () => { order.push('mined'); });
  mockEnsureGas.mockImplementation(async () => { order.push('balance'); return true; });
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'deposit' });
  expect(mockWaitMined).toHaveBeenCalledWith(146, '0xtop', expect.objectContaining({ timeoutMs: 60_000 }));
  expect(order).toEqual(['mined', 'balance']);
});

it('503 gas_unavailable with a txHash waits for that tx', async () => {
  mockTopUp.mockResolvedValue({ success: false, status: 503, code: 'gas_unavailable', data: { txHash: '0xslow' } });
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'approve' });
  expect(mockWaitMined).toHaveBeenCalledWith(146, '0xslow', expect.anything());
});

it('503 without a txHash does not wait', async () => {
  mockTopUp.mockResolvedValue({ success: false, status: 503, code: 'gas_unavailable' });
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'approve' });
  expect(mockWaitMined).not.toHaveBeenCalled();
});

it('a top-up that never lands still falls back to the balance check', async () => {
  mockTopUp.mockResolvedValue({ success: true, data: { skipped: false, txHash: '0xtop' } });
  mockWaitMined.mockRejectedValue(new Error('timed out'));
  await expect(ensureP2PGas({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't' })).resolves.toBe(true);
});

it('a refused top-up with enough balance stays silent and proceeds', async () => {
  const onAlert = jest.fn();
  mockTopUp.mockResolvedValue({ success: false, status: 409, code: 'gas_not_needed', message: 'x' });
  await expect(ensureP2PGas({ chainId: 146, walletAddress: W, action: 'dispute', tradeId: 't', onAlert })).resolves.toBe(true);
  expect(onAlert).not.toHaveBeenCalled();
});

it('a refused top-up + empty signer wallet surfaces the friendly refusal copy', async () => {
  const onAlert = jest.fn();
  mockTopUp.mockResolvedValue({ success: false, status: 429, message: "you've used today's gas top-ups" });
  mockEnsureGas.mockImplementation(async (_c: string, _w: string, alert: any) => {
    alert('Not enough gas', 'You need S on Sonic to cover the transaction fee.');
    return false;
  });
  await expect(ensureP2PGas({ chainId: 146, walletAddress: W, action: 'dispute', tradeId: 't', onAlert })).resolves.toBe(false);
  expect(onAlert).toHaveBeenCalledWith('Not enough gas', expect.stringMatching(/daily limit/i));
});

it('empty wallet with no sponsor refusal keeps the original ensureGas alert', async () => {
  const onAlert = jest.fn();
  mockTopUp.mockResolvedValue({ success: true, data: { skipped: true } });
  mockEnsureGas.mockImplementation(async (_c: string, _w: string, alert: any) => {
    alert('Not enough gas', 'You need S on Sonic');
    return false;
  });
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't', onAlert });
  expect(onAlert).toHaveBeenCalledWith('Not enough gas', 'You need S on Sonic');
});

const emptySigner = () =>
  mockEnsureGas.mockImplementation(async (_c: string, _w: string, alert: any) => {
    alert('Not enough gas', 'You need S on Sonic to cover the transaction fee.');
    return false;
  });

it.each(['gas_not_needed', 'gas_topup_unused', 'sponsorship_in_progress'])(
  'signer empty + %s keeps ensureGas\'s own message',
  async (code) => {
    const onAlert = jest.fn();
    mockTopUp.mockResolvedValue({ success: false, status: 409, code });
    emptySigner();
    await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't', onAlert });
    expect(onAlert).toHaveBeenCalledWith('Not enough gas', 'You need S on Sonic to cover the transaction fee.');
  },
);

it('signer empty + gas_unavailable shows the refusal copy', async () => {
  const onAlert = jest.fn();
  mockTopUp.mockResolvedValue({ success: false, status: 503, code: 'gas_unavailable' });
  emptySigner();
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't', onAlert });
  expect(onAlert).toHaveBeenCalledWith('Not enough gas', expect.stringMatching(/unavailable/i));
});

it.each([
  ['kyc_legal_name_missing', 'Contact support', /legal name.*setting up P2P/, false],
  ['kyc_verification_required', 'Verify your identity', /trade on P2P/, true],
  [undefined, 'Verify your identity', /trade on P2P/, true], // older code-less 403
])('signer empty + 403 %s: the KYC prompt, not a gas alert', async (code, title, copy, verify) => {
  const onAlert = jest.fn();
  mockTopUp.mockResolvedValue({ success: false, status: 403, code, message: 'p2p: identity verification required' });
  emptySigner();
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', onAlert });
  expect(onAlert).not.toHaveBeenCalled();
  const [t, m, buttons] = mockRnAlert.mock.calls[0];
  expect(t).toBe(title);
  expect(m).toMatch(copy);
  expect(m).not.toMatch(/p2p:/);
  expect((buttons ?? []).some((b: { text: string }) => b.text === 'Verify now')).toBe(verify);
});

it('a thrown network error shows friendly text, never the raw message', async () => {
  const onAlert = jest.fn();
  mockTopUp.mockRejectedValue(new Error('TypeError: Network request failed at fetch'));
  emptySigner();
  await ensureP2PGas({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't', onAlert });
  const msg = onAlert.mock.calls[0][1];
  expect(msg).not.toMatch(/TypeError|fetch/);
});

it('the action\'s entity id is required by the type (tsc checks this file)', () => {
  // @ts-expect-error release needs its tradeId
  const noTrade: EnsureP2PGasParams = { chainId: 146, walletAddress: W, action: 'release' };
  // @ts-expect-error open_trade takes an adId, not a tradeId
  const wrongId: EnsureP2PGasParams = { chainId: 146, walletAddress: W, action: 'open_trade', tradeId: 't' };
  const ok: EnsureP2PGasParams = { chainId: 146, walletAddress: W, action: 'approve' };
  expect([noTrade.action, wrongId.action, ok.action]).toEqual(['release', 'open_trade', 'approve']);
});
