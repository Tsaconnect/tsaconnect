// services/__tests__/p2pPending.test.ts
//
// Covers the non-overwriting recovery store: add/list/remove, dedupe-by-txHash,
// per-mode isolation, and case-insensitive wallet matching. Uses a small
// in-memory AsyncStorage mock so the array semantics (the whole point of the
// store) are exercised for real, not stubbed to null.

// Prefixed `mock` so jest allows referencing it inside the (hoisted) factory.
let mockStore: Record<string, string>;

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn((key: string) => Promise.resolve(mockStore[key] ?? null)),
  setItem: jest.fn((key: string, value: string) => {
    mockStore[key] = value;
    return Promise.resolve();
  }),
  removeItem: jest.fn((key: string) => {
    delete mockStore[key];
    return Promise.resolve();
  }),
}));

// eslint-disable-next-line import/first -- jest.mock must be hoisted above this import.
import {
  addPending,
  findPendingOpen,
  listPending,
  removePending,
  type PendingEscrowConfirm,
} from '../p2pPending';

function rec(over: Partial<PendingEscrowConfirm> = {}): PendingEscrowConfirm {
  return {
    mode: 'deposit',
    token: 'USDT',
    chainId: 146,
    amount: '1000000',
    txHash: '0xabc',
    walletAddress: '0xWALLET',
    createdAt: 1_700_000_000_000,
    ...over,
  };
}

beforeEach(() => {
  mockStore = {};
});

describe('addPending / listPending', () => {
  it('appends and lists a record for the matching mode + wallet', async () => {
    await addPending(rec());
    const list = await listPending('deposit', '0xWALLET');
    expect(list).toHaveLength(1);
    expect(list[0].txHash).toBe('0xabc');
  });

  it('keeps multiple distinct records (a list, not a singleton)', async () => {
    await addPending(rec({ txHash: '0x1', amount: '1' }));
    await addPending(rec({ txHash: '0x2', amount: '2' }));
    const list = await listPending('deposit', '0xWALLET');
    expect(list.map((r) => r.txHash).sort()).toEqual(['0x1', '0x2']);
    // The second add must NOT orphan the first.
    expect(list.map((r) => r.amount).sort()).toEqual(['1', '2']);
  });
});

describe('dedupe by txHash', () => {
  it('replaces rather than duplicates when the same txHash is re-added', async () => {
    await addPending(rec({ txHash: '0xdup', amount: '1' }));
    await addPending(rec({ txHash: '0xdup', amount: '999' }));
    const list = await listPending('deposit', '0xWALLET');
    expect(list).toHaveLength(1);
    expect(list[0].amount).toBe('999'); // latest write wins
  });
});

describe('per-mode isolation', () => {
  it('does not surface a withdraw record under a deposit query (and vice versa)', async () => {
    await addPending(rec({ mode: 'deposit', txHash: '0xdep' }));
    await addPending(rec({ mode: 'withdraw', txHash: '0xwd' }));

    const deposits = await listPending('deposit', '0xWALLET');
    expect(deposits.map((r) => r.txHash)).toEqual(['0xdep']);

    const withdrawals = await listPending('withdraw', '0xWALLET');
    expect(withdrawals.map((r) => r.txHash)).toEqual(['0xwd']);
  });
});

describe('case-insensitive wallet matching', () => {
  it('lists a record stashed under a checksummed wallet when queried lowercased', async () => {
    await addPending(rec({ walletAddress: '0xAbCdEf', txHash: '0xcase' }));
    const lower = await listPending('deposit', '0xabcdef');
    const upper = await listPending('deposit', '0XABCDEF');
    expect(lower).toHaveLength(1);
    expect(upper).toHaveLength(1);
    expect(lower[0].txHash).toBe('0xcase');
  });

  it('excludes a different wallet', async () => {
    await addPending(rec({ walletAddress: '0xAAA', txHash: '0xa' }));
    const other = await listPending('deposit', '0xBBB');
    expect(other).toHaveLength(0);
  });
});

describe('removePending', () => {
  it('removes only the matching txHash, leaving others intact', async () => {
    await addPending(rec({ txHash: '0x1' }));
    await addPending(rec({ txHash: '0x2' }));
    await removePending('0x1');
    const list = await listPending('deposit', '0xWALLET');
    expect(list.map((r) => r.txHash)).toEqual(['0x2']);
  });

  it('is a no-op for an unknown txHash', async () => {
    await addPending(rec({ txHash: '0x1' }));
    await removePending('0xnope');
    const list = await listPending('deposit', '0xWALLET');
    expect(list).toHaveLength(1);
  });

  it('clears storage when the last record is removed', async () => {
    await addPending(rec({ txHash: '0xonly' }));
    await removePending('0xonly');
    expect(mockStore['p2p.pendingEscrowConfirms.v1']).toBeUndefined();
    const list = await listPending('deposit', '0xWALLET');
    expect(list).toHaveLength(0);
  });
});

describe('corrupt / empty storage', () => {
  it('returns [] when the stored blob is not an array', async () => {
    mockStore['p2p.pendingEscrowConfirms.v1'] = JSON.stringify({ not: 'an array' });
    const list = await listPending('deposit', '0xWALLET');
    expect(list).toEqual([]);
  });

  it('returns [] when the stored blob is unparseable JSON', async () => {
    mockStore['p2p.pendingEscrowConfirms.v1'] = '{not json';
    const list = await listPending('deposit', '0xWALLET');
    expect(list).toEqual([]);
  });
});

describe("mode 'open' (a trade open whose confirm was lost)", () => {
  it('finds the stash by trade id and keeps it out of deposit/withdraw lists', async () => {
    await addPending(rec({ mode: 'open', txHash: '0xopen', tradeId: 'trade-1' }));
    await addPending(rec({ mode: 'deposit', txHash: '0xdep' }));
    expect((await findPendingOpen('trade-1'))?.txHash).toBe('0xopen');
    expect(await findPendingOpen('trade-2')).toBeNull();
    expect((await listPending('deposit', '0xWALLET')).map((r) => r.txHash)).toEqual(['0xdep']);
  });
});
