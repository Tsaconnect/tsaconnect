// services/mcgpFlow.ts
//
// Orchestrates the full client-side MCGP swap flows (approve → prepare → sign →
// broadcast → confirm) so the screens stay thin. All signing happens with the
// device wallet key; the backend only prepares unsigned txs and confirms events.

import { signTransaction, broadcastTransaction, getActiveWallet } from '@/services/wallet';
import { unsignedTxToTxLike } from '@/services/p2p';
import {
  prepareMcgpApprove,
  prepareMcgpBuy,
  prepareMcgpSell,
  prepareMcgpLegacySell,
  prepareMcgpWithdraw,
  prepareMcgpPay,
  confirmMcgpSwap,
  confirmMcgpPay,
  type McgpUnsignedTx,
} from '@/services/mcgpSwapApi';

const SONIC = 'sonic' as const;

function gte(a: string, b: string): boolean {
  try {
    return BigInt(a || '0') >= BigInt(b || '0');
  } catch {
    return false;
  }
}

// Sign + broadcast an unsigned tx, wait for it to mine, return the tx hash.
// Waiting before the backend confirm avoids a "receipt not found" race.
async function signBroadcast(tx: McgpUnsignedTx, from: string): Promise<string> {
  const signed = await signTransaction(unsignedTxToTxLike(tx as any), from);
  const resp = await broadcastTransaction(SONIC, signed);
  await resp.wait();
  return resp.hash;
}

// Ensure the swap contract is approved to pull `amount` of `token`; broadcasts
// an exact-amount approve and waits for it to mine when the allowance is short.
async function ensureApprove(token: 'usdc' | 'mcgp', amount: string, from: string): Promise<void> {
  const r = await prepareMcgpApprove(token, amount);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare approval');
  if (gte(r.data.currentAllowance, amount)) return; // already covered
  const signed = await signTransaction(unsignedTxToTxLike(r.data.tx as any), from);
  const resp = await broadcastTransaction(SONIC, signed);
  await resp.wait(); // buy/redeem gas-estimate reverts if the allowance isn't set yet
}

async function requireWallet(): Promise<string> {
  const from = await getActiveWallet();
  if (!from) throw new Error('No wallet found. Set up your wallet first.');
  return from;
}

/** Buy spendable premium MCGP: approve USDC (if needed) → buyPremium → confirm. */
export async function executeBuy(mcgpAmount: string, maxUsdc: string): Promise<string> {
  const from = await requireWallet();
  await ensureApprove('usdc', maxUsdc, from);
  const r = await prepareMcgpBuy(mcgpAmount, maxUsdc);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare buy');
  const hash = await signBroadcast(r.data, from);
  await confirmMcgpSwap(hash, 'buy_premium');
  return hash;
}

/** Sell premium MCGP back for USDC. */
export async function executeSellPremium(mcgpAmount: string, minUsdc: string): Promise<string> {
  const from = await requireWallet();
  const r = await prepareMcgpSell(mcgpAmount, minUsdc);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare sell');
  const hash = await signBroadcast(r.data, from);
  await confirmMcgpSwap(hash, 'sell_premium');
  return hash;
}

/** Redeem legacy MCGP (capped at snapshot): approve MCGP (if needed) → sellLegacy → confirm. */
export async function executeLegacyRedeem(mcgpAmount: string, minUsdc: string): Promise<string> {
  const from = await requireWallet();
  await ensureApprove('mcgp', mcgpAmount, from);
  const r = await prepareMcgpLegacySell(mcgpAmount, minUsdc);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare redeem');
  const hash = await signBroadcast(r.data, from);
  await confirmMcgpSwap(hash, 'sell_legacy');
  return hash;
}

/** Withdraw premium balance to the wallet as ordinary (non-spendable) MCGP. */
export async function executeWithdraw(mcgpAmount: string): Promise<string> {
  const from = await requireWallet();
  const r = await prepareMcgpWithdraw(mcgpAmount);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare withdraw');
  const hash = await signBroadcast(r.data, from);
  await confirmMcgpSwap(hash, 'withdraw');
  return hash;
}

/** Pay another user from premium balance by username. */
export async function executePay(username: string, mcgpAmount: string): Promise<string> {
  const from = await requireWallet();
  const r = await prepareMcgpPay(username, mcgpAmount);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare payment');
  const hash = await signBroadcast(r.data.tx, from);
  await confirmMcgpPay(hash, r.data.ref, r.data.recipient);
  return hash;
}
