jest.mock('../walletApi', () => ({ __esModule: true, readWalletBalances: jest.fn() }));
jest.mock('../mcgpSwapApi', () => ({ __esModule: true, getPremiumMcgpAsset: jest.fn() }));
jest.mock('../wallet', () => ({ __esModule: true, getSignableWallet: jest.fn() }));

/* eslint-disable import/first */
import { readPortfolioUsd } from '../portfolioTotal';
import { readWalletBalances } from '../walletApi';
import { getPremiumMcgpAsset } from '../mcgpSwapApi';
import { getSignableWallet } from '../wallet';

const rows = readWalletBalances as jest.Mock;
const premium = getPremiumMcgpAsset as jest.Mock;
const signer = getSignableWallet as jest.Mock;

const row = (symbol: string, usdValue: string) => ({ symbol, usdValue, balance: '1', chainKey: 'bsc' });

beforeEach(() => {
  jest.resetAllMocks();
  signer.mockResolvedValue('0xabc');
  premium.mockResolvedValue({ balance: 0, usdValue: 0 });
});

describe('readPortfolioUsd', () => {
  it('sums MCGP, USDC and USDT across chains and overlays Spendable MCGP', async () => {
    rows.mockResolvedValue([row('USDT', '10'), row('USDT', '5.5'), row('USDC', '2'), row('ETH', '100')]);
    premium.mockResolvedValue({ balance: 3, usdValue: 30 });
    await expect(readPortfolioUsd()).resolves.toBe(47.5);
  });

  it('is unknown (null), never zero, when the wallet balances cannot be read', async () => {
    rows.mockResolvedValue(null);
    premium.mockResolvedValue({ balance: 3, usdValue: 30 });
    await expect(readPortfolioUsd()).resolves.toBeNull();
  });

  it('with no key on this device counts no wallet balances, only Spendable MCGP', async () => {
    signer.mockResolvedValue(null);
    premium.mockResolvedValue({ balance: 1, usdValue: 9 });
    await expect(readPortfolioUsd()).resolves.toBe(9);
    expect(rows).not.toHaveBeenCalled();
  });

  it('a failed Spendable MCGP read counts as none held', async () => {
    rows.mockResolvedValue([row('USDC', '4')]);
    premium.mockRejectedValue(new Error('down'));
    await expect(readPortfolioUsd()).resolves.toBe(4);
  });
});
