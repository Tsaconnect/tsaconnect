import * as p2p from '@/services/p2p';
import type { P2PTrade } from '@/services/p2p';
import {
  ACTIVE_TRADE_NO_LINK,
  ACTIVE_TRADE_TITLE,
  ACTIVE_TRADE_WITH_LINK,
  allowanceReadFollowUp,
  approveFollowUp,
  buyAdFeeNote,
  findMyActiveTradeOnAd,
  openFollowUp,
  openThrownFollowUp,
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

const OPEN_FAILED = 'Could not open trade';
const trade = { id: 't1', tokenAmount: '1000000' } as P2PTrade;
const prepared: OpenPrep = { trade, unsignedTx: null };
const hash = '0xabc';

describe('openFollowUp (every runP2PTx outcome of an open)', () => {
  const ad = { id: 'ad1', side: 'sell' as const };
  const open = (r: Parameters<typeof openFollowUp>[0]) => openFollowUp(r, ad);

  it('confirmed: lands on the trade', async () => {
    expect(await open({ status: 'confirmed', txHash: hash, prepared, data: trade }))
      .toEqual({ navigate: 't1', reload: false });
  });

  it('confirm-failed: still lands on the trade (R4 banner there) with a notice, mined or not', async () => {
    for (const mined of [true, false]) {
      expect(await open({ status: 'confirm-failed', txHash: hash, prepared, message: 'x', mined })).toEqual({
        alert: undefined,
        toast: { type: 'info', message: 'Trade opened — still confirming the escrow.' },
        navigate: 't1',
        reload: false,
      });
    }
  });

  it('reverted: nothing was locked, the kept trade expires on its own, offers View trade — no "try again"', async () => {
    const f = await open({ status: 'reverted', txHash: hash, prepared });
    expect(f).toMatchObject({ alert: { title: OPEN_FAILED, viewTradeId: 't1' }, reload: true });
    const msg = f.alert?.message ?? '';
    expect(msg).toMatch(/nothing was locked/);
    expect(msg).toMatch(/This trade will expire and cancel on its own; then you can open a new one\./);
    expect(msg).not.toMatch(/try again/i);
  });

  it('refused because a trade on this ad is still live: links it ("Close" / "View trade")', async () => {
    jest.spyOn(p2p.tradesApi, 'list').mockResolvedValue({
      success: true, data: [{ id: 'live', adId: 'ad1', status: 'paid' } as P2PTrade],
    });
    expect(await open({
      status: 'refused',
      response: { success: false, status: 409, message: 'p2p: you already have an active trade on this ad' },
    })).toEqual({
      alert: { title: ACTIVE_TRADE_TITLE, message: ACTIVE_TRADE_WITH_LINK, viewTradeId: 'live', dismissText: 'Close' },
      reload: false,
    });
  });

  it('refused because a trade is live but the lookup finds nothing: points at My trades', async () => {
    jest.spyOn(p2p.tradesApi, 'list').mockResolvedValue({ success: true, data: [] });
    expect(await open({
      status: 'refused',
      response: { success: false, status: 409, message: 'p2p: you already have an active trade on this ad' },
    })).toEqual({ alert: { title: ACTIVE_TRADE_TITLE, message: ACTIVE_TRADE_NO_LINK }, reload: false });
  });

  it('aborted and busy: nothing to do (the gas alert or the first run covers it)', async () => {
    expect(await open({ status: 'aborted' })).toEqual({ reload: false });
    expect(await open({ status: 'busy' })).toEqual({ reload: false });
  });

  it('no-tx: nothing was sent', async () => {
    expect(await open({ status: 'no-tx', prepared })).toEqual({
      alert: { title: OPEN_FAILED, message: 'The server did not return a transaction. Nothing was sent — please try again.' },
      reload: false,
    });
  });

  it('refused 503 stale rate: the live-rate copy and a reload', async () => {
    const f = await open({
      status: 'refused',
      response: { success: false, status: 503, message: 'p2p: rate source is stale; cannot quote' },
    });
    expect(f).toMatchObject({ alert: { title: 'Live rate unavailable' }, reload: true });
  });

  it('refused 503 for another reason (RPC down, pool closed): its own copy, still a reload', async () => {
    expect(await open({
      status: 'refused',
      response: { success: false, status: 503, message: 'p2p: blockchain temporarily unavailable, please retry' },
    })).toEqual({ alert: { title: OPEN_FAILED, message: 'The network is busy right now. Try again in a minute.' }, reload: true });
    const closed = await open({
      status: 'refused',
      response: { success: false, status: 503, code: 'liquidity_pool_closed', message: 'Liquidity pool is closed' },
    });
    expect(closed).toMatchObject({ alert: { title: OPEN_FAILED }, reload: true });
    expect(closed.alert?.message).not.toMatch(/rate/i);
  });

  it('refused with a KYC code: routes to the KYC prompt (buy-ad openers too)', async () => {
    expect(await open({
      status: 'refused',
      response: { success: false, status: 403, code: 'kyc_verification_required', message: 'verify' },
    })).toEqual({ kyc: { code: 'kyc_verification_required', message: 'verify' }, reload: false });
  });

  it('refused with an older code-less KYC 403: still the KYC prompt', async () => {
    expect(await open({
      status: 'refused',
      response: { success: false, status: 403, message: 'p2p: identity verification required' },
    })).toMatchObject({ kyc: { code: 'kyc_verification_required' } });
  });

  it('refused 409 ad not active (suspended seller / closed): "no longer available" and a reload', async () => {
    expect(await open({
      status: 'refused',
      response: { success: false, status: 409, message: 'p2p: ad is not active' },
    })).toEqual({ alert: { title: OPEN_FAILED, message: 'This ad is no longer available.' }, reload: true });
  });

  it('refused otherwise: friendly copy, no reload', async () => {
    expect(await open({
      status: 'refused',
      response: { success: false, status: 400, message: 'p2p: amount outside ad min/max' },
    })).toEqual({ alert: { title: OPEN_FAILED, message: "That amount is outside this ad's limits." }, reload: false });
  });
});

describe('approveFollowUp', () => {
  const prep = { unsignedTx: null };

  it('confirmed: go on to the open', () => {
    expect(approveFollowUp({ status: 'confirmed', txHash: hash, prepared: prep })).toEqual({ proceed: true, reload: false });
  });

  it('aborted and busy: stop quietly', () => {
    expect(approveFollowUp({ status: 'aborted' })).toEqual({ proceed: false, reload: false });
    expect(approveFollowUp({ status: 'busy' })).toEqual({ proceed: false, reload: false });
  });

  it('reverted: approval failed, nothing changed', () => {
    const f = approveFollowUp({ status: 'reverted', txHash: hash, prepared: prep });
    expect(f).toMatchObject({ proceed: false, reload: false });
    expect(f.alert?.title).toBe('Approval failed');
    expect(f.alert?.message).toMatch(/nothing changed/);
  });

  it('confirm-failed: sent but not confirmed — wait and tap again, never "failed"', () => {
    const f = approveFollowUp({ status: 'confirm-failed', txHash: hash, prepared: prep, message: 'x', mined: false });
    expect(f.proceed).toBe(false);
    expect(f.alert?.title).toBe('Approval still confirming');
    expect(f.alert?.message).not.toMatch(/failed/i);
    expect(f.alert?.message).toMatch(/may not need to approve again/);
  });

  it('refused with a KYC code (approve, allowance or gas 403): the KYC prompt', () => {
    expect(approveFollowUp({
      status: 'refused',
      response: { success: false, status: 403, code: 'kyc_verification_required', message: 'verify' },
    })).toEqual({ proceed: false, kyc: { code: 'kyc_verification_required', message: 'verify' }, reload: false });
  });

  it('refused: friendly copy', () => {
    const f = approveFollowUp({ status: 'refused', response: { success: false, message: 'Network request failed' } });
    expect(f).toEqual({
      proceed: false,
      alert: { title: 'Approval failed', message: "Can't reach TSA Connect. Check your connection and try again." },
      reload: false,
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

describe('allowanceReadFollowUp (a failed read never starts an approve)', () => {
  it('a good read: proceed with the allowance', () => {
    expect(allowanceReadFollowUp({ success: true, data: { allowance: '7' } })).toEqual({ proceed: true, allowance: '7', reload: false });
  });
  it('a network error: stop with the mapped copy', () => {
    expect(allowanceReadFollowUp({ success: false, message: 'Network request failed' })).toEqual({
      proceed: false,
      alert: { title: OPEN_FAILED, message: "Can't reach TSA Connect. Check your connection and try again." },
      reload: false,
    });
  });
  it('a success without a body: stop, not "0"', () => {
    expect(allowanceReadFollowUp({ success: true }).proceed).toBe(false);
  });
  it('a KYC 403: the KYC prompt', () => {
    expect(allowanceReadFollowUp({ success: false, status: 403, code: 'kyc_verification_required', message: 'v' }))
      .toEqual({ proceed: false, kyc: { code: 'kyc_verification_required', message: 'v' }, reload: false });
  });
});

describe('openThrownFollowUp (sign/broadcast threw — nothing reached the chain)', () => {
  it('before any prepare: just the reason, no trade link', () => {
    expect(openThrownFollowUp(new Error('No wallet found. Please set up a wallet first.'))).toEqual({
      alert: { title: OPEN_FAILED, message: 'No wallet found. Please set up a wallet first.' }, reload: false,
    });
  });
  it('never shows a raw RPC blob', () => {
    expect(openThrownFollowUp(new Error('<html>502</html>')).alert?.message).toBe('Something went wrong. Please try again.');
  });
  it('after a successful prepare: the kept trade expires on its own, with View trade', () => {
    const f = openThrownFollowUp(new Error('User rejected the request'), 't9');
    expect(f).toMatchObject({ alert: { title: OPEN_FAILED, viewTradeId: 't9' }, reload: true });
    expect(f.alert?.message).toMatch(
      /Nothing was locked\. This trade will expire and cancel on its own; then you can open a new one\./,
    );
  });
});

describe('findMyActiveTradeOnAd', () => {
  const row = (id: string, adId: string, status: P2PTrade['status']) => ({ id, adId, status }) as P2PTrade;
  it('asks as the opener (buyer on a sell-ad, seller on a buy-ad) and returns the live trade on this ad', async () => {
    const list = jest.spyOn(p2p.tradesApi, 'list').mockResolvedValue({
      success: true,
      data: [row('old', 'ad1', 'cancelled'), row('other', 'ad2', 'pending_payment'), row('live', 'ad1', 'paid')],
    });
    expect(await findMyActiveTradeOnAd({ id: 'ad1', side: 'sell' })).toBe('live');
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ role: 'buyer' }));
    await findMyActiveTradeOnAd({ id: 'ad1', side: 'buy' });
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ role: 'seller' }));
  });
  it('null when none is live or the lookup fails (never throws)', async () => {
    jest.spyOn(p2p.tradesApi, 'list').mockResolvedValueOnce({ success: true, data: [row('x', 'ad1', 'released')] });
    expect(await findMyActiveTradeOnAd({ id: 'ad1', side: 'sell' })).toBeNull();
    jest.spyOn(p2p.tradesApi, 'list').mockRejectedValueOnce(new Error('offline'));
    expect(await findMyActiveTradeOnAd({ id: 'ad1', side: 'sell' })).toBeNull();
  });
});

describe('sellAdReceivePreview (fee comes out of the crypto the buyer receives)', () => {
  it('known rate: net amount and the fee line', () => {
    // 100 USDT gross at 50 bps → 0.5 fee, 99.5 net.
    expect(sellAdReceivePreview('100.0000', 6, 50, 'USDT')).toEqual({
      amount: '99.5',
      note: 'After the 0.5% fee (0.5 USDT).',
    });
  });

  it('uses the chain decimals (BSC 18)', () => {
    expect(sellAdReceivePreview('10', 18, 100, 'USDT')).toEqual({
      amount: '9.9',
      note: 'After the 1% fee (0.1 USDT).',
    });
  });

  it('zero rate: gross amount, no fee', () => {
    expect(sellAdReceivePreview('10', 6, 0, 'USDT')).toEqual({ amount: '10', note: 'No fee on this ad.' });
  });

  it('unknown rate: gross amount and the neutral note — never a guessed number', () => {
    const neutral = 'A fee is deducted from the crypto you receive; the exact amount shows on the trade.';
    expect(sellAdReceivePreview('12.3456', 6, undefined, 'USDT')).toEqual({ amount: '12.3456', note: neutral });
    expect(sellAdReceivePreview('12.3456', 6, 1.5, 'USDT')).toEqual({ amount: '12.3456', note: neutral });
  });

  it('unparseable preview: falls back to the gross text', () => {
    expect(sellAdReceivePreview('abc', 6, 50, 'USDT').amount).toBe('abc');
  });
});

describe('buyAdFeeNote (the ad poster receives the crypto and pays the fee)', () => {
  it('names the rate when known', () => {
    expect(buyAdFeeNote(75)).toBe('The buyer pays the 0.75% fee out of the crypto they receive.');
  });
  it('neutral when unknown', () => {
    expect(buyAdFeeNote(undefined)).toBe('The buyer pays the fee out of the crypto they receive.');
  });
});
