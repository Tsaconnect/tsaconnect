import { API_BASE_URL } from '../../constants/api/config';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getMnemonic } from '../wallet';
import { getNetwork } from '../../hooks/useNetwork';
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
  };
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=tron&network=${getNetwork()}`, {
    method: 'POST', headers, body: JSON.stringify(body),
  });
  const json = await readJson(res);
  if (!res.ok) {
    // 402 carries the friendly gas-preflight note in `message`.
    throw new Error(json?.message || `prepare-tx failed (${res.status})`);
  }
  if (!json?.data?.prepared) throw new Error(json?.message || 'prepare failed');
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
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=tron&network=${getNetwork()}`, {
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

/** Prepare → sign → submit a Tron transfer in one call. */
export async function sendTron(params: SendTronParams): Promise<{ txHash: string }> {
  const { prepared } = await prepareTron(params);
  return broadcastTron(prepared, params);
}
