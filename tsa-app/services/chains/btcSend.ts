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
  };
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=btc&network=${networkFor(params)}`, {
    method: 'POST', headers, body: JSON.stringify(body),
  });
  const json = await readJson(res);
  if (!res.ok) {
    // 402 carries the friendly fee-preflight note in `message`.
    throw new Error(json?.message || `prepare-tx failed (${res.status})`);
  }
  if (!json?.data?.prepared) throw new Error(json?.message || 'prepare failed');
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
    }),
  });
  const json = await readJson(res);
  if (!res.ok || !json?.data?.txHash) {
    throw new Error(json?.message || `submit-tx failed (${res.status})`);
  }
  return { txHash: json.data.txHash };
}

/** Prepare → sign → submit a BTC transfer in one call. */
export async function sendBtc(params: SendBtcParams): Promise<{ txHash: string }> {
  const { prepared } = await prepareBtc(params);
  return broadcastBtc(prepared, params);
}
