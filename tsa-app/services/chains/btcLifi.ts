// services/chains/btcLifi.ts
//
// Execute a LiFi swap whose SOURCE chain is Bitcoin. Bitcoin has no smart
// contracts, so a LiFi UTXO route is a *deposit*: the quote names a deposit
// address, the exact number of sats to pay it, and (for THORChain-style
// bridges) an OP_RETURN memo that is the only thing tying that deposit to the
// user's swap. We hand all three to the same backend prepare/sign/submit path
// a normal BTC send uses (services/chains/btcSend.ts) — the backend selects
// UTXOs, attaches the memo as a zero-value nulldata output, and returns one
// BIP-143 digest per input for the app's BIP-84 key to sign.
//
// Everything here fails CLOSED. A deposit that goes out with the wrong amount,
// or without its memo, is an unrecoverable send to a bridge's address — so any
// transactionRequest we cannot read exactly as expected aborts the swap
// instead of guessing.
import { getNetwork } from '../../hooks/useNetwork';
import type { LiFiTransactionRequest } from '../lifi';
import { getBtcAddress } from './btcWallet';
import { prepareBtc, broadcastBtc } from './btcSend';
import { registerNonEVMAddress } from './nonevmApi';

/** Bitcoin's standard OP_RETURN relay limit, mirrored from the backend. */
const MAX_MEMO_BYTES = 80;

// Swaps are mainnet-only (LiFi has no testnets), so the deposit is pinned to
// mainnet rather than following the app's network toggle — the same pin
// solanaLifi.ts applies. The testnet guard below still runs: it stops a
// testnet-mode wallet from quoting against balances it isn't about to spend.
const SWAP_NETWORK = 'mainnet' as const;

/**
 * Read the sats figure out of a LiFi transactionRequest `value`. LiFi ships it
 * as base units — hex-quantity on EVM, and the same convention on UTXO routes —
 * so both spellings are accepted and anything else throws.
 */
export function parseLiFiSats(value: string | number | undefined): bigint {
  if (value === undefined || value === null || value === '') {
    throw new Error('The route did not say how much BTC to send. Refresh the quote and try again.');
  }
  // Stringify BEFORE parsing so a JSON number takes the same guarded path as a
  // string: BigInt(0.00125) throws a raw RangeError whose internal wording
  // would land in the swap UI, while BigInt('0.00125') is caught below.
  const raw = parseSatsString(String(value).trim());
  if (raw <= 0n) throw new Error('The route quoted a zero BTC deposit. Refresh the quote and try again.');
  return raw;
}

function parseSatsString(s: string): bigint {
  try {
    // BigInt() handles both "0x1e848" and "125000"; it rejects decimals, which
    // is what we want — a fractional "value" would mean LiFi quoted BTC rather
    // than sats and every assumption below would be off by 1e8.
    return BigInt(s);
  } catch {
    throw new Error('Could not read the deposit amount for this route. Try a different amount or token.');
  }
}

/**
 * Normalize the memo from a LiFi transactionRequest `data` field: hex without
 * the 0x prefix, or '' when the route carries no memo (Chainflip-style bridges
 * use a unique deposit address instead). Throws when `data` is present but not
 * something we can put in an OP_RETURN.
 */
export function parseLiFiBtcMemo(data: string | undefined): string {
  if (!data) return '';
  const hex = data.startsWith('0x') || data.startsWith('0X') ? data.slice(2) : data;
  if (hex === '') return '';
  if (hex.length % 2 !== 0 || !/^[0-9a-fA-F]+$/.test(hex)) {
    throw new Error('This route carries a deposit tag this app cannot encode. Try a different route or amount.');
  }
  if (hex.length / 2 > MAX_MEMO_BYTES) {
    throw new Error('This route carries a deposit tag too large for Bitcoin. Try a different route or amount.');
  }
  return hex.toLowerCase();
}

export interface ExecuteLiFiBtcSwapParams {
  /** quote.transactionRequest — the deposit address, sats and optional memo. */
  txRequest: LiFiTransactionRequest;
  /** Sats the user actually authorized (the amount the quote was fetched for). */
  fromAmountRaw: string;
  /** EVM wallet id — used to look up the mnemonic + derived BTC address. */
  walletAddress: string;
  onStage?: (label: string) => void;
}

/**
 * Sign + broadcast a LiFi Bitcoin-source swap. Resolves with the deposit txid;
 * the bridge credits the destination chain afterwards, which the caller tracks
 * separately. Throws with a user-facing message on failure.
 */
export async function executeLiFiBtcSwap({
  txRequest,
  fromAmountRaw,
  walletAddress,
  onStage,
}: ExecuteLiFiBtcSwapParams): Promise<string> {
  // Swaps are mainnet-only (LiFi has no testnets) and the BTC address is
  // network-specific, so a testnet-mode wallet would be signing with a tb1…
  // key against a mainnet quote. Bitcoin is hidden from the swap pickers on
  // testnet, but a selection made on mainnet survives the toggle.
  if (getNetwork() === 'testnet') {
    throw new Error('Swaps are mainnet-only. Switch the app back to mainnet to swap.');
  }

  const btcAddress = await getBtcAddress(walletAddress);
  if (!btcAddress) throw new Error('No Bitcoin key for this wallet.');

  const deposit = txRequest?.to;
  if (!deposit) {
    throw new Error('The route did not return a Bitcoin deposit address. Refresh the quote and try again.');
  }

  const sats = parseLiFiSats(txRequest.value);
  // The quote is what the user saw and approved. If LiFi asks for a different
  // number of sats than we quoted, something about the route changed under us
  // — refuse rather than move an amount the user never agreed to.
  if (sats !== BigInt(fromAmountRaw)) {
    console.error('[swap] btc route amount mismatch', { quoted: fromAmountRaw, route: sats.toString() });
    throw new Error('This route changed the amount to send. Refresh the quote and try again.');
  }

  const memoHex = parseLiFiBtcMemo(txRequest.data);

  // submit-tx only broadcasts for addresses registered to this user;
  // registration is an idempotent upsert, so make sure it happened even if the
  // user never opened the wallet's Bitcoin view.
  await registerNonEVMAddress(walletAddress, 'btc', btcAddress).catch(() => {});

  const params = {
    from: btcAddress,
    to: deposit,
    amount: sats.toString(),
    symbol: 'BTC',
    walletAddress,
    memoHex,
    network: SWAP_NETWORK,
  };

  onStage?.('Building Bitcoin deposit…');
  const { prepared } = await prepareBtc(params);

  onStage?.('Signing swap…');
  const { txHash } = await broadcastBtc(prepared, params);
  return txHash;
}
