import type { P2PTrade } from '@/services/p2p';
import type { PendingEscrowConfirm } from '@/services/p2pPending';
import {
  markPaidConfirmMessage,
  PENDING_TRADE_TX_STALE_MS,
  pendingTradeTxCopy,
  RELEASE_CONFIRM_TITLE,
  releaseConfirmMessage,
  splitPendingTradeTxs,
  tradeActions,
  tradeFeeLines,
  tradeTxFollowUp,
} from '../p2pTradeView';

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
      .toEqual({ trade, reload: true });
  });
  it('confirmed without a body: reloads to pick up the new status', () => {
    expect(tradeTxFollowUp({ status: 'confirmed', txHash: hash, prepared: {} }))
      .toEqual({ trade: undefined, reload: true });
  });
  it.each([
    ['no-tx', { status: 'no-tx', prepared: {} }],
    ['reverted', { status: 'reverted', txHash: hash, prepared: {} }],
    ['confirm-failed (mined)', { status: 'confirm-failed', txHash: hash, prepared: {}, message: 'x', mined: true }],
    ['confirm-failed (not mined)', { status: 'confirm-failed', txHash: hash, prepared: {}, message: 'x', mined: false }],
    ['refused', { status: 'refused', response: { success: false, message: 'no' } }],
  ] as const)('%s: reloads the server truth, keeps no stale trade', (_name, r) => {
    expect(tradeTxFollowUp(r)).toEqual({ trade: undefined, reload: true });
  });
  it('aborted: nothing changed — no reload', () => {
    expect(tradeTxFollowUp({ status: 'aborted' })).toEqual({ trade: undefined, reload: false });
  });
  it('busy: another screen runs the same action — nothing to do here', () => {
    expect(tradeTxFollowUp({ status: 'busy' })).toEqual({ trade: undefined, reload: false });
  });
});

const NOW = Date.parse('2026-10-09T12:00:00Z');
function t(over: Partial<P2PTrade> = {}): P2PTrade {
  return {
    id: 't', adId: 'a', buyerId: 'b', sellerId: 's', token: 'USDT', chainId: 146,
    tokenAmount: '100000000', fiatAmount: '150000', fiatCurrency: 'NGN', rate: '1500',
    paymentMethodId: 'pm', status: 'pending_payment', openTxHash: '0x' + '1'.repeat(64),
    expiresAt: '2026-10-09T12:30:00Z', createdAt: '', updatedAt: '', ...over,
  } as P2PTrade;
}
const payee = { provider: 'bank_transfer', bankName: 'GTBank', accountNumber: '0123456789', accountName: 'Seller' };

describe('tradeActions — direct trades', () => {
  it('payer sees the payee card and the mark-paid button while pending', () => {
    const a = tradeActions(t({ payee }), 'buyer', NOW);
    expect(a.payToFor).toBe('direct-payer');
    expect(a.canMarkPaid).toBe(true);
    expect(a.showProof).toBe(false);
  });
  it('payee sees the account to check, then the proof and Release once paid', () => {
    expect(tradeActions(t({ payee }), 'seller', NOW).payToFor).toBe('direct-payee');
    const paid = tradeActions(t({ payee, status: 'paid', proofImageUrl: 'https://img/p.jpg' }), 'seller', NOW);
    expect(paid.canRelease).toBe(true);
    expect(paid.showProof).toBe(true);
  });
  it('no payee from the API → no card (e.g. before the open is on chain)', () => {
    expect(tradeActions(t({ payee: null }), 'buyer', NOW).payToFor).toBeNull();
  });
  it('both parties see the proof while disputed', () => {
    const d = t({ status: 'disputed', proofImageUrl: 'https://img/p.jpg' });
    expect(tradeActions(d, 'buyer', NOW).showProof).toBe(true);
    expect(tradeActions(d, 'seller', NOW).showProof).toBe(true);
  });
  it('open not yet recorded (R4)', () => {
    expect(tradeActions(t({ openTxHash: '' }), 'buyer', NOW).openUnconfirmed).toBe(true);
    expect(tradeActions(t({ openTxHash: 'reconciled' }), 'buyer', NOW).openUnconfirmed).toBe(false);
  });
  it('cancel only after expiry', () => {
    expect(tradeActions(t(), 'buyer', NOW).canCancel).toBe(false);
    expect(tradeActions(t({ expiresAt: '2026-10-09T11:00:00Z' }), 'buyer', NOW).canCancel).toBe(true);
  });
});

describe('tradeActions — PM unchanged', () => {
  const pmPayTo = { bank: 'Nomba', accountNumber: '9', accountName: 'PM' };
  it('active PM: pay-to card for the buyer, no manual buttons, no direct payee card', () => {
    const a = tradeActions(t({ isPm: true, pmPayTo, payee }), 'buyer', NOW);
    expect(a).toMatchObject({ pmActive: true, showPmPayTo: true, canMarkPaid: false, canRelease: false, payToFor: null });
  });
  it('dormant PM falls back to the manual flow', () => {
    const a = tradeActions(t({ isPm: true, pmPayTo: null }), 'buyer', NOW);
    expect(a).toMatchObject({ pmDormant: true, canMarkPaid: true });
  });
});

describe('tradeFeeLines', () => {
  const fee = { feeBps: 50, feeAmount: '500000', buyerNetAmount: '99500000' };
  it('the crypto receiver (buyer) sees "You receive" with the fee', () => {
    expect(tradeFeeLines(t(fee), 'buyer', 6)).toEqual({
      feeLabel: 'Platform fee (0.5%)', feeValue: '0.5 USDT', receiveLabel: 'You receive', receiveValue: '99.5 USDT',
    });
  });
  it('the other party sees "They receive"', () => {
    expect(tradeFeeLines(t(fee), 'seller', 6)?.receiveLabel).toBe('They receive');
  });
  it('no fee data → no lines', () => {
    expect(tradeFeeLines(t(), 'buyer', 6)).toBeNull();
  });
});

describe('tradeActions — mark-paid needs the open on chain', () => {
  it('no mark-paid until the open is recorded ("reconciled" counts)', () => {
    expect(tradeActions(t({ openTxHash: '' }), 'buyer', NOW).canMarkPaid).toBe(false);
    expect(tradeActions(t({ openTxHash: 'reconciled' }), 'buyer', NOW).canMarkPaid).toBe(true);
  });
});

describe('tradeActions — a sent trade action still confirming', () => {
  const expired = { expiresAt: '2026-10-09T11:00:00Z' };
  it('hides the action buttons instead of re-offering them', () => {
    const a = tradeActions(t(expired), 'buyer', NOW, 'mark_paid');
    expect(a).toMatchObject({ confirmingTx: true, canMarkPaid: false, canCancel: false, canDispute: false });
    const r = tradeActions(t({ status: 'paid' }), 'seller', NOW, 'release');
    expect(r).toMatchObject({ confirmingTx: true, canRelease: false, canDispute: false });
    expect(tradeActions(t(expired), 'seller', NOW, 'cancel_trade').sellerAwaitingPayment).toBe(false);
  });
  it('a lost open is the R4 banner, not a confirming action', () => {
    const a = tradeActions(t({ openTxHash: '', ...expired }), 'buyer', NOW, 'open');
    expect(a).toMatchObject({ confirmingTx: false, openUnconfirmed: true, canCancel: true });
  });
  it('nothing stashed → unchanged', () => {
    expect(tradeActions(t(), 'buyer', NOW, null)).toEqual(tradeActions(t(), 'buyer', NOW));
  });
});

describe('splitPendingTradeTxs', () => {
  function stash(over: Partial<PendingEscrowConfirm>): PendingEscrowConfirm {
    return { mode: 'mark_paid', token: 'USDT', chainId: 146, amount: '1', txHash: '0xa', walletAddress: '0xW',
      tradeId: 't', createdAt: NOW - 60_000, ...over };
  }
  it('open: settled once the open is recorded — a real hash or "reconciled"', () => {
    const open = stash({ mode: 'open', createdAt: NOW - 24 * 3600_000 });
    expect(splitPendingTradeTxs([open], t({ openTxHash: '' }), NOW)).toEqual({ active: open, settled: [] });
    expect(splitPendingTradeTxs([open], t({ openTxHash: 'reconciled' }), NOW)).toEqual({ active: null, settled: [open] });
    expect(splitPendingTradeTxs([open], t({ openTxHash: '', status: 'cancelled' }), NOW).settled).toEqual([open]);
  });
  it('mark-paid and cancel: settled once the trade leaves pending_payment', () => {
    const mp = stash({ mode: 'mark_paid' });
    const c = stash({ mode: 'cancel_trade', txHash: '0xc' });
    expect(splitPendingTradeTxs([mp], t(), NOW).active).toBe(mp);
    expect(splitPendingTradeTxs([mp, c], t({ status: 'paid' }), NOW)).toEqual({ active: null, settled: [mp, c] });
    expect(splitPendingTradeTxs([c], t({ status: 'cancelled' }), NOW).settled).toEqual([c]);
  });
  it('release: unresolved while paid or releasing', () => {
    const rel = stash({ mode: 'release' });
    expect(splitPendingTradeTxs([rel], t({ status: 'paid' }), NOW).active).toBe(rel);
    expect(splitPendingTradeTxs([rel], t({ status: 'releasing' }), NOW).active).toBe(rel);
    expect(splitPendingTradeTxs([rel], t({ status: 'released' }), NOW).settled).toEqual([rel]);
  });
  it('a trade action that never moved the trade expires, so a dropped tx cannot hide the buttons forever', () => {
    const old = stash({ createdAt: NOW - PENDING_TRADE_TX_STALE_MS - 1 });
    expect(splitPendingTradeTxs([old], t(), NOW)).toEqual({ active: null, settled: [old] });
  });
  it('shows the newest unresolved one', () => {
    const a = stash({ txHash: '0x1', createdAt: NOW - 120_000 });
    const b = stash({ txHash: '0x2', createdAt: NOW - 30_000 });
    expect(splitPendingTradeTxs([a, b], t(), NOW).active).toBe(b);
  });
  it('ignores records for other trades or of other kinds', () => {
    const other = stash({ tradeId: 'x' });
    const dep = stash({ mode: 'deposit', tradeId: undefined });
    expect(splitPendingTradeTxs([other, dep], t(), NOW)).toEqual({ active: null, settled: [] });
  });
});

describe('pendingTradeTxCopy', () => {
  const hash = '0x' + 'ab'.repeat(32);
  it('names the short hash and never asks to send again', () => {
    const c = pendingTradeTxCopy({ mode: 'mark_paid', txHash: hash });
    expect(c.title).toBe('Confirming 0xabab…abab…');
    expect(c.body).toMatch(/don’t send it again/i);
    expect(c.label).toBe('Mark paid');
  });
  it('open: says it will be picked up — no promise of an automatic catch-up', () => {
    const c = pendingTradeTxCopy({ mode: 'open', txHash: hash });
    expect(c.title).toBe('Escrow still confirming');
    expect(c.body).toMatch(/picked up shortly/);
    expect(c.body).not.toMatch(/reconciler/);
  });
});

describe('markPaidConfirmMessage', () => {
  it('within the window: the usual warning only', () => {
    const m = markPaidConfirmMessage('USDT', false);
    expect(m).toMatch(/cannot be undone/);
    expect(m).not.toMatch(/payment window has ended/);
  });
  it('after the window: says it races the seller’s cancel', () => {
    expect(markPaidConfirmMessage('USDT', true)).toMatch(
      /The payment window has ended — marking paid now stops the seller cancelling, if it lands first\./,
    );
  });
});
