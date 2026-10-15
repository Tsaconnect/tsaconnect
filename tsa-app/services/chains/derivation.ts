import { mnemonicToSeedSync } from '@scure/bip39';
import { HDKey } from '@scure/bip32';
import { keccak_256 } from '@noble/hashes/sha3';
import { sha256 } from '@noble/hashes/sha256';
import { ripemd160 } from '@noble/hashes/ripemd160';
import { secp256k1 } from '@noble/curves/secp256k1';
import { ed25519 } from '@noble/curves/ed25519';
import { base58check, bech32, bech32m, base58 } from '@scure/base';
import { hmac } from '@noble/hashes/hmac';
import { sha512 } from '@noble/hashes/sha512';
import { blake2b } from '@noble/hashes/blake2b';
import { bytesToHex } from '@noble/hashes/utils';

export const DERIVATION_PATHS = {
  tron: "m/44'/195'/0'/0/0",
  btc: "m/84'/0'/0'/0/0",
  btcTestnet: "m/84'/1'/0'/0/0", // BIP-84 testnet coin type
  solana: "m/44'/501'/0'/0'",
  solanaTrustWallet: "m/44'/501'/0'", // Trust Wallet's wallet-core default — one hop shorter
  sui: "m/44'/784'/0'/0'/0'", // SLIP-0044 coin type 784, all-hardened (matches @mysten/sui)
} as const;

// Which path a wallet's Solana account lives at. 'phantom' is the ecosystem
// standard (Phantom/Solflare/Backpack, m/44'/501'/0'/0'); 'trustwallet' is
// Trust Wallet's m/44'/501'/0'. Same seed, different address — resolved per
// wallet by the balance scan in solanaWallet.ts, so imported Trust Wallet
// seeds keep showing the address their funds are on.
export type SolanaDerivation = 'phantom' | 'trustwallet';

const SOLANA_PATHS: Record<SolanaDerivation, string> = {
  phantom: DERIVATION_PATHS.solana,
  trustwallet: DERIVATION_PATHS.solanaTrustWallet,
};

export type BtcNetwork = 'mainnet' | 'testnet';

const b58check = base58check(sha256);

function seedOf(mnemonic: string): Uint8Array {
  return mnemonicToSeedSync(mnemonic.trim());
}

function secpNodeFromSeed(seed: Uint8Array, path: string): HDKey {
  const node = HDKey.fromMasterSeed(seed).derive(path);
  if (!node.privateKey || !node.publicKey) throw new Error('derivation failed');
  return node;
}

function secpNode(mnemonic: string, path: string): HDKey {
  return secpNodeFromSeed(seedOf(mnemonic), path);
}

// Tron: keccak256(uncompressed pubkey[1:])[12:] → prepend 0x41 → base58check.
export function deriveTronAddress(mnemonic: string): string {
  return tronAddressFromSeed(seedOf(mnemonic));
}

function tronAddressFromSeed(seed: Uint8Array): string {
  const node = secpNodeFromSeed(seed, DERIVATION_PATHS.tron);
  const pub = secp256k1.getPublicKey(node.privateKey!, false); // 65 bytes, 0x04 prefix
  const hash = keccak_256(pub.slice(1));
  const body = new Uint8Array(21);
  body[0] = 0x41;
  body.set(hash.slice(12), 1);
  return b58check.encode(body);
}

// The Tron signing key — derived at the Tron path (m/44'/195'), NOT the EVM
// path. Signing a Tron tx with the EVM key would produce a signature that
// recovers to a different address than the registered owner_address, so Tron
// rejects it (SIGERROR). Returns a 0x-less 32-byte hex private key.
export function deriveTronPrivateKey(mnemonic: string): string {
  const node = secpNode(mnemonic, DERIVATION_PATHS.tron);
  return bytesToHex(node.privateKey!);
}

// Validate a Tron mainnet base58check address: 'T'-prefixed, 21-byte payload
// with a 0x41 version byte and a valid double-sha256 checksum.
export function isValidTronAddress(addr: string): boolean {
  if (!/^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(addr)) return false;
  try {
    const decoded = b58check.decode(addr);
    return decoded.length === 21 && decoded[0] === 0x41;
  } catch {
    return false;
  }
}

// BTC native SegWit (BIP-84): bech32(hash160(compressed pubkey)). Unlike
// Tron, the address is network-specific — testnet uses coin type 1' and the
// 'tb' prefix, so the same seed yields different mainnet/testnet addresses
// (matching Sparrow/Electrum behavior).
const btcPath = (network: BtcNetwork) => (network === 'testnet' ? DERIVATION_PATHS.btcTestnet : DERIVATION_PATHS.btc);

function btcNode(mnemonic: string, network: BtcNetwork): HDKey {
  return secpNode(mnemonic, btcPath(network));
}

export function deriveBtcAddress(mnemonic: string, network: BtcNetwork = 'mainnet'): string {
  return btcAddressFromSeed(seedOf(mnemonic), network);
}

function btcAddressFromSeed(seed: Uint8Array, network: BtcNetwork): string {
  const node = secpNodeFromSeed(seed, btcPath(network));
  const h160 = ripemd160(sha256(node.publicKey!)); // publicKey is compressed (33 bytes)
  const words = bech32.toWords(h160);
  return bech32.encode(network === 'testnet' ? 'tb' : 'bc', [0x00, ...words]);
}

// The BTC signing key — derived at the BIP-84 path, NOT the EVM path. The
// backend recovers the pubkey from each input signature and requires it to
// hash to the sender address's witness program, so signing with any other
// key is rejected before broadcast. Returns a 0x-less 32-byte hex key.
export function deriveBtcPrivateKey(mnemonic: string, network: BtcNetwork = 'mainnet'): string {
  return bytesToHex(btcNode(mnemonic, network).privateKey!);
}

// Validate a BTC address for the given network. Accepts native SegWit v0
// (bech32: P2WPKH/P2WSH), taproot v1 (bech32m), and legacy base58check
// (P2PKH/P2SH) — everything the backend can build an output script for.
export function isValidBtcAddress(addr: string, network: BtcNetwork = 'mainnet'): boolean {
  if (!addr) return false;
  const hrp = network === 'testnet' ? 'tb' : 'bc';
  const lower = addr.toLowerCase();
  if (lower.startsWith(`${hrp}1`)) {
    // Mixed-case bech32 is invalid per BIP-173.
    if (addr !== lower && addr !== addr.toUpperCase()) return false;
    return decodeSegwit(lower, hrp) !== null;
  }
  // Legacy base58check: 1 version byte + 20-byte hash.
  try {
    const decoded = b58check.decode(addr);
    if (decoded.length !== 21) return false;
    const version = decoded[0];
    return network === 'testnet' ? version === 0x6f || version === 0xc4 : version === 0x00 || version === 0x05;
  } catch {
    return false;
  }
}

// BIP-173/350: witness v0 must checksum as bech32, v1+ as bech32m.
function decodeSegwit(addr: string, hrp: string): { version: number; program: Uint8Array } | null {
  for (const codec of [bech32, bech32m] as const) {
    let decoded;
    try {
      decoded = codec.decode(addr as `${string}1${string}`, 90);
    } catch {
      continue;
    }
    if (decoded.prefix !== hrp || decoded.words.length === 0) return null;
    const [version, ...rest] = decoded.words;
    let program: Uint8Array;
    try {
      program = bech32.fromWords(rest);
    } catch {
      return null;
    }
    // Only witness v0 (P2WPKH/P2WSH) and v1 (taproot) — the versions the
    // backend can build output scripts for. Future versions are rejected.
    const ok =
      version === 0
        ? codec === bech32 && (program.length === 20 || program.length === 32)
        : codec === bech32m && version === 1 && program.length === 32;
    return ok ? { version, program } : null;
  }
  return null;
}

// Solana: ed25519 via SLIP-0010 hardened path → base58 of the 32-byte pubkey.
export function deriveSolanaAddress(mnemonic: string, derivation: SolanaDerivation = 'phantom'): string {
  return solanaAddressFromSeed(mnemonicToSeedSync(mnemonic.trim()), derivation);
}

// Both paths' addresses from ONE seed. mnemonicToSeedSync is 2048 PBKDF2
// rounds and blocks the JS thread, so the path resolver pays for it once
// instead of once per path.
export function deriveSolanaAddresses(mnemonic: string): Record<SolanaDerivation, string> {
  const seed = mnemonicToSeedSync(mnemonic.trim());
  return {
    phantom: solanaAddressFromSeed(seed, 'phantom'),
    trustwallet: solanaAddressFromSeed(seed, 'trustwallet'),
  };
}

function solanaAddressFromSeed(seed: Uint8Array, derivation: SolanaDerivation): string {
  const priv = ed25519Slip10(seed, SOLANA_PATHS[derivation]);
  return b58EncodeNoCheck(ed25519.getPublicKey(priv));
}

// The Solana signing key — the 32-byte ed25519 seed at the Phantom path
// (m/44'/501'), NOT the EVM/Tron key. Unlike secp256k1 chains the "private
// key" here is the SLIP-0010 seed; @noble derives the public key + signs from
// it. The backend recovers no key from the signature — it verifies the 64-byte
// ed25519 signature against the fee-payer pubkey it embedded in the message, so
// signing with any other seed fails verification before broadcast. Returns a
// 0x-less 32-byte hex key.
export function deriveSolanaPrivateKey(mnemonic: string, derivation: SolanaDerivation = 'phantom'): string {
  const seed = mnemonicToSeedSync(mnemonic.trim());
  return bytesToHex(ed25519Slip10(seed, SOLANA_PATHS[derivation]));
}

// Validate a Solana address: base58 that decodes to exactly 32 bytes. Solana
// accepts off-curve system addresses (e.g. PDAs), so curve membership is not
// checked — matching what the backend validator allows.
export function isValidSolanaAddress(addr: string): boolean {
  if (!/^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(addr)) return false;
  try {
    return base58.decode(addr).length === 32;
  } catch {
    return false;
  }
}

// --- Sui (ed25519, same SLIP-0010 scheme as Solana; different path + address) ---

// Sui derives the 32-byte ed25519 seed at m/44'/784'/0'/0'/0' (all hardened),
// exactly like Solana but for coin type 784. The public/private key handling is
// identical to Solana (@noble treats the 32-byte seed as the ed25519 key).
export function deriveSuiPrivateKey(mnemonic: string): string {
  const seed = mnemonicToSeedSync(mnemonic.trim());
  return bytesToHex(ed25519Slip10(seed, DERIVATION_PATHS.sui));
}

// The 32-byte ed25519 public key hex. Sui gives no key recovery from a
// signature (unlike secp256k1), so the app hands this to the backend alongside
// the signature; the backend assembles the on-chain signature
// base64(flag=0x00 || sig64 || pubkey32) and verifies it hashes to the sender.
export function deriveSuiPublicKey(mnemonic: string): string {
  return bytesToHex(ed25519.getPublicKey(deriveSuiPrivateKey(mnemonic)));
}

// A Sui address is 0x || hex(blake2b256(0x00 || pubkey)) — the 0x00 is the
// ed25519 signature-scheme flag. 32-byte digest → 66-char address. Verified
// against @mysten/sui.
export function deriveSuiAddress(mnemonic: string): string {
  return suiAddressFromSeed(seedOf(mnemonic));
}

function suiAddressFromSeed(seed: Uint8Array): string {
  const pub = ed25519.getPublicKey(ed25519Slip10(seed, DERIVATION_PATHS.sui));
  const flagged = new Uint8Array(1 + pub.length); // flag 0x00 already set
  flagged.set(pub, 1);
  return '0x' + bytesToHex(blake2b(flagged, { dkLen: 32 }));
}

// Every non-EVM receive address from ONE seed. mnemonicToSeedSync is 2048
// PBKDF2 rounds of pure-JS SHA-512 and blocks the JS thread for seconds on a
// phone; deriving per chain paid that once per chain on every cold start.
export type NonEvmAddresses = {
  tron: string;
  btc: Record<BtcNetwork, string>;
  sui: string;
  solana: Record<SolanaDerivation, string>;
};

export function deriveNonEvmAddresses(mnemonic: string): NonEvmAddresses {
  const seed = seedOf(mnemonic);
  return {
    tron: tronAddressFromSeed(seed),
    btc: { mainnet: btcAddressFromSeed(seed, 'mainnet'), testnet: btcAddressFromSeed(seed, 'testnet') },
    sui: suiAddressFromSeed(seed),
    solana: { phantom: solanaAddressFromSeed(seed, 'phantom'), trustwallet: solanaAddressFromSeed(seed, 'trustwallet') },
  };
}

// Validate a Sui address: 0x + exactly 64 lowercase hex chars (32 bytes). Sui
// accepts short forms in some tooling, but our derived/receive addresses are
// always the full 32-byte form, and the backend validator requires the same.
export function isValidSuiAddress(addr: string): boolean {
  return /^0x[0-9a-fA-F]{64}$/.test(addr);
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
