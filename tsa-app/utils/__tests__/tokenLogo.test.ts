// Tests for the shared token-logo lookup (utils/tokenLogo.ts).

import { isKnownContract, tokenLogoSource } from '../tokenLogo';
import { DEFAULT_TOKENS, NONEVM_CHAINS } from '../../constants/chains';

describe('tokenLogoSource', () => {
  it('prefers a logo that describes the exact token', () => {
    expect(tokenLogoSource('USDT', { explicitUri: 'https://lifi.example/usdt0.png' }))
      .toEqual({ uri: 'https://lifi.example/usdt0.png' });
  });

  it('uses the bundled MCGP image, which no registry entry carries', () => {
    const source = tokenLogoSource('MCGP');
    expect(source).not.toBeNull();
    expect(source).not.toHaveProperty('uri');
  });

  it('finds EVM tokens in the token registry, case-insensitively', () => {
    expect(tokenLogoSource('usdc')).toEqual({ uri: DEFAULT_TOKENS.USDC.iconUrl });
  });

  it('finds non-EVM native coins on their chain config', () => {
    expect(tokenLogoSource('TRX')).toEqual({ uri: NONEVM_CHAINS.tron.iconUrl });
    expect(tokenLogoSource('SOL')).toEqual({ uri: NONEVM_CHAINS.solana.iconUrl });
  });

  it('never lends a curated logo to an uncurated token with the same symbol', () => {
    expect(tokenLogoSource('USDT', { curated: false })).toBeNull();
    expect(tokenLogoSource('MCGP', { curated: false })).toBeNull();
  });

  it('ignores an explicit logo that is not https', () => {
    expect(tokenLogoSource('USDT', { explicitUri: 'http://cdn.example/usdt.png' }))
      .toEqual({ uri: DEFAULT_TOKENS.USDT.iconUrl });
    expect(tokenLogoSource('PEPE2', { explicitUri: 'data:image/png;base64,AAAA', curated: false })).toBeNull();
  });

  it('vouches for MCGP only at its own contract', () => {
    const mcgp = '0x517600323E5E2938207FA2E2E915B9D80E5B2B21';
    expect(isKnownContract(146, mcgp, 'MCGP')).toBe(true);
    expect(isKnownContract(146, '0x0000000000000000000000000000000000000001', 'MCGP')).toBe(false);
    expect(isKnownContract(56, mcgp, 'MCGP')).toBe(false);
    expect(isKnownContract(146, mcgp, 'USDT')).toBe(false);
    expect(isKnownContract(undefined, mcgp, 'MCGP')).toBe(false);
  });

  it('returns null for an unknown token or a blank explicit logo', () => {
    expect(tokenLogoSource('PEPE2', { explicitUri: '  ' })).toBeNull();
  });
});
