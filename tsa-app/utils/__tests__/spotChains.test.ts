/* eslint-disable import/first -- the mock is written above the imports it
   affects; jest hoists it either way, but the order documents the dependency. */

// spotChains pulls the LiFi chain-id constants from services/lifi, which
// reaches AsyncStorage at module scope. Mocked per-file (jest hoists this above
// the imports below), matching the other util tests.
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(), setItem: jest.fn(), removeItem: jest.fn() },
}));

import {
  resolveSpotChain, isBitcoinSpotChain, unsupportedChainMessage, singleParam, matchSpotBalance,
  type SpotBalanceRow,
} from '../spotChains';
import { SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID, SOLANA_NATIVE_ADDRESS } from '@/services/lifi';
import { CHAINS } from '@/constants/chains';

describe('resolveSpotChain', () => {
  it('resolves EVM chains to their numeric chain id', () => {
    expect(resolveSpotChain('sonic')).toEqual({
      lifiChainId: CHAINS.sonic.chainId, name: CHAINS.sonic.name, family: 'evm', evmKey: 'sonic',
    });
    expect(resolveSpotChain('bsc')?.lifiChainId).toBe(CHAINS.bsc.chainId);
  });

  it('resolves Robinhood Chain, which the admin picker also lists', () => {
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

  // Defence in depth only. A repeated query param is stopped at the edge by
  // singleParam, because by the time it reaches the resolver the render loop it
  // causes has already been set up — see the singleParam tests below.
  it('refuses a non-string key, whatever shape it arrives in', () => {
    expect(resolveSpotChain(['sonic', 'bsc'] as never)).toBeNull();
    expect(resolveSpotChain(42 as never)).toBeNull();
    expect(resolveSpotChain({} as never)).toBeNull();
  });

  it('is case- and whitespace-insensitive, since the key crosses an API', () => {
    const sonic = resolveSpotChain('  SONIC ');
    expect(sonic?.family === 'evm' && sonic.evmKey).toBe('sonic');
    expect(resolveSpotChain('Solana')?.family).toBe('solana');
  });
});

describe('singleParam', () => {
  it('passes a scalar through unchanged', () => {
    expect(singleParam('solana')).toBe('solana');
    expect(singleParam(undefined)).toBe('');
  });

  it('collapses a repeated param rather than guessing which copy was meant', () => {
    expect(singleParam(['solana', 'sui'])).toBe('');
    expect(singleParam([])).toBe('');
    // A single-element array is still a repeated-param shape, and still
    // ambiguous enough not to trust — it is not evidence of intent.
    expect(singleParam(['solana'])).toBe('');
  });

  /**
   * The render loop, at the layer it actually happens.
   *
   * expo-router rebuilds its params object — and any array inside it — on every
   * render, so `params.chain` for `?chain=solana&chain=sui` is a NEW array each
   * time even though the URL never changed. React compares dependency lists with
   * Object.is, so an array dependency never settles: the memo recomputes, the
   * setup callback is rebuilt, the effect re-fires, state flips, and the screen
   * spins forever instead of showing the unsupported-network card.
   *
   * Normalising to a string is what breaks the cycle, and this is the property
   * that does it: the same URL must produce a dependency that compares equal
   * across renders. It holds for the repeated param and for the scalar alike.
   */
  it('yields a dependency that compares equal across renders of the same URL', () => {
    const renderParams = () => ({ chain: ['solana', 'sui'] } as { chain: string[] });

    const first = renderParams();
    const second = renderParams();
    // Precondition: the raw param really does churn, or this proves nothing.
    expect(Object.is(first.chain, second.chain)).toBe(false);

    expect(Object.is(singleParam(first.chain), singleParam(second.chain))).toBe(true);
    expect(Object.is(singleParam('sonic'), singleParam('sonic'))).toBe(true);
  });
});

describe('unsupportedChainMessage', () => {
  it('tells the Bitcoin case apart, since it is a routing limit not a wallet gap', () => {
    expect(unsupportedChainMessage('btc')).toContain('All Swap');
    expect(unsupportedChainMessage('tron')).not.toContain('All Swap');
    expect(unsupportedChainMessage('')).not.toContain('All Swap');
  });
});

describe('matchSpotBalance', () => {
  const row = (over: Partial<SpotBalanceRow>): SpotBalanceRow =>
    ({ symbol: 'USDC', balance: '0', contractAddress: '', ...over });

  const REAL_SUI_USDC = '0xdba34672e30cb065b1f93e3ab55318768fd6fef66c15942c9f7cb846e2f900e7::usdc::USDC';
  const IMPOSTOR_SUI_USDC = '0xbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbad0::usdc::USDC';

  describe('sui', () => {
    it('matches the leg by coin type, not by the symbol printed on it', () => {
      const rows = [
        row({ symbol: 'USDC', balance: '1000', contractAddress: IMPOSTOR_SUI_USDC }),
        row({ symbol: 'USDC', balance: '25', contractAddress: REAL_SUI_USDC }),
      ];
      expect(matchSpotBalance('sui', rows, { symbol: 'USDC', address: REAL_SUI_USDC })).toBe('25');
    });

    // The whole point of Fix 2: anyone can publish a coin called USDC. Reading
    // its balance as the real leg's shows funds the user does not have and lets
    // Max size a trade that can only fail once it reaches the chain.
    it('reads zero when the wallet holds only an impostor coin of the same symbol', () => {
      const rows = [row({ symbol: 'USDC', balance: '1000', contractAddress: IMPOSTOR_SUI_USDC })];
      expect(matchSpotBalance('sui', rows, { symbol: 'USDC', address: REAL_SUI_USDC })).toBe('0');
    });

    it('falls back to symbol for the native row, the one row with no coin type', () => {
      const rows = [
        row({ symbol: 'SUI', balance: '7.5', contractAddress: '' }),
        row({ symbol: 'USDC', balance: '1000', contractAddress: IMPOSTOR_SUI_USDC }),
      ];
      expect(matchSpotBalance('sui', rows, { symbol: 'SUI', address: '0x2::sui::SUI' })).toBe('7.5');
    });

    it('ignores hex case, which differs between the sources a coin type comes from', () => {
      const rows = [row({ symbol: 'USDC', balance: '25', contractAddress: REAL_SUI_USDC.toUpperCase() })];
      expect(matchSpotBalance('sui', rows, { symbol: 'USDC', address: REAL_SUI_USDC })).toBe('25');
    });

    it('reads zero for a leg the wallet does not hold at all', () => {
      expect(matchSpotBalance('sui', [], { symbol: 'USDC', address: REAL_SUI_USDC })).toBe('0');
    });
  });

  describe('solana', () => {
    const REAL_USDC_MINT = 'EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v';
    const IMPOSTOR_USDC_MINT = 'BADBADBADBADBADBADBADBADBADBADBADBADBADBADBA';

    it('matches the leg by mint, not by the symbol printed on it', () => {
      const rows = [
        row({ symbol: 'USDC', balance: '1000', contractAddress: IMPOSTOR_USDC_MINT }),
        row({ symbol: 'USDC', balance: '25', contractAddress: REAL_USDC_MINT }),
      ];
      expect(matchSpotBalance('solana', rows, { symbol: 'USDC', address: REAL_USDC_MINT })).toBe('25');
    });

    it('reads zero when the wallet holds only an impostor mint of the same symbol', () => {
      const rows = [row({ symbol: 'USDC', balance: '1000', contractAddress: IMPOSTOR_USDC_MINT })];
      expect(matchSpotBalance('solana', rows, { symbol: 'USDC', address: REAL_USDC_MINT })).toBe('0');
    });

    // Base58 is case-significant, so two mints differing only in case are two
    // different mints — the Sui hex normalisation must not leak over here.
    it('keeps mints that differ only in case apart', () => {
      const rows = [row({ symbol: 'USDC', balance: '1000', contractAddress: REAL_USDC_MINT.toUpperCase() })];
      expect(matchSpotBalance('solana', rows, { symbol: 'USDC', address: REAL_USDC_MINT })).toBe('0');
    });

    it('matches native SOL to the row that carries no mint', () => {
      const rows = [
        row({ symbol: 'USDC', balance: '1000', contractAddress: REAL_USDC_MINT }),
        row({ symbol: 'SOL', balance: '2.25', contractAddress: '' }),
      ];
      expect(matchSpotBalance('solana', rows, { symbol: 'SOL', address: SOLANA_NATIVE_ADDRESS })).toBe('2.25');
    });
  });

  it('refuses a family it was never taught, rather than guessing a rule', () => {
    expect(() => matchSpotBalance('aptos' as never, [], { symbol: 'USDC', address: '0x1' }))
      .toThrow(/Unhandled spot chain family/);
  });
});
