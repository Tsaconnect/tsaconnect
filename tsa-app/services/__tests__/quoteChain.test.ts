/* eslint-disable import/first -- jest hoists the mock above the import it
   replaces; writing it first documents which import it stands in for. */
jest.mock('@/services/walletApi', () => ({
  __esModule: true,
  getWalletBalances: jest.fn(),
  normalizeWalletBalances: jest.fn(),
}));

import * as WalletApi from '@/services/walletApi';
import { fundedQuoteChain } from '../quoteChain';

const walletApi = WalletApi as unknown as { getWalletBalances: jest.Mock; normalizeWalletBalances: jest.Mock };

beforeEach(() => {
  walletApi.getWalletBalances.mockReset();
  walletApi.normalizeWalletBalances.mockReset();
});

describe('fundedQuoteChain', () => {
  it('uses the chain the wallet read reports the most of', async () => {
    walletApi.getWalletBalances.mockResolvedValue({ success: true, data: {} });
    walletApi.normalizeWalletBalances.mockReturnValue([
      { chainKey: 'bsc', symbol: 'USDT', balance: '2' },
      { chainKey: 'polygon', symbol: 'USDT', balance: '40' },
    ]);
    await expect(fundedQuoteChain('USDT', 'bsc')).resolves.toBe('polygon');
  });

  it('falls back when the read fails', async () => {
    walletApi.getWalletBalances.mockRejectedValue(new Error('offline'));
    await expect(fundedQuoteChain('USDT', 'ethereum')).resolves.toBe('ethereum');
  });

  it('stops waiting on a slow read and falls back', async () => {
    jest.useFakeTimers();
    try {
      walletApi.getWalletBalances.mockReturnValue(new Promise(() => {}));
      const pick = fundedQuoteChain('USDC', undefined, 2500);
      jest.advanceTimersByTime(2500);
      await expect(pick).resolves.toBe('base');
    } finally {
      jest.useRealTimers();
    }
  });
});
