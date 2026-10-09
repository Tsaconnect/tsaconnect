// services/__tests__/p2pTx.test.ts
//
// runP2PTx is the one gas → prepare → sign → broadcast → mine → confirm path
// for every P2P on-chain action. Collaborators are mocked; the ordering, the
// outcome union and the lost-confirm stash run for real.
jest.mock('expo-router', () => ({ router: { push: jest.fn() } }));
const mockEnsureP2PGas = jest.fn();
jest.mock('../p2pGas', () => ({ ensureP2PGas: (...a: any[]) => mockEnsureP2PGas(...a) }));
const mockSign = jest.fn();
const mockBroadcast = jest.fn();
jest.mock('../wallet', () => ({
  signTransaction: (...a: any[]) => mockSign(...a),
  broadcastTransaction: (...a: any[]) => mockBroadcast(...a),
}));
jest.mock('../p2p', () => ({
  p2pChainKey: (id: number) => `chain-${id}`,
  unsignedTxToTxLike: (tx: any) => ({ __txLike: true, ...tx }),
}));
const mockWaitMined = jest.fn();
jest.mock('../txWait', () => ({
  ...jest.requireActual('../txWait'),
  waitMined: (...a: any[]) => mockWaitMined(...a),
}));
const mockAddPending = jest.fn();
const mockRemovePending = jest.fn();
jest.mock('../p2pPending', () => ({
  addPending: (...a: any[]) => mockAddPending(...a),
  removePending: (...a: any[]) => mockRemovePending(...a),
}));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { runP2PTx, txOutcomeAlert } from '../p2pTx';
// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { TxRevertedError } from '../txWait';

const W = '0xWALLET';
const UTX = { to: '0xescrow', value: '0', data: 'ab', nonce: 3, chainId: '146' };
const prepared = { unsignedTx: UTX, trade: { id: 't1', tokenAmount: '5000000' } };
const ok = async () => ({ success: true, data: prepared });

function arrange() {
  mockEnsureP2PGas.mockResolvedValue(true);
  mockSign.mockResolvedValue('0xsigned');
  mockBroadcast.mockResolvedValue({ hash: '0xhash' });
  mockWaitMined.mockResolvedValue(undefined);
  mockAddPending.mockResolvedValue(undefined);
  mockRemovePending.mockResolvedValue(undefined);
}
beforeEach(() => {
  jest.clearAllMocks();
  arrange();
});

it('gas → prepare → sign as the wallet → broadcast on the trade chain → mine → confirm', async () => {
  const order: string[] = [];
  mockEnsureP2PGas.mockImplementation(async () => { order.push('gas'); return true; });
  mockWaitMined.mockImplementation(async () => { order.push('mined'); });
  const prepare = jest.fn(async () => { order.push('prepare'); return { success: true, data: prepared }; });
  const confirm = jest.fn(async () => { order.push('confirm'); return { success: true, data: { id: 't1', status: 'paid' } }; });

  const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'mark_paid', tradeId: 't1', prepare, confirm });

  expect(order).toEqual(['gas', 'prepare', 'mined', 'confirm']);
  expect(mockSign).toHaveBeenCalledWith({ __txLike: true, ...UTX }, W);
  expect(mockBroadcast).toHaveBeenCalledWith('chain-146', '0xsigned');
  expect(mockWaitMined).toHaveBeenCalledWith(146, '0xhash', expect.any(Object));
  expect(confirm).toHaveBeenCalledWith('0xhash', prepared);
  expect(r).toEqual({ status: 'confirmed', txHash: '0xhash', prepared, data: { id: 't1', status: 'paid' } });
});

it('asks the gas sponsor for the action with its trade / ad id', async () => {
  await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't9', prepare: ok, confirm: async () => ({ success: true }) });
  expect(mockEnsureP2PGas).toHaveBeenLastCalledWith(
    expect.objectContaining({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't9' }),
  );
  await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a7', prepare: ok, confirm: async () => ({ success: true }) });
  expect(mockEnsureP2PGas).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'open_trade', adId: 'a7' }));
});

it('aborts before prepare when gas cannot be ensured', async () => {
  mockEnsureP2PGas.mockResolvedValue(false);
  const prepare = jest.fn();
  expect(await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare, confirm: jest.fn() }))
    .toEqual({ status: 'aborted' });
  expect(prepare).not.toHaveBeenCalled();
});

it('skipGas skips the gas step', async () => {
  await runP2PTx({ chainId: 146, walletAddress: W, action: 'cancel_ad', adId: 'a', skipGas: true,
    prepare: ok, confirm: async () => ({ success: true }) });
  expect(mockEnsureP2PGas).not.toHaveBeenCalled();
});

it('returns the raw refusal so the caller can route KYC / 503', async () => {
  const response = { success: false, status: 403, code: 'kyc_verification_required', message: 'x' };
  const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: async () => response, confirm: jest.fn() });
  expect(r).toEqual({ status: 'refused', response });
  expect(mockSign).not.toHaveBeenCalled();
});

it('a thrown prepare is a refusal too (nothing reached the chain)', async () => {
  const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1',
    prepare: async () => { throw new Error('Network request failed'); }, confirm: jest.fn() });
  expect(r).toEqual({ status: 'refused', response: { success: false, message: 'Network request failed' } });
});

it('no-tx: a null unsignedTx (buy-ad cancel) is done, with nothing signed', async () => {
  const confirm = jest.fn();
  const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'cancel_ad', adId: 'a',
    prepare: async () => ({ success: true, data: { unsignedTx: null, ad: { id: 'a' } } }), confirm });
  expect(r).toEqual({ status: 'no-tx', prepared: { unsignedTx: null, ad: { id: 'a' } } });
  expect(mockSign).not.toHaveBeenCalled();
  expect(confirm).not.toHaveBeenCalled();
});

it('maps a broadcast failure to friendly copy and never confirms', async () => {
  mockBroadcast.mockRejectedValue(new Error('insufficient funds for gas * price + value'));
  const confirm = jest.fn();
  await expect(runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok, confirm }))
    .rejects.toThrow('Not enough native token to pay gas on this chain.');
  expect(confirm).not.toHaveBeenCalled();
  expect(mockAddPending).not.toHaveBeenCalled();
});

it('confirm-failed keeps the hash (the tx is on chain)', async () => {
  const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok,
    confirm: async () => ({ success: false, status: 500, message: 'p2p: invalid state transition' }) });
  expect(r).toEqual({
    status: 'confirm-failed', txHash: '0xhash', prepared, mined: true,
    message: 'This trade has already moved on. Pull down to refresh.',
  });
});

it('a thrown confirm is confirm-failed too', async () => {
  const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok,
    confirm: async () => { throw new Error('Network request failed'); } });
  expect(r.status).toBe('confirm-failed');
});

describe('mining', () => {
  it('a reverted tx is a clear failure and is never sent to confirm', async () => {
    mockWaitMined.mockRejectedValue(new TxRevertedError('0xhash'));
    const confirm = jest.fn();
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok, confirm });
    expect(r).toEqual({ status: 'reverted', txHash: '0xhash', prepared });
    expect(confirm).not.toHaveBeenCalled();
  });

  it('a wait timeout still asks the server (its RPC may see it); success is confirmed', async () => {
    mockWaitMined.mockRejectedValue(new Error('Transaction confirmation timed out'));
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok,
      confirm: async () => ({ success: true, data: { id: 't1' } }) });
    expect(r.status).toBe('confirmed');
  });

  it('a wait timeout then a failed confirm is confirm-failed with mined:false (may still go through)', async () => {
    mockWaitMined.mockRejectedValue(new Error('Transaction confirmation timed out'));
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok,
      confirm: async () => ({ success: false, status: 500, message: 'get receipt: context deadline exceeded' }) });
    expect(r).toMatchObject({ status: 'confirm-failed', txHash: '0xhash', mined: false });
  });

  it("the server's own revert verdict is a revert, not \"still confirming\"", async () => {
    mockWaitMined.mockRejectedValue(new Error('Transaction confirmation timed out'));
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok,
      confirm: async () => ({ success: false, status: 500, message: 'verify on-chain release: transaction reverted on chain' }) });
    expect(r).toEqual({ status: 'reverted', txHash: '0xhash', prepared });
  });
});

describe('lost-confirm stash', () => {
  const stash = (p: typeof prepared) => ({ mode: 'open' as const, tradeId: p.trade.id, token: 'USDT', amount: p.trade.tokenAmount });

  it('stashes the broadcast hash before the confirm, and drops it once recorded', async () => {
    const order: string[] = [];
    mockAddPending.mockImplementation(async () => { order.push('stash'); });
    mockRemovePending.mockImplementation(async () => { order.push('unstash'); });
    const confirm = jest.fn(async () => { order.push('confirm'); return { success: true }; });
    jest.spyOn(Date, 'now').mockReturnValue(1_700_000_000_000);

    await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: ok, confirm, stash });

    expect(order).toEqual(['stash', 'confirm', 'unstash']);
    expect(mockAddPending).toHaveBeenCalledWith({
      mode: 'open', tradeId: 't1', token: 'USDT', amount: '5000000',
      txHash: '0xhash', chainId: 146, walletAddress: W, createdAt: 1_700_000_000_000,
    });
    expect(mockRemovePending).toHaveBeenCalledWith('0xhash');
    jest.restoreAllMocks();
  });

  it('keeps the stash when the confirm fails, so a retry can record it', async () => {
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: ok, stash,
      confirm: async () => ({ success: false, message: 'Network request failed' }) });
    expect(r.status).toBe('confirm-failed');
    expect(mockAddPending).toHaveBeenCalled();
    expect(mockRemovePending).not.toHaveBeenCalled();
  });

  it('drops the stash on a revert — there is nothing to record', async () => {
    mockWaitMined.mockRejectedValue(new TxRevertedError('0xhash'));
    await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: ok, stash, confirm: jest.fn() });
    expect(mockRemovePending).toHaveBeenCalledWith('0xhash');
  });

  it('a storage failure never blocks the confirm', async () => {
    mockAddPending.mockRejectedValue(new Error('disk full'));
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: ok, stash,
      confirm: async () => ({ success: true }) });
    expect(r.status).toBe('confirmed');
    warn.mockRestore();
  });

  it('a throwing stash builder never turns a broadcast tx into a thrown error', async () => {
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: ok,
      stash: () => { throw new Error('no trade in prepare'); }, confirm: async () => ({ success: true }) });
    expect(r.status).toBe('confirmed');
    warn.mockRestore();
  });

  it('no stash builder → storage untouched', async () => {
    await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok, confirm: async () => ({ success: true }) });
    expect(mockAddPending).not.toHaveBeenCalled();
    expect(mockRemovePending).not.toHaveBeenCalled();
  });
});

describe('txOutcomeAlert', () => {
  const HASH = '0x' + 'a'.repeat(64);
  it('says nothing for confirmed / no-tx / aborted', () => {
    expect(txOutcomeAlert('Release', { status: 'confirmed', txHash: '0x', prepared: {} })).toBeNull();
    expect(txOutcomeAlert('Release', { status: 'no-tx', prepared: {} })).toBeNull();
    expect(txOutcomeAlert('Release', { status: 'aborted' })).toBeNull();
  });
  it('refused → friendly copy', () => {
    expect(txOutcomeAlert('Release', { status: 'refused', response: { success: false, message: 'p2p: not authorized for this action' } }))
      .toEqual({ title: 'Release failed', message: "You can't do that on this trade." });
  });
  it('confirm-failed → do-not-resend, with the full hash for support', () => {
    const a = txOutcomeAlert('Mark paid', { status: 'confirm-failed', txHash: HASH, prepared: {}, message: 'm', mined: true });
    expect(a?.title).toBe('Mark paid sent — still confirming');
    expect(a?.message).toMatch(/on chain/);
    expect(a?.message).toMatch(/Don't send it again/);
    expect(a?.message).toContain(HASH);
    expect(a?.message).not.toMatch(/failed/i);
  });
  it('unmined confirm-failed → it may still go through, never "failed"', () => {
    const a = txOutcomeAlert('Release', { status: 'confirm-failed', txHash: HASH, prepared: {}, message: 'm', mined: false });
    expect(a?.title).toBe('Release sent — still confirming');
    expect(a?.message).toMatch(/may still go through/);
    expect(a?.message).toMatch(/Don't send it again/);
    expect(a?.message).toContain(HASH);
    expect(a?.message).not.toMatch(/on chain|failed/i);
  });
  it('reverted → a clear failure: nothing changed, safe to try again', () => {
    const a = txOutcomeAlert('Release', { status: 'reverted', txHash: HASH, prepared: {} });
    expect(a?.title).toBe('Release failed');
    expect(a?.message).toMatch(/nothing changed/i);
    expect(a?.message).toMatch(/try again/i);
  });
});
