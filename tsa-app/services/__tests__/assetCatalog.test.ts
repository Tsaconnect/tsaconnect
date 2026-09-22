jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

import {
  groupAssets, attachHoldings, assetFromHolding, networksForSide, buildSections, isNativeLiFiToken,
  onlyMajorTokens, nativeAddressFor, type Holding,
} from '../assetCatalog';
import { BTC_LIFI_CHAIN_ID, SOLANA_LIFI_CHAIN_ID, SUI_LIFI_CHAIN_ID, SOLANA_NATIVE_ADDRESS, type LiFiToken } from '../lifi';

const BASE = 8453, BSC = 56, ETH = 1, ZKSYNC = 324;

const tok = (over: Partial<LiFiToken> & { chainId: number; symbol: string }): LiFiToken => ({
  address: `0x${over.symbol.toLowerCase()}${over.chainId}`,
  name: over.symbol,
  decimals: 6,
  logoURI: '',
  verificationStatus: 'verified',
  ...over,
});

const USDC_BASE = tok({ chainId: BASE, symbol: 'USDC', coinKey: 'USDC' });
const USDC_BSC = tok({ chainId: BSC, symbol: 'USDC', coinKey: 'USDC' });
const USDC_ZK = tok({ chainId: ZKSYNC, symbol: 'USDC', coinKey: 'USDC' });
const FAKE_USDC = tok({ chainId: BASE, symbol: 'USDC', address: '0xfake', verificationStatus: 'unverified' });
const ETH_NATIVE = tok({ chainId: ETH, symbol: 'ETH', coinKey: 'ETH', address: '0x0000000000000000000000000000000000000000', decimals: 18 });
const ETH_BASE = tok({ chainId: BASE, symbol: 'ETH', coinKey: 'ETH', address: '0x0000000000000000000000000000000000000000', decimals: 18 });

const holding = (over: Partial<Holding> & { chainId: number; symbol: string }): Holding => ({
  address: '', name: over.symbol, decimals: 6, balance: 1, usdValue: 1, ...over,
});

describe('groupAssets', () => {
  it('makes one coin per coinKey with each network once', () => {
    const [usdc] = groupAssets([USDC_BASE, USDC_BSC, USDC_BASE]);
    expect(usdc.key).toBe('USDC');
    expect(usdc.networks.map(n => n.chainId)).toEqual([BASE, BSC]);
  });

  it('never merges a lookalike into the real coin by its name', () => {
    const assets = groupAssets([USDC_BASE, FAKE_USDC]);
    expect(assets).toHaveLength(2);
    const fake = assets.find(a => a.key !== 'USDC')!;
    expect(fake.verified).toBe(false);
    expect(fake.networks).toHaveLength(1);
  });

  it('keeps an UNVERIFIED token out of the real coin even when LiFi gives it the same coinKey', () => {
    // Seen live: 74 unverified tokens carry USDC/USDT/SOL/BNB coinKeys.
    const lookalike = tok({ chainId: BASE, symbol: 'USDC', coinKey: 'USDC', address: '0xrewards', name: 'USDC Rewards', verificationStatus: 'unverified' });
    const assets = groupAssets([lookalike, USDC_BASE]);
    const real = assets.find(a => a.key === 'USDC')!;
    expect(real.networks.map(n => n.token.address)).toEqual([USDC_BASE.address]);
    expect(assets.find(a => a.key !== 'USDC')!.verified).toBe(false);
  });

  it('keeps a second verified copy on the same chain as an alternate, and swaps in the one held', () => {
    const second = tok({ chainId: BSC, symbol: 'USDC', coinKey: 'USDC', address: '0xusdc2' });
    const [usdc] = groupAssets([USDC_BSC, second]);
    expect(usdc.networks).toHaveLength(1);
    const { assets } = attachHoldings([usdc], [holding({ chainId: BSC, symbol: 'USDC', address: '0xusdc2', balance: 4, usdValue: 4 })]);
    expect(assets[0].networks[0].token.address).toBe('0xusdc2');
    expect(assets[0].balance).toBe(4);
  });

  it('drops hidden coins (TRX)', () => {
    expect(groupAssets([tok({ chainId: 728126428, symbol: 'TRX', coinKey: 'TRX' })])).toEqual([]);
  });
});

describe('isNativeLiFiToken', () => {
  it('recognises each family’s native coin', () => {
    expect(isNativeLiFiToken(ETH_NATIVE)).toBe(true);
    expect(isNativeLiFiToken({ chainId: SOLANA_LIFI_CHAIN_ID, address: SOLANA_NATIVE_ADDRESS })).toBe(true);
    expect(isNativeLiFiToken({ chainId: BTC_LIFI_CHAIN_ID, address: 'bitcoin' })).toBe(true);
    expect(isNativeLiFiToken({ chainId: SUI_LIFI_CHAIN_ID, address: '0x2::sui::SUI' })).toBe(true);
    expect(isNativeLiFiToken(USDC_BASE)).toBe(false);
  });
});

describe('attachHoldings', () => {
  it('attaches balances per network, native by the empty address, and returns the rest', () => {
    const imported = holding({ chainId: BSC, symbol: 'CREPE', address: '0xcrepe', balance: 5, usdValue: 2 });
    const { assets, unmatched } = attachHoldings(groupAssets([USDC_BASE, USDC_BSC, ETH_NATIVE]), [
      holding({ chainId: BSC, symbol: 'USDC', address: USDC_BSC.address.toUpperCase(), balance: 2.33, usdValue: 2.33 }),
      holding({ chainId: ETH, symbol: 'ETH', balance: 0.01, usdValue: 35 }),
      imported,
    ]);
    const usdc = assets.find(a => a.key === 'USDC')!;
    expect(usdc.balance).toBe(2.33);
    expect(usdc.networks.find(n => n.chainId === BSC)!.balance).toBe(2.33);
    expect(usdc.networks.find(n => n.chainId === BASE)!.balance).toBeUndefined();
    expect(assets.find(a => a.key === 'ETH')!.usdValue).toBe(35);
    expect(unmatched).toEqual([imported]);
  });

  it("gives a held native coin the chain's real native address, never ''", () => {
    // S on Sonic is native and not a major: it arrives as a holding only.
    const s = assetFromHolding(holding({ chainId: 146, symbol: 'S', address: '', balance: 12, usdValue: 5 }));
    expect(s.networks[0].token.address).toBe('0x0000000000000000000000000000000000000000');
    expect(nativeAddressFor(SOLANA_LIFI_CHAIN_ID)).toBe(SOLANA_NATIVE_ADDRESS);
    expect(nativeAddressFor(BTC_LIFI_CHAIN_ID)).toBe('bitcoin');
    expect(nativeAddressFor(SUI_LIFI_CHAIN_ID)).toBe('0x2::sui::SUI');
  });

  it('lists an unmatched holding as its own, unverified coin', () => {
    const a = assetFromHolding(holding({ chainId: BSC, symbol: 'CREPE', address: '0xcrepe', balance: 5 }));
    expect(a.verified).toBe(false);
    expect(a.networks[0].token.address).toBe('0xcrepe');
    expect(a.balance).toBe(5);
  });
});

describe('networksForSide', () => {
  const [usdc] = groupAssets([USDC_BSC, USDC_ZK, USDC_BASE]);

  it('prefers Base when nothing is held', () => {
    expect(networksForSide(usdc, 'to').map(n => n.chainId)).toEqual([BASE, BSC, ZKSYNC]);
  });

  it('puts held networks first, largest first', () => {
    const { assets } = attachHoldings([usdc], [holding({ chainId: BSC, symbol: 'USDC', address: USDC_BSC.address, balance: 3, usdValue: 3 })]);
    expect(networksForSide(assets[0], 'from')[0].chainId).toBe(BSC);
  });

  it('only offers networks this device can sign on when paying', () => {
    expect(networksForSide(usdc, 'from').map(n => n.chainId)).not.toContain(ZKSYNC);
    expect(networksForSide(usdc, 'to').map(n => n.chainId)).toContain(ZKSYNC);
  });

  it('honours a screen’s own network limit', () => {
    expect(networksForSide(usdc, 'to', id => id === BSC).map(n => n.chainId)).toEqual([BSC]);
  });
});

describe('buildSections', () => {
  const [usdc, eth] = groupAssets([USDC_BASE, USDC_BSC, ETH_NATIVE, ETH_BASE]);
  const mcgp = groupAssets([tok({ chainId: 146, symbol: 'MCGP', coinKey: 'MCGP' })])[0];
  const crepe = assetFromHolding(holding({ chainId: BSC, symbol: 'CREPE', address: '0xcrepe', balance: 5, usdValue: 2 }));

  it('orders On TSA, then majors in their fixed order, then wallet coins', () => {
    const sections = buildSections({ listed: [mcgp], majors: [eth, usdc], held: [crepe], side: 'to' });
    expect(sections.map(s => s.id)).toEqual(['tsa', 'major', 'wallet']);
    expect(sections[1].assets.map(a => a.key)).toEqual(['USDC', 'ETH']);
  });

  it('shows a coin once, in the first section that has it', () => {
    const sections = buildSections({ listed: [usdc], majors: [usdc, eth], held: [usdc], side: 'to' });
    expect(sections.flatMap(s => s.assets).filter(a => a.key === 'USDC')).toHaveLength(1);
    expect(sections[0].id).toBe('tsa');
  });

  it('leaves out coins with no network this side may offer', () => {
    const zkOnly = groupAssets([tok({ chainId: ZKSYNC, symbol: 'ZK', coinKey: 'ZK' })])[0];
    expect(buildSections({ listed: [], majors: [zkOnly], held: [], side: 'from' })).toEqual([]);
    expect(buildSections({ listed: [], majors: [zkOnly], held: [], side: 'to' })).toHaveLength(1);
  });
});

describe('onlyMajorTokens', () => {
  it('keeps only verified tokens of the major coins, whatever an API sent', () => {
    // What an API that ignored ?coinKeys returned: the alphabet's first tokens.
    const junk = [
      tok({ chainId: ETH, symbol: 'SHOP', coinKey: undefined }),
      tok({ chainId: ETH, symbol: '$AAVETHENA', coinKey: '$AAVETHENA' }),
      tok({ chainId: BASE, symbol: 'USDC', coinKey: 'USDC', address: '0xfake', verificationStatus: 'unverified' }),
    ];
    expect(onlyMajorTokens([...junk, USDC_BASE, ETH_NATIVE])).toEqual([USDC_BASE, ETH_NATIVE]);
  });
});
