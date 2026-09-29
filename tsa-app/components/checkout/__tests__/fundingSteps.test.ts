/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn(async () => null),
  setItem: jest.fn(async () => {}),
  removeItem: jest.fn(async () => {}),
}));

import { fundingSteps } from '../fundingSteps';
import type { FundingRecord, FundingRunState } from '@/services/checkoutFundingRun';

const rec = (over: Partial<FundingRecord> = {}): FundingRecord => ({
  v: 1, groupId: 'g', orders: [{ id: 'o1' }, { id: 'o2' }] as any, token: { symbol: 'BNB' } as any,
  chainId: 56, wallet: '0x', startedAt: 0, paid: [], progress: {}, etaSeconds: 1080, ...over,
});
const opts = { sourceSymbol: 'BNB', orderToken: 'USDC', orderNames: ['Shoes', 'Bag'] };
const states = (s: FundingRunState) => fundingSteps(s, opts).map((st) => `${st.key}:${st.state}`);

test('bridging: the swap is done, the funds are on their way with the route ETA', () => {
  const steps = fundingSteps({ stage: 'bridging', record: rec({ intentId: 'i', swapHash: '0xh' }) }, opts);
  expect(steps.map((s) => `${s.key}:${s.state}`)).toEqual(['orders:done', 'swap:done', 'bridge:active', 'pay-0:pending', 'pay-1:pending']);
  expect(steps[2].detail).toMatch(/about 18 minutes/);
});

test('paying: each order in turn, a paid one done', () => {
  expect(states({ stage: 'paying', orderIndex: 1, record: rec({ delivered: true, paid: ['o1'] }) }))
    .toEqual(['orders:done', 'swap:done', 'bridge:done', 'pay-0:done', 'pay-1:active']);
});

test('the Sonic gas step shows only when it was needed', () => {
  expect(states({ stage: 'sonic_gas', record: rec({ delivered: true }) })).toContain('gas:active');
  expect(states({ stage: 'paying', orderIndex: 0, record: rec({ delivered: true }) }).some((s) => s.startsWith('gas'))).toBe(false);
});

test('a failure marks the step it failed at', () => {
  expect(states({ stage: 'failed', outcome: 'retry', record: rec() })).toEqual(
    ['orders:done', 'swap:failed', 'bridge:pending', 'pay-0:pending', 'pay-1:pending'],
  );
  expect(states({ stage: 'failed', outcome: 'refunded', record: rec({ intentId: 'i', swapHash: '0xh' }) })[2]).toBe('bridge:failed');
});

test('done: every step done', () => {
  expect(states({ stage: 'done', record: rec({ delivered: true, paid: ['o1', 'o2'] }) }).every((s) => s.endsWith(':done'))).toBe(true);
});
