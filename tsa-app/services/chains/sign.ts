import { secp256k1 } from '@noble/curves/secp256k1';
import { ed25519 } from '@noble/curves/ed25519';
import { bytesToHex } from '@noble/hashes/utils';

/** Sign a 32-byte hex digest with secp256k1; return 65-byte r||s||v hex (Tron format). */
export function signSecpDigest(hashHex: string, privateKey: string): string {
  const pk = privateKey.startsWith('0x') ? privateKey.slice(2) : privateKey;
  const sig = secp256k1.sign(hashHex, pk); // RecoveredSignature
  const r = sig.r.toString(16).padStart(64, '0');
  const s = sig.s.toString(16).padStart(64, '0');
  const v = sig.recovery.toString(16).padStart(2, '0');
  return r + s + v;
}

/**
 * Sign an arbitrary message with ed25519 (Solana). Unlike secp256k1 chains,
 * ed25519 signs the FULL message, not a pre-computed 32-byte digest — so
 * `messageHex` is the serialized Solana transaction message the backend built,
 * and the result is the 64-byte signature placed into the tx's signature slot.
 * The private key is the 32-byte SLIP-0010 seed from deriveSolanaPrivateKey.
 */
export function signEd25519Message(messageHex: string, privateKey: string): string {
  const pk = privateKey.startsWith('0x') ? privateKey.slice(2) : privateKey;
  const msg = messageHex.startsWith('0x') ? messageHex.slice(2) : messageHex;
  return bytesToHex(ed25519.sign(msg, pk)); // 64-byte r||s signature
}
