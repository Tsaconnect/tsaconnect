// services/mcgpFlow.ts
//
// Orchestrates the full client-side MCGP swap flows (approve → prepare → sign →
// broadcast → confirm) so the screens stay thin. All signing happens with the
// device wallet key; the backend only prepares unsigned txs and confirms events.

import { signTransaction, broadcastTransaction, getActiveWallet, ensureGas } from '@/services/wallet';
import { unsignedTxToTxLike } from '@/services/p2p';
import {
  prepareMcgpApprove,
  prepareMcgpBuy,
  prepareMcgpSell,
  prepareMcgpWithdraw,
  prepareMcgpPay,
  confirmMcgpSwap,
  confirmMcgpPay,
  type McgpUnsignedTx,
} from '@/services/mcgpSwapApi';

const SONIC = 'sonic' as const;

// Cap on how long we wait for a broadcast tx to mine. Without it a tx the RPC
// accepted but never mines (dropped, nonce gap, stuck mempool) leaves the
// calling screen on "Approving & buying…" forever. Mirrors TX_TIMEOUT_MS in
// services/transaction.ts and APPROVE_WAIT_MS in services/escrowFunding.ts.
const TX_WAIT_MS = 90_000;

function gte(a: string, b: string): boolean {
  try {
    return BigInt(a || '0') >= BigInt(b || '0');
  } catch {
    return false;
  }
}

// Wait for a broadcast tx to mine, bounded by TX_WAIT_MS, and require a
// success receipt — a mined-but-reverted approve must abort the flow before
// the next step broadcasts against a missing allowance.
async function waitMined(resp: { hash: string; wait: () => Promise<{ status?: number | null } | null> }): Promise<void> {
  const receipt = await Promise.race([
    resp.wait(),
    new Promise<never>((_, reject) =>
      setTimeout(
        () => reject(new Error('The transaction is taking too long to confirm. It may still complete — check your balance before retrying.')),
        TX_WAIT_MS,
      ),
    ),
  ]);
  if (!receipt || receipt.status !== 1) {
    throw new Error('The transaction failed on-chain. No tokens were moved (only gas was spent). Please try again.');
  }
}

// Sign + broadcast an unsigned tx, wait for it to mine, return the tx hash.
// Waiting before the backend confirm avoids a "receipt not found" race.
async function signBroadcast(tx: McgpUnsignedTx, from: string): Promise<string> {
  const signed = await signTransaction(unsignedTxToTxLike(tx as any), from);
  const resp = await broadcastTransaction(SONIC, signed);
  await waitMined(resp);
  return resp.hash;
}

// Ensure the swap contract is approved to pull `amount` of USDC; broadcasts
// an exact-amount approve and waits for it to mine when the allowance is short.
async function ensureApprove(amount: string, from: string): Promise<void> {
  const r = await prepareMcgpApprove('usdc', amount);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare approval');
  if (gte(r.data.currentAllowance, amount)) return; // already covered
  const signed = await signTransaction(unsignedTxToTxLike(r.data.tx as any), from);
  const resp = await broadcastTransaction(SONIC, signed);
  await waitMined(resp); // buy gas-estimate reverts if the allowance isn't set yet
}

async function requireWallet(): Promise<string> {
  const from = await getActiveWallet();
  if (!from) throw new Error('No wallet found. Set up your wallet first.');
  return from;
}

// Gas preflight: fail fast with a clear message instead of broadcasting a tx
// the chain will reject (or worse, queue) when the wallet holds no S.
async function requireGas(from: string): Promise<void> {
  let alertMsg = '';
  const ok = await ensureGas(SONIC, from, (title, message) => {
    alertMsg = `${title}: ${message}`;
  });
  if (!ok) {
    throw new Error(alertMsg || 'You need S (Sonic) in this wallet to pay the network fee.');
  }
}

// The confirm endpoints only write the audit ledger row — by the time they
// run, the swap already happened on-chain and balances read from the chain.
// Never fail the flow (the user would see "failed" for a successful buy);
// just log so a missing ledger row is traceable.
async function confirmBestEffort(p: Promise<{ success: boolean; message?: string }>, what: string): Promise<void> {
  const r = await p;
  if (!r.success) console.warn(`[mcgpFlow] ${what} confirm failed (tx already on-chain):`, r.message);
}

/** Buy spendable premium MCGP: approve USDC (if needed) → buyPremium → confirm. */
export async function executeBuy(mcgpAmount: string, maxUsdc: string): Promise<string> {
  const from = await requireWallet();
  await requireGas(from);
  await ensureApprove(maxUsdc, from);
  const r = await prepareMcgpBuy(mcgpAmount, maxUsdc);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare buy');
  const hash = await signBroadcast(r.data, from);
  await confirmBestEffort(confirmMcgpSwap(hash, 'buy_premium'), 'buy_premium');
  return hash;
}

/** Sell premium MCGP back for USDC. */
export async function executeSellPremium(mcgpAmount: string, minUsdc: string): Promise<string> {
  const from = await requireWallet();
  await requireGas(from);
  const r = await prepareMcgpSell(mcgpAmount, minUsdc);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare sell');
  const hash = await signBroadcast(r.data, from);
  await confirmBestEffort(confirmMcgpSwap(hash, 'sell_premium'), 'sell_premium');
  return hash;
}

/** Withdraw premium balance to the wallet as ordinary (non-spendable) MCGP. */
export async function executeWithdraw(mcgpAmount: string): Promise<string> {
  const from = await requireWallet();
  await requireGas(from);
  const r = await prepareMcgpWithdraw(mcgpAmount);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare withdraw');
  const hash = await signBroadcast(r.data, from);
  await confirmBestEffort(confirmMcgpSwap(hash, 'withdraw'), 'withdraw');
  return hash;
}

/** Pay another user from premium balance by username. */
export async function executePay(username: string, mcgpAmount: string): Promise<string> {
  const from = await requireWallet();
  await requireGas(from);
  const r = await prepareMcgpPay(username, mcgpAmount);
  if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare payment');
  const hash = await signBroadcast(r.data.tx, from);
  await confirmBestEffort(confirmMcgpPay(hash, r.data.ref, r.data.recipient), 'pay');
  return hash;
}
