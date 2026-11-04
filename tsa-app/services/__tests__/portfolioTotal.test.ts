jest.mock('@react-native-async-storage/async-storage', () => require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('../walletApi', () => ({ __esModule: true }));
jest.mock('../chains/tronWallet', () => ({ __esModule: true }));
jest.mock('../chains/btcWallet', () => ({ __esModule: true }));
jest.mock('../chains/solanaWallet', () => ({ __esModule: true }));
jest.mock('../chains/suiWallet', () => ({ __esModule: true }));
jest.mock('../mcgpSwapApi', () => ({ __esModule: true }));
jest.mock('../wallet', () => ({ __esModule: true, getSignableWallet: jest.fn() }));
jest.mock('../walletSnapshot', () => ({ __esModule: true, walletSnapshot: jest.fn() }));
jest.mock('../../hooks/useNetwork', () => ({ __esModule: true, getNetwork: () => 'mainnet' }));
jest.mock('../walletAssetSources', () => {
  const actual = jest.requireActual('../walletAssetSources');
  return {
    __esModule: true,
    ...actual,
    loadEvmAssets: jest.fn(),
    loadTronAssets: jest.fn(),
    loadBtcAssets: jest.fn(),
    loadSolanaAssets: jest.fn(),
    loadSuiAssets: jest.fn(),
    loadPremiumAssets: jest.fn(),
    readHiddenTokens: jest.fn(),
  };
});

/* eslint-disable import/first */
import { readPortfolioUsd } from '../portfolioTotal';
import { getSignableWallet } from '../wallet';
import { walletSnapshot } from '../walletSnapshot';
import * as sources from '../walletAssetSources';
import { emptySourceAssets } from '../../utils/walletAssets';

const m = sources as unknown as Record<string, jest.Mock>;
const signer = getSignableWallet as jest.Mock;
const snapshot = walletSnapshot as jest.Mock;

const asset = (symbol: string, usdValue: number, chainKey = 'bsc', extra: object = {}) => ({
  id: symbol, symbol, name: symbol, balance: 1, usdValue, usdPrice: usdValue, iconColor: '#000',
  details: { type: 'Token', chain: chainKey, chainKey }, ...extra,
});

const saveSnapshot = (value: unknown) => snapshot.mockReturnValue({ read: jest.fn(async () => (value ? { value } : null)) });

beforeEach(() => {
  jest.resetAllMocks();
  signer.mockResolvedValue('0xabc');
  saveSnapshot(null);
  m.readHiddenTokens.mockResolvedValue(new Set());
  m.loadEvmAssets.mockResolvedValue([]);
  m.loadTronAssets.mockResolvedValue([]);
  m.loadBtcAssets.mockResolvedValue([]);
  m.loadSolanaAssets.mockResolvedValue([]);
  m.loadSuiAssets.mockResolvedValue([]);
  m.loadPremiumAssets.mockResolvedValue([]);
});

describe('readPortfolioUsd', () => {
  it('sums every chain the Wallet tab lists, not just MCGP and stablecoins', async () => {
    m.loadEvmAssets.mockResolvedValue([asset('USDT', 10), asset('ETH', 100, 'ethereum'), asset('MCGP', 2, 'sonic')]);
    m.loadTronAssets.mockResolvedValue([asset('TRX', 3, 'tron')]);
    m.loadBtcAssets.mockResolvedValue([asset('BTC', 50, 'btc')]);
    m.loadSolanaAssets.mockResolvedValue([asset('SOL', 7, 'solana')]);
    m.loadSuiAssets.mockResolvedValue([asset('SUI', 1, 'sui')]);
    m.loadPremiumAssets.mockResolvedValue([asset('MCGP', 30, 'sonic', { tag: 'Spendable' })]);
    // Wallet MCGP and Spendable MCGP are both counted, as the Wallet tab lists both.
    await expect(readPortfolioUsd()).resolves.toBe(203);
    expect(m.loadEvmAssets).toHaveBeenCalledWith([], '0xabc');
  });

  it('leaves out tokens the user hid on the Wallet tab', async () => {
    m.loadEvmAssets.mockResolvedValue([asset('USDT', 10), asset('SCAM', 999)]);
    m.readHiddenTokens.mockResolvedValue(new Set(['bsc::SCAM']));
    await expect(readPortfolioUsd()).resolves.toBe(10);
  });

  it('counts discovered tokens the Wallet tab last saw, unless a source now reports them', async () => {
    m.loadEvmAssets.mockResolvedValue([asset('USDT', 10)]);
    saveSnapshot({
      sources: emptySourceAssets(),
      extras: [asset('PEPE', 4, 'base', { discovered: true }), asset('USDT', 10, 'bsc', { discovered: true })],
    });
    await expect(readPortfolioUsd()).resolves.toBe(14);
  });

  it('a failed source uses the rows the Wallet tab last saw for it', async () => {
    m.loadEvmAssets.mockResolvedValue([asset('USDT', 10)]);
    m.loadBtcAssets.mockRejectedValue(new Error('esplora down'));
    saveSnapshot({ sources: { ...emptySourceAssets(), btc: [asset('BTC', 40, 'btc')] }, extras: [] });
    await expect(readPortfolioUsd()).resolves.toBe(50);
  });

  it('is unknown (null), never a partial sum, when the EVM read fails with nothing saved', async () => {
    m.loadEvmAssets.mockRejectedValue(new Error('api down'));
    m.loadBtcAssets.mockResolvedValue([asset('BTC', 40, 'btc')]);
    await expect(readPortfolioUsd()).resolves.toBeNull();
  });

  it('an EVM read the Wallet tab never completed (saved as null) stays unknown', async () => {
    m.loadEvmAssets.mockRejectedValue(new Error('api down'));
    saveSnapshot({ sources: emptySourceAssets(), extras: [] });
    await expect(readPortfolioUsd()).resolves.toBeNull();
  });

  it('a non-EVM chain that fails with nothing saved counts as none, as on the Wallet tab', async () => {
    m.loadEvmAssets.mockResolvedValue([asset('USDT', 10)]);
    m.loadSolanaAssets.mockRejectedValue(new Error('dormant chain'));
    await expect(readPortfolioUsd()).resolves.toBe(10);
  });

  it('an imported token shadows a discovered copy of it, as on the Wallet tab', async () => {
    m.loadEvmAssets.mockResolvedValue([]);
    saveSnapshot({
      sources: emptySourceAssets(),
      extras: [
        asset('FOO', 0, 'base', { contractAddress: '0xF00', details: { type: 'Imported', chain: 'base', chainKey: 'base' } }),
        asset('FOO', 25, 'base', { discovered: true, contractAddress: '0xf00' }),
      ],
    });
    await expect(readPortfolioUsd()).resolves.toBe(0);
  });

  it('a failed Spendable MCGP read counts as none held', async () => {
    m.loadEvmAssets.mockResolvedValue([asset('USDC', 4)]);
    m.loadPremiumAssets.mockRejectedValue(new Error('down'));
    await expect(readPortfolioUsd()).resolves.toBe(4);
  });

  it('with no key on this device, a failed Spendable MCGP read is unknown, not $0', async () => {
    signer.mockResolvedValue(null);
    m.loadPremiumAssets.mockRejectedValue(new Error('down'));
    await expect(readPortfolioUsd()).resolves.toBeNull();
  });

  it('with no key on this device counts only Spendable MCGP', async () => {
    signer.mockResolvedValue(null);
    m.loadPremiumAssets.mockResolvedValue([asset('MCGP', 9, 'sonic')]);
    await expect(readPortfolioUsd()).resolves.toBe(9);
    expect(m.loadEvmAssets).not.toHaveBeenCalled();
  });
});
