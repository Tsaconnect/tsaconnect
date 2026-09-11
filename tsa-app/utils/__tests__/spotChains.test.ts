/* eslint-disable import/first -- the mock is written above the imports it
   affects; jest hoists it either way, but the order documents the dependency. */

// spotChains pulls the LiFi chain-id constants from services/lifi, which
// reaches AsyncStorage at module scope. Mocked per-file (jest hoists this above
// the imports below), matching the other util tests.
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(), setItem: jest.fn(), removeItem: jest.fn() },
}));

import { resolveSpotChain, isBitcoinSpotChain } from '../spotChains';
import { SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID } from '@/services/lifi';
import { CHAINS } from '@/constants/chains';

describe('resolveSpotChain', () => {
  it('resolves EVM chains to their numeric chain id', () => {
    expect(resolveSpotChain('sonic')).toEqual({
      lifiChainId: CHAINS.sonic.chainId, name: CHAINS.sonic.name, family: 'evm', evmKey: 'sonic',
    });
    expect(resolveSpotChain('bsc')?.lifiChainId).toBe(CHAINS.bsc.chainId);
  });

  it('resolves Robinhood Chain, which the app supports but the picker omitted', () => {
    expect(resolveSpotChain('robinhood')?.lifiChainId).toBe(4663);
  });

  it('resolves Solana and Sui to their LiFi pseudo chain ids', () => {
    expect(resolveSpotChain('solana')).toEqual({
      lifiChainId: SOLANA_LIFI_CHAIN_ID, name: 'Solana', family: 'solana', nonEvmKey: 'solana',
    });
    expect(resolveSpotChain('sui')).toEqual({
      lifiChainId: SUI_LIFI_CHAIN_ID, name: 'Sui', family: 'sui', nonEvmKey: 'sui',
    });
  });

  it('refuses Bitcoin — it carries one asset, so no same-chain pair exists', () => {
    expect(resolveSpotChain('btc')).toBeNull();
    expect(isBitcoinSpotChain('btc')).toBe(true);
    expect(isBitcoinSpotChain('solana')).toBe(false);
  });

  it('refuses Tron, which LiFi does not route at all', () => {
    expect(resolveSpotChain('tron')).toBeNull();
  });

  it('refuses unknown, empty and nullish chains rather than guessing', () => {
    expect(resolveSpotChain('aptos')).toBeNull();
    expect(resolveSpotChain('')).toBeNull();
    expect(resolveSpotChain(undefined)).toBeNull();
    expect(resolveSpotChain(null)).toBeNull();
  });

  // The key arrives from the API and from a deep link, so it is untrusted.
  it('refuses inherited Object keys, which a plain-object lookup would resolve', () => {
    expect(resolveSpotChain('constructor')).toBeNull();
    expect(resolveSpotChain('valueOf')).toBeNull();
    expect(resolveSpotChain('hasOwnProperty')).toBeNull();
    expect(resolveSpotChain('__proto__')).toBeNull();
  });

  it('refuses a repeated query param, which expo-router hands over as an array', () => {
    expect(resolveSpotChain(['sonic', 'bsc'] as never)).toBeNull();
    expect(resolveSpotChain(42 as never)).toBeNull();
    expect(resolveSpotChain({} as never)).toBeNull();
  });

  it('is case- and whitespace-insensitive, since the key crosses an API', () => {
    expect(resolveSpotChain('  SONIC ')?.evmKey).toBe('sonic');
    expect(resolveSpotChain('Solana')?.family).toBe('solana');
  });
});
