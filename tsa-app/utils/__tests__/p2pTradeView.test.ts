import type { P2PTrade } from '@/services/p2p';
import { RELEASE_CONFIRM_TITLE, releaseConfirmMessage, tradeActions, tradeFeeLines, tradeTxFollowUp } from '../p2pTradeView';

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
