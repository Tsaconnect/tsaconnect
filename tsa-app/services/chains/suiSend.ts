import { API_BASE_URL } from '../../constants/api/config';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getMnemonic } from '../wallet';
import { getNetwork } from '../../hooks/useNetwork';
import { deriveSuiPrivateKey, deriveSuiPublicKey } from './derivation';
import { signEd25519Message } from './sign';

async function authHeaders() {
  const token = await AsyncStorage.getItem('authToken');
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
}

export interface SuiFeeQuote {
  gasSymbol: string; // "SUI"
  estFeeRaw: string; // integer string in MIST (the SUI you must hold; only actual gas is charged)
  gasDecimals: number; // 9
  needsActivation: boolean; // always false on Sui (no account activation)
  hasEnoughGas: boolean;
  note?: string;
}

export interface SuiPrepared {
  prepared: { family: string; raw: string; payloads: { index: number; hashHex: string; curve: string }[] };
  fee: SuiFeeQuote;
}

export interface SendSuiParams {
  from: string; // sender Sui address (0x + 64 hex)
  to: string;
  amount: string; // integer string, base units (SUI = 9 decimals, token = coin decimals)
  symbol: string;
  tokenAddr?: string; // Sui coin type; empty for native SUI
  walletAddress: string; // EVM wallet id used to look up the mnemonic
  /**
   * Optional pre-built transaction (base64 BCS TransactionData) to sign instead
   * of building one. Aggregator Sui routes hand the whole transaction back this
   * way; the backend dry-runs it and checks what it would move against `from`
   * and `amount` before returning anything to sign.
   */
  txBytesB64?: string;
  /**
   * Fee bucket + human-units amount for an aggregator swap, recorded server-side
   * at broadcast so the platform fee survives a client that never calls settle.
   */
  fee?: { feature: string; amountHuman: string; chainId?: number };
  /** Network override; swaps pin mainnet, wallet sends follow the app toggle. */
  network?: string;
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
 * Build the unsigned Sui tx and run the mandatory gas preflight. Throws with the
 * backend's human note (e.g. "You need SUI in this wallet to send USDC (network
 * fee)") when the sender lacks the SUI the transfer requires (HTTP 402). Gas is
 * always paid in SUI, so a token send with no SUI is blocked here.
 */
export async function prepareSui(params: SendSuiParams): Promise<SuiPrepared> {
  const headers = await authHeaders();
  const body = {
    from: params.from, to: params.to, amount: params.amount,
    symbol: params.symbol, tokenAddr: params.tokenAddr ?? '',
    ...(params.txBytesB64 ? { txBytesB64: params.txBytesB64 } : {}),
  };
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=sui&network=${params.network ?? getNetwork()}`, {
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
 * Sign the prepared tx's intent digest with the wallet's Sui key and broadcast.
 * Signing uses the Sui-path ed25519 key (m/44'/784'), not the EVM key. Sui has
 * no key recovery from a signature, so we pack the 32-byte public key AHEAD of
 * the 64-byte signature (`sigHex = pubkeyHex || sigHex`); the backend splits it,
 * verifies the signature against the prepared tx, and serializes the on-chain
 * signature base64(flag || sig || pubkey).
 */
export async function broadcastSui(
  prepared: SuiPrepared['prepared'],
  params: SendSuiParams,
): Promise<{ txHash: string }> {
  const payloads = prepared?.payloads;
  if (!Array.isArray(payloads) || payloads.length === 0) {
    throw new Error('prepare-tx returned no payloads');
  }
  const mnemonic = await getMnemonic(params.walletAddress);
  if (!mnemonic) throw new Error('no key for wallet');
  const suiKey = deriveSuiPrivateKey(mnemonic);
  const suiPub = deriveSuiPublicKey(mnemonic); // 32-byte pubkey hex, packed ahead of the sig
  // ed25519 signs the 32-byte intent digest carried in hashHex. Guard the
  // declared curve so a mismatched backend payload fails loudly, not silently
  // with a wrong-scheme signature.
  const sigs = payloads.map((p) => {
    if (p.curve !== 'ed25519') throw new Error(`unexpected signing curve: ${p.curve}`);
    return { index: p.index, sigHex: suiPub + signEd25519Message(p.hashHex, suiKey) };
  });

  const headers = await authHeaders();
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=sui&network=${params.network ?? getNetwork()}`, {
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
  if (!res.ok || !json?.data?.txHash) {
    throw new Error(json?.message || `submit-tx failed (${res.status})`);
  }
  return { txHash: json.data.txHash };
}

/** Prepare → sign → submit a Sui transfer in one call. */
export async function sendSui(params: SendSuiParams): Promise<{ txHash: string }> {
  const { prepared } = await prepareSui(params);
  return broadcastSui(prepared, params);
}
