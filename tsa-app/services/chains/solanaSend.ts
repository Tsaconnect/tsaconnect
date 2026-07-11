import { API_BASE_URL } from '../../constants/api/config';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getMnemonic } from '../wallet';
import { getNetwork } from '../../hooks/useNetwork';
import { deriveSolanaPrivateKey } from './derivation';
import { signEd25519Message } from './sign';

async function authHeaders() {
  const token = await AsyncStorage.getItem('authToken');
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
}

export interface SolanaFeeQuote {
  gasSymbol: string; // "SOL"
  estFeeRaw: string;
  gasDecimals: number; // 9
  needsActivation: boolean; // recipient's associated token account must be created
  hasEnoughGas: boolean;
  note?: string;
}

export interface SolanaPrepared {
  prepared: { family: string; raw: string; payloads: { index: number; hashHex: string; curve: string }[] };
  fee: SolanaFeeQuote;
}

export interface SendSolanaParams {
  from: string; // sender Solana address (base58 pubkey)
  to: string;
  amount: string; // integer string, base units (SOL = 9 decimals, SPL = mint decimals)
  symbol: string;
  tokenAddr?: string; // SPL mint; empty for native SOL
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
 * Build the unsigned Solana tx and run the mandatory gas preflight. Throws with
 * the backend's human note (e.g. "You need SOL in this wallet to send USDC
 * (network fee + account rent)") when the sender lacks the SOL the transfer
 * requires (HTTP 402). The rent surcharge covers creating the recipient's
 * associated token account when it doesn't exist yet.
 */
export async function prepareSolana(params: SendSolanaParams): Promise<SolanaPrepared> {
  const headers = await authHeaders();
  const body = {
    from: params.from, to: params.to, amount: params.amount,
    symbol: params.symbol, tokenAddr: params.tokenAddr ?? '',
  };
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=solana&network=${getNetwork()}`, {
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
 * Sign the prepared tx's serialized message with the wallet's Solana key and
 * broadcast. Signing uses the Solana-path ed25519 key (m/44'/501'), not the EVM
 * key — the 64-byte signature must verify against the fee-payer pubkey the
 * backend embedded in the message.
 */
export async function broadcastSolana(
  prepared: SolanaPrepared['prepared'],
  params: SendSolanaParams,
): Promise<{ txHash: string }> {
  const payloads = prepared?.payloads;
  if (!Array.isArray(payloads) || payloads.length === 0) {
    throw new Error('prepare-tx returned no payloads');
  }
  const mnemonic = await getMnemonic(params.walletAddress);
  if (!mnemonic) throw new Error('no key for wallet');
  const solKey = deriveSolanaPrivateKey(mnemonic);
  // ed25519 signs the whole serialized message carried in hashHex.
  const sigs = payloads.map((p) => ({ index: p.index, sigHex: signEd25519Message(p.hashHex, solKey) }));

  const headers = await authHeaders();
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=solana&network=${getNetwork()}`, {
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

/** Prepare → sign → submit a Solana transfer in one call. */
export async function sendSolana(params: SendSolanaParams): Promise<{ txHash: string }> {
  const { prepared } = await prepareSolana(params);
  return broadcastSolana(prepared, params);
}
