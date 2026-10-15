import { poolBuyParams, poolCardView, poolDefaultWant } from '../poolCard';
import type { PoolCard } from '@/services/liquidityApi';

const live: PoolCard = {
  enabled: true, name: 'TSA Connect', badge: 'Recommended', available: true, rate: '1361.51',
  fundingFeePercent: '1.4', minNgn: '2762.00', maxNgn: '171000.00',
  token: { symbol: 'USDT', address: '0x55d398326f99059fF775485246999027B3197955', decimals: 18 },
  chain: { id: 56, key: 'bsc', name: 'BNB Smart Chain' },
};

describe('poolCardView', () => {
  it('is hidden while the pool is dormant, and on the sell side', () => {
    expect(poolCardView({ enabled: false, available: false }, 'sell')).toBeNull();
    expect(poolCardView(undefined, 'sell')).toBeNull();
    expect(poolCardView(live, 'buy')).toBeNull(); // adSide 'buy' = she is selling crypto
  });

  it('shows rate, fee and limits, and opens the IT buy screen with the pool preselected', () => {
    const v = poolCardView(live, 'sell')!;
    expect(v.title).toBe('TSA Connect');
    expect(v.badge).toBe('Recommended');
    expect(v.disabled).toBe(false);
    expect(v.lines.join(' ')).toContain('₦1,361.51');
    expect(v.lines.join(' ')).toContain('1.4%');
    expect(v.route).toBe('/wallet/paycrest/buy?asset=USDT&pool=1&chainId=56&chain=bsc&tokenAddress=0x55d398326f99059fF775485246999027B3197955&currency=NGN');
  });

  it('stays pinned but disabled when it cannot fill (M6), and never promises returns', () => {
    const v = poolCardView({ ...live, available: false }, 'sell')!;
    expect(v.disabled).toBe(true);
    expect(v.route).toBeUndefined();
    expect(v.lines).toEqual(['Unavailable right now']);
    expect(JSON.stringify(poolCardView(live, 'sell'))).not.toMatch(/APR|interest|earn/i);
  });
});

describe('poolBuyParams', () => {
  it('reads the card route back into the buy screen', () => {
    expect(poolBuyParams({ pool: '1', asset: 'usdt', chainId: '56', chain: 'bsc', tokenAddress: '0x55d3' })).toEqual({
      pool: true, currency: 'NGN', want: { token: 'USDT', chainId: 56, chain: 'bsc', address: '0x55d3' },
    });
    expect(poolBuyParams({ asset: 'USDC' })).toEqual({ pool: false });
    expect(poolBuyParams({ pool: '1', chainId: 'x' })).toEqual({ pool: true, currency: 'NGN' });
  });

  it('pins a pool buy to naira whatever currency the route or her saved ramp choice says (I-5)', () => {
    expect(poolBuyParams({ pool: '1', currency: 'GHS' }).currency).toBe('NGN');
    expect(poolBuyParams({ currency: 'GHS' }).currency).toBeUndefined();
  });
});

describe('poolDefaultWant', () => {
  const usdtBsc = { token: 'USDT', chainId: 56, chain: 'bsc', address: live.token!.address };

  it('starts a naira buy on the pool token and chain while the pool can fill', () => {
    expect(poolDefaultWant(live, { currency: 'NGN' })).toEqual(usdtBsc);
    expect(poolDefaultWant(live, { asset: 'usdt', currency: 'NGN' })).toEqual(usdtBsc);
  });

  it('keeps the screen default when the pool is empty, dormant or unreadable', () => {
    expect(poolDefaultWant({ ...live, available: false }, { currency: 'NGN' })).toBeNull();
    expect(poolDefaultWant({ enabled: false, available: false }, { currency: 'NGN' })).toBeNull();
    expect(poolDefaultWant(null, { currency: 'NGN' })).toBeNull();
  });

  it('never overrides another currency or a token she asked for', () => {
    expect(poolDefaultWant(live, { currency: 'KES' })).toBeNull();
    expect(poolDefaultWant(live, { asset: 'USDC', currency: 'NGN' })).toBeNull();
  });
});
