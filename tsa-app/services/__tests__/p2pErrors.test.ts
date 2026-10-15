jest.mock('expo-router', () => ({ router: { push: jest.fn() } }));
jest.mock('react-native', () => ({ Alert: { alert: jest.fn() } }));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { errorMessage, P2PCopyError, p2pCaughtMessage, p2pErrorMessage, p2pKycCode, p2pRefusalError } from '../p2pErrors';

describe('p2pKycCode', () => {
  it('passes the API code through', () => {
    expect(p2pKycCode({ status: 403, code: 'kyc_verification_required' })).toBe('kyc_verification_required');
    expect(p2pKycCode({ status: 403, code: 'kyc_legal_name_missing' })).toBe('kyc_legal_name_missing');
  });
  it('recognises the pre-code P2P gate message', () => {
    expect(p2pKycCode({ status: 403, message: 'Identity verification required for P2P' })).toBe('kyc_verification_required');
  });
  it('ignores other 403s and empty input', () => {
    expect(p2pKycCode({ status: 403, message: 'p2p: not authorized for this action' })).toBeUndefined();
    expect(p2pKycCode(undefined)).toBeUndefined();
  });
});

describe('p2pErrorMessage', () => {
  it.each([
    ['p2p: trade amount exceeds ad remaining', /can't cover that amount/],
    ["p2p: seller's available balance can no longer cover this trade", /can't cover that amount/],
    ['p2p: invalid state transition', /already moved on/],
    ['p2p: seller cannot open a trade on their own ad', /your own ad/],
    ['p2p: you already have an active trade on this ad', /already have a trade on this ad.*My trades/],
    ['p2p: ad is not active', /no longer available/],
    ['p2p: rate source is stale; cannot quote', /live rate is unavailable/],
    ['p2p: blockchain temporarily unavailable, please retry', /network is busy/],
    ['p2p: amount outside ad min/max limits', /outside this ad's limits/],
    ['p2p: trade already has an open dispute', /already open or being raised.*few minutes/],
    ['p2p: the trade could not be prepared on-chain: execution reverted: Bad voucher signature', /no longer available/],
    ['Network request failed', /Check your connection/],
  ])('%s', (raw, re) => {
    expect(p2pErrorMessage({ message: raw })).toMatch(re);
  });

  it('maps an undecodable revert to a nothing-was-sent sentence', () => {
    expect(p2pErrorMessage('execution reverted: 0xdeadbeef00')).toMatch(/nothing was sent/i);
  });
  it('maps the old mark-paid route to an update prompt', () => {
    expect(p2pErrorMessage({ status: 409, code: 'app_update_required', message: 'x' })).toMatch(/update the app/i);
  });
  it('maps 401 to a sign-in prompt', () => {
    expect(p2pErrorMessage({ status: 401, message: 'Please authenticate' })).toMatch(/sign in/i);
  });
  it('strips the p2p: prefix from short unmapped messages', () => {
    expect(p2pErrorMessage({ message: 'p2p: invalid parameters' })).toBe('Invalid parameters');
  });
  it('falls back instead of showing HTML or SQL', () => {
    expect(p2pErrorMessage('<html>502</html>', 'Try later')).toBe('Try later');
    expect(p2pErrorMessage('sql: no rows', 'Try later')).toBe('Try later');
  });
  it('reuses the broadcast mapping (gas)', () => {
    expect(p2pErrorMessage('insufficient funds for gas * price + value')).toBe('Not enough native token to pay gas on this chain.');
  });
});

describe('p2pErrorMessage: final-review copy', () => {
  it('points an active-trade refusal at My trades', () => {
    expect(p2pErrorMessage({ status: 409, message: 'p2p: you already have an active trade on this ad' }))
      .toBe('You already have a trade on this ad — find it in My trades.');
  });
  it('turns the approve wait timeout into "may still go through" copy', () => {
    const msg = p2pErrorMessage('Approve confirmation timed out');
    expect(msg).toMatch(/may still go through/);
    expect(msg).not.toMatch(/timed out/);
  });
});

describe('p2pErrorMessage: API codes', () => {
  it('maps KYC code to a verification sentence', () => {
    expect(p2pErrorMessage({ status: 403, code: 'kyc_verification_required', message: 'x' })).toMatch(/verify your identity/i);
  });
  it('shows the allowed band for rate_out_of_band', () => {
    expect(
      p2pErrorMessage({ status: 400, code: 'rate_out_of_band', message: 'x', data: { minRate: 1400, maxRate: 1600 } }),
    ).toBe('Rate must be between 1400 and 1600.');
    expect(p2pErrorMessage({ status: 400, code: 'rate_out_of_band', message: 'x' })).toMatch(/outside the allowed range/i);
  });
  it.each([
    ['gas_not_needed', /doesn't need gas/i],
    ['gas_topup_unused', /already sent/i],
    ['sponsorship_in_progress', /in progress/i],
    ['gas_unavailable', /gas top-up is unavailable/i],
    ['WALLET_PROOF_REQUIRED', /prove ownership/i],
  ])('gas/wallet code %s', (code, re) => {
    expect(p2pErrorMessage({ code, message: 'raw' })).toMatch(re);
  });
  it('maps the gas 429 to the daily cap', () => {
    expect(p2pErrorMessage({ status: 429, message: "you've used today's gas top-ups" })).toMatch(/daily limit/i);
  });
  it('maps any other 429 to a generic wait', () => {
    expect(p2pErrorMessage({ status: 429, message: 'Too many requests, please try again later.' })).toBe(
      'Too many requests. Wait a moment and try again.',
    );
  });
  it('treats waitMined and ETIMEDOUT as unconfirmed', () => {
    expect(p2pErrorMessage('approve confirmation timed out')).toMatch(/may still go through/i);
    expect(p2pErrorMessage('connect ETIMEDOUT')).toMatch(/may still go through/i);
  });
  it('does not call server timeouts or aborts unconfirmed', () => {
    expect(p2pErrorMessage({ status: 504, message: 'upstream timeout' })).not.toMatch(/may still go through/i);
    expect(p2pErrorMessage({ message: 'p2p: trade aborted' })).not.toMatch(/may still go through/i);
  });
  it('never says a timeout failed', () => {
    const msg = p2pErrorMessage('Request timed out');
    expect(msg).toMatch(/may still go through/i);
    expect(msg).not.toMatch(/fail/i);
  });
});

describe('wrong active wallet for this trade', () => {
  const copy = 'This trade belongs to a different wallet. Switch to the wallet you used for this trade and try again.';
  // The prepare 422 exactly as the API sends it since API-19: ErrTradePrepareReverted
  // wrapping buildUnsignedTx's error, with withRevertReason appending the
  // decoded reason (V3 custom error "Name()", or a V1/V2 Error(string)) to the
  // node's bare "execution reverted".
  const prepared = (reason: string) =>
    'p2p: the trade could not be prepared on-chain: estimate gas (would revert): ' +
    `failed to estimate gas: execution reverted: ${reason}`;
  it.each(['OnlyBuyer()', 'OnlySeller()', 'NotAParty()', 'Only buyer', 'Only seller'])('422 %s', (reason) => {
    expect(p2pErrorMessage({ status: 422, message: prepared(reason) })).toBe(copy);
  });
  it('leaves other reverts alone', () => {
    expect(p2pErrorMessage({ status: 422, message: prepared('NotPending()') })).toMatch(/would reject this transaction/);
  });
});

describe('p2pCaughtMessage', () => {
  it('shows app copy as-is, even past the raw-message length guard', () => {
    const copy = 'A long, specific sentence the app wrote. '.repeat(8);
    expect(p2pCaughtMessage(new P2PCopyError(copy))).toBe(copy);
  });
  it('maps anything else through p2pErrorMessage', () => {
    expect(p2pCaughtMessage(new Error('p2p: invalid state transition'))).toMatch(/already moved on/);
    expect(p2pCaughtMessage('<html>oops</html>', 'Fallback.')).toBe('Fallback.');
  });
  it('keeps the code an error carries', () => {
    expect(p2pCaughtMessage(Object.assign(new Error('x'), { code: 'gas_unavailable' }))).toMatch(/gas top-up is unavailable/);
  });
});

describe('p2pRefusalError', () => {
  it('throws the copy as-is and keeps the code, status and data a screen routes on', () => {
    const e = p2pRefusalError({ status: 400, code: 'rate_out_of_band', message: 'raw', data: { minRate: '1' } }, 'Copy.');
    expect(e).toBeInstanceOf(P2PCopyError);
    expect(e).toMatchObject({ message: 'Copy.', code: 'rate_out_of_band', status: 400, data: { minRate: '1' } });
    expect(p2pCaughtMessage(e)).toBe('Copy.');
  });
  it('normalises an older code-less KYC 403 to the KYC code', () => {
    expect(p2pRefusalError({ status: 403, message: 'Identity verification required' }, 'x'))
      .toMatchObject({ code: 'kyc_verification_required', status: 403 });
  });
});

describe('errorMessage', () => {
  it('reads an Error, a refusal or anything else', () => {
    expect(errorMessage(new Error('boom'))).toBe('boom');
    expect(errorMessage({ message: 'm' })).toBe('m');
    expect(errorMessage('s')).toBe('s');
  });
});

describe('p2p_direct_moved (Sonic closed to direct P2P)', () => {
  it('maps the 409 code to the BNB Chain copy', () => {
    expect(p2pErrorMessage({ status: 409, code: 'p2p_direct_moved', message: 'p2p: direct p2p has moved to bsc' })).toBe(
      'Direct P2P has moved to BNB Chain. Choose BNB Chain as the network and try again.',
    );
  });
  it('maps the message when the code is missing', () => {
    expect(p2pErrorMessage({ status: 409, message: 'Direct P2P has moved to BNB Chain' })).toMatch(/^Direct P2P has moved to BNB Chain\./);
  });
});

describe('direct BNB Chain PM refusal', () => {
  it('maps p2p_pm_not_on_direct to the stay-on-Sonic copy', () => {
    const msg = p2pErrorMessage({ status: 409, code: 'p2p_pm_not_on_direct', message: 'p2p: payment merchant ads are not available on this chain' });
    expect(msg).toBe('Payment Merchant ads stay on Sonic. Choose Sonic as the network and try again.');
  });
});
