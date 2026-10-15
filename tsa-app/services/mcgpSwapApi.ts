// services/mcgpSwapApi.ts
//
// Client for the MCGP Swap Contract B endpoints (/api/mcgp/*). Mirrors the
// prepare→sign→confirm contract used by walletApi/p2p: prepare endpoints return
// an unsigned tx the app signs + broadcasts; confirm endpoints verify the
// on-chain event and write the audit ledger row.
//
// PREMIUM MCGP only — bought (buyPremium, paid in USDC), spendable (pay) +
// sellable.

import { ethers } from 'ethers';
import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders, withWalletProof } from '@/services/walletApi';
import { getSignableWallet } from '@/services/wallet';
import { boughtBase } from '@/services/mcgpValuation';

/**
 * Returned instead of calling the API when this device holds no signing key.
 * Callers surface it as "import your wallet", never as a zero balance — a zero
 * is indistinguishable from an empty wallet and hides the real problem.
 */
export const NO_LOCAL_WALLET = 'No wallet on this device. Import your recovery phrase to see your MCGP.';

export type McgpUnsignedTx = {
  nonce?: number;
  gasPrice?: string;
  gasLimit?: string;
  to: string;
  value?: string;
  data: string;
  chainId?: string;
};

export interface McgpBalance {
  walletAddress: string;
  premiumBalance: string; // MCGP base units (18dp) — bought + credited
  /** USDC-bought, self-exitable bucket. Additive field; absent on an older API. */
  premiumBought?: string;
  /** Naira-bought (IT/bills), spend-only-in-app bucket. Additive field; absent on an older API. */
  premiumCredited?: string;
}

export interface McgpQuote {
  kind: string;
  mcgpAmount: string;
  usdcAmount: string; // USDC base units (6dp)
}

export interface McgpPreparePay {
  ref: string;
  recipient: string;
  recipientName: string;
  mcgpAmount: string;
  tx: McgpUnsignedTx;
}

export interface McgpLedgerEntry {
  id: string;
  direction: string;
  mcgpAmount: string;
  usdcAmount?: string;
  txHash?: string;
  status: string;
  createdAt: string;
}

export type ApiResult<T> = { success: boolean; data?: T; message?: string; code?: string };

export type SwapDirection =
  | 'buy_premium'
  | 'sell_premium'
  | 'withdraw';

export type QuoteKind = 'premiumBuy' | 'premiumSell';

async function safeJson(res: Response): Promise<any> {
  try {
    return await res.json();
  } catch {
    return null;
  }
}

// GET helper for wrapped {success,data} responses.
async function getJson<T>(path: string): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, { method: 'GET', headers });
    const body = await safeJson(res);
    if (body?.success) return { success: true, data: body.data as T };
    return { success: false, message: body?.message || `Request failed (${res.status})`, code: body?.code };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// POST helper for wrapped {success,data} responses.
async function postJson<T>(path: string, body: unknown): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    });
    const json = await safeJson(res);
    if (json?.success) return { success: true, data: json.data as T };
    return { success: false, message: json?.message || `Request failed (${res.status})`, code: json?.code };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// POST helper for the prepare endpoints that return the RAW unsigned tx
// ({to,data,...}) on success, or the {success:false,message} envelope on error.
async function postRawTx(path: string, body: unknown): Promise<ApiResult<McgpUnsignedTx>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    });
    const json = await safeJson(res);
    if (json && json.success === false) {
      return { success: false, message: json.message || `Request failed (${res.status})`, code: json.code };
    }
    if (json && typeof json.to === 'string' && typeof json.data === 'string') {
      return { success: true, data: json as McgpUnsignedTx };
    }
    return { success: false, message: `Unexpected response (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// ─────────────────────────────── reads ────────────────────────────────────

/**
 * The premium MCGP balance for the wallet this device holds a key for.
 *
 * The address is sent explicitly so the backend can reconcile it against the
 * account's registered wallet and refuse (403) when they differ, rather than
 * silently returning the registered wallet's entitlement — a number the caller
 * would render as spendable but could never sign for.
 */
export const getMcgpBalance = async (): Promise<ApiResult<McgpBalance>> => {
  const address = await getSignableWallet();
  if (!address) {
    return { success: false, message: NO_LOCAL_WALLET };
  }
  return withWalletProof(() => getJson<McgpBalance>(`/mcgp/balance?address=${encodeURIComponent(address)}`));
};

export const getMcgpQuote = (kind: QuoteKind, amount: string) =>
  getJson<McgpQuote>(`/mcgp/quote?kind=${kind}&amount=${encodeURIComponent(amount)}`);

/**
 * The user's spendable premium MCGP as a wallet asset: human quantity plus a
 * best-effort USD value from a premiumSell quote (what they'd receive in USDC).
 * Premium MCGP is a Contract-B entitlement, not an ERC-20 in the wallet, so the
 * standard balance read misses it — this is how the asset lists surface it.
 * Returns a zero balance when unavailable so callers can simply skip it.
 *
 * `usdValue` prices ONLY the USDC-bought bucket (`premiumBought`) at the
 * premiumSell quote. The naira-bought bucket (`premiumCredited`) has no USDC
 * exit — sellPremium reverts "Insufficient bought premium" for it — so
 * pricing it the same way overstated the wallet's USD total. Its quantity is
 * still counted in `balance`; it just contributes no USD value here (no
 * USD-denominated naira rate is wired up to price it against instead).
 */
export async function getPremiumMcgpAsset(): Promise<{ balance: number; usdValue: number }> {
  const res = await getMcgpBalance();
  if (!res.success || !res.data) return { balance: 0, usdValue: 0 };
  return premiumAssetFrom(res.data);
}

/**
 * Like getPremiumMcgpAsset, but a failed read throws instead of reading as an
 * empty balance — for callers that keep the last known value on failure.
 */
export async function readPremiumMcgpAsset(): Promise<{ balance: number; usdValue: number }> {
  const res = await getMcgpBalance();
  if (!res.success || !res.data) throw new Error('MCGP balance unavailable');
  return premiumAssetFrom(res.data);
}

async function premiumAssetFrom(
  data: NonNullable<Awaited<ReturnType<typeof getMcgpBalance>>['data']>,
): Promise<{ balance: number; usdValue: number }> {
  const rawBase = data.premiumBalance || '0'; // 18dp base units, bought + credited
  const balance = Number(ethers.formatUnits(rawBase, 18)) || 0;
  if (balance <= 0) return { balance: 0, usdValue: 0 };
  const boughtRaw = boughtBase({ totalBase: rawBase, premiumBought: data.premiumBought });
  let usdValue = 0;
  if (BigInt(boughtRaw || '0') > 0n) {
    try {
      const q = await getMcgpQuote('premiumSell', boughtRaw);
      if (q.success && q.data) usdValue = Number(ethers.formatUnits(q.data.usdcAmount || '0', 6)) || 0;
    } catch {
      /* price unavailable — show the quantity with no USD rather than failing */
    }
  }
  return { balance, usdValue };
}

export const getMcgpLedger = () =>
  getJson<{ entries: McgpLedgerEntry[] }>('/mcgp/ledger');

// ────────────────────────────── prepares ──────────────────────────────────
// Every prepare names the wallet the device will sign with, so the backend can
// refuse to build a tx for an address we hold no key for. Without it the
// mismatch only surfaces at broadcast, as a nonce/sender error that names
// neither wallet.
//
// A fromAddress other than the account's linked wallet is refused with
// WALLET_PROOF_REQUIRED until this device proves it owns it; withWalletProof
// proves and links the active wallet, then asks again.
export const prepareMcgpApprove = (token: 'usdc', amount: string, fromAddress: string) =>
  withWalletProof(() =>
    postJson<{ currentAllowance: string; tx: McgpUnsignedTx }>('/mcgp/approve/prepare', {
      token,
      amount,
      fromAddress,
    }),
  );

export const prepareMcgpBuy = (mcgpAmount: string, maxUsdc: string, fromAddress: string) =>
  withWalletProof(() => postRawTx('/mcgp/buy/prepare', { mcgpAmount, maxUsdc, fromAddress }));

export const prepareMcgpSell = (mcgpAmount: string, minUsdc: string, fromAddress: string) =>
  withWalletProof(() => postRawTx('/mcgp/sell/prepare', { mcgpAmount, minUsdc, fromAddress }));

export const prepareMcgpWithdraw = (mcgpAmount: string, fromAddress: string) =>
  withWalletProof(() => postRawTx('/mcgp/withdraw/prepare', { mcgpAmount, fromAddress }));

export const prepareMcgpPay = (username: string, mcgpAmount: string, fromAddress: string) =>
  withWalletProof(() => postJson<McgpPreparePay>('/mcgp/pay/prepare', { username, mcgpAmount, fromAddress }));

// ────────────────────────────── confirms ──────────────────────────────────
export const confirmMcgpSwap = (txHash: string, direction: SwapDirection) =>
  postJson<{ ledgerId?: string; mcgpAmount?: string; usdcAmount?: string }>('/mcgp/confirm', {
    txHash,
    direction,
  });

export const confirmMcgpPay = (txHash: string, ref: string, username: string) =>
  postJson<{ ledgerId?: string }>('/mcgp/pay/confirm', { txHash, ref, username });
