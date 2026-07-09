import { secp256k1 } from '@noble/curves/secp256k1';

/** Sign a 32-byte hex digest with secp256k1; return 65-byte r||s||v hex (Tron format). */
export function signSecpDigest(hashHex: string, privateKey: string): string {
  const pk = privateKey.startsWith('0x') ? privateKey.slice(2) : privateKey;
  const sig = secp256k1.sign(hashHex, pk); // RecoveredSignature
  const r = sig.r.toString(16).padStart(64, '0');
  const s = sig.s.toString(16).padStart(64, '0');
  const v = sig.recovery.toString(16).padStart(2, '0');
  return r + s + v;
}
