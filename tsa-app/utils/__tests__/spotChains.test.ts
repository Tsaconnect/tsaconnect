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
  resolveSpotChain, resolveSpotLegChain, resolveLiFiChainId, isBitcoinSpotChain, unsupportedChainMessage,
  singleParam, matchSpotBalance, spotLegLookup, isPinnedLegToken, planQuoteLeg, spotMaxAmount,
  type SpotBalanceRow, type SpotChain,
} from '../spotChains';
import {
  SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID, SOLANA_NATIVE_ADDRESS, TRON_LIFI_CHAIN_ID, TRON_NATIVE_ADDRESS,
  BTC_LIFI_CHAIN_ID,
} from '@/services/lifi';
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

  it('resolves Tron, which LiFi has routed since 2026-10', () => {
    expect(resolveSpotChain('tron')).toMatchObject({ lifiChainId: TRON_LIFI_CHAIN_ID, family: 'tron', nonEvmKey: 'tron' });
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

describe('resolveSpotLegChain', () => {
  it('accepts Bitcoin as one leg, which resolveSpotChain refuses as a whole pair', () => {
    expect(resolveSpotChain('btc')).toBeNull();
    expect(resolveSpotLegChain('btc')).toMatchObject({ lifiChainId: BTC_LIFI_CHAIN_ID, family: 'btc' });
    expect(resolveLiFiChainId('btc')).toBe(BTC_LIFI_CHAIN_ID);
  });

  it('agrees with resolveSpotChain on every chain that can hold a pair', () => {
    for (const key of ['sonic', 'bsc', 'solana', 'sui', 'tron']) {
      expect(resolveSpotLegChain(key)).toEqual(resolveSpotChain(key));
    }
    expect(resolveSpotLegChain('aptos')).toBeNull();
  });
});

describe('Tron token pinning', () => {
  const tron = resolveSpotChain('tron') as SpotChain;
  const bsc = resolveSpotChain('bsc') as SpotChain;
  const REAL_USDT = 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t';

  it('looks a Tron leg up by its pinned address, never by symbol', () => {
    expect(spotLegLookup(tron, 'usdt')).toBe(REAL_USDT);
    expect(spotLegLookup(tron, 'TRX')).toBe(TRON_NATIVE_ADDRESS);
  });

  // LiFi's only "USDC" on Tron is an unverified 18-decimal token.
  it('refuses a Tron symbol it has no pin for', () => {
    expect(spotLegLookup(tron, 'USDC')).toBeNull();
  });

  it('rejects an impostor USDT LiFi might hand back', () => {
    expect(isPinnedLegToken(tron, 'USDT', REAL_USDT)).toBe(true);
    expect(isPinnedLegToken(tron, 'USDT', 'TXenxWkADkZPE2gVvMydhjNfJVg7rp92Pz')).toBe(false);
    expect(isPinnedLegToken(tron, 'USDT', undefined)).toBe(false);
  });

  it('leaves families without pins to the symbol lookup', () => {
    expect(spotLegLookup(bsc, 'USDT')).toBe('USDT');
    expect(isPinnedLegToken(bsc, 'USDT', '0xanything')).toBe(true);
  });
});

describe('unsupportedChainMessage', () => {
  it('tells the Bitcoin case apart, since it is a property of the chain not a wallet gap', () => {
    expect(unsupportedChainMessage('btc')).toContain('another network');
    expect(unsupportedChainMessage('')).not.toContain('another network');
  });
});

describe('matchSpotBalance', () => {
  const row = (over: Partial<SpotBalanceRow>): SpotBalanceRow =>
    ({ symbol: 'USDC', balance: '0', contractAddress: '', ...over });

  const REAL_SUI_USDC = '0xdba34672e30cb065b1f93e3ab55318768fd6fef66c15942c9f7cb846e2f900e7::usdc::USDC';
  const IMPOSTOR_SUI_USDC = '0xbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbad0::usdc::USDC';

  describe('tron', () => {
    const REAL_USDT = 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t';
    const rows = [
      row({ symbol: 'TRX', balance: '12', contractAddress: '' }),
      row({ symbol: 'USDT', balance: '40', contractAddress: REAL_USDT }),
      row({ symbol: 'USDT', balance: '9999', contractAddress: 'TXenxWkADkZPE2gVvMydhjNfJVg7rp92Pz' }),
    ];

    it('matches a TRC-20 leg by its exact contract', () => {
      expect(matchSpotBalance('tron', rows, { symbol: 'USDT', address: REAL_USDT })).toBe('40');
    });

    it('matches native TRX to the row with no contract', () => {
      expect(matchSpotBalance('tron', rows, { symbol: 'TRX', address: TRON_NATIVE_ADDRESS })).toBe('12');
    });

    it('does not match base58 case-insensitively', () => {
      expect(matchSpotBalance('tron', rows, { symbol: 'USDT', address: REAL_USDT.toLowerCase() })).toBe('0');
    });
  });

  describe('btc', () => {
    it('reads the one BTC row', () => {
      expect(matchSpotBalance('btc', [row({ symbol: 'BTC', balance: '0.5' })], { symbol: 'BTC', address: 'bitcoin' }))
        .toBe('0.5');
    });
  });

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

    // The gap the first attempt left: the fallback asked "does this ROW carry a
    // coin type", a question that never looks at the leg, so a wrapped or
    // spoofed SUI in LiFi's list — which the screen picks by first symbol match
    // — still captured the user's real native balance by symbol.
    it('denies a non-native coin called SUI the native row, however it is spelled', () => {
      const rows = [row({ symbol: 'SUI', balance: '7.5', contractAddress: '' })];
      const fakeSui = '0xbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbad0::sui::SUI';
      expect(matchSpotBalance('sui', rows, { symbol: 'SUI', address: fakeSui })).toBe('0');
      // Same package as native, different module: still not the native coin.
      expect(matchSpotBalance('sui', rows, { symbol: 'SUI', address: '0x2::wsui::SUI' })).toBe('0');
    });

    it('accepts both spellings of native SUI, short and zero-padded', () => {
      const rows = [row({ symbol: 'SUI', balance: '7.5', contractAddress: '' })];
      const padded = `0x${'0'.repeat(63)}2::sui::SUI`;
      expect(matchSpotBalance('sui', rows, { symbol: 'SUI', address: padded })).toBe('7.5');
      expect(matchSpotBalance('sui', rows, { symbol: 'SUI', address: '0x2::sui::SUI' })).toBe('7.5');
    });

    it('ignores hex case, which differs between the sources a coin type comes from', () => {
      const rows = [row({ symbol: 'USDC', balance: '25', contractAddress: REAL_SUI_USDC.toUpperCase() })];
      expect(matchSpotBalance('sui', rows, { symbol: 'USDC', address: REAL_SUI_USDC })).toBe('25');
    });

    // Zero-padding is presentational on Sui, so a holding must not read zero
    // just because LiFi and the RPC wrote the same package address differently.
    it('ignores leading-zero padding, which the two sources also disagree on', () => {
      const short = '0x2a::usdc::USDC';
      const padded = `0x${'0'.repeat(62)}2a::usdc::USDC`;
      expect(matchSpotBalance('sui', [row({ balance: '25', contractAddress: padded })],
        { symbol: 'USDC', address: short })).toBe('25');
      expect(matchSpotBalance('sui', [row({ balance: '25', contractAddress: short })],
        { symbol: 'USDC', address: padded })).toBe('25');
    });

    it('reads zero for a leg LiFi returned with no address, rather than throwing', () => {
      const rows = [row({ symbol: 'SUI', balance: '7.5', contractAddress: '' })];
      expect(matchSpotBalance('sui', rows, { symbol: 'SUI' })).toBe('0');
      expect(matchSpotBalance('sui', rows, { symbol: 'SUI', address: '  ' })).toBe('0');
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

    // Same guard as Sui's: an id-less leg must not collide with the id-less row.
    it('reads zero for a leg LiFi returned with no mint, not the native row', () => {
      const rows = [row({ symbol: 'SOL', balance: '2.25', contractAddress: '' })];
      expect(matchSpotBalance('solana', rows, { symbol: 'SOL' })).toBe('0');
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

describe('planQuoteLeg', () => {
  it('keeps a same-chain pair on its chain', () => {
    expect(planQuoteLeg('bsc', '', false)).toEqual({ key: 'bsc' });
  });

  it('honours a quote chain the admin named', () => {
    expect(planQuoteLeg('btc', 'base', false)).toEqual({ key: 'base' });
  });

  it('follows the balance on auto, falling back to the named chain, else the base chain', () => {
    expect(planQuoteLeg('btc', 'bsc', true)).toEqual({ funded: true, fallback: 'bsc' });
    expect(planQuoteLeg('ethereum', '', true)).toEqual({ funded: true, fallback: 'ethereum' });
    // Bitcoin cannot hold the quote leg, so there is no base-chain fallback.
    expect(planQuoteLeg('btc', '', true)).toEqual({ funded: true, fallback: undefined });
  });

  // The balance read only sees EVM chains: a Tron USDT holder would be sent
  // across chains from whatever dust they keep on BSC.
  it('keeps auto on a non-EVM base chain that can carry the pair', () => {
    expect(planQuoteLeg('tron', 'bsc', true)).toEqual({ key: 'tron' });
    expect(planQuoteLeg('solana', '', true)).toEqual({ key: 'solana' });
  });
});

describe('spotMaxAmount', () => {
  const tron = resolveSpotChain('tron') as SpotChain;
  const bsc = resolveSpotChain('bsc') as SpotChain;

  it('keeps TRX back for the network fee when selling native TRX', () => {
    expect(spotMaxAmount(tron, TRON_NATIVE_ADDRESS, '100.5')).toBe('85.5');
    expect(spotMaxAmount(tron, TRON_NATIVE_ADDRESS, '10')).toBe('0');
  });

  it('spends a whole token balance, whose fee is paid in TRX', () => {
    expect(spotMaxAmount(tron, 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t', '40')).toBe('40');
  });

  it('leaves EVM balances alone (gas is checked separately)', () => {
    expect(spotMaxAmount(bsc, '0x0000000000000000000000000000000000000000', '1.5')).toBe('1.5');
  });
});
