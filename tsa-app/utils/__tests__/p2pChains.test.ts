import type { P2PChainInfo } from '@/services/p2p';
import {
  adChainsFor,
  adChainsView,
  adPublishChain,
  adTokenSymbols,
  bscTopUpSwapParams,
  openAdChains,
  sortP2PChains,
  walletShort,
} from '../p2pChains';

const chain = (chainId: number, extra: Partial<P2PChainInfo> = {}): P2PChainInfo => ({
  chainId,
  assets: ['USDT', 'USDC'],
  ...extra,
});

describe('sortP2PChains', () => {
  it('puts BSC first, then Sonic, then the rest by id', () => {
    const ids = sortP2PChains([chain(8453), chain(146), chain(56), chain(14601), chain(97), chain(1)]).map((c) => c.chainId);
    expect(ids).toEqual([56, 97, 146, 14601, 1, 8453]);
  });
  it('does not mutate its input', () => {
    const list = [chain(146), chain(56)];
    sortP2PChains(list);
    expect(list.map((c) => c.chainId)).toEqual([146, 56]);
  });
});

describe('adsOpen', () => {
  it('hides a chain with adsOpen:false and keeps a missing flag as open', () => {
    const list = [chain(146, { adsOpen: false }), chain(56, { adsOpen: true }), chain(8453)];
    expect(openAdChains(list).map((c) => c.chainId)).toEqual([56, 8453]);
    expect(adChainsFor(list, 'USDT').map((c) => c.chainId)).toEqual([56, 8453]);
  });
  it('offers only the tokens of open chains', () => {
    const list = [chain(146, { adsOpen: false, assets: ['USDT', 'MCGP'] }), chain(56, { assets: ['USDT'] })];
    expect(adTokenSymbols(list)).toEqual(['USDT']);
  });
  it('an old API (no flag) still lists Sonic, BSC first', () => {
    expect(adChainsFor([chain(146), chain(56)], 'USDC').map((c) => c.chainId)).toEqual([56, 146]);
  });
});

describe('adPublishChain — publish never goes out without a chainId', () => {
  it('blocks with Retry copy when the chains fetch failed', () => {
    const d = adPublishChain('failed', undefined);
    expect(d).toEqual({ kind: 'block', message: expect.stringMatching(/Retry/) });
  });
  it('blocks while loading', () => {
    expect(adPublishChain('loading', undefined).kind).toBe('block');
  });
  it('blocks when no open chain offers the token', () => {
    expect(adPublishChain('ready', undefined)).toEqual({ kind: 'block', message: expect.stringMatching(/No network/) });
  });
  it('a failed fetch blocks even with a stale selection', () => {
    expect(adPublishChain('failed', 56).kind).toBe('block');
  });
  it('returns the selected chain once loaded', () => {
    expect(adPublishChain('ready', 56)).toEqual({ kind: 'ok', chainId: 56 });
  });
});

describe('All Swap hand-off for a short BSC balance', () => {
  it('opens All Swap with BSC as the receive leg on mainnet only', () => {
    expect(bscTopUpSwapParams(56, 'USDT')).toEqual({ tab: 'allswap', toSymbol: 'USDT', toChain: 'bsc' });
    expect(bscTopUpSwapParams(97, 'USDT')).toBeNull();
    expect(bscTopUpSwapParams(146, 'USDT')).toBeNull();
    expect(bscTopUpSwapParams(undefined, 'USDT')).toBeNull();
  });
  it('is short only on a known balance below the amount', () => {
    expect(walletShort('5', '10')).toBe(true);
    expect(walletShort('10', '10')).toBe(false);
    expect(walletShort('-', '10')).toBe(false);
    expect(walletShort(null, '10')).toBe(false);
    expect(walletShort('0', '')).toBe(false);
  });
});

describe('adChainsView', () => {
  it('passes the load state through while loading or failed', () => {
    expect(adChainsView('loading', [])).toBe('loading');
    expect(adChainsView('failed', [chain(56)])).toBe('failed');
  });
  it('is none_open when the chains loaded but none is open for new ads', () => {
    expect(adChainsView('ready', [chain(56, { adsOpen: false }), chain(146, { adsOpen: false })])).toBe('none_open');
    expect(adChainsView('ready', [chain(56, { assets: [] })])).toBe('none_open');
  });
  it('is ready when at least one chain is open (a missing adsOpen is open)', () => {
    expect(adChainsView('ready', [chain(146, { adsOpen: false }), chain(56)])).toBe('ready');
  });
});
