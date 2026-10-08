// services/escrowFunding.ts
//
// Fund the seller's on-chain escrow pool to cover a single ad, folded into ad
// creation ("hide the pool", spec 2026-06-05). Framework-free (no React) so it
// can be called from create-ad's handleSubmit without dragging in hooks.
//
// It reuses the EXACT primitives the standalone pool screen used —
// poolApi.{balance,allowance,prepareApprove,prepareDeposit,confirmDeposit} plus
// the wallet's sign/broadcast/gas helpers — so funding behaves identically to
// the old Pool deposit flow; it just runs inline at publish time.
//
// CONTRACT (caller's responsibilities):
//   - ALL amounts here are BASE UNITS. Decimal scaling via
//     p2pTokenDecimals(chainId, symbol) is the caller's job — this module does
//     pure BigInt math and never touches decimals.
//   - chainId MUST be a concrete, resolved chain id (> 0). We refuse to
//     broadcast on an unresolved/defaulted chain, because a silent fallback to
//     Sonic would fund the wrong chain and the voucher domain would disagree
//     (spec "Review fixes" §2).
//
// On a successful broadcast whose confirm POST fails, the deposit is stashed in
// the non-overwriting pending store (services/p2pPending.ts) and the caller is
// told funds are already on-chain — there is no server-side deposit reconciler,
// so recovery is user-initiated.

import {
  poolApi,
  p2pChainKey,
  unsignedTxToTxLike,
} from './p2p';
import {
  signTransaction,
  broadcastTransaction,
  ensureGas,
  getProvider,
} from './wallet';
import { addPending } from './p2pPending';

export interface EnsureFundingParams {
  token: string;
  chainId: number;
  /** Base units the ad must be backed by (already scaled by the caller). */
  amountBaseUnits: string;
  walletAddress: string;
  /** Progress callback for stage labels ("Approving…", "Depositing…", …). */
  onStage?: (label: string) => void;
  /** Called with a single human-readable string when the wallet lacks gas. */
  onGasAlert?: (msg: string) => void;
  /**
   * When set, the deposit must be exactly this many base units (a TSA Liquidity
   * top-up: the LP's typed amount). If this read's shortfall differs — the
   * balance moved since the caller read it, or this read failed — we stop with
   * `aborted: 'balance-changed'` before any gas check or broadcast, rather than
   * deposit more or less than the LP asked for.
   */
  exactDepositBaseUnits?: string;
}

export type EnsureFundingResult =
  | { status: 'already-funded' }
  | { status: 'funded'; deposited: string }
  | { status: 'pending-confirm'; txHash: string; deposited: string }
  | { status: 'aborted'; reason: string };

// Cap on how long we wait for the approve tx to mine before broadcasting the
// deposit. The deposit's transferFrom needs the allowance in place; mirrors the
// 90s confirmation budget used elsewhere (services/transaction.ts).
const APPROVE_WAIT_MS = 90_000;

/**
 * Wait for a broadcast tx to mine, with a timeout so a stuck RPC can't hang the
 * whole publish flow. Uses provider.waitForTransaction on the chain's primary
 * RPC. Known limitation: if that endpoint is unreachable for reads the race
 * rejects on the timeout and the publish aborts — but that fails safe (an
 * approve moves no funds) and a retry finds the allowance already in place, so
 * the deposit then skips straight past the approve.
 */
async function waitMined(chainId: number, txHash: string): Promise<void> {
  const provider = getProvider(p2pChainKey(chainId));
  await Promise.race([
    provider.waitForTransaction(txHash),
    new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error('Approve confirmation timed out')), APPROVE_WAIT_MS),
    ),
  ]);
}

/**
 * Top up the seller's on-chain `available` balance so it covers `amountBaseUnits`
 * for this ad. See the file header + the per-step comments for behavior; all
 * math is BigInt over base-unit strings.
 */
export async function ensureEscrowFunding(p: EnsureFundingParams): Promise<EnsureFundingResult> {
  const { token, chainId, amountBaseUnits, walletAddress, onStage, onGasAlert, exactDepositBaseUnits } = p;

  // Never broadcast on an unresolved/defaulted chain — a silent Sonic fallback
  // would fund the wrong chain and desync the voucher domain.
  if (!(chainId > 0)) {
    throw new Error(`ensureEscrowFunding: chainId must be a resolved chain id, got ${chainId}`);
  }

  // How much the ad needs vs. what the seller already has available on-chain.
  onStage?.('Checking balance…');
  const balRes = await poolApi.balance(token, walletAddress, chainId);
  const available = balRes.success && balRes.data ? balRes.data.available : '0';

  const need = BigInt(amountBaseUnits);
  const have = BigInt(available || '0');
  const shortfall = need > have ? need - have : 0n;

  // Already covered by the shared on-chain balance — publish with the gasless
  // voucher only. No gas, no tx (spec edge case: repeat ads / post-refund).
  if (shortfall === 0n) {
    return { status: 'already-funded' };
  }

  if (
    exactDepositBaseUnits !== undefined &&
    (!balRes.success || !balRes.data || shortfall !== BigInt(exactDepositBaseUnits))
  ) {
    return { status: 'aborted', reason: 'balance-changed' };
  }

  const chainKey = p2pChainKey(chainId);

  // Gas guard before any broadcast — both approve and deposit pay gas here.
  // ensureGas takes a (title, message) alerter; collapse to the single-string
  // onGasAlert this module exposes.
  onStage?.('Checking gas…');
  const hasGas = await ensureGas(
    chainKey,
    walletAddress,
    onGasAlert ? (title, message) => onGasAlert(`${title}: ${message}`) : undefined,
  );
  if (!hasGas) {
    return { status: 'aborted', reason: 'no gas' };
  }

  // Approve the escrow for the shortfall if the current allowance is short.
  // (transferFrom inside deposit() draws from this allowance.)
  onStage?.('Checking allowance…');
  const allowRes = await poolApi.allowance(token, walletAddress, chainId);
  let allowance = 0n;
  if (allowRes.success && allowRes.data) {
    try {
      allowance = BigInt(allowRes.data.allowance);
    } catch {
      // Non-numeric allowance → treat as 0 and run the approve rather than
      // risk a deposit that reverts on an unmet transferFrom.
      allowance = 0n;
    }
  }

  if (allowance < shortfall) {
    onStage?.('Preparing approve…');
    const aPrep = await poolApi.prepareApprove(token, shortfall.toString(), walletAddress, chainId);
    if (!aPrep.success || !aPrep.data) {
      throw new Error(aPrep.message || 'Prepare approve failed');
    }
    onStage?.('Signing approve…');
    const signedApprove = await signTransaction(unsignedTxToTxLike(aPrep.data.unsignedTx), walletAddress);
    onStage?.('Broadcasting approve…');
    const approveResp = await broadcastTransaction(chainKey, signedApprove);
    // Poll until the approve mines so the deposit's transferFrom can't race it.
    onStage?.('Waiting for approve…');
    await waitMined(chainId, approveResp.hash);
  }

  // Deposit the shortfall into the seller's `available` balance.
  onStage?.('Preparing deposit…');
  const dPrep = await poolApi.prepareDeposit(token, shortfall.toString(), walletAddress, chainId);
  if (!dPrep.success || !dPrep.data) {
    throw new Error(dPrep.message || 'Prepare deposit failed');
  }
  onStage?.('Signing deposit…');
  const signedDeposit = await signTransaction(unsignedTxToTxLike(dPrep.data.unsignedTx), walletAddress);
  onStage?.('Depositing…');
  const depositResp = await broadcastTransaction(chainKey, signedDeposit);
  const txHash = depositResp.hash;
  const deposited = shortfall.toString();

  // Confirm with the backend. On failure the on-chain deposit already happened,
  // so stash it (non-overwriting, keyed by txHash) and let the caller surface a
  // "funds are safe, retry confirm" banner instead of losing the record.
  onStage?.('Confirming deposit…');
  const confirm = await poolApi.confirmDeposit(token, deposited, txHash, walletAddress, chainId);
  if (confirm.success) {
    return { status: 'funded', deposited };
  }

  await addPending({
    mode: 'deposit',
    token,
    chainId,
    amount: deposited,
    txHash,
    walletAddress,
    createdAt: Date.now(),
  });
  return { status: 'pending-confirm', txHash, deposited };
}
