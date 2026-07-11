import {
  deriveTronAddress,
  deriveBtcAddress,
  deriveBtcPrivateKey,
  deriveSolanaAddress,
  isValidBtcAddress,
} from '../derivation';
import { secp256k1 } from '@noble/curves/secp256k1';
import { sha256 } from '@noble/hashes/sha256';
import { ripemd160 } from '@noble/hashes/ripemd160';
import { bech32 } from '@scure/base';

// Canonical BIP-39 test mnemonic.
const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';

describe('non-EVM derivation (one seed, standard paths)', () => {
  it('BTC matches the published BIP-84 vector (m/84h/0h/0h/0/0)', () => {
    expect(deriveBtcAddress(M)).toBe('bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu');
  });

  it('BTC testnet uses coin type 1h and the tb prefix (m/84h/1h/0h/0/0)', () => {
    // Community BIP-84 testnet fixture for the canonical mnemonic.
    expect(deriveBtcAddress(M, 'testnet')).toBe('tb1q6rz28mcfaxtmd6v789l9rrlrusdprr9pqcpvkl');
    expect(deriveBtcAddress(M, 'testnet')).not.toBe(deriveBtcAddress(M, 'mainnet'));
  });

  it('BTC private key pairs with the derived address (per network)', () => {
    for (const network of ['mainnet', 'testnet'] as const) {
      const priv = deriveBtcPrivateKey(M, network);
      expect(priv).toMatch(/^[0-9a-f]{64}$/);
      const pub = secp256k1.getPublicKey(priv, true);
      const h160 = ripemd160(sha256(pub));
      const hrp = network === 'testnet' ? 'tb' : 'bc';
      const addr = bech32.encode(hrp, [0x00, ...bech32.toWords(h160)]);
      expect(addr).toBe(deriveBtcAddress(M, network));
    }
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

describe('isValidBtcAddress', () => {
  it('accepts standard mainnet address types', () => {
    const valid = [
      'bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4', // P2WPKH (BIP-173 vector)
      'bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3', // P2WSH (BIP-173 vector)
      'bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0', // taproot (BIP-350 vector)
      '1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa', // legacy P2PKH
      '3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy', // legacy P2SH
    ];
    for (const a of valid) expect(isValidBtcAddress(a)).toBe(true);
  });

  it('accepts testnet addresses only on testnet', () => {
    const tb = 'tb1q6rz28mcfaxtmd6v789l9rrlrusdprr9pqcpvkl';
    expect(isValidBtcAddress(tb, 'testnet')).toBe(true);
    expect(isValidBtcAddress(tb, 'mainnet')).toBe(false);
    expect(isValidBtcAddress('bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu', 'testnet')).toBe(false);
    // legacy testnet P2PKH
    expect(isValidBtcAddress('mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn', 'testnet')).toBe(true);
    expect(isValidBtcAddress('mipcBbFg9gMiCh81Kj8tqqdgoZub1ZJRfn', 'mainnet')).toBe(false);
  });

  it('rejects malformed addresses', () => {
    const invalid = [
      '',
      '0x14dC79964da2C08b23698B3D3cc7Ca32193d9955', // EVM
      'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t', // Tron
      'bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyx', // bad checksum
      'bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kV8f3t4', // mixed case (BIP-173 invalid)
      'bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqh2y7hd', // v1 with bech32 (not m) checksum
      'bc1zw508d6qejxtdg4y5r3zarvaryvaxxpcs', // witness v2 — unsupported downstream
    ];
    for (const a of invalid) expect(isValidBtcAddress(a)).toBe(false);
  });
});
