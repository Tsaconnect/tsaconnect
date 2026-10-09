/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The trade screen's lost-confirm stash. What this pins: a stash written while
 * the trade object stays the same (a confirm-failed whose reload also failed)
 * still shows after refresh(); a stale one is kept; a settled one is dropped.
 */
let mockStore: Record<string, string> = {};
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn((k: string) => Promise.resolve(mockStore[k] ?? null)),
  setItem: jest.fn((k: string, v: string) => { mockStore[k] = v; return Promise.resolve(); }),
  removeItem: jest.fn((k: string) => { delete mockStore[k]; return Promise.resolve(); }),
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import type { P2PTrade } from '@/services/p2p';
import { addPending, listPendingForTrade, type PendingTradeTx } from '@/services/p2pPending';
import { PENDING_TRADE_TX_STALE_MS } from '@/utils/p2pTradeView';
import { usePendingTradeTx, type PendingTradeTxState } from '../usePendingTradeTx';

const trade = { id: 't1', status: 'pending_payment', openTxHash: '0x' + '1'.repeat(64) } as P2PTrade;
function stash(over: Partial<PendingTradeTx> = {}): PendingTradeTx {
  return { mode: 'mark_paid', token: 'USDT', chainId: 146, amount: '1', txHash: '0xa', walletAddress: '0xW',
    tradeId: 't1', createdAt: Date.now(), ...over };
}

let state: PendingTradeTxState;
let tree: ReactTestRenderer;
function Screen({ t }: { t: P2PTrade | null }) {
  state = usePendingTradeTx(t);
  return null;
}
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

beforeEach(() => { mockStore = {}; });
afterEach(() => act(() => tree.unmount()));

it('refresh() picks up a stash written while the trade object did not change', async () => {
  await act(async () => { tree = TestRenderer.create(<Screen t={trade} />); });
  await flush();
  expect(state.pending).toBeNull();
  await addPending(stash());
  act(() => state.refresh());
  await flush();
  expect(state.pending?.txHash).toBe('0xa');
  expect(state.stale).toBe(false);
});

it('keeps a stale stash (still retryable) and flags it', async () => {
  await addPending(stash({ createdAt: Date.now() - PENDING_TRADE_TX_STALE_MS - 1000 }));
  await act(async () => { tree = TestRenderer.create(<Screen t={trade} />); });
  await flush();
  expect(state.pending?.txHash).toBe('0xa');
  expect(state.stale).toBe(true);
  expect(await listPendingForTrade('t1')).toHaveLength(1);
});

it('drops a stash the trade has moved past', async () => {
  await addPending(stash());
  await act(async () => { tree = TestRenderer.create(<Screen t={{ ...trade, status: 'paid' }} />); });
  await flush();
  expect(state.pending).toBeNull();
  expect(await listPendingForTrade('t1')).toEqual([]);
});
