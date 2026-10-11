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
const mockTradesApi = {
  confirmOpen: jest.fn(),
  confirmMarkPaid: jest.fn(),
  confirmRelease: jest.fn(),
  confirmCancel: jest.fn(),
  confirmRaiseDispute: jest.fn(),
};
jest.mock('../p2p', () => ({
  p2pChainKey: (id: number) => `chain-${id}`,
  unsignedTxToTxLike: (tx: any) => ({ __txLike: true, ...tx }),
  // A getter: the factory runs before the const above is initialised.
  get tradesApi() { return mockTradesApi; },
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
import { P2P_MINE_WAIT_MS, p2pTxFollowUp, retryStashedConfirm, runP2PTx } from '../p2pTx';
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
afterEach(() => jest.restoreAllMocks());

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
  expect(mockWaitMined).toHaveBeenCalledWith(146, '0xhash', expect.objectContaining({ timeoutMs: P2P_MINE_WAIT_MS }));
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

it('refuses an unsigned tx built for another chain, signing nothing', async () => {
  const r = await runP2PTx({ chainId: 56, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok, confirm: jest.fn() });
  expect(r).toMatchObject({ status: 'refused', response: { success: false } });
  expect(mockSign).not.toHaveBeenCalled();
  expect(mockBroadcast).not.toHaveBeenCalled();
  expect(p2pTxFollowUp(r, { label: 'Release' }).alert?.message).toMatch(/different network/);
});

describe('in-flight guard', () => {
  function deferred<T>() {
    let resolve!: (v: T) => void;
    const promise = new Promise<T>((r) => { resolve = r; });
    return { promise, resolve };
  }

  it('a concurrent double call is busy: nothing prepared or signed twice', async () => {
    const gate = deferred<{ success: boolean; data: typeof prepared }>();
    const prepare = jest.fn(() => gate.promise);
    const params = { chainId: 146, walletAddress: W, action: 'release' as const, tradeId: 't1', prepare, confirm: async () => ({ success: true }) };

    const first = runP2PTx(params);
    const second = await runP2PTx(params);
    expect(second).toEqual({ status: 'busy' });
    expect(p2pTxFollowUp(second, { label: 'Release' })).toEqual({ reload: false });

    gate.resolve({ success: true, data: prepared });
    expect((await first).status).toBe('confirmed');
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(mockSign).toHaveBeenCalledTimes(1);
    expect(mockEnsureP2PGas).toHaveBeenCalledTimes(1);
  });

  it('different trades do not block each other', async () => {
    const gate = deferred<{ success: boolean; data: typeof prepared }>();
    const first = runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: () => gate.promise, confirm: async () => ({ success: true }) });
    const other = await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't2', prepare: ok, confirm: async () => ({ success: true }) });
    expect(other.status).toBe('confirmed');
    gate.resolve({ success: true, data: prepared });
    await first;
  });

  it('is released after success, a failure outcome and a throw', async () => {
    const run = (prepare: () => Promise<any>) =>
      runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare, confirm: async () => ({ success: true }) });

    expect((await run(ok)).status).toBe('confirmed');
    expect((await run(ok)).status).toBe('confirmed');
    expect((await run(async () => ({ success: false, message: 'x' }))).status).toBe('refused');
    mockBroadcast.mockRejectedValueOnce(new Error('nonce too low'));
    await expect(run(ok)).rejects.toThrow();
    mockEnsureP2PGas.mockRejectedValueOnce(new Error('boom'));
    await expect(run(ok)).rejects.toThrow('boom');
    expect((await run(ok)).status).toBe('confirmed');
  });
});

describe('mining', () => {
  it('waits on the provider that accepted the broadcast (it may be a fallback RPC)', async () => {
    const provider = { waitForTransaction: jest.fn() };
    mockBroadcast.mockResolvedValue({ hash: '0xhash', provider });
    await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok, confirm: async () => ({ success: true }) });
    expect(mockWaitMined).toHaveBeenCalledWith(146, '0xhash', expect.objectContaining({ provider }));
  });

  it('waits less than the server does on confirm (~30s)', () => {
    expect(P2P_MINE_WAIT_MS).toBeLessThanOrEqual(25_000);
  });

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
    jest.spyOn(console, 'warn').mockImplementation(() => {});
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: ok, stash,
      confirm: async () => ({ success: true }) });
    expect(r.status).toBe('confirmed');
  });

  it('a throwing stash builder never turns a broadcast tx into a thrown error', async () => {
    jest.spyOn(console, 'warn').mockImplementation(() => {});
    const r = await runP2PTx({ chainId: 146, walletAddress: W, action: 'open_trade', adId: 'a', prepare: ok,
      stash: () => { throw new Error('no trade in prepare'); }, confirm: async () => ({ success: true }) });
    expect(r.status).toBe('confirmed');
  });

  it('no stash builder → storage untouched', async () => {
    await runP2PTx({ chainId: 146, walletAddress: W, action: 'release', tradeId: 't1', prepare: ok, confirm: async () => ({ success: true }) });
    expect(mockAddPending).not.toHaveBeenCalled();
    expect(mockRemovePending).not.toHaveBeenCalled();
  });
});

describe('p2pTxFollowUp (the shared default per outcome)', () => {
  const HASH = '0x' + 'a'.repeat(64);
  const f = (r: Parameters<typeof p2pTxFollowUp>[0], label = 'Release') => p2pTxFollowUp(r, { label });
  it('confirmed / no-tx: just the reload; aborted / busy: nothing', () => {
    expect(f({ status: 'confirmed', txHash: '0x', prepared: {} })).toEqual({ reload: true });
    expect(f({ status: 'no-tx', prepared: {} })).toEqual({ reload: true });
    expect(f({ status: 'aborted' })).toEqual({ reload: false });
    expect(f({ status: 'busy' })).toEqual({ reload: false });
  });
  it('refused → friendly copy and a reload', () => {
    expect(f({ status: 'refused', response: { success: false, message: 'p2p: not authorized for this action' } }))
      .toEqual({ alert: { title: 'Release failed', message: "You can't do that on this trade." }, reload: true });
  });
  it('refused for KYC → the KYC prompt only, no alert or reload', () => {
    expect(f({ status: 'refused', response: { success: false, status: 403, code: 'kyc_verification_required', message: 'v' } }))
      .toEqual({ kyc: { code: 'kyc_verification_required', message: 'v' }, reload: false });
  });
  it('confirm-failed → do-not-resend, with the full hash for support', () => {
    const a = f({ status: 'confirm-failed', txHash: HASH, prepared: {}, message: 'm', mined: true }, 'Mark paid').alert;
    expect(a?.title).toBe('Mark paid sent — still confirming');
    expect(a?.message).toMatch(/on chain/);
    expect(a?.message).toMatch(/Don't send it again/);
    expect(a?.message).toContain(HASH);
    expect(a?.message).not.toMatch(/failed/i);
  });
  it('unmined confirm-failed → it may still go through, never "failed"', () => {
    const a = f({ status: 'confirm-failed', txHash: HASH, prepared: {}, message: 'm', mined: false }).alert;
    expect(a?.title).toBe('Release sent — still confirming');
    expect(a?.message).toMatch(/may still go through/);
    expect(a?.message).toMatch(/Don't send it again/);
    expect(a?.message).toContain(HASH);
    expect(a?.message).not.toMatch(/on chain|failed/i);
  });
  it('reverted → a clear failure: nothing changed, safe to try again', () => {
    const next = f({ status: 'reverted', txHash: HASH, prepared: {} });
    expect(next.reload).toBe(true);
    expect(next.alert?.title).toBe('Release failed');
    expect(next.alert?.message).toMatch(/nothing changed/i);
    expect(next.alert?.message).toMatch(/try again/i);
  });
  it('a status override replaces fields of that outcome only; a function gets the narrowed result', () => {
    const overrides = {
      label: 'Dispute',
      confirmed: { navigate: 't1' },
      'confirm-failed': (r: { txHash: string }) => ({ navigate: r.txHash }),
    };
    expect(p2pTxFollowUp({ status: 'confirmed', txHash: HASH, prepared: {} }, overrides)).toEqual({ reload: true, navigate: 't1' });
    expect(p2pTxFollowUp({ status: 'confirm-failed', txHash: HASH, prepared: {}, message: 'm', mined: false }, overrides))
      .toMatchObject({ navigate: HASH, alert: { title: 'Dispute sent — still confirming' } });
    expect(p2pTxFollowUp({ status: 'aborted' }, overrides)).toEqual({ reload: false });
  });
});

describe('retryStashedConfirm (the trade screen\'s retry for a lost confirm)', () => {
  const HASH = '0x' + 'b'.repeat(64);
  const base = { token: 'USDT', chainId: 146, amount: '1', txHash: HASH, walletAddress: W, tradeId: 't1', createdAt: 0 };
  const trade = { id: 't1', status: 'paid' };

  it.each([
    ['open', 'confirmOpen', ['t1', HASH, W]],
    ['mark_paid', 'confirmMarkPaid', ['t1', HASH]],
    ['release', 'confirmRelease', ['t1', HASH]],
    ['cancel_trade', 'confirmCancel', ['t1', HASH]],
    ['dispute', 'confirmRaiseDispute', ['t1', HASH]],
  ] as const)('%s → %s with the stashed hash, then unstashes', async (mode, fn, args) => {
    mockTradesApi[fn].mockResolvedValue({ success: true, data: trade });
    await expect(retryStashedConfirm({ ...base, mode })).resolves.toEqual({ status: 'confirmed', data: trade });
    expect(mockTradesApi[fn]).toHaveBeenCalledWith(...args);
    expect(mockRemovePending).toHaveBeenCalledWith(HASH);
  });
  it('a server-side revert unstashes and says so', async () => {
    mockTradesApi.confirmMarkPaid.mockResolvedValue({ success: false, message: 'p2p: transaction reverted on chain' });
    await expect(retryStashedConfirm({ ...base, mode: 'mark_paid' })).resolves.toEqual({ status: 'reverted' });
    expect(mockRemovePending).toHaveBeenCalledWith(HASH);
  });
  it('anything else keeps the stash for the next retry', async () => {
    mockTradesApi.confirmRelease.mockResolvedValue({ success: false, status: 503, message: 'p2p: blockchain temporarily unavailable' });
    const r = await retryStashedConfirm({ ...base, mode: 'release' });
    expect(r).toEqual({ status: 'pending', message: 'The network is busy right now. Try again in a minute.' });
    expect(mockRemovePending).not.toHaveBeenCalled();
  });
  it('a thrown request is still "pending", never a crash', async () => {
    mockTradesApi.confirmCancel.mockRejectedValue(new Error('Network request failed'));
    expect((await retryStashedConfirm({ ...base, mode: 'cancel_trade' })).status).toBe('pending');
  });
  it('refuses a record that is not a trade tx', async () => {
    expect((await retryStashedConfirm({ ...base, mode: 'deposit' })).status).toBe('pending');
    expect(mockRemovePending).not.toHaveBeenCalled();
  });
});
