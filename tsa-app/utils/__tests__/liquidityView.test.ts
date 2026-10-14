jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

/* eslint-disable import/first */
import { canClaimGasFree, formatLiquidityAmount, isZeroAmount, liquidityAmountError } from '@/utils/liquidityView';
import type { LiquidityMe } from '@/services/liquidityApi';

const me = (over: Partial<LiquidityMe> = {}): LiquidityMe => ({
  mode: 'vault', status: 'active', poolOpen: true, balanceReadable: true, paused: false, allowed: true, walletChanged: false,
  chain: { id: 56, key: 'bsc', name: 'BNB Smart Chain' },
  token: { symbol: 'USDT', address: '0x55d398326f99059fF775485246999027B3197955', decimals: 18 },
  vault: '0x00000000000000000000000000000000000000v1', wallet: '0x00000000000000000000000000000000000000d1',
  principal: '100', claimable: '0.4', earned: '1.2', sharePercent: '2',
  limits: { min: '5', max: '5000' }, minClaim: '0.5',
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

describe('isZeroAmount', () => {
  it('reads empty and all-zero decimals as zero', () => {
    expect(isZeroAmount(undefined)).toBe(true);
    expect(isZeroAmount('0.000')).toBe(true);
    expect(isZeroAmount('0.000000000000000001')).toBe(false);
  });
});

describe('liquidityAmountError', () => {
  it('rejects malformed input and too many decimals', () => {
    expect(liquidityAmountError(me(), 'deposit', 'abc')).toMatch(/valid amount/);
    expect(liquidityAmountError(me(), 'deposit', '1e3')).toMatch(/valid amount/);
    expect(liquidityAmountError(me(), 'deposit', '0')).toMatch(/Enter an amount/);
    expect(liquidityAmountError(me(), 'deposit', `5.${'1'.repeat(19)}`)).toMatch(/18 decimal places/);
  });
  it('holds every deposit to the minimum and the total to the maximum', () => {
    expect(liquidityAmountError(me(), 'deposit', '4.99')).toMatch(/at least 5 USDT/);
    expect(liquidityAmountError(me(), 'deposit', '4900')).toBeNull();
    expect(liquidityAmountError(me(), 'deposit', '4900.000000000000000001')).toMatch(/at most 4900 USDT/);
  });
  it('withdraws up to the principal, exactly', () => {
    expect(liquidityAmountError(me(), 'withdraw', '100')).toBeNull();
    expect(liquidityAmountError(me(), 'withdraw', '0.5')).toBeNull();
    expect(liquidityAmountError(me(), 'withdraw', '100.000000000000000001')).toMatch(/at most 100 USDT/);
  });
});

describe('canClaimGasFree', () => {
  it('needs the minimum claim and the account still on the LP wallet', () => {
    expect(canClaimGasFree(me())).toBe(false);
    expect(canClaimGasFree(me({ claimable: '0.5' }))).toBe(true);
    expect(canClaimGasFree(me({ claimable: '3', walletChanged: true }))).toBe(false);
  });
});
