// services/chains/nonevmApi.ts
// Thin client for the backend /wallet/nonevm/* endpoints (Tron today; BTC/
// Solana later). Every endpoint is dormant-safe on the server: when a chain
// isn't configured it returns 503 "chain unavailable", which surfaces here as a
// thrown error the caller can swallow so the chain simply doesn't appear.
import { API_BASE_URL } from '../../constants/api/config';
import { getAuthHeaders } from '../walletApi';
import { getNetwork } from '../../hooks/useNetwork';

export interface NonEVMBalance {
  symbol: string;
  raw: string; // integer string in base units
  decimals: number;
  tokenAddress?: string;
  isNative: boolean;
  usdPrice?: number; // attached by the backend price service
  usdValue?: number;
}

export interface NonEVMTxStatus {
  found: boolean;
  confirmed: boolean;
  success: boolean;
  confirmations: number;
  note?: string;
}

interface Envelope<T> {
  success?: boolean;
  message?: string;
  data?: T;
}

async function readJson<T>(res: Response): Promise<Envelope<T>> {
  const text = await res.text();
  if (!text) return { success: res.ok };
  try {
    return JSON.parse(text) as Envelope<T>;
  } catch {
    return { success: false, message: text.slice(0, 200) };
  }
}

/**
 * Register a derived non-EVM address so the backend reconciler tracks it.
 * Idempotent server-side (upsert on wallet+chain, scoped to the user). Safe to
 * call on every wallet load.
 */
export async function registerNonEVMAddress(
  walletId: string,
  chainKey: string,
  address: string
): Promise<void> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/register?network=${getNetwork()}`, {
    method: 'POST',
    headers,
    body: JSON.stringify({ walletId, chainKey, address }),
  });
  if (!res.ok) {
    const body = await readJson(res);
    throw new Error(body.message || `register failed (${res.status})`);
  }
}

/** Fetch native + token balances for a non-EVM address. */
export async function getNonEVMBalances(
  chainKey: string,
  address: string
): Promise<NonEVMBalance[]> {
  const headers = await getAuthHeaders();
  const qp = new URLSearchParams({ chain: chainKey, address, network: getNetwork() });
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/balances?${qp.toString()}`, { headers });
  const body = await readJson<{ balances?: NonEVMBalance[] }>(res);
  if (!res.ok || !body.data?.balances) {
    throw new Error(body.message || `balances failed (${res.status})`);
  }
  return body.data.balances;
}

export interface NonEVMActivity {
  hash: string;
  direction: 'in' | 'out';
  amountRaw: string;
  decimals: number;
  symbol: string;
  counterparty: string;
  status: string;
  blockTime: number; // seconds since epoch
}

/** Fetch recent on-chain transfer history (sends + receives) for an address. */
export async function getNonEVMHistory(
  chainKey: string,
  address: string
): Promise<NonEVMActivity[]> {
  const headers = await getAuthHeaders();
  const qp = new URLSearchParams({ chain: chainKey, address, network: getNetwork() });
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/history?${qp.toString()}`, { headers });
  const body = await readJson<{ items?: NonEVMActivity[] }>(res);
  if (!res.ok) {
    throw new Error(body.message || `history failed (${res.status})`);
  }
  return body.data?.items ?? [];
}

/** Poll the confirmation status of a broadcast non-EVM transaction. */
export async function getNonEVMTxStatus(
  chainKey: string,
  hash: string
): Promise<NonEVMTxStatus> {
  const headers = await getAuthHeaders();
  const qp = new URLSearchParams({ chain: chainKey, hash, network: getNetwork() });
  const res = await fetch(`${API_BASE_URL}/wallet/nonevm/tx-status?${qp.toString()}`, { headers });
  const body = await readJson<{ status?: NonEVMTxStatus }>(res);
  if (!res.ok || !body.data?.status) {
    throw new Error(body.message || `status failed (${res.status})`);
  }
  return body.data.status;
}
