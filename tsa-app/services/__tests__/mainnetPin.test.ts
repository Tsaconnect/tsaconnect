/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Marketplace escrow lives on Sonic MAINNET. With the app's testnet toggle on,
 * CHAINS holds the testnet map — so anything that pays the escrow must resolve
 * a mainnet provider explicitly, never the active one, and must never mutate
 * CHAINS to get it.
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn(),
  removeItem: jest.fn(),
}));
jest.mock('../../hooks/useNetwork', () => ({ getNetwork: () => 'testnet' }));

import { CHAINS, MAINNET_CHAINS, TESTNET_CHAINS, setActiveNetwork } from '../../constants/chains';
import { chainConfigFor, clearProviderCache, getMainnetProvider, getProvider } from '../wallet';

beforeEach(() => {
  setActiveNetwork('testnet');
  clearProviderCache();
});
afterAll(() => setActiveNetwork('mainnet'));

test('the mainnet accessor resolves Sonic mainnet while the toggle is on testnet', async () => {
  expect(CHAINS.sonic.chainId).toBe(TESTNET_CHAINS.sonic.chainId);
  expect(chainConfigFor('sonic', 'mainnet').chainId).toBe(146);
  expect(chainConfigFor('sonic').chainId).toBe(TESTNET_CHAINS.sonic.chainId);

  const mainnet = getMainnetProvider('sonic');
  expect((await mainnet.getNetwork()).chainId).toBe(146n);
  expect(mainnet._getConnection().url).toBe(MAINNET_CHAINS.sonic.rpcUrl);
  // The active map was not touched to get it.
  expect(CHAINS.sonic.chainId).toBe(TESTNET_CHAINS.sonic.chainId);
});

test('a mainnet provider and the active (testnet) one are never the same instance', async () => {
  const active = getProvider('sonic');
  const mainnet = getProvider('sonic', undefined, 'mainnet');
  expect(active).not.toBe(mainnet);
  expect((await active.getNetwork()).chainId).toBe(BigInt(TESTNET_CHAINS.sonic.chainId));
});
