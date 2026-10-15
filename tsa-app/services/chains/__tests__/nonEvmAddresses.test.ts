import AsyncStorage from '@react-native-async-storage/async-storage';
import {
  clearNonEvmAddressCache,
  forgetNonEvmAddresses,
  getNonEvmAddresses,
} from '../nonEvmAddresses';
import {
  deriveBtcAddress,
  deriveNonEvmAddresses,
  deriveSolanaAddresses,
  deriveSuiAddress,
  deriveTronAddress,
} from '../derivation';
import { getMnemonic } from '../../wallet';

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
  removeItem: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../../wallet', () => ({ getMnemonic: jest.fn() }));
jest.mock('../derivation', () => {
  const actual = jest.requireActual('../derivation');
  return { ...actual, deriveNonEvmAddresses: jest.fn(actual.deriveNonEvmAddresses) };
});

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';
const WALLET = '0xabc0000000000000000000000000000000000001';
const KEY = `tsa-nonevm-addresses-v1-${WALLET}`;

const mockedGetItem = AsyncStorage.getItem as jest.Mock;
const mockedSetItem = AsyncStorage.setItem as jest.Mock;
const mockedMnemonic = getMnemonic as jest.Mock;
const mockedDerive = deriveNonEvmAddresses as jest.Mock;

beforeEach(() => {
  jest.clearAllMocks();
  clearNonEvmAddressCache();
  mockedGetItem.mockResolvedValue(null);
  mockedMnemonic.mockResolvedValue(M);
});

describe('deriveNonEvmAddresses', () => {
  it('matches the per-chain derivations', () => {
    const solana = deriveSolanaAddresses(M);
    expect(jest.requireActual('../derivation').deriveNonEvmAddresses(M)).toEqual({
      tron: deriveTronAddress(M),
      btc: { mainnet: deriveBtcAddress(M, 'mainnet'), testnet: deriveBtcAddress(M, 'testnet') },
      sui: deriveSuiAddress(M),
      solana,
    });
  });
});

describe('getNonEvmAddresses', () => {
  it('derives once on a cold store and persists only public addresses', async () => {
    const [a, b] = await Promise.all([getNonEvmAddresses(WALLET), getNonEvmAddresses(WALLET)]);
    expect(a).toEqual(b);
    expect(mockedDerive).toHaveBeenCalledTimes(1);
    expect(mockedSetItem).toHaveBeenCalledWith(KEY, JSON.stringify(a));
    expect(mockedSetItem.mock.calls[0][1]).not.toContain('abandon');
  });

  it('serves a stored entry without reading the mnemonic or deriving', async () => {
    const stored = jest.requireActual('../derivation').deriveNonEvmAddresses(M);
    mockedGetItem.mockResolvedValue(JSON.stringify(stored));
    await expect(getNonEvmAddresses(WALLET)).resolves.toEqual(stored);
    expect(mockedMnemonic).not.toHaveBeenCalled();
    expect(mockedDerive).not.toHaveBeenCalled();
  });

  it('re-derives over a corrupt or incomplete entry', async () => {
    mockedGetItem.mockResolvedValue(JSON.stringify({ tron: 'T...' }));
    const out = await getNonEvmAddresses(WALLET);
    expect(out?.sui).toBe(deriveSuiAddress(M));
    expect(mockedDerive).toHaveBeenCalledTimes(1);
  });

  it('returns null for a wallet with no mnemonic and stores nothing', async () => {
    mockedMnemonic.mockResolvedValue(null);
    await expect(getNonEvmAddresses(WALLET)).resolves.toBeNull();
    expect(mockedSetItem).not.toHaveBeenCalled();
  });

  it('does not write back a wallet removed while its load was in flight', async () => {
    const pending = getNonEvmAddresses(WALLET);
    await forgetNonEvmAddresses(WALLET);
    await pending;
    expect(mockedSetItem).not.toHaveBeenCalled();
  });
});
