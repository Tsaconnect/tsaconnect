import { feeBpsFor, feeSplit, formatFeePct, tradeFee } from '../p2pFee';
import type { P2PChainInfo } from '../p2p';

describe('feeSplit (floor, as P2PEscrowV3 computes it)', () => {
  it('splits 100 USDT (6dp) at 50 bps', () => {
    expect(feeSplit('100000000', 50)).toEqual({ fee: '500000', net: '99500000' });
  });
  it('floors fractional base units', () => {
    expect(feeSplit('199', 50)).toEqual({ fee: '0', net: '199' });
    expect(feeSplit('333', 100)).toEqual({ fee: '3', net: '330' });
  });
  it('handles 18-decimal amounts beyond Number precision', () => {
    expect(feeSplit('123456789012345678901234', 25)).toEqual({ fee: '308641972530864197253', net: '123148147039814814703981' });
  });
  it('returns null when the rate is unknown or invalid — never a guessed fee', () => {
    expect(feeSplit('100', undefined)).toBeNull();
    expect(feeSplit('100', null)).toBeNull();
    expect(feeSplit('100', -1)).toBeNull();
    expect(feeSplit('100', 1.5)).toBeNull();
    expect(feeSplit('abc', 50)).toBeNull();
  });
  it('zero fee is a real rate', () => {
    expect(feeSplit('100', 0)).toEqual({ fee: '0', net: '100' });
  });
});

describe('formatFeePct', () => {
  it.each([[100, '1%'], [50, '0.5%'], [125, '1.25%'], [0, '0%']])('%d bps → %s', (bps, s) => {
    expect(formatFeePct(bps)).toBe(s);
  });
  it('null when unknown', () => expect(formatFeePct(undefined)).toBeNull());
});

describe('feeBpsFor', () => {
  const chains = [
    { chainId: 146, assets: ['USDT', 'USDC'], feeBps: { USDT: 50, USDC: 75 } },
    { chainId: 56, assets: ['USDT'], feeBps: { USDT: 100 } },
  ] as P2PChainInfo[];
  it('reads the chosen chain', () => expect(feeBpsFor(chains, 56, 'usdt')).toBe(100));
  it('falls back to the first chain offering the token', () => expect(feeBpsFor(chains, undefined, 'USDC')).toBe(75));
  it('null when the API sent none', () => expect(feeBpsFor([{ chainId: 146, assets: ['USDT'] }] as P2PChainInfo[], 146, 'USDT')).toBeNull());
});

describe('tradeFee', () => {
  it('prefers the API’s own figures', () => {
    expect(tradeFee({ tokenAmount: '1000', feeBps: 50, feeAmount: '7', buyerNetAmount: '993' })).toEqual({ fee: '7', net: '993' });
  });
  it('ignores malformed server figures and derives from bps', () => {
    expect(tradeFee({ tokenAmount: '1000', feeBps: 50, feeAmount: '-7', buyerNetAmount: '993' })).toEqual({ fee: '5', net: '995' });
    expect(tradeFee({ tokenAmount: '1000', feeBps: 50, feeAmount: '0.5', buyerNetAmount: 'abc' })).toEqual({ fee: '5', net: '995' });
  });
  it('ignores figures that do not add up to the gross', () => {
    expect(tradeFee({ tokenAmount: '1000', feeBps: 50, feeAmount: '7', buyerNetAmount: '900' })).toEqual({ fee: '5', net: '995' });
  });
  it('derives from the snapshot bps otherwise', () => {
    expect(tradeFee({ tokenAmount: '1000', feeBps: 50 })).toEqual({ fee: '5', net: '995' });
  });
  it('null without a rate', () => expect(tradeFee({ tokenAmount: '1000' })).toBeNull());
});
