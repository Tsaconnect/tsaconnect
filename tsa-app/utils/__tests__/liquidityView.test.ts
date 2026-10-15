jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

/* eslint-disable import/first */
import {
  canClaimGasFree, claimRefusalMessage, formatLiquidityAmount, isZeroAmount, liquidityAmountError, liquidityHoldings, liquidityMessage,
  limitsKnown, oldWalletFundsMessage, oldWalletHasFunds, overBalanceError, withdrawalMessage,
} from '@/utils/liquidityView';
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

describe('withdraw lock', () => {
  it('limits a withdraw to the unlocked principal and says when the rest unlocks', () => {
    const locked = me({ principal: '100', withdrawable: '60', lockedUntil: '2026-10-15T09:00:00Z' });
    expect(liquidityAmountError(locked, 'withdraw', '60')).toBeNull();
    expect(liquidityAmountError(locked, 'withdraw', '61')).toMatch(/withdraw 60 USDT now.*unlocks/);
    expect(liquidityAmountError(locked, 'withdraw', '101')).toMatch(/at most 60 USDT/);
    expect(liquidityAmountError(me({ principal: '100' }), 'withdraw', '100')).toBeNull(); // older API: no lock fields
  });
});

describe('canClaimGasFree', () => {
  it('needs the minimum claim and the account still on the LP wallet', () => {
    expect(canClaimGasFree(me())).toBe(false);
    expect(canClaimGasFree(me({ claimable: '0.5' }))).toBe(true);
    expect(canClaimGasFree(me({ claimable: '3', walletChanged: true }))).toBe(false);
  });
});

describe('oldWalletHasFunds', () => {
  it('is false while the account is still on its LP wallet', () => {
    expect(oldWalletHasFunds(me())).toBe(false);
  });
  it('is true when the old wallet still holds principal or earnings', () => {
    expect(oldWalletHasFunds(me({ walletChanged: true }))).toBe(true);
    expect(oldWalletHasFunds(me({ walletChanged: true, principal: '0' }))).toBe(true);
  });
  it('is false once the old wallet is empty', () => {
    expect(oldWalletHasFunds(me({ walletChanged: true, principal: '0', claimable: '0.0' }))).toBe(false);
  });
  it('is true while only a pending withdrawal is left', () => {
    expect(oldWalletHasFunds(me({ walletChanged: true, principal: '0', claimable: '0', pendingWithdrawal: '5' }))).toBe(true);
  });
});

describe('oldWalletFundsMessage', () => {
  const old = { walletChanged: true, wallet: '0xold' };
  it('asks for withdraw and claim while principal or earnings remain', () => {
    expect(oldWalletFundsMessage(me({ ...old, pendingWithdrawal: '5' })))
      .toBe('Withdraw and claim from 0xold first (switch to it on this device), then activate your new wallet.');
  });
  it('is neutral and asks nothing when only a pending withdrawal is left', () => {
    expect(oldWalletFundsMessage(me({ ...old, principal: '0', claimable: '0', pendingWithdrawal: '5' })))
      .toBe('A withdrawal from your old wallet is still pending. You can switch wallets once it completes.');
  });
});

describe('liquidityMessage', () => {
  it('strips the package prefix from a server message', () => {
    expect(liquidityMessage('liquidity: the pool is closed', 'x')).toBe('The pool is closed');
  });
  it('falls back when there is no message', () => {
    expect(liquidityMessage(undefined, 'Try again.')).toBe('Try again.');
    expect(liquidityMessage('liquidity: ', 'Try again.')).toBe('Try again.');
  });
  it('leaves an ordinary message alone', () => {
    expect(liquidityMessage('Your liquidity is paused', 'x')).toBe('Your liquidity is paused');
  });
});

describe('claimRefusalMessage', () => {
  it('maps the liquidity refusal to the neutral copy', () => {
    expect(claimRefusalMessage('claim_unavailable', 'liquidity: insufficient liquidity'))
      .toBe('Claims are not available right now. Try again later.');
    expect(claimRefusalMessage(undefined, 'liquidity: nothing to claim')).toBe('Nothing to claim');
    expect(claimRefusalMessage(undefined, '')).toBe('Try again in a moment.');
  });
});

describe('withdrawalMessage', () => {
  const E18 = 10n ** 18n;
  it('splits paid-now and pending from the receipt', () => {
    expect(withdrawalMessage(me(), { paidNow: 30n * E18, pending: 20n * E18 }))
      .toBe('Withdrawal submitted. 30 USDT sent now, 20 USDT pending.');
    expect(withdrawalMessage(me(), { paidNow: 0n, pending: E18 / 4n })).toBe('Withdrawal submitted. 0.25 USDT pending.');
    expect(withdrawalMessage(me(), { paidNow: 50n * E18, pending: 0n })).toBe('Withdrawal sent to your wallet');
  });
  it('is neutral without a decoded receipt', () => {
    expect(withdrawalMessage(me(), undefined)).toBe('Withdrawal submitted.');
  });
});

describe('overBalanceError', () => {
  it('refuses more than the wallet holds, at full precision', () => {
    expect(overBalanceError('1.308402020000102049', '1.308402020000102048')).toBe('You have 1.3084 USDT in this wallet.');
    expect(overBalanceError('1.308402020000102048', '1.308402020000102048')).toBeNull();
  });
  it('stays quiet while the balance is loading or unknown', () => {
    expect(overBalanceError('10', undefined)).toBeNull();
    expect(overBalanceError('10', null)).toBeNull();
  });
});

describe('unreadable pool limits', () => {
  it('refuses deposits and reports the limits unknown', () => {
    const m = me({ limits: { min: '', max: '' }, limitsReadable: false });
    expect(limitsKnown(m)).toBe(false);
    expect(liquidityAmountError(m, 'deposit', '10')).toBe('Deposits are not available right now. Try again shortly.');
  });
  it('treats an API without the flag as readable', () => {
    expect(limitsKnown(me())).toBe(true);
  });
});

describe('liquidityHoldings', () => {
  it('adds staked principal, unclaimed earnings and a pending withdrawal, exactly', () => {
    expect(liquidityHoldings(me({ principal: '100.1', claimable: '0.2', pendingWithdrawal: '5' }))).toBe('105.3');
  });

  it('leaves out already-claimed earnings (they are in the wallet)', () => {
    expect(liquidityHoldings(me({ principal: '100', claimable: '0.4', earned: '9' }))).toBe('100.4');
  });

  it('is 0 for a user who never joined the pool', () => {
    expect(liquidityHoldings(me({ status: 'none', balanceReadable: false, principal: undefined, claimable: undefined }))).toBe('0');
  });

  it('is unknown, never zero, when an LP position could not be read', () => {
    expect(liquidityHoldings(me({ balanceReadable: false }))).toBeNull();
  });
});
