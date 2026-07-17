// Derivation-path resolution for imported seeds: TSA uses the Phantom path
// (m/44'/501'/0'/0') but Trust Wallet derives at m/44'/501'/0', so the same
// seed shows a different SOL address there. On first load the wallet scans
// both paths' balances and picks where the funds actually are, then persists
// the choice per wallet so address and signing key never disagree.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { ed25519 } from '@noble/curves/ed25519';
import { base58 } from '@scure/base';
import {
  getSolanaAddress,
  getSolanaSigner,
  getSolanaSigningKey,
  loadSolanaWallet,
  forgetSolanaDerivation,
  clearSolanaCache,
} from '../solanaWallet';
import { getNonEVMBalances, registerNonEVMAddress } from '../nonevmApi';
import { getNetwork } from '../../../hooks/useNetwork';

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';
// Phantom path m/44'/501'/0'/0' and Trust Wallet path m/44'/501'/0' addresses
// for the canonical mnemonic (see derivation.test.ts for vector provenance).
const PHANTOM_ADDR = 'HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk';
const TRUST_ADDR = 'GjJyeC1r2RgkuoCWMyPYkCWSGSGLcz266EaAkLA27AhL';
const WALLET = '0xWallet1';
const PREF_KEY = `tsa-solana-derivation-${WALLET}`;

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
  removeItem: jest.fn().mockResolvedValue(undefined),
}));

jest.mock('../../wallet', () => ({
  getActiveWallet: jest.fn().mockResolvedValue('0xWallet1'),
  getMnemonic: jest.fn().mockResolvedValue(
    'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about'
  ),
}));

jest.mock('../nonevmApi', () => ({
  getNonEVMBalances: jest.fn(),
  registerNonEVMAddress: jest.fn().mockResolvedValue(undefined),
}));

jest.mock('../../../hooks/useNetwork', () => ({
  getNetwork: jest.fn(() => 'mainnet'),
}));

const mockedBalances = getNonEVMBalances as jest.Mock;
const mockedRegister = registerNonEVMAddress as jest.Mock;
const mockedGetItem = AsyncStorage.getItem as jest.Mock;
const mockedSetItem = AsyncStorage.setItem as jest.Mock;
const mockedGetNetwork = getNetwork as jest.Mock;

const row = (raw: string) => ({ symbol: 'SOL', raw, decimals: 9, isNative: true });

beforeEach(() => {
  jest.clearAllMocks();
  mockedGetItem.mockResolvedValue(null);
  mockedGetNetwork.mockReturnValue('mainnet');
  clearSolanaCache();
});

describe('getSolanaAddress derivation resolution', () => {
  it('uses the Phantom path when the standard address has funds', async () => {
    mockedBalances.mockResolvedValueOnce([row('5000')]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    // Decisive scan → persisted; legacy path never queried.
    expect(mockedBalances).toHaveBeenCalledTimes(1);
    expect(mockedBalances).toHaveBeenCalledWith('solana', PHANTOM_ADDR);
    expect(mockedSetItem).toHaveBeenCalledWith(PREF_KEY, 'phantom');
  });

  it('falls back to the Trust Wallet path when only it has funds', async () => {
    mockedBalances
      .mockResolvedValueOnce([row('0')]) // Phantom path: empty
      .mockResolvedValueOnce([row('123')]); // Trust Wallet path: funded
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
    expect(mockedBalances).toHaveBeenNthCalledWith(2, 'solana', TRUST_ADDR);
    expect(mockedSetItem).toHaveBeenCalledWith(PREF_KEY, 'trustwallet');
  });

  it('defaults to the Phantom path when neither address has funds', async () => {
    mockedBalances.mockResolvedValue([]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    expect(mockedSetItem).toHaveBeenCalledWith(PREF_KEY, 'phantom');
  });

  it('honors a stored preference without scanning', async () => {
    mockedGetItem.mockResolvedValue('trustwallet');
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
    expect(mockedBalances).not.toHaveBeenCalled();
    expect(mockedSetItem).not.toHaveBeenCalled();
  });

  it('falls back to Phantom without persisting when the scan fails', async () => {
    mockedBalances.mockRejectedValue(new Error('offline'));
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    // Not persisted and not cached — the next call retries the scan.
    expect(mockedSetItem).not.toHaveBeenCalled();
    mockedBalances.mockReset();
    mockedBalances.mockResolvedValueOnce([row('0')]).mockResolvedValueOnce([row('9')]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
  });

  it('skips the scan entirely off mainnet — devnet balances say nothing about real funds', async () => {
    mockedGetNetwork.mockReturnValue('testnet');
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    expect(mockedBalances).not.toHaveBeenCalled();
    expect(mockedSetItem).not.toHaveBeenCalled();
    // A stored preference is still honored on testnet.
    mockedGetItem.mockResolvedValue('trustwallet');
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
  });

  it('shares one scan between concurrent cold-cache callers', async () => {
    mockedBalances.mockResolvedValueOnce([row('5000')]);
    const [a, b] = await Promise.all([getSolanaAddress(WALLET), getSolanaAddress(WALLET)]);
    expect(a).toBe(PHANTOM_ADDR);
    expect(b).toBe(PHANTOM_ADDR);
    expect(mockedBalances).toHaveBeenCalledTimes(1);
    expect(mockedSetItem).toHaveBeenCalledTimes(1);
  });

  it('does not run a scan (which could overwrite a decision) when the pref read fails', async () => {
    mockedGetItem.mockRejectedValue(new Error('storage unavailable'));
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    expect(mockedBalances).not.toHaveBeenCalled();
    expect(mockedSetItem).not.toHaveBeenCalled();
  });

  it('forgetSolanaDerivation drops the persisted pref and cached decision', async () => {
    mockedGetItem.mockResolvedValue('trustwallet');
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
    await forgetSolanaDerivation(WALLET);
    expect(AsyncStorage.removeItem).toHaveBeenCalledWith(PREF_KEY);
    mockedGetItem.mockResolvedValue(null);
    mockedBalances.mockResolvedValueOnce([row('7')]);
    // Cache is gone → resolves fresh from a new scan.
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
  });
});

describe('loadSolanaWallet path self-healing', () => {
  it('follows the funds to the other path when the resolved path is empty', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    mockedBalances
      .mockResolvedValueOnce([row('0')]) // phantom balances: empty
      .mockResolvedValueOnce([row('42')]) // other-path probe: funded
      .mockResolvedValueOnce([row('42')]); // re-fetch for display rows
    const result = await loadSolanaWallet(WALLET);
    expect(result?.address).toBe(TRUST_ADDR);
    expect(result?.balances[0]?.balance).toBe('0.000000042');
    expect(mockedSetItem).toHaveBeenCalledWith(PREF_KEY, 'trustwallet');
    expect(mockedRegister).toHaveBeenCalledWith(WALLET, 'solana', TRUST_ADDR);
    // The flip sticks: subsequent address lookups return the healed path.
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
  });

  it('keeps the resolved path when the other path is empty too, and probes only once per session', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    mockedBalances.mockResolvedValue([]);
    const first = await loadSolanaWallet(WALLET);
    expect(first?.address).toBe(PHANTOM_ADDR);
    const callsAfterFirst = mockedBalances.mock.calls.length;
    expect(callsAfterFirst).toBe(2); // display fetch + one other-path probe
    const second = await loadSolanaWallet(WALLET);
    expect(second?.address).toBe(PHANTOM_ADDR);
    expect(mockedBalances.mock.calls.length).toBe(callsAfterFirst + 1); // no second probe
    expect(mockedSetItem).not.toHaveBeenCalled();
  });
});

describe('getSolanaSigningKey', () => {
  it('returns the key pairing with the resolved Trust Wallet address', async () => {
    mockedGetItem.mockResolvedValue('trustwallet');
    const key = await getSolanaSigningKey(WALLET);
    expect(key).toMatch(/^[0-9a-f]{64}$/);
    expect(base58.encode(ed25519.getPublicKey(key!))).toBe(TRUST_ADDR);
  });

  it('returns the Phantom-path key for a stored phantom preference', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    const key = await getSolanaSigningKey(WALLET);
    expect(base58.encode(ed25519.getPublicKey(key!))).toBe(PHANTOM_ADDR);
  });

  it('pairs with the address a live scan resolved — no rescan, no divergence', async () => {
    // Scan decides trustwallet; the signing key must follow that decision from
    // cache rather than re-resolving (which could flip mid-flow).
    mockedBalances.mockResolvedValueOnce([]).mockResolvedValueOnce([row('9')]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
    const scanCalls = mockedBalances.mock.calls.length;
    const key = await getSolanaSigningKey(WALLET);
    expect(base58.encode(ed25519.getPublicKey(key!))).toBe(TRUST_ADDR);
    expect(mockedBalances.mock.calls.length).toBe(scanCalls);
  });

  it('getSolanaSigner returns a matching address/key pair from one resolution', async () => {
    mockedGetItem.mockResolvedValue('trustwallet');
    const signer = await getSolanaSigner(WALLET);
    expect(signer?.address).toBe(TRUST_ADDR);
    expect(base58.encode(ed25519.getPublicKey(signer!.key))).toBe(TRUST_ADDR);
  });
});
