// services/chains/solanaLifi.ts
//
// Execute a LiFi swap whose SOURCE chain is Solana. Unlike sends (where the
// backend builds the tx via /wallet/nonevm/prepare-tx), the transaction here is
// built by LiFi and arrives as a base64-serialized tx in
// quote.transactionRequest.data. We split it into [signature slots][message],
// verify the wallet's Solana key is the one-and-only required signer, ed25519-
// sign the message bytes, and broadcast through the SAME
// /wallet/nonevm/submit-tx path sends use (AssembleAndBroadcast is
// message-agnostic and handles both legacy and v0 versioned messages).
import { base58, base64 } from '@scure/base';
import { bytesToHex } from '@noble/hashes/utils';
import { API_BASE_URL } from '../../constants/api/config';
import { getAuthHeaders } from '../walletApi';
import { getMnemonic } from '../wallet';
import { getNetwork } from '../../hooks/useNetwork';
import { deriveSolanaPrivateKey } from './derivation';
import { signEd25519Message } from './sign';
import { getSolanaAddress } from './solanaWallet';
import { registerNonEVMAddress } from './nonevmApi';

// Swaps are mainnet-only (LiFi has no testnets), so the broadcast pins mainnet
// instead of following the app's network toggle.
const SWAP_NETWORK = 'mainnet';

export interface ParsedLiFiSolanaTx {
  /** Hex of the serialized message (what ed25519 signs), no 0x prefix. */
  messageHex: string;
  /** Base58 pubkeys of the accounts that must sign, in slot order. */
  requiredSigners: string[];
}

// Solana's compact-u16: 1-3 bytes, 7 value bits per byte, high bit = continue.
// Rejects non-canonical encodings (a continuation followed by a zero byte) so
// this parser can never read a value differently than the runtime would.
function readCompactU16(bytes: Uint8Array, offset: number): { value: number; size: number } {
  let value = 0;
  for (let i = 0; i < 3; i++) {
    const b = bytes[offset + i];
    if (b === undefined) throw new Error('truncated compact-u16');
    value |= (b & 0x7f) << (7 * i);
    if ((b & 0x80) === 0) {
      if (i > 0 && b === 0) throw new Error('non-canonical compact-u16');
      return { value, size: i + 1 };
    }
  }
  throw new Error('compact-u16 too long');
}

/**
 * Split a base64-serialized Solana transaction into its message bytes and the
 * required-signer pubkeys. Handles legacy and versioned (v0) messages — for
 * both, the static account keys follow a 3-byte header, and the fee payer plus
 * other required signers are the first `numRequiredSignatures` keys.
 */
export function parseLiFiSolanaTx(dataB64: string): ParsedLiFiSolanaTx {
  const raw = base64.decode(dataB64);
  const sigCount = readCompactU16(raw, 0);
  const msgStart = sigCount.size + sigCount.value * 64;
  if (raw.length <= msgStart) throw new Error('malformed Solana transaction: no message');
  const msg = raw.slice(msgStart);

  // Versioned messages prefix the header with a version byte (0x80 | version).
  // Only v0 exists today — refuse unknown versions rather than guess their
  // layout (a legacy header byte can never be ≥ 0x80: that would mean 128+
  // signatures, which cannot fit Solana's 1232-byte packet).
  let o = 0;
  if ((msg[0] & 0x80) !== 0) {
    if (msg[0] !== 0x80) throw new Error('unsupported Solana transaction version');
    o = 1;
  }
  const numRequiredSignatures = msg[o];
  const keysCount = readCompactU16(msg, o + 3);
  let keyOffset = o + 3 + keysCount.size;
  if (numRequiredSignatures === 0 || numRequiredSignatures > keysCount.value) {
    throw new Error('malformed Solana transaction: bad signer count');
  }
  if (sigCount.value !== numRequiredSignatures) {
    throw new Error('malformed Solana transaction: signature slot count mismatch');
  }
  if (msg.length < keyOffset + keysCount.value * 32) {
    throw new Error('malformed Solana transaction: truncated account keys');
  }
  const requiredSigners: string[] = [];
  for (let i = 0; i < numRequiredSignatures; i++) {
    requiredSigners.push(base58.encode(msg.slice(keyOffset, keyOffset + 32)));
    keyOffset += 32;
  }
  return { messageHex: bytesToHex(msg), requiredSigners };
}

export interface ExecuteLiFiSolanaSwapParams {
  /** quote.transactionRequest.data — LiFi's base64-serialized Solana tx. */
  txDataB64: string;
  /** From-side amount in base units + symbol, for the backend's activity row. */
  fromAmountRaw: string;
  fromSymbol: string;
  /** EVM wallet id — used to look up the mnemonic + derived Solana address. */
  walletAddress: string;
  /**
   * Fee bucket + human-units amount, recorded server-side at broadcast so the
   * platform fee survives a client that never calls settle. Omitted for a swap
   * that owes nothing.
   */
  fee?: { feature: string; amountHuman: string; chainId?: number };
  onStage?: (label: string) => void;
}

/**
 * Sign + broadcast a LiFi Solana swap tx. Resolves with the tx signature
 * (Solana's hash equivalent). Throws with a user-facing message on failure.
 */
export async function executeLiFiSolanaSwap({
  txDataB64,
  fromAmountRaw,
  fromSymbol,
  walletAddress,
  fee,
  onStage,
}: ExecuteLiFiSolanaSwapParams): Promise<string> {
  // The broadcast is pinned to mainnet, so refuse to execute while the app is
  // in testnet mode — the screen would be showing devnet balances against a
  // real mainnet fund movement (Solana is hidden from swap pickers on testnet,
  // but a selection made on mainnet survives the toggle).
  if (getNetwork() === 'testnet') {
    throw new Error('Swaps are mainnet-only. Switch the app back to mainnet to swap.');
  }
  const solAddress = await getSolanaAddress(walletAddress);
  if (!solAddress) throw new Error('No Solana key for this wallet.');

  // The submit endpoint only broadcasts for addresses registered to this user;
  // registration is an idempotent upsert, so make sure it happened even if the
  // user never opened the wallet's Solana view.
  await registerNonEVMAddress(walletAddress, 'solana', solAddress).catch(() => {});

  const parsed = parseLiFiSolanaTx(txDataB64);
  // The wallet must be the one-and-only required signer. A route needing extra
  // signers can't be signed here — fail loudly rather than broadcast a tx that
  // dies in signature verification.
  if (parsed.requiredSigners.length !== 1 || parsed.requiredSigners[0] !== solAddress) {
    console.error('[swap] solana route signer mismatch', {
      signers: parsed.requiredSigners, wallet: solAddress,
    });
    throw new Error('This route requires signing this wallet does not support. Try a different amount or token.');
  }

  onStage?.('Signing swap…');
  const mnemonic = await getMnemonic(walletAddress);
  if (!mnemonic) throw new Error('No key for this wallet.');
  const sigHex = signEd25519Message(parsed.messageHex, deriveSolanaPrivateKey(mnemonic));

  onStage?.('Broadcasting swap…');
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=solana&network=${SWAP_NETWORK}`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      prepared: {
        family: 'solana',
        raw: parsed.messageHex,
        payloads: [{ index: 0, hashHex: parsed.messageHex, curve: 'ed25519' }],
      },
      sigs: [{ index: 0, sigHex }],
      from: solAddress,
      amount: fromAmountRaw,
      symbol: fromSymbol,
      // amount above is base units; the fee amount is whole tokens, hence a
      // separate field rather than reusing it.
      ...(fee && fee.feature !== 'none'
        ? {
            feeDeferred: true,
            feeFeature: fee.feature,
            feeAmountHuman: fee.amountHuman,
            feeChainId: fee.chainId ?? 0,
          }
        : {}),
    }),
  });
  const text = await res.text();
  let json: any = {};
  try { json = text ? JSON.parse(text) : {}; } catch { json = { message: text.slice(0, 200) }; }
  if (!res.ok || !json?.data?.txHash) {
    console.error('[swap] solana submit-tx failed', { status: res.status, message: json?.message });
    throw new Error(json?.message || 'Swap broadcast failed. The network may be congested — try again.');
  }
  return json.data.txHash;
}
