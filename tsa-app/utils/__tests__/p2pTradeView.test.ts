import type { P2PTrade } from '@/services/p2p';
import { RELEASE_CONFIRM_TITLE, releaseConfirmMessage, tradeTxFollowUp } from '../p2pTradeView';

// babel-jest hoists this above the imports.
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));

describe('release confirm (R10)', () => {
  it('asks the owner’s question', () => {
    expect(RELEASE_CONFIRM_TITLE).toBe('Have you checked the money is in your account?');
  });
  it('names the fiat to look for and what is released', () => {
    const m = releaseConfirmMessage({ fiatAmount: '150000', fiatCurrency: 'NGN', tokenAmount: '100000000', token: 'USDT' }, 6);
    expect(m).toContain('150,000 NGN');
    expect(m).toContain('100 USDT');
    expect(m).toMatch(/can't be undone/);
  });
});

describe('tradeTxFollowUp (every runP2PTx outcome)', () => {
  const trade = { id: 't1', status: 'paid' } as P2PTrade;
  const hash = '0xabc';

  it('confirmed: shows the trade the server recorded and reloads', () => {
    expect(tradeTxFollowUp({ status: 'confirmed', txHash: hash, prepared: {}, data: trade }))
      .toEqual({ trade, reload: true, keepActing: false });
  });
  it('confirmed without a body: reloads to pick up the new status', () => {
    expect(tradeTxFollowUp({ status: 'confirmed', txHash: hash, prepared: {} }))
      .toEqual({ trade: undefined, reload: true, keepActing: false });
  });
  it.each([
    ['no-tx', { status: 'no-tx', prepared: {} }],
    ['reverted', { status: 'reverted', txHash: hash, prepared: {} }],
    ['confirm-failed (mined)', { status: 'confirm-failed', txHash: hash, prepared: {}, message: 'x', mined: true }],
    ['confirm-failed (not mined)', { status: 'confirm-failed', txHash: hash, prepared: {}, message: 'x', mined: false }],
    ['refused', { status: 'refused', response: { success: false, message: 'no' } }],
  ] as const)('%s: reloads the server truth, keeps no stale trade', (_name, r) => {
    expect(tradeTxFollowUp(r)).toEqual({ trade: undefined, reload: true, keepActing: false });
  });
  it('aborted: nothing changed — no reload', () => {
    expect(tradeTxFollowUp({ status: 'aborted' })).toEqual({ trade: undefined, reload: false, keepActing: false });
  });
  it('busy: another run owns the spinner — leave it alone', () => {
    expect(tradeTxFollowUp({ status: 'busy' })).toEqual({ trade: undefined, reload: false, keepActing: true });
  });
});
