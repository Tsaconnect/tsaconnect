import { mnemonicToSeedSync } from '@scure/bip39';
import { HDKey } from '@scure/bip32';
import { keccak_256 } from '@noble/hashes/sha3';
import { sha256 } from '@noble/hashes/sha256';
import { ripemd160 } from '@noble/hashes/ripemd160';
import { secp256k1 } from '@noble/curves/secp256k1';
import { ed25519 } from '@noble/curves/ed25519';
import { base58check, bech32, base58 } from '@scure/base';
import { hmac } from '@noble/hashes/hmac';
import { sha512 } from '@noble/hashes/sha512';

export const DERIVATION_PATHS = {
  tron: "m/44'/195'/0'/0/0",
  btc: "m/84'/0'/0'/0/0",
  solana: "m/44'/501'/0'/0'",
} as const;

const b58check = base58check(sha256);

function secpNode(mnemonic: string, path: string): HDKey {
  const seed = mnemonicToSeedSync(mnemonic.trim());
  const node = HDKey.fromMasterSeed(seed).derive(path);
  if (!node.privateKey || !node.publicKey) throw new Error('derivation failed');
  return node;
}

// Tron: keccak256(uncompressed pubkey[1:])[12:] → prepend 0x41 → base58check.
export function deriveTronAddress(mnemonic: string): string {
  const node = secpNode(mnemonic, DERIVATION_PATHS.tron);
  const pub = secp256k1.getPublicKey(node.privateKey!, false); // 65 bytes, 0x04 prefix
  const hash = keccak_256(pub.slice(1));
  const body = new Uint8Array(21);
  body[0] = 0x41;
  body.set(hash.slice(12), 1);
  return b58check.encode(body);
}

// BTC native SegWit (BIP-84): bech32(hash160(compressed pubkey)).
export function deriveBtcAddress(mnemonic: string): string {
  const node = secpNode(mnemonic, DERIVATION_PATHS.btc);
  const h160 = ripemd160(sha256(node.publicKey!)); // publicKey is compressed (33 bytes)
  const words = bech32.toWords(h160);
  return bech32.encode('bc', [0x00, ...words]);
}

// Solana: ed25519 via SLIP-0010 hardened path → base58 of the 32-byte pubkey.
export function deriveSolanaAddress(mnemonic: string): string {
  const seed = mnemonicToSeedSync(mnemonic.trim());
  const priv = ed25519Slip10(seed, DERIVATION_PATHS.solana);
  const pub = ed25519.getPublicKey(priv);
  return b58EncodeNoCheck(pub);
}

// --- SLIP-0010 ed25519 (all-hardened) ---
function ed25519Slip10(seed: Uint8Array, path: string): Uint8Array {
  let I = hmac(sha512, new TextEncoder().encode('ed25519 seed'), seed);
  let key = I.slice(0, 32);
  let chain = I.slice(32);
  for (const seg of path.split('/').slice(1)) {
    const idx = (parseInt(seg) | 0x80000000) >>> 0; // ed25519 is always hardened
    const data = new Uint8Array(1 + 32 + 4);
    data.set(key, 1);
    new DataView(data.buffer).setUint32(33, idx, false);
    I = hmac(sha512, chain, data);
    key = I.slice(0, 32);
    chain = I.slice(32);
  }
  return key;
}

function b58EncodeNoCheck(b: Uint8Array): string {
  return base58.encode(b);
}
