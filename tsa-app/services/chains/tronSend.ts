import { API_BASE_URL } from '../../constants/api/config';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getMnemonic } from '../wallet';
import { getNetwork } from '../../hooks/useNetwork';
import { txHashOrThrow, mapSubmitError, TRANSFER_COPY, type SubmitErrorCopy } from '../submitErrors';
import { deriveTronPrivateKey } from './derivation';
import { signSecpDigest } from './sign';

async function authHeaders() {
  const token = await AsyncStorage.getItem('authToken');
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
}

export interface TronFeeQuote {
  gasSymbol: string;
  estFeeRaw: string;
  gasDecimals: number;
  needsActivation: boolean;
  hasEnoughGas: boolean;
  note?: string;
}

export interface TronPrepared {
  prepared: { family: string; raw: string; payloads: { index: number; hashHex: string; curve: string }[] };
  fee: TronFeeQuote;
}

export interface SendTronParams {
  from: string; // sender Tron address (owner_address)
  to: string;
  amount: string; // integer string, base units (TRX/TRC-20 = 6 decimals)
  symbol: string;
  tokenAddr?: string; // TRC-20 contract; empty for native TRX
  walletAddress: string; // EVM wallet id used to look up the mnemonic
  /**
   * Optional pre-built transaction (TransactionRaw protobuf, hex) to sign
   * instead of building a transfer. Aggregator Tron routes hand the whole
   * transaction back this way; the backend decodes it and checks its owner,
   * contract type, value and fee limit against `from` and `amount` first.
   */
  tronRawHex?: string;
  /**
   * Build a TRC-20 approval of `tokenAddr` for spender `to` instead of a
   * transfer; `amount` is the allowance actually needed. The backend answers
   * with no payloads when the allowance already covers it.
   */
  approve?: boolean;
  /** Fee bucket + human-units amount for an aggregator swap; see SendSuiParams.fee. */
  fee?: { feature: string; amountHuman: string; chainId?: number };
  /** Network override; swaps pin mainnet, wallet sends follow the app toggle. */
  network?: string;
  /** Wording for errors; a swap reads as a swap, not a transfer. */
  errorCopy?: SubmitErrorCopy;
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
 * Build the unsigned Tron tx and run the mandatory gas preflight. Throws with
 * the backend's human note (e.g. "You need TRX in this wallet to send USDT")
 * when the sender lacks the native gas the transfer requires (HTTP 402).
 */
export async function prepareTron(params: SendTronParams): Promise<TronPrepared> {
  const headers = await authHeaders();
  const body = {
    from: params.from, to: params.to, amount: params.amount,
    symbol: params.symbol, tokenAddr: params.tokenAddr ?? '',
    ...(params.tronRawHex ? { tronRawHex: params.tronRawHex } : {}),
    ...(params.approve ? { approve: true } : {}),
  };
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=tron&network=${params.network ?? getNetwork()}`, {
    method: 'POST', headers, body: JSON.stringify(body),
  });
  const json = await readJson(res);
  if (!res.ok) {
    // 402 carries the friendly gas-preflight note in `message`.
    throw new Error(
      json?.message
        ? mapSubmitError(json.message, params.errorCopy ?? TRANSFER_COPY)
        : `prepare-tx failed (${res.status})`,
    );
  }
  if (!json?.data?.prepared) {
    throw new Error(json?.message ? mapSubmitError(json.message, params.errorCopy ?? TRANSFER_COPY) : 'prepare failed');
  }
  return { prepared: json.data.prepared, fee: json.data.fee };
}

/**
 * Sign the prepared tx's payload(s) with the wallet's Tron key and broadcast.
 * Signing uses the Tron-path key (m/44'/195'), not the EVM key — the signature
 * must recover to the registered owner_address.
 */
export async function broadcastTron(
  prepared: TronPrepared['prepared'],
  params: SendTronParams,
): Promise<{ txHash: string }> {
  const payloads = prepared?.payloads;
  if (!Array.isArray(payloads) || payloads.length === 0) {
    throw new Error('prepare-tx returned no payloads');
  }
  const mnemonic = await getMnemonic(params.walletAddress);
  if (!mnemonic) throw new Error('no key for wallet');
  const tronKey = deriveTronPrivateKey(mnemonic);
  const sigs = payloads.map((p) => ({ index: p.index, sigHex: signSecpDigest(p.hashHex, tronKey) }));

  const headers = await authHeaders();
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=tron&network=${params.network ?? getNetwork()}`, {
    method: 'POST', headers,
    body: JSON.stringify({
      prepared, sigs,
      from: params.from, amount: params.amount, symbol: params.symbol,
      // `amount` is base units; the fee amount is whole tokens, hence a
      // separate field rather than reusing it.
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
      params.errorCopy ?? TRANSFER_COPY,
    ),
  };
}

/** Prepare → sign → submit a Tron transfer in one call. */
export async function sendTron(params: SendTronParams): Promise<{ txHash: string }> {
  const { prepared } = await prepareTron(params);
  return broadcastTron(prepared, params);
}
