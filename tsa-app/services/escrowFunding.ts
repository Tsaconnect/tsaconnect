// services/escrowFunding.ts
//
// Fund the seller's on-chain escrow pool to cover a single ad, folded into ad
// creation ("hide the pool", spec 2026-06-05). Framework-free (no React) so it
// can be called from create-ad's handleSubmit without dragging in hooks.
//
// It reuses the EXACT primitives the standalone pool screen used —
// poolApi.{balance,allowance,prepareApprove,prepareDeposit,confirmDeposit} plus
// the wallet's gas check and runP2PTx's sign/broadcast (p2pTx.signAndBroadcast,
// which maps a raw wallet/RPC failure to copy) — so funding behaves identically
// to the old Pool deposit flow; it just runs inline at publish time.
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

import { p2pErrorMessage, p2pKycCode, p2pRefusalError, type P2PErrorLike } from './p2pErrors';
import { poolApi, p2pChainKey } from './p2p';
import { ensureGas } from './wallet';
import { addPending } from './p2pPending';
import { waitMined } from './txWait';
import { ensureP2PGas } from './p2pGas';
import { signAndBroadcast } from './p2pTx';

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
  /**
   * Ask the P2P gas sponsor (R3) instead of the plain balance check. Each tx
   * gets its own drip right before it ('approve', then 'deposit'). TSA
   * Liquidity (lpDeposit) leaves this unset, so its behaviour is unchanged.
   */
  gasAction?: 'deposit';
}

/** A refusal as mapped copy that keeps the code and status, so the screen can
 *  route a KYC 403 to the Verify prompt. */
function refusalError(res: P2PErrorLike, fallback: string): Error {
  return p2pRefusalError(res, p2pErrorMessage(res, fallback));
}

/** A KYC refusal on a read must stop the flow, not read as an empty balance. */
function throwIfKycRefused(res: { success: boolean; message?: string; status?: number; code?: string }): void {
  if (!res.success && p2pKycCode(res)) throw refusalError(res, 'Identity verification required');
}

export type EnsureFundingResult =
  | { status: 'already-funded' }
  | { status: 'funded'; deposited: string }
  | { status: 'pending-confirm'; txHash: string; deposited: string }
  | { status: 'aborted'; reason: string };

/**
 * Top up the seller's on-chain `available` balance so it covers `amountBaseUnits`
 * for this ad. See the file header + the per-step comments for behavior; all
 * math is BigInt over base-unit strings.
 */
export async function ensureEscrowFunding(p: EnsureFundingParams): Promise<EnsureFundingResult> {
  const { token, chainId, amountBaseUnits, walletAddress, onStage, onGasAlert, exactDepositBaseUnits, gasAction } = p;

  // Never broadcast on an unresolved/defaulted chain — a silent Sonic fallback
  // would fund the wrong chain and desync the voucher domain.
  if (!(chainId > 0)) {
    throw new Error(`ensureEscrowFunding: chainId must be a resolved chain id, got ${chainId}`);
  }

  // How much the ad needs vs. what the seller already has available on-chain.
  onStage?.('Checking balance…');
  const balRes = await poolApi.balance(token, walletAddress, chainId);
  throwIfKycRefused(balRes);
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

  // Gas guard before each broadcast — approve and deposit both pay gas.
  // ensureGas takes a (title, message) alerter; collapse to the single-string
  // onGasAlert this module exposes. Sponsored flows drip per step instead, so
  // an approve's S is not requested until the approve is about to be sent.
  const alert = onGasAlert ? (title: string, message: string) => onGasAlert(`${title}: ${message}`) : undefined;
  const sponsoredGas = (action: 'approve' | 'deposit') =>
    ensureP2PGas({ chainId, walletAddress, action, onStage, onAlert: alert });
  if (!gasAction) {
    onStage?.('Checking gas…');
    if (!(await ensureGas(chainKey, walletAddress, alert))) return { status: 'aborted', reason: 'no gas' };
  }

  // Approve the escrow for the shortfall if the current allowance is short.
  // (transferFrom inside deposit() draws from this allowance.)
  onStage?.('Checking allowance…');
  const allowRes = await poolApi.allowance(token, walletAddress, chainId);
  throwIfKycRefused(allowRes);
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
    if (gasAction && !(await sponsoredGas('approve'))) return { status: 'aborted', reason: 'no gas' };
    onStage?.('Preparing approve…');
    const aPrep = await poolApi.prepareApprove(token, shortfall.toString(), walletAddress, chainId);
    if (!aPrep.success || !aPrep.data) {
      throw refusalError(aPrep, 'Prepare approve failed');
    }
    const approveResp = await signAndBroadcast(chainId, aPrep.data.unsignedTx, walletAddress, {
      onStage,
      signing: 'Signing approve…',
      broadcasting: 'Broadcasting approve…',
    });
    // Poll until the approve mines so the deposit's transferFrom can't race it.
    onStage?.('Waiting for approve…');
    // Known limitation: if the chain's primary RPC is unreachable for reads the
    // wait times out and the publish aborts — that fails safe (an approve moves
    // no funds) and a retry finds the allowance already in place. A reverted
    // approve throws TxRevertedError before the deposit is prepared.
    await waitMined(chainId, approveResp.hash, { label: 'Approve' });
  }

  // Deposit the shortfall into the seller's `available` balance.
  if (gasAction && !(await sponsoredGas('deposit'))) return { status: 'aborted', reason: 'no gas' };
  onStage?.('Preparing deposit…');
  const dPrep = await poolApi.prepareDeposit(token, shortfall.toString(), walletAddress, chainId);
  if (!dPrep.success || !dPrep.data) {
    throw refusalError(dPrep, 'Prepare deposit failed');
  }
  const depositResp = await signAndBroadcast(chainId, dPrep.data.unsignedTx, walletAddress, {
    onStage,
    signing: 'Signing deposit…',
    broadcasting: 'Depositing…',
  });
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
