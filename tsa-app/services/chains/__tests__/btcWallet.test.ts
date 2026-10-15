import { getBtcBalances, loadBtcWallet, getBtcAddress } from '../btcWallet';
import * as nonevmApi from '../nonevmApi';
import * as wallet from '../../wallet';
import { clearNonEvmAddressCache } from '../nonEvmAddresses';

// Factory mocks so the real modules (which import AsyncStorage / SecureStore
// native shims) never load under jest.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
  removeItem: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../nonevmApi', () => ({
  registerNonEVMAddress: jest.fn(),
  getNonEVMBalances: jest.fn(),
  getNonEVMTxStatus: jest.fn(),
}));
jest.mock('../../wallet', () => ({
  getActiveWallet: jest.fn(),
  getMnemonic: jest.fn(),
}));
jest.mock('../../../hooks/useNetwork', () => ({
  getNetwork: jest.fn().mockReturnValue('mainnet'),
}));

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';
const EVM = '0xabc0000000000000000000000000000000000001';
const BTC_ADDR = 'bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu'; // BIP-84 vector for M

const mockedApi = nonevmApi as jest.Mocked<typeof nonevmApi>;
const mockedWallet = wallet as jest.Mocked<typeof wallet>;

beforeEach(() => {
  jest.clearAllMocks();
  clearNonEvmAddressCache();
  mockedWallet.getActiveWallet.mockResolvedValue(EVM);
  mockedWallet.getMnemonic.mockResolvedValue(M);
});

describe('getBtcAddress', () => {
  it('derives the BIP-84 address from the active wallet mnemonic', async () => {
    expect(await getBtcAddress()).toBe(BTC_ADDR);
  });

  it('returns null when no mnemonic is available (key-only wallet)', async () => {
    mockedWallet.getMnemonic.mockResolvedValue(null as any);
    expect(await getBtcAddress('0xother0000000000000000000000000000000002')).toBeNull();
  });
});

describe('getBtcBalances', () => {
  it('maps sats to a human BTC row with the backend price', async () => {
    mockedApi.getNonEVMBalances.mockResolvedValue([
      { symbol: 'BTC', raw: '12500000', decimals: 8, isNative: true, usdPrice: 100000, usdValue: 12500 },
    ]);

    const rows = await getBtcBalances(BTC_ADDR);

    expect(rows).toHaveLength(1);
    expect(rows[0].symbol).toBe('BTC');
    expect(rows[0].name).toBe('Bitcoin');
    expect(rows[0].balance).toBe('0.125');
    expect(rows[0].usdValue).toBe('12500.00');
    expect(rows[0].chainKey).toBe('btc');
    expect(rows[0].decimals).toBe(8);
  });

  it('shows no USD value (not a wrong one) when the backend has no price', async () => {
    mockedApi.getNonEVMBalances.mockResolvedValue([
      { symbol: 'BTC', raw: '100000000', decimals: 8, isNative: true },
    ]);
    const rows = await getBtcBalances(BTC_ADDR);
    expect(rows[0].balance).toBe('1.0');
    expect(rows[0].usdValue).toBe('0.00');
  });
});

describe('loadBtcWallet', () => {
  it('registers the address (best-effort) and returns balances', async () => {
    mockedApi.registerNonEVMAddress.mockResolvedValue(undefined);
    mockedApi.getNonEVMBalances.mockResolvedValue([
      { symbol: 'BTC', raw: '50000', decimals: 8, isNative: true },
    ]);

    const res = await loadBtcWallet(EVM);

    expect(res?.address).toBe(BTC_ADDR);
    expect(res?.balances).toHaveLength(1);
    expect(mockedApi.registerNonEVMAddress).toHaveBeenCalledWith(EVM, 'btc', BTC_ADDR);
  });

  it('still returns the address when the chain is dormant server-side', async () => {
    mockedApi.registerNonEVMAddress.mockRejectedValue(new Error('chain unavailable'));
    mockedApi.getNonEVMBalances.mockRejectedValue(new Error('chain unavailable'));

    const res = await loadBtcWallet(EVM);

    expect(res?.address).toBe(BTC_ADDR);
    expect(res?.balances).toEqual([]);
    // Flagged, so the wallet screen keeps its last known rows instead of zero.
    expect(res?.failed).toBe(true);
  });

  it('returns null when there is no wallet', async () => {
    mockedWallet.getActiveWallet.mockResolvedValue(null as any);
    expect(await loadBtcWallet()).toBeNull();
  });
});
