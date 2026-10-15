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
  loadSolanaWallet,
  forgetSolanaDerivation,
  switchSolanaDerivation,
  clearSolanaCache,
} from '../solanaWallet';
import { getNonEVMBalances, registerNonEVMAddress } from '../nonevmApi';
import { deriveNonEvmAddresses } from '../derivation';
import { clearNonEvmAddressCache } from '../nonEvmAddresses';
import { getNetwork } from '../../../hooks/useNetwork';

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

// Spy on the one-seed derivation without changing what it returns.
jest.mock('../derivation', () => {
  const actual = jest.requireActual('../derivation');
  return { ...actual, deriveNonEvmAddresses: jest.fn(actual.deriveNonEvmAddresses) };
});

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

const mockedDeriveAll = deriveNonEvmAddresses as jest.Mock;
// The shared address store also writes to AsyncStorage; these tests are about
// the path decision only.
const derivationWrites = () =>
  mockedSetItem.mock.calls.filter(([key]) => String(key).startsWith('tsa-solana-derivation'));

const row = (raw: string) => ({ symbol: 'SOL', raw, decimals: 9, isNative: true });
const spl = (symbol: string, raw: string, usdPrice?: number) => ({
  symbol, raw, decimals: 6, isNative: false, tokenAddress: `mint-${symbol}`, usdPrice,
});

beforeEach(() => {
  jest.clearAllMocks();
  mockedGetItem.mockResolvedValue(null);
  mockedGetNetwork.mockReturnValue('mainnet');
  clearSolanaCache();
  clearNonEvmAddressCache();
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
    expect(derivationWrites()).toEqual([]);
  });

  it('falls back to Phantom without persisting when the scan fails', async () => {
    mockedBalances.mockRejectedValue(new Error('offline'));
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    // Not persisted and not cached — the next call retries the scan.
    expect(derivationWrites()).toEqual([]);
    mockedBalances.mockReset();
    mockedBalances.mockResolvedValueOnce([row('0')]).mockResolvedValueOnce([row('9')]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
  });

  it('skips the scan entirely off mainnet — devnet balances say nothing about real funds', async () => {
    mockedGetNetwork.mockReturnValue('testnet');
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    expect(mockedBalances).not.toHaveBeenCalled();
    expect(derivationWrites()).toEqual([]);
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
    expect(derivationWrites()).toHaveLength(1);
  });

  it('does not run a scan (which could overwrite a decision) when the pref read fails', async () => {
    mockedGetItem.mockRejectedValue(new Error('storage unavailable'));
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    expect(mockedBalances).not.toHaveBeenCalled();
    expect(derivationWrites()).toEqual([]);
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
      .mockResolvedValueOnce([row('42')]); // other-path probe: funded, rows reused
    const result = await loadSolanaWallet(WALLET);
    expect(mockedBalances).toHaveBeenCalledTimes(2);
    expect(result?.otherPath).toBeNull();
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
    expect(derivationWrites()).toEqual([]);
  });

  it('does not follow, or remember, a probe answered after a switch to testnet', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    mockedBalances
      .mockResolvedValueOnce([row('0')]) // phantom (mainnet): empty
      .mockImplementationOnce(async () => {
        // The user toggles to testnet while the probe is in flight, so this
        // answer is devnet's and says nothing about real funds.
        mockedGetNetwork.mockReturnValue('testnet');
        return [row('42')];
      });
    const result = await loadSolanaWallet(WALLET);
    expect(result?.address).toBe(PHANTOM_ADDR);
    expect(result?.otherPath).toBeNull();
    expect(derivationWrites()).toEqual([]);
    expect(mockedRegister).not.toHaveBeenCalledWith(WALLET, 'solana', TRUST_ADDR);

    // Back on mainnet the probe runs again instead of trusting the devnet read.
    mockedGetNetwork.mockReturnValue('mainnet');
    mockedBalances.mockResolvedValueOnce([row('0')]).mockResolvedValueOnce([row('42')]);
    const healed = await loadSolanaWallet(WALLET);
    expect(healed?.address).toBe(TRUST_ADDR);
  });

  it('does not memoize an empty probe answered from devnet', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    mockedBalances
      .mockResolvedValueOnce([row('10')]) // phantom (mainnet): funded
      .mockImplementationOnce(async () => {
        mockedGetNetwork.mockReturnValue('testnet');
        return []; // devnet: the other path looks empty
      });
    await loadSolanaWallet(WALLET);

    mockedGetNetwork.mockReturnValue('mainnet');
    mockedBalances.mockResolvedValueOnce([row('10')]).mockResolvedValueOnce([row('20')]);
    const again = await loadSolanaWallet(WALLET);
    expect(again?.otherPath).toMatchObject({ derivation: 'trustwallet', address: TRUST_ADDR });
  });
});

describe('getSolanaSigner', () => {
  const pubOf = (key: string) => base58.encode(ed25519.getPublicKey(key));

  it('returns the key pairing with the resolved Trust Wallet address', async () => {
    mockedGetItem.mockResolvedValue('trustwallet');
    const signer = await getSolanaSigner(WALLET);
    expect(signer?.key).toMatch(/^[0-9a-f]{64}$/);
    expect(signer?.address).toBe(TRUST_ADDR);
    expect(pubOf(signer!.key)).toBe(TRUST_ADDR);
  });

  it('returns the Phantom-path key for a stored phantom preference', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    const signer = await getSolanaSigner(WALLET);
    expect(pubOf(signer!.key)).toBe(PHANTOM_ADDR);
  });

  it('pairs with the address a live scan resolved — no rescan, no divergence', async () => {
    // Scan decides trustwallet; the signing key must follow that decision from
    // cache rather than re-resolving (which could flip mid-flow).
    mockedBalances.mockResolvedValueOnce([]).mockResolvedValueOnce([row('9')]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
    const scanCalls = mockedBalances.mock.calls.length;
    const signer = await getSolanaSigner(WALLET);
    expect(pubOf(signer!.key)).toBe(TRUST_ADDR);
    expect(mockedBalances.mock.calls.length).toBe(scanCalls);
  });

  it('refuses to sign with a stand-in address when the scan failed', async () => {
    mockedBalances.mockRejectedValue(new Error('rpc down'));
    await expect(getSolanaSigner(WALLET)).rejects.toThrow("Couldn't confirm your Solana address");
  });

  it('refuses to sign when the network changed mid-scan', async () => {
    mockedBalances.mockImplementation(async () => {
      mockedGetNetwork.mockReturnValue('testnet');
      return [row('5')];
    });
    await expect(getSolanaSigner(WALLET)).rejects.toThrow("Couldn't confirm your Solana address");
    expect(derivationWrites()).toEqual([]);
  });

  it('signs on testnet with the standard path, without a scan', async () => {
    mockedGetNetwork.mockReturnValue('testnet');
    const signer = await getSolanaSigner(WALLET);
    expect(signer?.address).toBe(PHANTOM_ADDR);
    expect(mockedBalances).not.toHaveBeenCalled();
  });
});

describe('seed derivation cost', () => {
  it('derives the seed once for both paths across scan, load and probe', async () => {
    // Scan (both paths), display, other-path probe, then a second load's display.
    mockedBalances.mockResolvedValue([]);
    await loadSolanaWallet(WALLET);
    await loadSolanaWallet(WALLET);
    expect(mockedDeriveAll).toHaveBeenCalledTimes(1);
  });
});

describe('funds detection', () => {
  it('ignores an unpriced spam token when choosing the path', async () => {
    mockedBalances
      .mockResolvedValueOnce([spl('Spam…Drop', '1000000')]) // phantom: airdrop only
      .mockResolvedValueOnce([row('77')]); // trust wallet: real SOL
    await expect(getSolanaAddress(WALLET)).resolves.toBe(TRUST_ADDR);
  });

  it('counts a stablecoin even when the price feed attached no price', async () => {
    mockedBalances.mockResolvedValueOnce([spl('USDC', '5000000')]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    expect(mockedBalances).toHaveBeenCalledTimes(1);
  });

  it('counts a priced token', async () => {
    mockedBalances.mockResolvedValueOnce([spl('JUP', '5000000', 0.8)]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    expect(mockedBalances).toHaveBeenCalledTimes(1);
  });
});

describe('backend registration', () => {
  it('does not register a stand-in address when the scan fails', async () => {
    mockedBalances.mockRejectedValue(new Error('rpc down'));
    const result = await loadSolanaWallet(WALLET);
    expect(result?.address).toBe(PHANTOM_ADDR);
    expect(mockedRegister).not.toHaveBeenCalled();
  });

  it('registers a settled decision', async () => {
    mockedGetItem.mockResolvedValue('trustwallet');
    mockedBalances.mockResolvedValue([row('1')]);
    await loadSolanaWallet(WALLET);
    expect(mockedRegister).toHaveBeenCalledWith(WALLET, 'solana', TRUST_ADDR);
  });

  it('does not persist a decision for a wallet removed mid-scan', async () => {
    let release!: (v: unknown) => void;
    mockedBalances.mockImplementationOnce(() => new Promise((r) => { release = r; }));
    const pending = getSolanaAddress(WALLET);
    while (mockedBalances.mock.calls.length === 0) await new Promise((r) => setImmediate(r));
    await forgetSolanaDerivation(WALLET);
    release([row('5')]);
    await pending;
    expect(derivationWrites()).toEqual([]);
  });
});

describe('both paths funded', () => {
  it('keeps the current path and reports the other one instead of hiding it', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    mockedBalances
      .mockResolvedValueOnce([row('10')]) // phantom: funded
      .mockResolvedValueOnce([row('20')]); // trust wallet: funded too
    const result = await loadSolanaWallet(WALLET);
    expect(result?.address).toBe(PHANTOM_ADDR);
    expect(result?.otherPath).toMatchObject({ wallet: WALLET, derivation: 'trustwallet', address: TRUST_ADDR });
    expect(result?.otherPath?.balances[0]?.balance).toBe('0.00000002');
    expect(derivationWrites()).toEqual([]);

    // A reload in the same session keeps reporting it without probing again.
    mockedBalances.mockResolvedValueOnce([row('10')]);
    const again = await loadSolanaWallet(WALLET);
    expect(again?.otherPath?.address).toBe(TRUST_ADDR);
    expect(mockedBalances).toHaveBeenCalledTimes(3);
  });

  it('switchSolanaDerivation persists, registers, and re-probes the path it left', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    mockedBalances.mockResolvedValueOnce([row('10')]).mockResolvedValueOnce([row('20')]);
    await loadSolanaWallet(WALLET);

    await expect(switchSolanaDerivation(WALLET, 'trustwallet')).resolves.toBe(TRUST_ADDR);
    expect(mockedSetItem).toHaveBeenCalledWith(PREF_KEY, 'trustwallet');
    expect(mockedRegister).toHaveBeenCalledWith(WALLET, 'solana', TRUST_ADDR);

    mockedBalances
      .mockResolvedValueOnce([row('20')]) // trust wallet: now current
      .mockResolvedValueOnce([row('10')]); // phantom: the path it left, still funded
    const after = await loadSolanaWallet(WALLET);
    expect(after?.address).toBe(TRUST_ADDR);
    expect(after?.otherPath).toMatchObject({ derivation: 'phantom', address: PHANTOM_ADDR });
  });

  it('switchSolanaDerivation changes nothing locally when the backend registration fails', async () => {
    mockedGetItem.mockResolvedValue('phantom');
    mockedBalances.mockResolvedValueOnce([row('10')]).mockResolvedValueOnce([row('20')]);
    await loadSolanaWallet(WALLET);

    mockedRegister.mockRejectedValueOnce(new Error('network down'));
    await expect(switchSolanaDerivation(WALLET, 'trustwallet')).rejects.toThrow('network down');
    // The app keeps showing, signing for, and reporting the other path exactly as
    // before, so the "Switch failed" alert is true and the backend still agrees.
    expect(derivationWrites()).toEqual([]);
    await expect(getSolanaAddress(WALLET)).resolves.toBe(PHANTOM_ADDR);
    mockedBalances.mockResolvedValueOnce([row('10')]);
    const after = await loadSolanaWallet(WALLET);
    expect(after?.otherPath).toMatchObject({ derivation: 'trustwallet', address: TRUST_ADDR });
  });
});
