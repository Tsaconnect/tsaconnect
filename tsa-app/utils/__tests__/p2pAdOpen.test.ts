import * as p2p from '@/services/p2p';
import type { P2PTrade } from '@/services/p2p';
import {
  approveFollowUp,
  buyAdFeeNote,
  OPEN_FAILED,
  openFollowUp,
  sellAdReceivePreview,
  waitForAllowance,
  type OpenPrep,
} from '../p2pAdOpen';

// babel-jest hoists this above the imports.
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));

afterEach(() => {
  jest.restoreAllMocks();
  jest.useRealTimers();
});

const trade = { id: 't1', tokenAmount: '1000000' } as P2PTrade;
const prepared: OpenPrep = { trade, unsignedTx: null };
const hash = '0xabc';

describe('openFollowUp (every runP2PTx outcome of an open)', () => {
  it('confirmed: lands on the trade', () => {
    expect(openFollowUp({ status: 'confirmed', txHash: hash, prepared, data: trade }))
      .toEqual({ kind: 'trade', tradeId: 't1' });
  });

  it('confirm-failed: still lands on the trade (R4 banner there) with a notice, mined or not', () => {
    for (const mined of [true, false]) {
      expect(openFollowUp({ status: 'confirm-failed', txHash: hash, prepared, message: 'x', mined }))
        .toEqual({ kind: 'trade', tradeId: 't1', notice: 'Trade opened — still confirming the escrow.' });
    }
  });

  it('reverted: stays on the ad, says nothing was locked, reloads', () => {
    const f = openFollowUp({ status: 'reverted', txHash: hash, prepared });
    expect(f).toMatchObject({ kind: 'alert', title: OPEN_FAILED, reload: true });
    expect(f.kind === 'alert' && f.message).toMatch(/nothing was locked/);
  });

  it('aborted and busy: nothing to do (the gas alert or the first run covers it)', () => {
    expect(openFollowUp({ status: 'aborted' })).toEqual({ kind: 'none' });
    expect(openFollowUp({ status: 'busy' })).toEqual({ kind: 'none' });
  });

  it('no-tx: nothing was sent', () => {
    expect(openFollowUp({ status: 'no-tx', prepared })).toEqual({
      kind: 'alert',
      title: OPEN_FAILED,
      message: 'The server did not return a transaction. Nothing was sent — please try again.',
      reload: false,
    });
  });

  it('refused 503: the live-rate copy and a reload', () => {
    const f = openFollowUp({ status: 'refused', response: { success: false, status: 503, message: 'rate source is stale' } });
    expect(f).toMatchObject({ kind: 'alert', title: 'Live rate unavailable', reload: true });
  });

  it('refused with a KYC code: routes to the KYC prompt (buy-ad openers too)', () => {
    expect(openFollowUp({
      status: 'refused',
      response: { success: false, status: 403, code: 'kyc_verification_required', message: 'verify' },
    })).toEqual({ kind: 'kyc', code: 'kyc_verification_required', message: 'verify' });
  });

  it('refused with an older code-less KYC 403: still the KYC prompt', () => {
    expect(openFollowUp({
      status: 'refused',
      response: { success: false, status: 403, message: 'p2p: identity verification required' },
    })).toMatchObject({ kind: 'kyc', code: 'kyc_verification_required' });
  });

  it('refused 409 ad not active (suspended seller / closed): "no longer available" and a reload', () => {
    expect(openFollowUp({
      status: 'refused',
      response: { success: false, status: 409, message: 'p2p: ad is not active' },
    })).toEqual({ kind: 'alert', title: OPEN_FAILED, message: 'This ad is no longer available.', reload: true });
  });

  it('refused otherwise: friendly copy, no reload', () => {
    expect(openFollowUp({
      status: 'refused',
      response: { success: false, status: 400, message: 'p2p: amount outside ad min/max' },
    })).toEqual({ kind: 'alert', title: OPEN_FAILED, message: "That amount is outside this ad's limits.", reload: false });
  });
});

describe('approveFollowUp', () => {
  const prep = { unsignedTx: null };

  it('confirmed: go on to the open', () => {
    expect(approveFollowUp({ status: 'confirmed', txHash: hash, prepared: prep })).toEqual({ proceed: true });
  });

  it('aborted and busy: stop quietly', () => {
    expect(approveFollowUp({ status: 'aborted' })).toEqual({ proceed: false });
    expect(approveFollowUp({ status: 'busy' })).toEqual({ proceed: false });
  });

  it('reverted: approval failed, nothing changed', () => {
    const f = approveFollowUp({ status: 'reverted', txHash: hash, prepared: prep });
    expect(f.proceed).toBe(false);
    expect(f.alert?.title).toBe('Approval failed');
    expect(f.alert?.message).toMatch(/nothing changed/);
  });

  it('confirm-failed: sent but not confirmed — wait and tap again, never "failed"', () => {
    const f = approveFollowUp({ status: 'confirm-failed', txHash: hash, prepared: prep, message: 'x', mined: false });
    expect(f.proceed).toBe(false);
    expect(f.alert?.title).toBe('Approval still confirming');
    expect(f.alert?.message).not.toMatch(/failed/i);
  });

  it('refused: friendly copy', () => {
    const f = approveFollowUp({ status: 'refused', response: { success: false, message: 'Network request failed' } });
    expect(f).toEqual({
      proceed: false,
      alert: { title: 'Approval failed', message: "Can't reach TSA Connect. Check your connection and try again." },
    });
  });

  it('no-tx: nothing was sent', () => {
    const f = approveFollowUp({ status: 'no-tx', prepared: prep });
    expect(f.proceed).toBe(false);
    expect(f.alert?.message).toMatch(/Nothing was sent/);
  });
});

describe('waitForAllowance (the approve "confirm")', () => {
  it('succeeds at once when the allowance already covers it', async () => {
    const spy = jest.spyOn(p2p.poolApi, 'allowance').mockResolvedValue({ success: true, data: { allowance: '5' } });
    await expect(waitForAllowance('USDT', '0xw', 56, '5')).resolves.toEqual({ success: true });
    expect(spy).toHaveBeenCalledWith('USDT', '0xw', 56);
  });

  it('polls until the allowance lands', async () => {
    jest.useFakeTimers();
    const spy = jest.spyOn(p2p.poolApi, 'allowance')
      .mockResolvedValueOnce({ success: true, data: { allowance: '0' } })
      .mockResolvedValueOnce({ success: false, message: 'rpc' })
      .mockResolvedValue({ success: true, data: { allowance: '10' } });
    const p = waitForAllowance('USDT', '0xw', 146, '5', { tries: 5, intervalMs: 1000 });
    await jest.advanceTimersByTimeAsync(2000);
    await expect(p).resolves.toEqual({ success: true });
    expect(spy).toHaveBeenCalledTimes(3);
  });

  it('gives up after the tries without throwing', async () => {
    jest.useFakeTimers();
    jest.spyOn(p2p.poolApi, 'allowance').mockResolvedValue({ success: true, data: { allowance: '1' } });
    const p = waitForAllowance('USDT', '0xw', 146, '5', { tries: 3, intervalMs: 1000 });
    await jest.advanceTimersByTimeAsync(5000);
    const r = await p;
    expect(r.success).toBe(false);
    expect(r.message).toMatch(/not confirmed yet/);
  });
});

describe('sellAdReceivePreview (fee comes out of the crypto the buyer receives)', () => {
  it('known rate: net amount and the fee line', () => {
    // 100 USDT gross at 50 bps → 0.5 fee, 99.5 net.
    expect(sellAdReceivePreview('100.0000', 6, 50, 'USDT')).toEqual({
      amount: '99.5',
      note: 'After the 0.5% platform fee (0.5 USDT).',
    });
  });

  it('uses the chain decimals (BSC 18)', () => {
    expect(sellAdReceivePreview('10', 18, 100, 'USDT')).toEqual({
      amount: '9.9',
      note: 'After the 1% platform fee (0.1 USDT).',
    });
  });

  it('zero rate: gross amount, no fee', () => {
    expect(sellAdReceivePreview('10', 6, 0, 'USDT')).toEqual({ amount: '10', note: 'No platform fee on this ad.' });
  });

  it('unknown rate: gross amount and the neutral note — never a guessed number', () => {
    const neutral = 'A platform fee is deducted from the crypto you receive; the exact amount shows on the trade.';
    expect(sellAdReceivePreview('12.3456', 6, undefined, 'USDT')).toEqual({ amount: '12.3456', note: neutral });
    expect(sellAdReceivePreview('12.3456', 6, 1.5, 'USDT')).toEqual({ amount: '12.3456', note: neutral });
  });

  it('unparseable preview: falls back to the gross text', () => {
    expect(sellAdReceivePreview('abc', 6, 50, 'USDT').amount).toBe('abc');
  });
});

describe('buyAdFeeNote (the ad poster receives the crypto and pays the fee)', () => {
  it('names the rate when known', () => {
    expect(buyAdFeeNote(75)).toBe('The buyer pays the 0.75% platform fee out of the crypto they receive.');
  });
  it('neutral when unknown', () => {
    expect(buyAdFeeNote(undefined)).toBe('The buyer pays the platform fee out of the crypto they receive.');
  });
});
