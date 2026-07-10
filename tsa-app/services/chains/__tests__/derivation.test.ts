import { deriveTronAddress, deriveBtcAddress, deriveSolanaAddress } from '../derivation';

// Canonical BIP-39 test mnemonic.
const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';

describe('non-EVM derivation (one seed, standard paths)', () => {
  it('BTC matches the published BIP-84 vector (m/84h/0h/0h/0/0)', () => {
    expect(deriveBtcAddress(M)).toBe('bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu');
  });

  it('Tron produces a base58 T-address (m/44h/195h/0h/0/0)', () => {
    const a = deriveTronAddress(M);
    expect(a).toMatch(/^T[1-9A-HJ-NP-Za-km-z]{33}$/);
  });

  it('Solana produces a base58 ed25519 pubkey (m/44h/501h/0h/0h)', () => {
    const a = deriveSolanaAddress(M);
    expect(a).toMatch(/^[1-9A-HJ-NP-Za-km-z]{43,44}$/);
  });

  it('is deterministic', () => {
    expect(deriveTronAddress(M)).toBe(deriveTronAddress(M));
    expect(deriveSolanaAddress(M)).toBe(deriveSolanaAddress(M));
  });
});
