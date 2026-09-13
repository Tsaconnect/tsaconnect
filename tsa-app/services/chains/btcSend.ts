// services/chains/btcSend.ts
// BTC send flow, mirroring tronSend: the backend selects UTXOs, builds the
// unsigned tx, and returns one BIP-143 digest per input; the app signs each
// digest with the BIP-84 key (same 65-byte r||s||v format as Tron — the
// backend recovers the pubkey for the witness from the recovery byte) and
// the backend assembles + broadcasts.
import { API_BASE_URL } from '../../constants/api/config';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getMnemonic } from '../wallet';
import { getNetwork } from '../../hooks/useNetwork';
import { txHashOrThrow, mapSubmitError, TRANSFER_COPY } from '../swapErrors';
import { deriveBtcPrivateKey, type BtcNetwork } from './derivation';
import { signSecpDigest } from './sign';

async function authHeaders() {
  const token = await AsyncStorage.getItem('authToken');
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
}

export interface BtcFeeQuote {
  gasSymbol: string;
  estFeeRaw: string; // sats
  gasDecimals: number;
  hasEnoughGas: boolean;
  note?: string;
}

export interface BtcPrepared {
  prepared: { family: string; raw: string; payloads: { index: number; hashHex: string; curve: string }[] };
  fee: BtcFeeQuote;
}

export interface SendBtcParams {
  from: string; // sender bc1…/tb1… address
  to: string;
  amount: string; // integer string, sats (8 decimals)
  symbol: string; // always 'BTC'
  walletAddress: string; // EVM wallet id used to look up the mnemonic
  /**
   * Optional bridge memo (hex, no 0x) the backend attaches as a zero-value
   * OP_RETURN output. Used by LiFi's Bitcoin swap routes, where the memo is the
   * only thing tying a deposit to the user's swap; plain sends leave it unset.
   * The backend prices the extra output into the fee and rejects a memo it
   * cannot encode, so a bad one fails before any BTC moves.
   */
  memoHex?: string;
  /**
   * Optional pre-built transaction (BIP-174, hex) to sign instead of building
   * one. Aggregator Bitcoin routes that construct the whole spend send it
   * here; the backend validates it against `from`, `to` and `amount` before
   * handing back anything to sign.
   */
  psbtHex?: string;
  /**
   * Fee bucket + human-units amount for an aggregator swap, recorded
   * server-side at broadcast so the platform fee survives a client that never
   * calls settle. Absent on a plain send, which owes no swap fee.
   */
  fee?: { feature: string; amountHuman: string; chainId?: number };
  /**
   * Network to build, sign and broadcast on. Defaults to the app's current
   * toggle, which is what a wallet send wants. A caller pinned to one network —
   * swaps, which LiFi only quotes on mainnet — MUST pass it explicitly: the
   * address, the signing key and the broadcast all have to agree, and a caller
   * that merely checks the toggle before it starts is one reordering away from
   * signing a mainnet route with a testnet key.
   */
  network?: BtcNetwork;
}

/** The network a send acts on: the caller's pin, else the app's toggle. */
function networkFor(params: SendBtcParams): BtcNetwork {
  return params.network ?? (getNetwork() as BtcNetwork);
}

async function readJson(res: Response): Promise<any> {
  const text = await res.text();
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    return { message: text.slice(0, 200) };
  }
}

/**
 * Build the unsigned BTC tx (UTXO selection + fee) server-side. Throws with
 * the backend's human note (e.g. "Not enough BTC to cover the amount plus the
 * network fee.") when the balance can't fund the send (HTTP 402).
 */
export async function prepareBtc(params: SendBtcParams): Promise<BtcPrepared> {
  const headers = await authHeaders();
  const body = {
    from: params.from, to: params.to, amount: params.amount,
    symbol: params.symbol, tokenAddr: '',
    ...(params.memoHex ? { memoHex: params.memoHex } : {}),
    ...(params.psbtHex ? { psbtHex: params.psbtHex } : {}),
  };
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=btc&network=${networkFor(params)}`, {
    method: 'POST', headers, body: JSON.stringify(body),
  });
  const json = await readJson(res);
  if (!res.ok) {
    // 402 carries the friendly fee-preflight note in `message`.
    throw new Error(
      json?.message
        ? mapSubmitError(json.message, TRANSFER_COPY)
        : `prepare-tx failed (${res.status})`,
    );
  }
  if (!json?.data?.prepared) {
    throw new Error(json?.message ? mapSubmitError(json.message, TRANSFER_COPY) : 'prepare failed');
  }
  return { prepared: json.data.prepared, fee: json.data.fee };
}

/**
 * Sign every input digest with the wallet's BIP-84 key and broadcast. The
 * backend rejects signatures whose recovered pubkey doesn't hash to the
 * sender address, so a wrong-path key fails closed before broadcast.
 */
export async function broadcastBtc(
  prepared: BtcPrepared['prepared'],
  params: SendBtcParams,
): Promise<{ txHash: string }> {
  const payloads = prepared?.payloads;
  if (!Array.isArray(payloads) || payloads.length === 0) {
    throw new Error('prepare-tx returned no payloads');
  }
  const mnemonic = await getMnemonic(params.walletAddress);
  if (!mnemonic) throw new Error('no key for wallet');
  const btcKey = deriveBtcPrivateKey(mnemonic, networkFor(params));
  const sigs = payloads.map((p) => ({ index: p.index, sigHex: signSecpDigest(p.hashHex, btcKey) }));

  const headers = await authHeaders();
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=btc&network=${networkFor(params)}`, {
    method: 'POST', headers,
    body: JSON.stringify({
      prepared, sigs,
      from: params.from, amount: params.amount, symbol: params.symbol,
      // `amount` is sats; the fee amount is whole BTC, hence a separate field.
      ...(params.fee && params.fee.feature !== 'none'
        ? {
            feeDeferred: true,
            feeFeature: params.fee.feature,
            feeAmountHuman: params.fee.amountHuman,
            feeChainId: params.fee.chainId ?? 0,
          }
        : {}),
    }),
  });
  const json = await readJson(res);
  // Same mapping as every EVM send: the server relays the node's own words, and
  // whatever this throws is rendered verbatim on the failure screen.
  return {
    txHash: txHashOrThrow(
      { success: res.ok, message: json?.message || `submit-tx failed (${res.status})`, data: json?.data },
      TRANSFER_COPY,
    ),
  };
}

/** Prepare → sign → submit a BTC transfer in one call. */
export async function sendBtc(params: SendBtcParams): Promise<{ txHash: string }> {
  const { prepared } = await prepareBtc(params);
  return broadcastBtc(prepared, params);
}
