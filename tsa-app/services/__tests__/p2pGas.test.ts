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
jest.mock('react-native', () => ({ Alert: { alert: jest.fn() } }));
jest.mock('expo-router', () => ({ router: { push: jest.fn() } }));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { ensureP2PGas } from '../p2pGas';

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
  mockTopUp.mockResolvedValue({ success: false, status: 429, message: 'daily cap' });
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
