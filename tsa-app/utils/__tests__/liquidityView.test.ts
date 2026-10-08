jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

/* eslint-disable import/first */
import { formatLiquidityAmount, liquidityStage, lpDepositPlan } from '@/utils/liquidityView';
import type { LiquidityMe } from '@/services/liquidityApi';

const me = (over: Partial<LiquidityMe> = {}): LiquidityMe => ({
  status: 'active', poolOpen: true, balanceReadable: true, owedNgn: '0.00', paused: false,
  chain: { id: 97, key: 'bsc', name: 'BNB Smart Chain Testnet' },
  token: { symbol: 'USDT', address: '0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7', decimals: 18 },
  wallet: '0x00000000000000000000000000000000000000d1',
  available: '0', locked: '0', stake: '0', aboveMax: '0', headroom: '5000',
  limits: { min: '5', max: '5000' },
  ...over,
});

describe('formatLiquidityAmount', () => {
  it('keeps 18-dp precision', () => {
    expect(formatLiquidityAmount('1234567.890123456789012345', 18)).toBe('1,234,567.890123456789012345');
    expect(formatLiquidityAmount('0.000000000000000001', 18)).toBe('0.000000000000000001');
    expect(formatLiquidityAmount('1234567.890123456789012345')).toBe('1,234,567.8901');
    expect(formatLiquidityAmount('5000')).toBe('5,000');
    expect(formatLiquidityAmount(undefined)).toBe('0');
  });
});

describe('liquidityStage', () => {
  it('maps approved to the activation step', () => {
    expect(liquidityStage(me({ status: 'approved' }))).toBe('activate');
    expect(liquidityStage(me({ status: 'none' }))).toBe('none');
    expect(liquidityStage(me({ status: 'paused', paused: true }))).toBe('paused');
  });
});

describe('lpDepositPlan', () => {
  it('works in 18-dp base units', () => {
    const plan = lpDepositPlan(me({ available: '40.5', headroom: '4959.5', voucher: { adId: 'a', status: 'active', remaining: '500000', renewDue: false } }), '10.25');
    expect(plan).toEqual({ ok: true, amountBaseUnits: '10250000000000000000', needsStockAd: false });
  });
  it('refuses above the headroom and below the first-deposit minimum', () => {
    expect(lpDepositPlan(me({ headroom: '100' }), '100.000000000000000001').ok).toBe(false);
    expect(lpDepositPlan(me(), '4.99').ok).toBe(false);
    expect(lpDepositPlan(me(), '5').ok).toBe(true);
  });
  it('allows a renew-only plan when stock is held and the voucher is due', () => {
    const plan = lpDepositPlan(me({ available: '40', voucher: { adId: 'a', status: 'active', remaining: '1', renewDue: true } }), '');
    expect(plan).toEqual({ ok: true, amountBaseUnits: '0', needsStockAd: true });
  });
  it('needs a stock ad when none exists or the live one is paused, and refuses a paused LP', () => {
    expect(lpDepositPlan(me(), '10')).toMatchObject({ ok: true, needsStockAd: true });
    expect(lpDepositPlan(me({ voucher: { adId: 'a', status: 'paused', remaining: '1', renewDue: false } }), '10')).toMatchObject({ ok: true, needsStockAd: true });
    expect(lpDepositPlan(me({ status: 'paused', paused: true }), '10').ok).toBe(false);
  });
});
