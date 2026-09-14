// Tests for the Send form's pure decisions (utils/sendForm.ts).

import {
  feePaidNative,
  formatNativeAmount,
  hasGasShortfall,
  nativeBalanceOn,
  pickDefaultSendAsset,
  preparedFeeNative,
  reviewBlocker,
  sendOutcome,
  shortAddress,
  type ReviewInput,
} from '../sendForm';
import type { WalletBalanceWithChain } from '../../services/walletApi';

const row = (
  symbol: string,
  chainKey: WalletBalanceWithChain['chainKey'],
  balance: string,
  usdValue: string,
): WalletBalanceWithChain => ({
  symbol, chainKey, balance, usdValue,
  name: symbol, contractAddress: '', decimals: 18, chainName: chainKey,
});

const wallet = [
  row('USDT', 'sonic', '0', '0'),
  row('S', 'sonic', '3', '0.09'),
  row('USDT', 'bsc', '25.4', '25.4'),
  row('BNB', 'bsc', '0.0042', '2.60'),
  row('USDC', 'base', '4', '4'),
];

describe('pickDefaultSendAsset', () => {
  it('keeps an explicit token and chain', () => {
    expect(pickDefaultSendAsset(wallet, { symbol: 'USDC', chainKey: 'base' })).toEqual({ symbol: 'USDC', chainKey: 'base' });
  });

  it('opens on the most valuable holding when nothing is preferred', () => {
    expect(pickDefaultSendAsset(wallet)).toEqual({ symbol: 'USDT', chainKey: 'bsc' });
  });

  it('opens a preferred token on the chain where it is held', () => {
    expect(pickDefaultSendAsset(wallet, { symbol: 'S' })).toEqual({ symbol: 'S', chainKey: 'sonic' });
  });

  it('returns null when nothing is held, so the caller keeps its default', () => {
    expect(pickDefaultSendAsset([row('USDT', 'sonic', '0', '0')])).toBeNull();
    expect(pickDefaultSendAsset(wallet, { symbol: 'MCGP' })).toBeNull();
  });
});

describe('nativeBalanceOn', () => {
  it('reads the native row case-insensitively', () => {
    expect(nativeBalanceOn(wallet, 'bsc', 'bnb')).toBeCloseTo(0.0042);
  });

  it('is null, not zero, when the row is missing', () => {
    expect(nativeBalanceOn(wallet, 'base', 'ETH')).toBeNull();
  });
});

describe('preparedFeeNative', () => {
  it('multiplies gas price by gas limit in whole units', () => {
    // 0.055 gwei × 105,000 gas
    expect(preparedFeeNative('55000000', '105000')).toBeCloseTo(0.000005775, 12);
  });

  it('is zero for malformed input', () => {
    expect(preparedFeeNative('abc', '21000')).toBe(0);
  });
});

describe('formatNativeAmount', () => {
  it('keeps two significant digits below one', () => {
    expect(formatNativeAmount(0.000005775)).toBe('0.0000058');
    expect(formatNativeAmount(0.0123)).toBe('0.012');
    expect(formatNativeAmount(0.5)).toBe('0.5');
  });

  it('keeps up to four decimals from one upward', () => {
    expect(formatNativeAmount(1.23456)).toBe('1.2346');
    expect(formatNativeAmount(10)).toBe('10');
  });

  it('shows zero for nothing', () => {
    expect(formatNativeAmount(0)).toBe('0');
  });
});

describe('hasGasShortfall', () => {
  it('never blocks on an unknown balance', () => {
    expect(hasGasShortfall(null, 0.01, 1, false)).toBe(false);
  });

  it('blocks a token send with no gas at all', () => {
    expect(hasGasShortfall(0, null, 10, false)).toBe(true);
  });

  it('counts the amount when the amount is the native coin', () => {
    expect(hasGasShortfall(1, 0.001, 1, true)).toBe(true);
    expect(hasGasShortfall(1, 0.001, 0.9, true)).toBe(false);
  });
});

describe('reviewBlocker', () => {
  const ready: ReviewInput = {
    address: '0x7a3F000000000000000000000000000000c91E',
    addressValid: true,
    amount: '10',
    tokenAmount: '10',
    balance: '25.4',
    gasBalance: 0.0042,
    fee: 0.000006,
    nativeSymbol: 'BNB',
    sendingNative: false,
  };

  it('allows a valid, funded send', () => {
    expect(reviewBlocker(ready)).toBeNull();
  });

  it('asks for each missing piece in order', () => {
    expect(reviewBlocker({ ...ready, amount: '' })).toBe('Enter an amount.');
    expect(reviewBlocker({ ...ready, tokenAmount: '30' })).toBe('The amount is more than your balance.');
    expect(reviewBlocker({ ...ready, address: ' ' })).toBe('Enter a recipient address.');
    expect(reviewBlocker({ ...ready, addressValid: false })).toBe('That address isn’t valid on this network.');
  });

  it('names the coin the fee needs', () => {
    expect(reviewBlocker({ ...ready, gasBalance: 0 })).toBe('Not enough BNB to pay the network fee.');
  });
});

describe('sendOutcome', () => {
  it('reads a mined, successful receipt as sent', () => {
    expect(sendOutcome({ status: 1 })).toBe('success');
  });

  it('reads a reverted receipt as reverted', () => {
    expect(sendOutcome({ status: 0 })).toBe('reverted');
  });

  it('never calls a missing receipt failed', () => {
    expect(sendOutcome(null)).toBe('pending');
  });
});

describe('feePaidNative', () => {
  it('multiplies gas used by the price paid', () => {
    // 50,000 gas at 1 gwei
    expect(feePaidNative(BigInt(50000), BigInt(1000000000))).toBeCloseTo(0.00005, 12);
  });
});

describe('shortAddress', () => {
  it('keeps the ends of a long address', () => {
    expect(shortAddress('0x7a3F000000000000000000000000000000c91E')).toBe('0x7a3F…c91E');
  });

  it('leaves a short string alone', () => {
    expect(shortAddress('0xabc')).toBe('0xabc');
  });
});
