import { getTronBalances, loadTronWallet, getTronAddress } from '../tronWallet';
import * as nonevmApi from '../nonevmApi';
import * as wallet from '../../wallet';

// Factory mocks so the real modules (which import AsyncStorage / SecureStore
// native shims) never load under jest.
jest.mock('../nonevmApi', () => ({
  registerNonEVMAddress: jest.fn(),
  getNonEVMBalances: jest.fn(),
  getNonEVMTxStatus: jest.fn(),
}));
jest.mock('../../wallet', () => ({
  getActiveWallet: jest.fn(),
  getMnemonic: jest.fn(),
}));

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';
const EVM = '0xabc0000000000000000000000000000000000001';

const mockedApi = nonevmApi as jest.Mocked<typeof nonevmApi>;
const mockedWallet = wallet as jest.Mocked<typeof wallet>;

beforeEach(() => {
  jest.clearAllMocks();
  mockedWallet.getActiveWallet.mockResolvedValue(EVM);
  mockedWallet.getMnemonic.mockResolvedValue(M);
});

describe('getTronBalances', () => {
  it('maps base-unit balances to human amounts and pins our listed stablecoins to $1', async () => {
    mockedApi.getNonEVMBalances.mockResolvedValue([
      { symbol: 'TRX', raw: '12500000', decimals: 6, isNative: true },
      { symbol: 'USDT', raw: '2500000', decimals: 6, tokenAddress: 'TR7...', isNative: false, listed: true },
    ]);

    const rows = await getTronBalances('TXaddr');

    expect(rows).toHaveLength(2);
    const trx = rows.find((r) => r.symbol === 'TRX')!;
    expect(trx.balance).toBe('12.5');
    expect(trx.usdValue).toBe('0.00'); // no TRX price feed yet
    expect(trx.chainKey).toBe('tron');

    const usdt = rows.find((r) => r.symbol === 'USDT')!;
    expect(usdt.balance).toBe('2.5');
    expect(usdt.usdValue).toBe('2.50'); // pinned $1
    expect(usdt.contractAddress).toBe('TR7...');
  });

  it('prefers backend-provided USD price/value over the fallback pin', async () => {
    mockedApi.getNonEVMBalances.mockResolvedValue([
      { symbol: 'TRX', raw: '2000000000', decimals: 6, isNative: true, usdPrice: 0.12, usdValue: 240 },
    ]);

    const [trx] = await getTronBalances('TXaddr');
    expect(trx.usdPrice).toBe(0.12); // real CoinGecko price, not 0
    expect(trx.usdValue).toBe('240.00');
  });
});

describe('loadTronWallet', () => {
  it('derives the Tron address, registers it, and returns balances', async () => {
    mockedApi.registerNonEVMAddress.mockResolvedValue(undefined);
    mockedApi.getNonEVMBalances.mockResolvedValue([
      { symbol: 'TRX', raw: '0', decimals: 6, isNative: true },
    ]);

    const result = await loadTronWallet(EVM);

    expect(result).not.toBeNull();
    expect(result!.address).toMatch(/^T[1-9A-HJ-NP-Za-km-z]{33}$/);
    expect(result!.balances).toHaveLength(1);
    expect(mockedApi.registerNonEVMAddress).toHaveBeenCalledWith(EVM, 'tron', result!.address);
  });

  it('returns the address with empty balances when the chain is dormant (balances throw)', async () => {
    mockedApi.registerNonEVMAddress.mockRejectedValue(new Error('chain unavailable'));
    mockedApi.getNonEVMBalances.mockRejectedValue(new Error('chain unavailable'));

    const result = await loadTronWallet(EVM);

    expect(result).not.toBeNull();
    expect(result!.address).toMatch(/^T/);
    expect(result!.balances).toEqual([]);
  });

  it('returns null when no mnemonic is available', async () => {
    // A wallet not seen before, so the derived-address cache can't satisfy it.
    const freshWallet = '0xdead0000000000000000000000000000000000ff';
    mockedWallet.getMnemonic.mockResolvedValue(null);
    expect(await loadTronWallet(freshWallet)).toBeNull();
  });
});

describe('getTronAddress', () => {
  it('is deterministic for a given mnemonic', async () => {
    const a = await getTronAddress(EVM);
    const b = await getTronAddress(EVM);
    expect(a).toBe(b);
    expect(a).toMatch(/^T[1-9A-HJ-NP-Za-km-z]{33}$/);
  });
});
