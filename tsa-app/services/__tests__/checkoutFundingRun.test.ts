/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Paying a checkout with another token: one swap into her own wallet on Sonic,
 * then the escrow for each order. These pin the run's stages, the terminal
 * outcomes (refunded, still on its way), what survives a restart, and that the
 * swap is signed on MAINNET against the funding's own routes and the price she
 * was shown.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn(async () => null),
  setItem: jest.fn(async () => {}),
  removeItem: jest.fn(async () => {}),
}));

import {
  canForgetRun,
  CheckoutFundingRunner,
  FUNDING_COPY,
  FUNDING_RUN_KEY,
  type FundingDeps,
  type FundingRecord,
  type FundingRunState,
  type FundingStage,
} from '../checkoutFundingRun';
import { FundingError } from '../checkoutFundingApi';

const token = { address: '0x55d398326f99059fF775485246999027B3197955', symbol: 'USDT', name: 'Tether', decimals: 18, logoURI: '', chainId: 56 };
const orders = [
  { id: 'o1', productId: 'p1', token: 'USDC' },
  { id: 'o2', productId: 'p2', token: 'USDC' },
] as any[];
const WALLET = '0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce';
const HASH = `0x${'ab'.repeat(32)}`;

function memoryStorage() {
  const map = new Map<string, string>();
  return {
    map,
    get: async (k: string) => map.get(k) ?? null,
    set: async (k: string, v: string) => { map.set(k, v); },
    remove: async (k: string) => { map.delete(k); },
  };
}

const pending = () => new FundingError('Still swapping — try again in a moment', 409, 'swap_pending');

function rig(over: Partial<FundingDeps> = {}) {
  const storage = memoryStorage();
  const deps: FundingDeps = {
    start: jest.fn(async () => ({
      intent: { id: 'i1', status: 'swapping' } as any,
      quote: { fromAmount: '100' } as any,
      gasTopUp: false,
      priced: { etaSeconds: 1080 } as any,
    })),
    record: jest.fn(async () => {}),
    confirm: jest.fn()
      .mockRejectedValueOnce(pending())
      .mockResolvedValue({ intent: { id: 'i1', status: 'delivered' } as any, destTxHash: '0xdest', needsGas: true }),
    get: jest.fn(async () => null),
    abandon: jest.fn(async () => {}),
    sourceGas: jest.fn(async () => ({})),
    sonicGas: jest.fn(async () => ({ txHash: '0xgas' })),
    executeSwap: jest.fn(async () => HASH),
    pendingHash: jest.fn(async () => null),
    clearPendingHash: jest.fn(async () => {}),
    escrow: jest.fn(async (o: any) => o),
    sleep: jest.fn(async () => {}),
    now: () => 1_000,
    storage,
    ...over,
  };
  const runner = new CheckoutFundingRunner(deps);
  const stages: FundingStage[] = [];
  runner.subscribe((s) => {
    if (stages[stages.length - 1] !== s.stage) stages.push(s.stage);
  });
  return { runner, deps, storage, stages };
}

const payArgs = (createOrders = jest.fn(async () => ({ orders, groupId: 'g1' }))) => ({
  token, chainId: 56, wallet: WALLET, pricedFromAmount: '100', createOrders,
});

const persisted = (over: Partial<FundingRecord>): FundingRecord => ({
  v: 1, groupId: 'g1', orders, token, chainId: 56, wallet: WALLET, startedAt: 1, paid: [], progress: {}, ...over,
});

describe('a funded checkout', () => {
  test('quote → sign → bridge → delivered → Sonic gas → escrow each order → done', async () => {
    const { runner, deps, storage, stages } = rig();
    await runner.pay(payArgs());

    expect(stages).toEqual(['creating_orders', 'starting', 'signing', 'bridging', 'sonic_gas', 'paying', 'done']);
    expect(deps.start).toHaveBeenCalledWith('g1', { sourceChainId: 56, sourceToken: token.address, walletAddress: WALLET });
    expect(deps.record).toHaveBeenCalledWith('i1', HASH);
    expect(deps.confirm).toHaveBeenCalledTimes(2); // waited out one "still swapping"
    expect(deps.sonicGas).toHaveBeenCalledWith('i1');
    expect((deps.escrow as jest.Mock).mock.calls.map((c) => c[0].id)).toEqual(['o1', 'o2']);
    // Finished: nothing left to resume, and the phone's copy of the hash is gone.
    expect(storage.map.has(FUNDING_RUN_KEY)).toBe(false);
    expect(deps.clearPendingHash).toHaveBeenCalledWith('i1');
  });

  test('the swap is signed on MAINNET, through the funding routes, held to the price she was shown', async () => {
    const { runner, deps } = rig();
    await runner.pay(payArgs());
    const run = (deps.executeSwap as jest.Mock).mock.calls[0][0];
    expect(run).toMatchObject({
      intentId: 'i1', sourceChainId: 56, sourceAddress: WALLET, pricedFromAmount: '100', network: 'mainnet',
      closeIntent: deps.abandon, requestGasTopUp: deps.sourceGas,
    });
    expect(run.paymentId).toBeUndefined();
  });

  test('no Sonic top-up is asked for when she already holds the gas', async () => {
    const { runner, deps } = rig({
      confirm: jest.fn(async () => ({ intent: {} as any, destTxHash: '0xd', needsGas: false })),
    });
    await runner.pay(payArgs());
    expect(deps.sonicGas).not.toHaveBeenCalled();
    expect(runner.getState().stage).toBe('done');
  });

  test('a refunded swap ends the run: her tokens come back, nothing is paid, and nothing is left to resume', async () => {
    const { runner, deps, storage } = rig({
      confirm: jest.fn(async () => { throw new FundingError('refunded', 409, 'swap_refunded'); }),
    });
    await runner.pay(payArgs());
    expect(runner.getState()).toMatchObject({ stage: 'failed', outcome: 'refunded', error: FUNDING_COPY.refunded });
    expect(deps.escrow).not.toHaveBeenCalled();
    expect(storage.map.has(FUNDING_RUN_KEY)).toBe(false);
  });

  test('a swap refused before it left her wallet can be paid again, on the same orders', async () => {
    const executeSwap = jest.fn()
      .mockRejectedValueOnce(new Error('The price moved while you were deciding.'))
      .mockResolvedValue(HASH);
    const { runner, deps, storage } = rig({ executeSwap });
    const createOrders = jest.fn(async () => ({ orders, groupId: 'g1' }));
    await runner.pay(payArgs(createOrders));
    expect(runner.getState()).toMatchObject({ stage: 'failed', outcome: 'retry' });
    const kept: FundingRecord = JSON.parse(storage.map.get(FUNDING_RUN_KEY)!);
    expect(kept.intentId).toBeUndefined();
    expect(kept.groupId).toBe('g1');

    await runner.retry('101');
    expect(createOrders).toHaveBeenCalledTimes(1); // the orders are not placed twice
    expect(deps.start).toHaveBeenCalledTimes(2);
    expect((executeSwap as jest.Mock).mock.calls[1][0].pricedFromAmount).toBe('101');
    expect(runner.getState().stage).toBe('done');
  });

  test('a broadcast whose answer was lost is followed by its hash, never re-sent', async () => {
    const lost = Object.assign(new Error('lost'), { name: 'SwapBroadcastUnconfirmedError', txHash: HASH });
    const { runner, deps } = rig({ executeSwap: jest.fn(async () => { throw lost; }) });
    await runner.pay(payArgs());
    expect(deps.record).toHaveBeenCalledWith('i1', HASH);
    expect(runner.getState().stage).toBe('done');
    expect(deps.executeSwap).toHaveBeenCalledTimes(1);
  });

  test('a bridge that outlasts the wait hands back "still on its way", and the run stays resumable', async () => {
    let t = 0;
    const { runner, storage } = rig({
      confirm: jest.fn(async () => { throw pending(); }),
      now: () => t,
      sleep: jest.fn(async (ms: number) => { t += ms; }),
    });
    await runner.pay(payArgs());
    expect(runner.getState()).toMatchObject({ stage: 'failed', outcome: 'wait', error: FUNDING_COPY.stillOnItsWay });
    const kept: FundingRecord = JSON.parse(storage.map.get(FUNDING_RUN_KEY)!);
    expect(kept.swapHash).toBe(HASH);
  });

  test('an order that fails to escrow keeps what was paid and what it already sent', async () => {
    const escrow = jest.fn()
      .mockImplementationOnce(async (o: any) => o)
      .mockImplementationOnce(async (_o: any, _p: any, progress: any) => {
        progress.approve = { hash: '0xapprove', confirmed: true };
        throw new Error('Transaction would fail on-chain.');
      });
    const { runner, storage } = rig({ escrow });
    await runner.pay(payArgs());
    expect(runner.getState()).toMatchObject({ stage: 'failed', outcome: 'retry' });
    const kept: FundingRecord = JSON.parse(storage.map.get(FUNDING_RUN_KEY)!);
    expect(kept.paid).toEqual(['o1']);
    expect(kept.progress.o2.approve?.hash).toBe('0xapprove');
    expect(kept.delivered).toBe(true);
  });
});

describe('after a restart', () => {
  test('a swap signed before the kill is reported from the phone and followed to the end — nothing is signed again', async () => {
    const { runner, deps, storage } = rig({ pendingHash: jest.fn(async () => HASH) });
    storage.map.set(FUNDING_RUN_KEY, JSON.stringify(persisted({ intentId: 'i1', etaSeconds: 1080 })));
    await runner.resume();
    expect(deps.executeSwap).not.toHaveBeenCalled();
    expect(deps.record).toHaveBeenCalledWith('i1', HASH);
    expect(runner.getState().stage).toBe('done');
  });

  test('a delivered checkout pays only the orders still unpaid, reusing their escrow progress', async () => {
    const { runner, deps, storage } = rig({
      confirm: jest.fn(async () => ({ intent: {} as any, destTxHash: '0xd', needsGas: false })),
    });
    storage.map.set(FUNDING_RUN_KEY, JSON.stringify(persisted({
      intentId: 'i1', swapHash: HASH, delivered: true, paid: ['o1'],
      progress: { o2: { approve: { hash: '0xa', confirmed: true } } },
    })));
    await runner.resume();
    const calls = (deps.escrow as jest.Mock).mock.calls;
    expect(calls.map((c) => c[0].id)).toEqual(['o2']);
    expect(calls[0][2]).toEqual({ approve: { hash: '0xa', confirmed: true } });
    expect(runner.getState().stage).toBe('done');
  });

  test('an intent never signed on this phone is closed, and her orders can be paid again', async () => {
    const { runner, deps, storage } = rig({ get: jest.fn(async () => ({ id: 'i1', status: 'swapping' } as any)) });
    storage.map.set(FUNDING_RUN_KEY, JSON.stringify(persisted({ intentId: 'i1' })));
    await runner.resume();
    expect(deps.abandon).toHaveBeenCalledWith('i1');
    expect(runner.getState()).toMatchObject({ stage: 'failed', outcome: 'retry', error: FUNDING_COPY.notSent });
    const kept: FundingRecord = JSON.parse(storage.map.get(FUNDING_RUN_KEY)!);
    expect(kept.intentId).toBeUndefined();
  });

  test('nothing persisted is nothing to do', async () => {
    const { runner, deps } = rig();
    await runner.resume();
    expect(runner.getState().stage).toBe('idle');
    expect(deps.confirm).not.toHaveBeenCalled();
  });

  test('one run at a time: a second pay while one is in flight does nothing', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => { release = r; });
    const createOrders = jest.fn(async () => { await gate; return { orders, groupId: 'g1' }; });
    const { runner } = rig();
    const first = runner.pay(payArgs(createOrders));
    await runner.pay(payArgs(createOrders));
    release();
    await first;
    expect(createOrders).toHaveBeenCalledTimes(1);
  });

  // resume() marks itself running BEFORE its own `await load()` settles, so a
  // Pay tapped on the very same tick (e.g. a mount effect racing a button) sees
  // `running` already claimed and is refused rather than starting a second,
  // competing run on top of the one resume() is about to pick up.
  test('resume claims "running" before its load, so a pay tapped in the same tick is refused', async () => {
    const { runner, deps, storage } = rig({
      confirm: jest.fn(async () => ({ intent: {} as any, destTxHash: '0xd', needsGas: false })),
    });
    storage.map.set(FUNDING_RUN_KEY, JSON.stringify(persisted({ intentId: 'i1', swapHash: HASH, delivered: true, paid: [] })));
    const resumeP = runner.resume();
    const payP = runner.pay(payArgs()); // fired before resume's `await load()` can settle
    await Promise.all([resumeP, payP]);
    expect(deps.start).not.toHaveBeenCalled(); // pay() never started a competing checkout
    expect(runner.getState().stage).toBe('done'); // resume's own run ran to completion
  });

  // The other half of the same race: pay() itself finds a run already on the
  // phone (via its own load()) and follows it instead of placing new orders.
  test('pay() finding a run already on the phone follows it instead of creating a second one', async () => {
    const { runner, storage } = rig({
      confirm: jest.fn(async () => ({ intent: {} as any, destTxHash: '0xd', needsGas: false })),
    });
    storage.map.set(FUNDING_RUN_KEY, JSON.stringify(persisted({ intentId: 'i1', swapHash: HASH, delivered: true, paid: [] })));
    const createOrders = jest.fn(async () => ({ orders, groupId: 'g1' }));
    await runner.pay(payArgs(createOrders));
    expect(createOrders).not.toHaveBeenCalled(); // the stored run was resumed, never shadowed
    expect(runner.getState().stage).toBe('done');
  });
});

// An order the escrow can never pay (expired or cancelled) must not turn a
// checkout that otherwise succeeded into an endless "tap Continue to retry":
// it is a terminal outcome — the funds already delivered stay in her wallet on
// Sonic, the run is forgotten, and she is told to check out again (S1).
describe('an order that can never be paid', () => {
  test('ends the run as "expired", not "retry" — after paying every order that still could be', async () => {
    const escrow = jest.fn()
      .mockImplementationOnce(async (o: any) => o) // o1 pays fine
      .mockImplementationOnce(async () => {
        throw Object.assign(new Error('this order has expired'), { name: 'OrderNotPayableError' });
      });
    const { runner, storage } = rig({
      confirm: jest.fn(async () => ({ intent: {} as any, destTxHash: '0xd', needsGas: false })),
      escrow,
    });
    await runner.pay(payArgs());
    expect(escrow).toHaveBeenCalledTimes(2); // o1 paid, o2 attempted and found unpayable
    expect(runner.getState()).toMatchObject({ stage: 'failed', outcome: 'expired', error: FUNDING_COPY.expired });
    // Terminal: nothing is left on the phone to retry — checking out again starts fresh.
    expect(storage.map.has(FUNDING_RUN_KEY)).toBe(false);
  });
});

// canForgetRun is what "View orders" / "Back to checkout" ask before discarding
// the persisted run: only once nothing of hers could still be in flight.
describe('canForgetRun', () => {
  const state = (over: Partial<FundingRunState>): FundingRunState => ({ stage: 'idle', ...over });

  test('a finished, idle, ended-short or expired run may be forgotten', () => {
    expect(canForgetRun(state({ stage: 'idle' }))).toBe(true);
    expect(canForgetRun(state({ stage: 'done' }))).toBe(true);
    expect(canForgetRun(state({ stage: 'failed', outcome: 'refunded' }))).toBe(true);
    expect(canForgetRun(state({ stage: 'failed', outcome: 'expired' }))).toBe(true);
  });

  test('anything still in flight — signing, bridging, waiting, or a hash we cannot find — must not be forgotten', () => {
    expect(canForgetRun(state({ stage: 'signing' }))).toBe(false);
    expect(canForgetRun(state({ stage: 'bridging' }))).toBe(false);
    expect(canForgetRun(state({ stage: 'sonic_gas' }))).toBe(false);
    expect(canForgetRun(state({ stage: 'paying' }))).toBe(false);
    expect(canForgetRun(state({ stage: 'failed', outcome: 'wait' }))).toBe(false);
    expect(canForgetRun(state({ stage: 'failed', outcome: 'held' }))).toBe(false);
  });

  test('a "retry" only forgets when nothing was ever sent or delivered', () => {
    expect(canForgetRun(state({ stage: 'failed', outcome: 'retry' }))).toBe(true); // no record at all
    expect(canForgetRun(state({ stage: 'failed', outcome: 'retry', record: persisted({}) }))).toBe(true);
    expect(canForgetRun(state({ stage: 'failed', outcome: 'retry', record: persisted({ intentId: 'i1' }) }))).toBe(false);
    expect(canForgetRun(state({ stage: 'failed', outcome: 'retry', record: persisted({ swapHash: HASH }) }))).toBe(false);
    expect(canForgetRun(state({ stage: 'failed', outcome: 'retry', record: persisted({ delivered: true }) }))).toBe(false);
  });

  test('discard() is refused while the run cannot be forgotten — "View orders" during a wait keeps the record for a later resume', async () => {
    let t = 0;
    const { runner, storage, deps } = rig({
      confirm: jest.fn(async () => { throw pending(); }),
      now: () => t,
      sleep: jest.fn(async (ms: number) => { t += ms; }),
    });
    await runner.pay(payArgs());
    expect(runner.getState()).toMatchObject({ stage: 'failed', outcome: 'wait' });
    expect(storage.map.has(FUNDING_RUN_KEY)).toBe(true);

    await runner.discard();
    expect(storage.map.has(FUNDING_RUN_KEY)).toBe(true); // still there: discard() was a no-op
    expect(deps.abandon).not.toHaveBeenCalled();

    const kept: FundingRecord = JSON.parse(storage.map.get(FUNDING_RUN_KEY)!);
    expect(kept.swapHash).toBe(HASH); // the hash checkout resumes from is intact
  });
});
