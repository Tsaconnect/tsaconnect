import { signSecpDigest, signEd25519Message } from '../sign';
import { deriveSolanaPrivateKey } from '../derivation';
import { ed25519 } from '@noble/curves/ed25519';
import { secp256k1 } from '@noble/curves/secp256k1';
import { hexToBytes } from '@noble/hashes/utils';

const M = 'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';

describe('signEd25519Message (Solana)', () => {
  const priv = deriveSolanaPrivateKey(M);

  it('produces a 64-byte signature that verifies against the derived pubkey', () => {
    const msg = 'deadbeef';
    const sig = signEd25519Message(msg, priv);
    expect(sig).toMatch(/^[0-9a-f]{128}$/); // 64 bytes
    const pub = ed25519.getPublicKey(priv);
    expect(ed25519.verify(hexToBytes(sig), msg, pub)).toBe(true);
  });

  it('matches the pinned vector for the canonical mnemonic', () => {
    expect(signEd25519Message('deadbeef', priv)).toBe(
      'a90493687a56d7247a8c661127b9890bf824ccb2ffd83c8b1102b24b1220ce68' +
        'f480a405722b40e026b4ad6afb9ac370a8c8322750b85b1371525555d1e0e90e'
    );
  });

  it('tolerates a 0x prefix on key and message', () => {
    const a = signEd25519Message('deadbeef', priv);
    const b = signEd25519Message('0xdeadbeef', '0x' + priv);
    expect(b).toBe(a);
  });
});

describe('signSecpDigest (Tron) — unchanged', () => {
  it('produces a recoverable 65-byte signature', () => {
    const pk = '37df573b3ac4ad5b522e064e25b63ea16bcbe79d449e81a0268d1047948bb445';
    const digest = 'a'.repeat(64);
    const sig = signSecpDigest(digest, pk);
    expect(sig).toMatch(/^[0-9a-f]{130}$/); // r(32)+s(32)+v(1)
    // recovery byte round-trips to the signer pubkey
    const r = BigInt('0x' + sig.slice(0, 64));
    const s = BigInt('0x' + sig.slice(64, 128));
    const rec = parseInt(sig.slice(128, 130), 16);
    const recovered = new secp256k1.Signature(r, s, rec).recoverPublicKey(digest).toHex(true);
    expect(recovered).toBe(secp256k1.getPublicKey(pk, true).reduce((h, b) => h + b.toString(16).padStart(2, '0'), ''));
  });
});
